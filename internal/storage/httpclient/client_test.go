package httpclient

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/storage"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
	"github.com/nickstrad/object-store/internal/storage/httpapi/httpapitest"
)

func TestFallbacksAgreeWithStatusTable(t *testing.T) {
	rows := httpapi.StatusTable()
	statuses := map[int]bool{}
	for _, r := range rows {
		statuses[r.Status] = true
	}
	for st := range statuses {
		for _, f := range fallback(st) {
			if got, ok := statusFor(rows, f); !ok || got != st {
				t.Errorf("fallback %v for %d maps to %d in StatusTable", f, st, got)
			}
		}
		if st != http.StatusInternalServerError && len(fallback(st)) == 0 {
			t.Errorf("status %d from StatusTable has no fallback sentinel", st)
		}
	}
	if len(fallback(http.StatusInternalServerError)) != 0 {
		t.Error("500 must have no fallback")
	}
}

func TestHTTPErrorIs(t *testing.T) {
	rows := httpapi.StatusTable()
	cases := []struct {
		status int
		msg    string
		is     []error
		isNot  []error
	}{
		{404, "node: bucket \"x\": storage: no such bucket", []error{storage.ErrNoSuchBucket}, []error{storage.ErrNotFound, occ.ErrNotFound}},
		{404, "node: object b/k: occ: not found", []error{occ.ErrNotFound}, []error{storage.ErrNoSuchBucket}},
		{404, "<html>gone</html>", []error{storage.ErrNotFound, occ.ErrNotFound}, []error{storage.ErrNoSuchBucket}},
		{412, "occ: precondition failed", []error{occ.ErrPreconditionFailed}, []error{storage.ErrInvalid}},
		{503, "node: bucket \"b\" is moving: storage: bucket busy", []error{storage.ErrBusy}, []error{storage.ErrUnavailable}},
		{500, "storage: busy text but wrong status: storage: bucket busy", nil, []error{storage.ErrBusy, storage.ErrCorrupt}},
	}
	for _, c := range cases {
		e := &HTTPError{Status: c.status, Message: c.msg, rows: rows}
		for _, s := range c.is {
			if !errors.Is(e, s) {
				t.Errorf("%d %q: want Is %v", c.status, c.msg, s)
			}
		}
		for _, s := range c.isNot {
			if errors.Is(e, s) {
				t.Errorf("%d %q: want not Is %v", c.status, c.msg, s)
			}
		}
	}
}

func TestNewAddress(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:8080", "http://h:1", "https://h", "http://h:1/"} {
		if _, err := New(ok, Options{}); err != nil {
			t.Errorf("New(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ftp://h", "http://h/x", "http://u@h", "http://h?x", "http://"} {
		if _, err := New(bad, Options{}); !errors.Is(err, storage.ErrInvalid) {
			t.Errorf("New(%q) = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestTraceLines(t *testing.T) {
	var lines []string
	fake := &recorder{oi: storage.ObjectInfo{ETag: "e1", Version: "v1"}}
	c, err := New("h:1", Options{Transport: httpapitest.InProcess(httpapi.NewHandler(fake, httpapi.Limits{})), Trace: func(l string) { lines = append(lines, l) }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.PutObject(context.Background(), storage.PutRequest{Bucket: "b", Key: "a b/?", Data: []byte("xyz"), ContentType: "text/plain",
		Cond: storage.Condition{IfMatchList: true, IfMatchAny: []string{"e0"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("trace lines %q, want 2", lines)
	}
	for _, want := range []string{`> PUT /b/a%20b/%3F [If-Match: "\"e0\"" Content-Type: "text/plain"] body=3B`, `< 200 OK etag="e1" version=v1`} {
		if !strings.Contains(strings.Join(lines, "\n"), want) {
			t.Errorf("trace %q lacks %q", lines, want)
		}
	}
}
