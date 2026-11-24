package telemetry

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nickstrad/object-store/internal/metrics"
)

var csvHeader = []string{"schema_version", "node_id", "region", "boot_id", "sequence", "start_unix_nano", "time_unix_nano", "component", "name", "kind", "unit", "labels_json", "counter_uint64", "gauge_uint64"}

const csvMaxFileBytes = 64 << 20

type csvSettings struct {
	Directory    string `json:"directory"`
	MaxFileBytes int64  `json:"max_file_bytes"`
	KeepFiles    int    `json:"keep_files"`
	Sync         bool   `json:"sync"
 "syscall"`
}
type csvAdapter struct {
	mu       sync.Mutex
	id       Identity
	settings csvSettings
	closed   bool
}

func newCSV(ctx context.Context, id Identity, raw json.RawMessage) (Adapter, error) {
	s := csvSettings{MaxFileBytes: 16 << 20, KeepFiles: 8, Sync: true}
	if err := decodeStrict(raw, &s); err != nil {
		return nil, fmt.Errorf("%w: csv settings: %v", ErrConfig, err)
	}
	if s.Directory == "" || s.MaxFileBytes < 1 || s.MaxFileBytes > csvMaxFileBytes || s.KeepFiles < 1 || s.KeepFiles > 1024 {
		return nil, fmt.Errorf("%w: csv directory or bounds", ErrConfig)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.Directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(s.Directory)
	if err != nil {
		return nil, err
	}
	stat, owned := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 || !owned || stat.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("%w: csv directory must be private", ErrConfig)
	}
	f, err := os.CreateTemp(s.Directory, ".probe-*.tmp")
	if err != nil {
		return nil, err
	}
	name := f.Name()
	err = f.Close()
	removeErr := os.Remove(name)
	if err != nil {
		return nil, err
	}
	if removeErr != nil {
		return nil, removeErr
	}
	return &csvAdapter{id: id, settings: s}, nil
}
func csvEncode(b metrics.Batch) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(csvHeader); err != nil {
		return nil, err
	}
	desc := map[string]metrics.Descriptor{}
	for _, d := range metrics.Catalogue() {
		desc[d.Name] = d
	}
	rows := 0
	for _, s := range b.Snapshots {
		for _, p := range s.Points {
			d := desc[p.Name]
			labels := map[string]string{}
			for _, l := range p.Labels {
				labels[l.Name] = l.Value
			}
			ls, err := json.Marshal(labels)
			if err != nil {
				return nil, err
			}
			kind, cv, gv := "counter", strconv.FormatUint(p.Counter, 10), ""
			if d.Kind == metrics.Gauge {
				kind, cv, gv = "gauge", "", strconv.FormatUint(p.Gauge, 10)
			}
			row := []string{strconv.FormatUint(uint64(b.SchemaVersion), 10), b.NodeID, b.Region, b.BootID, strconv.FormatUint(b.Sequence, 10), strconv.FormatInt(b.StartUnixNano, 10), strconv.FormatInt(b.TimeUnixNano, 10), s.Component, p.Name, kind, d.Unit, string(ls), cv, gv}
			if err = w.Write(row); err != nil {
				return nil, err
			}
			rows++
		}
	}
	if rows == 0 {
		return nil, errors.New("CSV batch contains no points")
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}
func (a *csvAdapter) Publish(ctx context.Context, b metrics.Batch) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := metrics.Validate(b); err != nil {
		return err
	}
	if b.NodeID != a.id.NodeID || b.Region != a.id.Region || b.BootID != a.id.BootID || b.StartUnixNano != a.id.StartUnixNano {
		return errors.New("CSV batch identity mismatch")
	}
	data, err := csvEncode(b)
	if err != nil {
		return err
	}
	if int64(len(data)) > a.settings.MaxFileBytes {
		return errors.New("CSV batch exceeds max_file_bytes")
	}
	f, err := os.CreateTemp(a.settings.Directory, ".metrics-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if a.settings.Sync {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	name := fmt.Sprintf("metrics-%020d-%020d-%s.csv", time.Now().UnixNano(), b.Sequence, strings.TrimSuffix(filepath.Base(tmp), ".tmp"))
	if err = os.Rename(tmp, filepath.Join(a.settings.Directory, name)); err != nil {
		return err
	}
	files, err := csvFiles(a.settings.Directory)
	if err != nil {
		return err
	}
	for len(files) > a.settings.KeepFiles {
		if err = os.Remove(files[0]); err != nil {
			return err
		}
		files = files[1:]
	}
	return nil
}
func (a *csvAdapter) Shutdown(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	return nil
}
func csvFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".csv") {
			if !e.Type().IsRegular() {
				return nil, errors.New("CSV segment is not a regular file")
			}
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	slices.Sort(files)
	return files, nil
}
func ReadCSVDir(dir string) ([]metrics.Batch, error) {
	files, err := csvFiles(dir)
	if err != nil {
		return nil, err
	}
	var batches []metrics.Batch
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		b, err := csvDecode(io.LimitReader(f, csvMaxFileBytes+1))
		closeErr := f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if closeErr != nil {
			return nil, closeErr
		}
		batches = append(batches, b)
	}
	return batches, nil
}
func csvDecode(r io.Reader) (metrics.Batch, error) {
	var b metrics.Batch
	reader := csv.NewReader(r)
	header, err := reader.Read()
	if err != nil {
		return b, err
	}
	if !slices.Equal(header, csvHeader) {
		return b, errors.New("invalid CSV header")
	}
	reader.FieldsPerRecord = len(csvHeader)
	desc := map[string]metrics.Descriptor{}
	for _, d := range metrics.Catalogue() {
		desc[d.Name] = d
	}
	components := map[string]int{}
	var metadata []string
	count := 0
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return b, err
		}
		count++
		if count > metrics.MaxPoints {
			return b, errors.New("CSV exceeds point bound")
		}
		if metadata == nil {
			metadata = slices.Clone(row[:7])
			schema, e := strconv.ParseUint(row[0], 10, 32)
			if e != nil {
				return b, e
			}
			b.SchemaVersion = uint32(schema)
			b.NodeID, b.Region, b.BootID = row[1], row[2], row[3]
			b.Sequence, err = strconv.ParseUint(row[4], 10, 64)
			if err != nil {
				return b, err
			}
			b.StartUnixNano, err = strconv.ParseInt(row[5], 10, 64)
			if err != nil {
				return b, err
			}
			b.TimeUnixNano, err = strconv.ParseInt(row[6], 10, 64)
			if err != nil {
				return b, err
			}
		} else if !slices.Equal(metadata, row[:7]) {
			return b, errors.New("mixed CSV batch metadata")
		}
		d, ok := desc[row[8]]
		if !ok || d.Unit != row[10] {
			return b, errors.New("unknown CSV metric or unit")
		}
		p := metrics.Point{Name: row[8]}
		var labels map[string]string
		if err = json.Unmarshal([]byte(row[11]), &labels); err != nil || labels == nil {
			return b, errors.New("invalid CSV labels")
		}
		for k, v := range labels {
			p.Labels = append(p.Labels, metrics.Label{Name: k, Value: v})
		}
		slices.SortFunc(p.Labels, func(a, b metrics.Label) int { return strings.Compare(a.Name, b.Name) })
		if d.Kind == metrics.Counter && row[9] == "counter" && row[13] == "" {
			p.Counter, err = strconv.ParseUint(row[12], 10, 64)
		} else if d.Kind == metrics.Gauge && row[9] == "gauge" && row[12] == "" {
			p.Gauge, err = strconv.ParseUint(row[13], 10, 64)
		} else {
			return b, errors.New("invalid CSV kind/value columns")
		}
		if err != nil {
			return b, err
		}
		i, ok := components[row[7]]
		if !ok {
			i = len(b.Snapshots)
			components[row[7]] = i
			b.Snapshots = append(b.Snapshots, metrics.Snapshot{Component: row[7]})
		}
		b.Snapshots[i].Points = append(b.Snapshots[i].Points, p)
	}
	if count == 0 {
		return b, errors.New("empty CSV segment")
	}
	return b, metrics.Validate(b)
}
