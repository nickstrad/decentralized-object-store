package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/storage"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
	"github.com/nickstrad/object-store/internal/storage/httpapi/httpapitest"
)

// recorder is a fake httpapi.Service that records the arguments of the one
// call it receives and answers with programmed results.
type recorder struct {
	calls int
	op    string
	args  []any
	err   error
	oi    storage.ObjectInfo
	data  []byte
	ois   []storage.ObjectInfo
	tag   storage.TagInfo
	tags  []storage.TagInfo
	acl   storage.ACLInfo
	quota storage.QuotaInfo
	mig   storage.MigrationInfo
	ms    []storage.MemberInfo
	h     storage.Health
}

func (r *recorder) rec(op string, args ...any) {
	r.calls++
	r.op, r.args = op, append([]any{}, args...)
}

func (r *recorder) CreateBucket(_ context.Context, spec storage.BucketSpec) error {
	r.rec("CreateBucket", spec)
	return r.err
}
func (r *recorder) ListObjects(_ context.Context, bucket, prefix string, limit int) ([]storage.ObjectInfo, error) {
	r.rec("ListObjects", bucket, prefix, limit)
	return r.ois, r.err
}
func (r *recorder) PutObject(_ context.Context, req storage.PutRequest) (storage.ObjectInfo, error) {
	r.rec("PutObject", req)
	return r.oi, r.err
}
func (r *recorder) GetObject(_ context.Context, bucket, key string) (storage.ObjectInfo, []byte, error) {
	r.rec("GetObject", bucket, key)
	return r.oi, r.data, r.err
}
func (r *recorder) HeadObject(_ context.Context, bucket, key string) (storage.ObjectInfo, error) {
	r.rec("HeadObject", bucket, key)
	return r.oi, r.err
}
func (r *recorder) DeleteObject(_ context.Context, bucket, key string, cond storage.Condition) (storage.ObjectInfo, error) {
	r.rec("DeleteObject", bucket, key, cond)
	return r.oi, r.err
}
func (r *recorder) PutTag(_ context.Context, bucket, key, name, value string, cond storage.Condition) (storage.TagInfo, error) {
	r.rec("PutTag", bucket, key, name, value, cond)
	return r.tag, r.err
}
func (r *recorder) Tags(_ context.Context, bucket, key string) ([]storage.TagInfo, error) {
	r.rec("Tags", bucket, key)
	return r.tags, r.err
}
func (r *recorder) PutACL(_ context.Context, bucket, principal, perm string, cond storage.Condition) (storage.ACLInfo, error) {
	r.rec("PutACL", bucket, principal, perm, cond)
	return r.acl, r.err
}
func (r *recorder) ACL(_ context.Context, bucket, principal string) (storage.ACLInfo, error) {
	r.rec("ACL", bucket, principal)
	return r.acl, r.err
}
func (r *recorder) Quota(_ context.Context, bucket string) (storage.QuotaInfo, error) {
	r.rec("Quota", bucket)
	return r.quota, r.err
}
func (r *recorder) SetQuota(_ context.Context, bucket string, bytes, objects uint64) (storage.QuotaInfo, error) {
	r.rec("SetQuota", bucket, bytes, objects)
	return r.quota, r.err
}
func (r *recorder) StartMigration(_ context.Context, bucket, to string) (storage.MigrationInfo, error) {
	r.rec("StartMigration", bucket, to)
	return r.mig, r.err
}
func (r *recorder) Migration(_ context.Context, id string) (storage.MigrationInfo, error) {
	r.rec("Migration", id)
	return r.mig, r.err
}
func (r *recorder) Members(context.Context) ([]storage.MemberInfo, error) {
	r.rec("Members")
	return r.ms, r.err
}
func (r *recorder) Health(context.Context) (storage.Health, error) {
	r.rec("Health")
	return r.h, r.err
}

