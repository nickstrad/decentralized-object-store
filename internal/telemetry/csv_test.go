package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/nickstrad/object-store/internal/metrics"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func openCSV(t *testing.T, dir string, extra map[string]any) *csvAdapter {
	t.Helper()
	settings := map[string]any{"directory": dir, "sync": false}
	for k, v := range extra {
		settings[k] = v
	}
	raw, _ := json.Marshal(settings)
	a, err := Open(context.Background(), Config{Enabled: true, Adapter: "csv", Settings: raw}, Identity{"n", "r", "b", 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	return a.(*csvAdapter)
}
func TestCSVExactRotationAndCrashTemp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "csv")
	a := openCSV(t, dir, map[string]any{"keep_files": 2, "sync": true})
	b := testBatch(a.id)
	b.Snapshots[0].Points[0].Counter = math.MaxUint64
	// Use HTTP counter and gauge together to verify exact integer/null representation.
	b.Snapshots[0].Points[0].Counter = 0
	b.Snapshots = append(b.Snapshots, structSnapshot())
	for i := uint64(1); i <= 3; i++ {
		b.Sequence = i
		if err := a.Publish(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "crash.tmp"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCSVDir(dir)
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	if got[0].Sequence != 2 || got[1].Sequence != 3 {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(got[1], b) {
		t.Fatalf("roundtrip\ngot %#v\nwant %#v", got[1], b)
	}
	files, _ := csvFiles(dir)
	for _, f := range files {
		st, _ := os.Stat(f)
		if st.Mode().Perm() != 0600 {
			t.Fatal(st.Mode())
		}
		data, _ := os.ReadFile(f)
		if !bytes.HasPrefix(data, []byte(strings.Join(csvHeader, ","))) {
			t.Fatal("missing header")
		}
		if !bytes.Contains(data, []byte("18446744073709551615")) {
			t.Fatal("lost uint64")
		}
	}
	a.settings.MaxFileBytes = 1
	if a.Publish(context.Background(), b) == nil {
		t.Fatal("oversized accepted")
	}
	after, _ := csvFiles(dir)
	if !reflect.DeepEqual(files, after) {
		t.Fatal("failure rotated files")
	}
	a.settings.MaxFileBytes = 16 << 20
	b.NodeID = "wrong"
	if a.Publish(context.Background(), b) == nil {
		t.Fatal("identity mismatch")
	}
}
func TestCSVConfigAndMalformed(t *testing.T) {
	for _, raw := range []string{`{}`, `{"directory":"x","unknown":1}`, `{"directory":"x","keep_files":0}`, `{"directory":"x","max_file_bytes":67108865}`, `{"directory":"x","sync":"yes"}`} {
		if _, err := newCSV(context.Background(), Identity{}, json.RawMessage(raw)); err == nil {
			t.Fatal(raw)
		}
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"directory": dir})
	if _, err := newCSV(context.Background(), Identity{}, raw); err == nil {
		t.Fatal("public directory")
	}
	b := testBatch(Identity{"n", "r", "b", 1})
	data, err := csvEncode(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{[]byte("wrong\n"), []byte(strings.Join(csvHeader, ",") + "\n"), bytes.Replace(data, []byte(",gauge,"), []byte(",counter,"), 1), bytes.Replace(data, []byte(",node,"), []byte(",other,"), 1)} {
		if _, err := csvDecode(bytes.NewReader(bad)); err == nil {
			t.Fatal("malformed accepted", string(bad))
		}
	}
}

func structSnapshot() metrics.Snapshot {
	return metrics.Snapshot{Component: "http", Points: []metrics.Point{{Name: "objstore_http_request_bytes_total", Counter: math.MaxUint64}, {Name: "objstore_http_requests_in_flight", Gauge: math.MaxUint64}}}
}
