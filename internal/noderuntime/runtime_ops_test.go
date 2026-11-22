package noderuntime_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/noderuntime"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

func TestShutdownDuringRepairAndRestartOnTheSameDisk(t *testing.T) {
	cfg := realConfig()
	var ids []env.NodeID
	for range 3 {
		ids = append(ids, env.NodeID(freeAddr(t)))
	}
	cfg.Seeds = ids
	dirs := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	start := func(i int) *noderuntime.Runtime {
		rt, err := noderuntime.NewRuntime(noderuntime.RuntimeConfig{ID: ids[i], Region: env.Region(fmt.Sprintf("r%d", i)), DataDir: dirs[i], Node: cfg})
		if err != nil {
			t.Fatal(err)
		}
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		return rt
	}
	c := &realCluster{rts: []*noderuntime.Runtime{start(0), start(1), start(2)}}
	bg := context.Background()
	eventually(t, 20*time.Second, "joined", func() bool { return c.joined(3) })
	if err := c.rts[0].CreateBucket(bg, storage.BucketSpec{Name: "b"}); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("r"), 200_000)
	if _, err := c.rts[0].PutObject(bg, storage.PutRequest{Bucket: "b", Key: "k", Data: data}); err != nil {
		t.Fatal(err)
	}
	var shards []string
	if err := filepath.WalkDir(filepath.Join(dirs[1], "shards"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			shards = append(shards, p)
		}
		return err
	}); err != nil || len(shards) == 0 {
		t.Fatalf("node 1 holds no shard: %v", err)
	}
	for _, p := range shards {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.rts[1].Close(); err != nil {
		t.Fatal(err)
	}
	c.rts[1] = start(1)
	t.Cleanup(func() {
		for _, rt := range c.rts {
			if err := rt.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	eventually(t, 30*time.Second, "the restarted node rebuilds its shards", func() bool {
		for _, p := range shards {
			if _, err := os.Stat(p); err != nil {
				return false
			}
		}
		return true
	})
	eventually(t, 30*time.Second, "the restarted node serves the object", func() bool {
		_, got, err := c.rts[1].GetObject(bg, "b", "k")
		return err == nil && bytes.Equal(got, data)
	})
}

func TestStatusRowsThroughHTTPOnARealNode(t *testing.T) {
	cfg := realConfig()
	cfg.MaxObjectSize = 1000
	c := startReal(t, 3, cfg)
	bg := context.Background()
	eventually(t, 20*time.Second, "joined", func() bool { return c.joined(3) })
	srv := storage.NewServer("127.0.0.1:0", c.rts[0], storage.Limits{MaxObjectSize: 4096}, node.StatusRows()...)
	if err := srv.Start(bg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := srv.Shutdown(bg); err != nil {
			t.Error(err)
		}
	})
	base := "http://" + srv.Addr()
	var etag string
	steps := []struct {
		name, method, path, body string
		hdr                      func() map[string]string
		want                     int
	}{
		{"a missing bucket", "GET", "/nob/k", "", nil, 404},
		{"creating a bucket with a quota", "PUT", "/b?quota-bytes=600", "", nil, 200},
		{"creating it again", "PUT", "/b", "", nil, 409},
		{"a missing object", "GET", "/b/k", "", nil, 404},
		{"a put", "PUT", "/b/k", strings.Repeat("a", 500), nil, 200},
		{"a create-only put over it", "PUT", "/b/k", "x", func() map[string]string { return map[string]string{"If-None-Match": "*"} }, 412},
		{"a malformed etag", "PUT", "/b/k", "x", func() map[string]string { return map[string]string{"If-Match": "zz"} }, 400},
		{"a put matching its etag", "PUT", "/b/k", "x", func() map[string]string { return map[string]string{"If-Match": etag} }, 200},
		{"a put over the quota", "PUT", "/b/q", strings.Repeat("q", 700), nil, 507},
		{"an object over the node limit", "PUT", "/b/big", strings.Repeat("q", 1500), nil, 413},
		{"an object over the http limit", "PUT", "/b/big", strings.Repeat("q", 5000), nil, 413},
		{"migrating an unrouted bucket", "POST", "/_migrations", `{"bucket":"b","to":"r1"}`, nil, 400},
		{"an unknown migration", "GET", "/_migrations/n-1-1", "", nil, 404},
		{"deleting with a stale etag", "DELETE", "/b/k", "", func() map[string]string { return map[string]string{"If-Match": etag} }, 412},
		{"a method the api lacks", "PATCH", "/b/k", "", nil, 405},
		{"health", "GET", "/_health", "", nil, 200},
		{"members", "GET", "/_cluster/members", "", nil, 200},
	}
	for _, s := range steps {
		req, err := http.NewRequest(s.method, base+s.path, strings.NewReader(s.body))
		if err != nil {
			t.Fatal(err)
		}
		if s.hdr != nil {
			for k, v := range s.hdr() {
				req.Header.Set(k, v)
			}
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		if cerr := resp.Body.Close(); err != nil || cerr != nil {
			t.Fatal(err, cerr)
		}
		if resp.StatusCode != s.want {
			t.Fatalf("%s: %d, want %d (%s)", s.name, resp.StatusCode, s.want, body)
		}
		if s.name == "a put" {
			etag = resp.Header.Get("ETag")
			if etag == "" || resp.Header.Get(storage.HeaderVersion) == "" {
				t.Fatal("a put response without ETag or version")
			}
		}
	}
	if _, err := c.rts[0].Quota(bg, "b"); err != nil {
		t.Fatal(err)
	}
}

type rowService struct {
	storage.Service
	err error
}

func (r rowService) GetObject(context.Context, string, string) (storage.ObjectInfo, []byte, error) {
	return storage.ObjectInfo{}, nil, r.err
}

func TestEveryNodeStatusRowThroughHTTP(t *testing.T) {
	rows := node.StatusRows()
	if len(rows) == 0 {
		t.Fatal("no node status rows")
	}
	for _, row := range rows {
		h := storage.NewHandler(rowService{err: fmt.Errorf("node: wrapped: %w", row.Err)}, storage.Limits{}, rows...)
		srv := httptest.NewServer(h)
		resp, err := http.Get(srv.URL + "/b/k")
		if err != nil {
			srv.Close()
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		srv.Close()
		if resp.StatusCode != row.Status {
			t.Errorf("%v: status %d, want %d", row.Err, resp.StatusCode, row.Status)
		}
	}
}