// headerSafe keeps the visible ASCII bytes of s other than a double quote: the
// shape of the hex ETags, versions and digests a real node puts in headers.
func headerSafe(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if c := s[i]; c > 0x20 && c < 0x7f && c != '"' {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// sendableContentType restates the content-type rule independently of the
// client: at most 255 bytes, no control byte but tab, no surrounding blanks.
func sendableContentType(s string) bool {
	if len(s) > 255 || strings.Trim(s, " \t") != s {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return r != '\t' && (r < 0x20 || r == 0x7f) })
}

// viaJSON returns v after a JSON encode and decode, which is what the server's
// JSON responses can carry (invalid UTF-8 becomes U+FFFD).
func viaJSON[T any](t *testing.T, v T) T {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func normCond(c storage.Condition) storage.Condition {
	if len(c.IfMatchAny) == 0 {
		c.IfMatchAny = nil
	}
	return c
}

func normArgs(args []any) []any {
	out := slices.Clone(args)
	for i, a := range out {
		switch v := a.(type) {
		case storage.Condition:
			out[i] = normCond(v)
		case storage.PutRequest:
			v.Cond = normCond(v.Cond)
			if len(v.Data) == 0 {
				v.Data = nil
			}
			out[i] = v
		}
	}
	return out
}

var sentinels = func() []error {
	var out []error
	for _, r := range httpapi.StatusTable() {
		if !slices.Contains(out, r.Err) {
			out = append(out, r.Err)
		}
	}
	return out
}()

const opCount = 16

var nastyKeys = []string{
	"a", "a/b", "/lead", "trail/", "//", "a//b", ".", "..", "./x", "../x", "a/../b", "a/./b", "%", "%2F", "%zz", "a%2fb",
	"?", "q?x=1&y=2", "#frag", "a#b?c", " ", "  sp  ", "tab\there", "nl\nx", "cr\rx", "\x00", "\x7f", "ü", "日本語/キー",
	"\xff\xfe", "+plus+", "semi;colon", "a=b&c=d", "~tilde", "*", "\"quoted\"", "back\\slash", strings.Repeat("k", storage.MaxKeyLen),
	strings.Repeat("k", storage.MaxKeyLen+1), "",
}

// FuzzRoundTrip drives every Client method through the real httpapi handler
// over an in-process HTTP/1.1 encoding into a recording fake Service. For each
// call either the client rejects the input locally (and the expected rule
// says it must), or the fake receives exactly the arguments the client was
// given (conditions in their Canonical form, a list limit <= 0 as
// httpapi.MaxListLimit) and the client returns exactly what the fake returned
// as the wire can carry it; a sentinel error from the fake comes back as an
// HTTPError with httpapi.StatusOf's status that errors.Is the sentinel.
func FuzzRoundTrip(f *testing.F) {
	for i, k := range nastyKeys {
		f.Add(uint8(i), "bkt", k, "text/plain", "v", []byte("data"), uint64(i), uint64(7), uint8(i), "abc\nd,e", uint8(0), "")
		f.Add(uint8(i+5), "b-1.x", k, k, k, []byte(k), uint64(1<<63), uint64(0), uint8(i*7), k, uint8(130+i), k)
	}
	for _, c := range []string{"*", `"a"`, `W/"a"`, "a b", `a"b`, "", ",", "\x80\xff", "\x7f", "x\ty"} {
		for op := range uint8(opCount) {
			f.Add(op, "bkt", "k", c, " r ", []byte(nil), uint64(0), uint64(0), uint8(0x1e), c+"\n"+c, uint8(200), "x")
		}
	}
	f.Add(uint8(1), "bkt", "k", "pre", "", []byte(nil), uint64(10001), uint64(0), uint8(0), "", uint8(0), "")
	f.Add(uint8(13), "bkt", "k", "", "", []byte(nil), uint64(0), uint64(0), uint8(0), "", uint8(0), "")
	f.Add(uint8(12), "\xff", "k", "", "r1", []byte(nil), uint64(0), uint64(0), uint8(0), "", uint8(0), "")
	f.Add(uint8(0), "Bad", "k", "", "", []byte(nil), uint64(0), uint64(0), uint8(0), "", uint8(0), "")
	f.Fuzz(func(t *testing.T, op uint8, bucket, key, s1, s2 string, data []byte, n1, n2 uint64, cb uint8, tags string, errSel uint8, errMsg string) {
		fake := &recorder{}
		var sentinel error
		if errSel >= 128 {
			i := int(errSel) % (len(sentinels) + 1)
			if i == len(sentinels) {
				fake.err = errors.New(errMsg)
			} else {
				sentinel = sentinels[i]
				fake.err = fmt.Errorf("%s: %w", errMsg, sentinel)
			}
		}
		etag, ver, sha := headerSafe(s1+"e"), headerSafe(s2+"v"), headerSafe(errMsg)
		fake.oi = storage.ObjectInfo{Bucket: bucket, Key: key, Size: int64(len(data)), ETag: etag, Version: ver, ContentSHA: sha, ContentType: headerSafe(s2), Deleted: n1%2 == 0}
		fake.data = data
		fake.ois = []storage.ObjectInfo{fake.oi, {Key: s1, Size: int64(n1 >> 1)}}
		if n2%3 == 0 {
			fake.ois = nil
		}
		fake.tag = storage.TagInfo{Name: s1, Value: s2, ETag: etag}
		fake.tags = []storage.TagInfo{fake.tag}
		if n2%5 == 0 {
			fake.tags = nil
		}
		fake.acl = storage.ACLInfo{Principal: s1, Perm: s2, ETag: etag}
		fake.quota = storage.QuotaInfo{Bucket: bucket, LimitBytes: n1, LimitObjects: n2, UsedBytes: n2, UsedObjects: n1}
		fake.mig = storage.MigrationInfo{ID: s1, Bucket: bucket, From: s2, To: key, State: "running", Step: int(int8(n1)), Attempt: int(cb), Outcome: errMsg}
		fake.ms = []storage.MemberInfo{{ID: s1, Region: s2, State: "alive", Incarnation: uint32(n1)}}
		fake.h = storage.Health{Node: s1, Region: s2, Ready: n1%2 == 1, LiveMembers: int(int16(n2)), Discards: n1,
			RecentDiscards: []storage.DiscardInfo{{Key: key, Winner: s1, Loser: s2}}, Counters: []storage.Counter{{Name: s1, Value: n2}}}

		var tagList []string
		if tags != "" {
			tagList = strings.Split(tags, "\n")
		}
		cond := storage.Condition{IfNoneMatch: cb&1 != 0, IfMatchList: cb&2 != 0}
		if cb&4 != 0 {
			cond.IfMatch = s1
			if cb&8 != 0 {
				cond.IfMatch = "*"
			}
		}
		if cb&16 != 0 {
			cond.IfMatchAny = tagList
		}
		wc, condErr := wireCondition(cond)

		c, err := New("objstore.test:8080", Options{Transport: httpapitest.InProcess(httpapi.NewHandler(fake, httpapi.Limits{}))})
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		badBucket := storage.ValidBucket(bucket) != nil
		badKey := badBucket || key == "" || len(key) > storage.MaxKeyLen
		limit := int(int16(n1))
		var (
			reject   bool
			wantArgs []any
			got      any
			want     any
			opErr    error
			gotData  []byte
		)
		switch op % opCount {
		case 0:
			reject = badBucket
			wantArgs = []any{storage.BucketSpec{Name: bucket, Region: s1, QuotaBytes: n1, QuotaObjects: n2}}
			opErr = c.CreateBucket(ctx, storage.BucketSpec{Name: bucket, Region: s1, QuotaBytes: n1, QuotaObjects: n2})
		case 1:
			reject = badBucket || limit > httpapi.MaxListLimit
			wl := limit
			if wl <= 0 {
				wl = httpapi.MaxListLimit
			}
			wantArgs = []any{bucket, s1, wl}
			w := fake.ois
			if w == nil {
				w = []storage.ObjectInfo{}
			}
			want = viaJSON(t, w)
			got, opErr = c.ListObjects(ctx, bucket, s1, limit)
		case 2:
			reject = badKey || condErr != nil || !sendableContentType(s1)
			wantArgs = []any{storage.PutRequest{Bucket: bucket, Key: key, Data: data, ContentType: s1, Cond: wc}}
			want = viaJSON(t, fake.oi)
			got, opErr = c.PutObject(ctx, storage.PutRequest{Bucket: bucket, Key: key, Data: data, ContentType: s1, Cond: cond})
		case 3:
			reject = badKey
			wantArgs = []any{bucket, key}
			w := storage.ObjectInfo{Bucket: bucket, Key: key, Size: int64(len(data)), ETag: etag, Version: ver, ContentSHA: sha, ContentType: fake.oi.ContentType}
			if w.ContentType == "" {
				w.ContentType = "application/octet-stream"
			}
			want = w
			var oi storage.ObjectInfo
			oi, gotData, opErr = c.GetObject(ctx, bucket, key)
			got = oi
		case 4:
			reject = badKey
			wantArgs = []any{bucket, key}
			want = viaJSON(t, fake.oi)
			got, opErr = c.HeadObject(ctx, bucket, key)
		case 5:
			reject = badKey || condErr != nil
			wantArgs = []any{bucket, key, wc}
			want = storage.ObjectInfo{Bucket: bucket, Key: key, ETag: etag, Version: ver, ContentSHA: sha, Deleted: true}
			got, opErr = c.DeleteObject(ctx, bucket, key, cond)
		case 6:
			reject = badKey || condErr != nil || len(s2) > httpapi.DefaultMaxSmallBody
			wantArgs = []any{bucket, key, s1, s2, wc}
			want = viaJSON(t, fake.tag)
			got, opErr = c.PutTag(ctx, bucket, key, s1, s2, cond)
		case 7:
			reject = badKey
			wantArgs = []any{bucket, key}
			w := fake.tags
			if w == nil {
				w = []storage.TagInfo{}
			}
			want = viaJSON(t, w)
			got, opErr = c.Tags(ctx, bucket, key)
		case 8:
			reject = badBucket || condErr != nil || strings.TrimSpace(s2) != s2 || len(s2) > httpapi.DefaultMaxSmallBody
			wantArgs = []any{bucket, s1, s2, wc}
			want = viaJSON(t, fake.acl)
			got, opErr = c.PutACL(ctx, bucket, s1, s2, cond)
		case 9:
			reject = badBucket
			wantArgs = []any{bucket, s1}
			want = viaJSON(t, fake.acl)
			got, opErr = c.ACL(ctx, bucket, s1)
		case 10:
			reject = badBucket
			wantArgs = []any{bucket}
			want = viaJSON(t, fake.quota)
			got, opErr = c.Quota(ctx, bucket)
		case 11:
			reject = badBucket
			wantArgs = []any{bucket, n1, n2}
			want = viaJSON(t, fake.quota)
			got, opErr = c.SetQuota(ctx, bucket, n1, n2)
		case 12:
			mb, _ := json.Marshal(map[string]string{"bucket": bucket, "to": s2})
			reject = !utf8.ValidString(bucket) || !utf8.ValidString(s2) || len(mb) > httpapi.DefaultMaxSmallBody
			wantArgs = []any{bucket, s2}
			want = viaJSON(t, fake.mig)
			got, opErr = c.StartMigration(ctx, bucket, s2)
		case 13:
			reject = s1 == ""
			wantArgs = []any{s1}
			want = viaJSON(t, fake.mig)
			got, opErr = c.Migration(ctx, s1)
		case 14:
			wantArgs = []any{}
			want = viaJSON(t, fake.ms)
			got, opErr = c.Members(ctx)
		case 15:
			wantArgs = []any{}
			want = viaJSON(t, fake.h)
			got, opErr = c.Health(ctx)
		}

		if reject {
			if fake.calls != 0 {
				t.Fatalf("op %d: input must be rejected locally, but the service received %s%q", op%opCount, fake.op, fake.args)
			}
			var he *HTTPError
			if opErr == nil || errors.As(opErr, &he) || !(errors.Is(opErr, storage.ErrInvalid) || errors.Is(opErr, env.ErrTooLarge)) {
				t.Fatalf("op %d: want a local ErrInvalid rejection, got %v", op%opCount, opErr)
			}
			return
		}
		if fake.calls != 1 {
			t.Fatalf("op %d: service received %d calls, want 1 (client error %v)", op%opCount, fake.calls, opErr)
		}
		if g, w := normArgs(fake.args), normArgs(wantArgs); !reflect.DeepEqual(g, w) {
			t.Fatalf("op %d %s: service received\n  %#v\nwant\n  %#v", op%opCount, fake.op, g, w)
		}
		if fake.err != nil {
			var he *HTTPError
			if !errors.As(opErr, &he) {
				t.Fatalf("op %d: service error %v came back as %v, want an HTTPError", op%opCount, fake.err, opErr)
			}
			if ws := httpapi.StatusOf(httpapi.StatusTable(), fake.err); he.Status != ws {
				t.Fatalf("op %d: status %d for %v, want %d", op%opCount, he.Status, fake.err, ws)
			}
			if sentinel != nil && !errors.Is(opErr, sentinel) {
				t.Fatalf("op %d: %v does not match sentinel %v", op%opCount, opErr, sentinel)
			}
			return
		}
		if opErr != nil {
			t.Fatalf("op %d %s: unexpected error %v", op%opCount, fake.op, opErr)
		}
		if want != nil && !reflect.DeepEqual(got, want) {
			t.Fatalf("op %d %s: client returned\n  %#v\nwant\n  %#v", op%opCount, fake.op, got, want)
		}
		if op%opCount == 3 && !bytes.Equal(gotData, data) {
			t.Fatalf("get returned %q, want %q", gotData, data)
		}
	})
}

// endless yields prefix and then, if more is set, an unbounded stream of 'x',
// counting every byte handed out.
type endless struct {
	prefix []byte
	more   bool
	n      int64
	closed bool
}

func (e *endless) Read(p []byte) (int, error) {
	if len(e.prefix) > 0 {
		k := copy(p, e.prefix)
		e.prefix = e.prefix[k:]
		e.n += int64(k)
		return k, nil
	}
	if !e.more {
		return 0, io.EOF
	}
	for i := range p {
		p[i] = 'x'
	}
	e.n += int64(len(p))
	return len(p), nil
}

func (e *endless) Close() error { e.closed = true; return nil }

type canned struct {
	status   int
	header   http.Header
	body     *endless
	declared int64
	sent     bool
}

func (c *canned) RoundTrip(req *http.Request) (*http.Response, error) {
	c.sent = true
	return &http.Response{StatusCode: c.status, Header: c.header, Body: c.body, ContentLength: c.declared, Request: req}, nil
}

// FuzzResponses feeds arbitrary statuses, headers, declared lengths (true,
// false or unknown) and (possibly endless) bodies to every Client method: the client never panics, never returns a nil
// error for a non-2xx status, closes the body, and never reads past its bound.
func FuzzResponses(f *testing.F) {
	f.Add(uint8(0), 200, "ETag", `"abc"`, []byte(`{"Bucket":"b"}`), false, int64(-1))
	f.Add(uint8(3), 200, "Content-Length", "5", []byte("abc"), false, int64(-1))
	f.Add(uint8(1), 404, "Content-Type", "application/json", []byte(`{"error":"storage: no such bucket","status":404}`), false, int64(-1))
	f.Add(uint8(15), 503, "", "", []byte("<html>busy</html>"), true, int64(-1))
	f.Add(uint8(14), 302, "Location", "http://elsewhere/", []byte(nil), false, int64(-1))
	f.Add(uint8(2), 204, "", "", []byte(nil), true, int64(-1))
	f.Add(uint8(7), 199, "", "", []byte("null"), false, int64(-1))
	f.Add(uint8(10), 200, "", "", []byte(`{"Bucket":"b"}`), false, int64(14))
	f.Add(uint8(10), 200, "", "", []byte(`{"Bucket":"b"}`), true, int64(14))
	f.Add(uint8(3), 200, "", "", []byte("abc"), false, int64(1<<20))
	f.Add(uint8(3), 200, "", "", []byte("abcdef"), false, int64(2))
	f.Add(uint8(14), 500, "", "", []byte("err"), true, int64(1<<20))
	f.Add(uint8(9), 600, "", "", []byte(`{"error":`), false, int64(-1))
	f.Fuzz(func(t *testing.T, op uint8, status int, hk, hv string, body []byte, more bool, declared int64) {
		const small, big, resp = 256, 1024, 4096
		e := &endless{prefix: body, more: more}
		h := http.Header{}
		if hk != "" {
			h[hk] = []string{hv}
		}
		if declared < -1 {
			declared = -1
		}
		rt := &canned{status: status, header: h, body: e, declared: declared}
		c, err := New("h:1", Options{Transport: rt, MaxSmallBody: small, MaxObjectSize: big, MaxResponse: resp})
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		cond := storage.Condition{}
		var opErr error
		switch op % opCount {
		case 0:
			opErr = c.CreateBucket(ctx, storage.BucketSpec{Name: "b"})
		case 1:
			_, opErr = c.ListObjects(ctx, "b", "", 0)
		case 2:
			_, opErr = c.PutObject(ctx, storage.PutRequest{Bucket: "b", Key: "k", Data: body})
		case 3:
			_, _, opErr = c.GetObject(ctx, "b", "k")
		case 4:
			_, opErr = c.HeadObject(ctx, "b", "k")
		case 5:
			_, opErr = c.DeleteObject(ctx, "b", "k", cond)
		case 6:
			_, opErr = c.PutTag(ctx, "b", "k", "n", "v", cond)
		case 7:
			_, opErr = c.Tags(ctx, "b", "k")
		case 8:
			_, opErr = c.PutACL(ctx, "b", "p", "r", cond)
		case 9:
			_, opErr = c.ACL(ctx, "b", "p")
		case 10:
			_, opErr = c.Quota(ctx, "b")
		case 11:
			_, opErr = c.SetQuota(ctx, "b", 1, 2)
		case 12:
			_, opErr = c.StartMigration(ctx, "b", "r")
		case 13:
			_, opErr = c.Migration(ctx, "m")
		case 14:
			_, opErr = c.Members(ctx)
		case 15:
			_, opErr = c.Health(ctx)
		}
		if (status < 200 || status > 299) && opErr == nil {
			t.Fatalf("op %d: status %d returned no error", op%opCount, status)
		}
		if !rt.sent {
			if opErr == nil {
				t.Fatalf("op %d: nothing sent and no error", op%opCount)
			}
			return
		}
		if !e.closed {
			t.Fatalf("op %d: response body not closed", op%opCount)
		}
		if bound := int64(resp + 1); e.n > bound {
			t.Fatalf("op %d: read %d body bytes, bound %d", op%opCount, e.n, bound)
		}
		if more && opErr == nil {
			t.Fatalf("op %d: endless body accepted", op%opCount)
		}
	})
}
