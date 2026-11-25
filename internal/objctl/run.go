package objctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/storage"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
)

// ServiceOptions are handed to Env.NewService for one invocation.
type ServiceOptions struct {
	// Trace, when set (by --verbose), receives one line per HTTP request and
	// response.
	Trace func(line string)
}

// Env carries everything a Command touches outside its arguments, so Run can
// be driven in-process by tests.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Open reads a put source file; Create writes a get destination file.
	Open   func(name string) (io.ReadCloser, error)
	Create func(name string) (io.WriteCloser, error)
	// NewService connects to the node at addr.
	NewService func(addr string, o ServiceOptions) (httpapi.Service, error)
	// PollInterval spaces migrate status --wait polls (default 200ms).
	PollInterval time.Duration
}

// Run validates and executes cmd within cmd.Timeout, writes its output to
// e.Stdout and any error to e.Stderr as one "objctl: " line, and returns the
// exit code (see ExitCode).
func Run(ctx context.Context, cmd Command, e Env) int {
	err := run(ctx, cmd, e)
	if err != nil {
		fmt.Fprintf(e.Stderr, "objctl: %v\n", err)
	}
	return ExitCode(err)
}

func run(ctx context.Context, cmd Command, e Env) error {
	if err := cmd.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, cmd.Timeout)
	defer cancel()
	var o ServiceOptions
	if cmd.Verbose {
		o.Trace = func(line string) { fmt.Fprintf(e.Stderr, "* %s\n", line) }
	}
	svc, err := e.NewService(cmd.Addr, o)
	if err != nil {
		return Usagef("--addr %q: %v", cmd.Addr, err)
	}
	return Execute(ctx, svc, cmd, e)
}

type statusCoder interface{ StatusCode() int }

// ExitCode maps an Execute or Run error to the process exit code: ExitOK for
// nil and ExitUsage for ErrUsage. Otherwise the HTTP status decides: the one
// the error carries, or for an error that never reached a server, the status
// httpapi.StatusTable gives it, except that a local storage.ErrInvalid or
// env.ErrTooLarge (an input the client refused to send) is ExitUsage. Status
// 404 is ExitNotFound, 412 ExitPrecondition, and anything else ExitFailure.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, ErrUsage) {
		return ExitUsage
	}
	var sc statusCoder
	var status int
	switch {
	case errors.As(err, &sc):
		status = sc.StatusCode()
	case errors.Is(err, storage.ErrInvalid) || errors.Is(err, env.ErrTooLarge):
		return ExitUsage
	default:
		status = httpapi.StatusOf(httpapi.StatusTable(), err)
	}
	switch status {
	case 404:
		return ExitNotFound
	case 412:
		return ExitPrecondition
	}
	return ExitFailure
}

// readAll reads r up to limit bytes, sizing its buffer from r's Stat when r
// is a regular file.
func readAll(r io.Reader, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	if f, ok := r.(interface{ Stat() (fs.FileInfo, error) }); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode().IsRegular() && fi.Size() <= limit {
			buf.Grow(int(fi.Size()) + 1)
		}
	}
	if _, err := buf.ReadFrom(io.LimitReader(r, limit+1)); err != nil {
		return nil, err
	}
	if int64(buf.Len()) > limit {
		return nil, fmt.Errorf("input over %d bytes: %w", limit, env.ErrTooLarge)
	}
	return buf.Bytes(), nil
}

// show renders v, or returns err when the call that produced v failed: one
// JSON document when asJSON is set, and otherwise the human form produced by
// text into an aligned table.
func show[T any](out io.Writer, asJSON bool, v T, err error, text func(*tabwriter.Writer, T)) error {
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(out).Encode(v)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	text(w, v)
	return w.Flush()
}

// nonNil returns s, or an empty slice for nil, so --json prints [] not null.
func nonNil[T any](s []T, err error) ([]T, error) {
	if s == nil && err == nil {
		s = []T{}
	}
	return s, err
}

