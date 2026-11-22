package noderuntime

import (
	"errors"
	"path"
	"strings"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

type flakyDirSync struct {
	*memFS
	fail   string
	failed int
}

type flakyFile struct {
	vfile
	fs *flakyDirSync
	p  string
}

func (f flakyFile) Sync() error {
	if f.p == f.fs.fail && f.fs.failed == 0 {
		f.fs.failed++
		return errInjected
	}
	return f.vfile.Sync()
}

func (f *flakyDirSync) Open(p string) (vfile, error) {
	v, err := f.memFS.Open(p)
	if err != nil {
		return nil, err
	}
	return flakyFile{vfile: v, fs: f, p: path.Clean(p)}, nil
}

func durableChain(m *memFS, p string) (string, bool) {
	n := m.root
	for _, part := range strings.Split(strings.TrimPrefix(path.Clean(p), "/"), "/") {
		c, ok := n.dEntries[part]
		if !ok {
			return part, false
		}
		n = c
	}
	return "", true
}

func TestSyncRetriedAfterAMidChainFailureSyncsEveryNewAncestor(t *testing.T) {
	const name = "shards/b/00/v/0.0"
	fsys := &flakyDirSync{memFS: freshMemFS(), fail: "/d/shards/b"}
	d, err := openFileDisk(fsys, "/d")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Write(name, []byte("blob")); err != nil {
		t.Fatal(err)
	}
	if err := d.Sync(name); !errors.Is(err, env.ErrIO) {
		t.Fatalf("sync with a failing ancestor: %v, want an I/O error", err)
	}
	if fsys.failed != 1 {
		t.Fatalf("the injected fault fired %d times, want once", fsys.failed)
	}
	if err := d.Sync(name); err != nil {
		t.Fatalf("retried sync: %v", err)
	}
	if missing, ok := durableChain(fsys.memFS, "/d/"+name); !ok {
		t.Fatalf("after the retried sync returned nil, %q is not durable in its parent directory", missing)
	}
}