// Execute performs cmd against svc and renders its result to e.Stdout (and a
// get's bytes to e.Stdout or the --output file).
func Execute(ctx context.Context, svc httpapi.Service, cmd Command, e Env) error {
	out, j := e.Stdout, cmd.JSON
	switch cmd.Verb {
	case VerbHealth:
		h, err := svc.Health(ctx)
		return show(out, j, h, err, healthText)
	case VerbMembers:
		ms, err := nonNil(svc.Members(ctx))
		return show(out, j, ms, err, membersText)
	case VerbMakeBucket:
		spec := storage.BucketSpec{Name: cmd.Bucket, Region: cmd.Region, QuotaBytes: cmd.QuotaBytes, QuotaObjects: cmd.QuotaObjects}
		return show(out, j, spec, svc.CreateBucket(ctx, spec), func(w *tabwriter.Writer, s storage.BucketSpec) { fmt.Fprintf(w, "created bucket %s\n", s.Name) })
	case VerbList:
		objs, err := nonNil(svc.ListObjects(ctx, cmd.Bucket, cmd.Prefix, cmd.Limit))
		return show(out, j, objs, err, listText)
	case VerbPut:
		return put(ctx, svc, cmd, e)
	case VerbGet:
		return get(ctx, svc, cmd, e)
	case VerbHead:
		oi, err := svc.HeadObject(ctx, cmd.Bucket, cmd.Key)
		return show(out, j, oi, err, objectText)
	case VerbRemove:
		oi, err := svc.DeleteObject(ctx, cmd.Bucket, cmd.Key, cmd.Cond)
		return show(out, j, oi, err, deletedText)
	case VerbTagSet:
		ti, err := svc.PutTag(ctx, cmd.Bucket, cmd.Key, cmd.Name, cmd.Value, cmd.Cond)
		return show(out, j, ti, err, tagText)
	case VerbTagList:
		ts, err := nonNil(svc.Tags(ctx, cmd.Bucket, cmd.Key))
		return show(out, j, ts, err, func(w *tabwriter.Writer, ts []storage.TagInfo) {
			for _, t := range ts {
				tagText(w, t)
			}
		})
	case VerbACLSet:
		ai, err := svc.PutACL(ctx, cmd.Bucket, cmd.Principal, cmd.Perm, cmd.Cond)
		return show(out, j, ai, err, aclText)
	case VerbACLGet:
		ai, err := svc.ACL(ctx, cmd.Bucket, cmd.Principal)
		return show(out, j, ai, err, aclText)
	case VerbQuotaSet:
		qi, err := svc.SetQuota(ctx, cmd.Bucket, cmd.QuotaBytes, cmd.QuotaObjects)
		return show(out, j, qi, err, quotaText)
	case VerbQuotaGet:
		qi, err := svc.Quota(ctx, cmd.Bucket)
		return show(out, j, qi, err, quotaText)
	case VerbMigrateStart:
		mi, err := svc.StartMigration(ctx, cmd.Bucket, cmd.To)
		return show(out, j, mi, err, migrationText)
	case VerbMigrateStatus:
		return migrationStatus(ctx, svc, cmd, e)
	}
	return Usagef("unknown command %q", cmd.Verb)
}

func put(ctx context.Context, svc httpapi.Service, cmd Command, e Env) error {
	src := e.Stdin
	if cmd.File != "" {
		f, err := e.Open(cmd.File)
		if err != nil {
			return fmt.Errorf("open %s: %w", cmd.File, err)
		}
		defer func() { _ = f.Close() }()
		src = f
	}
	data, err := readAll(src, httpapi.DefaultMaxObjectSize)
	if err != nil {
		return err
	}
	oi, err := svc.PutObject(ctx, storage.PutRequest{Bucket: cmd.Bucket, Key: cmd.Key, Data: data, ContentType: cmd.ContentType, Cond: cmd.Cond})
	return show(e.Stdout, cmd.JSON, oi, err, objectText)
}

func get(ctx context.Context, svc httpapi.Service, cmd Command, e Env) error {
	oi, data, err := svc.GetObject(ctx, cmd.Bucket, cmd.Key)
	if err != nil {
		return err
	}
	if cmd.Output == "" {
		_, err := e.Stdout.Write(data)
		return err
	}
	f, err := e.Create(cmd.Output)
	if err != nil {
		return fmt.Errorf("create %s: %w", cmd.Output, err)
	}
	_, werr := f.Write(data)
	if err := errors.Join(werr, f.Close()); err != nil {
		return fmt.Errorf("write %s: %w", cmd.Output, err)
	}
	if cmd.JSON {
		return show(e.Stdout, true, oi, nil, objectText)
	}
	return nil
}

func migrationStatus(ctx context.Context, svc httpapi.Service, cmd Command, e Env) error {
	every := e.PollInterval
	if every <= 0 {
		every = 200 * time.Millisecond
	}
	for {
		mi, err := svc.Migration(ctx, cmd.ID)
		if err != nil {
			return err
		}
		done := mi.State == "completed" || mi.State == "compensated"
		if !cmd.Wait || done {
			if err := show(e.Stdout, cmd.JSON, mi, nil, migrationText); err != nil {
				return err
			}
			if cmd.Wait && mi.State == "compensated" {
				return fmt.Errorf("migration %s compensated: %s", mi.ID, mi.Error)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("migration %s still %s: %w", mi.ID, mi.State, ctx.Err())
		case <-time.After(every):
		}
	}
}

func healthText(w *tabwriter.Writer, h storage.Health) {
	fmt.Fprintf(w, "node\t%s\nregion\t%s\nready\t%v\nlive members\t%d\nmeta digest\t%s\nmeta root\t%s\ndiscards\t%d\n",
		h.Node, h.Region, h.Ready, h.LiveMembers, h.MetaDigest, h.MetaRoot, h.Discards)
}

func membersText(w *tabwriter.Writer, ms []storage.MemberInfo) {
	fmt.Fprintln(w, "ID\tREGION\tSTATE\tINCARNATION")
	for _, m := range ms {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", m.ID, m.Region, m.State, m.Incarnation)
	}
}

func listText(w *tabwriter.Writer, objs []storage.ObjectInfo) {
	for _, o := range objs {
		fmt.Fprintf(w, "%d\t%s\t%s\n", o.Size, o.ETag, Printable(o.Key))
	}
}

func objectText(w *tabwriter.Writer, oi storage.ObjectInfo) {
	fmt.Fprintf(w, "object\t%s/%s\nsize\t%d\netag\t%s\nversion\t%s\nsha256\t%s\ncontent type\t%s\n",
		oi.Bucket, Printable(oi.Key), oi.Size, oi.ETag, oi.Version, oi.ContentSHA, Printable(oi.ContentType))
}

func deletedText(w *tabwriter.Writer, oi storage.ObjectInfo) {
	fmt.Fprintf(w, "deleted %s/%s\ntombstone etag\t%s\n", oi.Bucket, Printable(oi.Key), oi.ETag)
}

func tagText(w *tabwriter.Writer, t storage.TagInfo) {
	fmt.Fprintf(w, "%s\t%s\t%s\n", Printable(t.Name), Printable(t.Value), t.ETag)
}

func aclText(w *tabwriter.Writer, ai storage.ACLInfo) {
	fmt.Fprintf(w, "%s\t%s\t%s\n", Printable(ai.Principal), ai.Perm, ai.ETag)
}

func quotaText(w *tabwriter.Writer, qi storage.QuotaInfo) {
	fmt.Fprintf(w, "bucket\t%s\nbytes\t%d used of %s\nobjects\t%d used of %s\n", qi.Bucket, qi.UsedBytes, limitText(qi.LimitBytes), qi.UsedObjects, limitText(qi.LimitObjects))
}

func migrationText(w *tabwriter.Writer, mi storage.MigrationInfo) {
	fmt.Fprintf(w, "id\t%s\nbucket\t%s\nfrom\t%s\nto\t%s\nstate\t%s\nstep\t%d\nattempt\t%d\n", mi.ID, mi.Bucket, mi.From, mi.To, mi.State, mi.Step, mi.Attempt)
	if mi.Outcome != "" {
		fmt.Fprintf(w, "outcome\t%s\n", mi.Outcome)
	}
	if mi.Error != "" {
		fmt.Fprintf(w, "error\t%s\n", mi.Error)
	}
}

func limitText(n uint64) string {
	if n == 0 {
		return "unlimited"
	}
	return strconv.FormatUint(n, 10)
}

// Printable returns s unchanged when it is valid UTF-8 made of printable
// characters (spaces allowed), and Go-quoted otherwise, so keys holding tabs,
// newlines or invalid bytes cannot break a line of human output.
func Printable(s string) string {
	if !utf8.ValidString(s) {
		return strconv.Quote(s)
	}
	for _, r := range s {
		if !unicode.IsPrint(r) && r != ' ' {
			return strconv.Quote(s)
		}
	}
	return s
}
