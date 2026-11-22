package noderuntime

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

type mInode struct {
	dir      bool
	data     []byte
	durable  []byte
	entries  map[string]*mInode
	dEntries map[string]*mInode
}

func newDir() *mInode {
	return &mInode{dir: true, entries: map[string]*mInode{}, dEntries: map[string]*mInode{}}
}

var errInjected = errors.New("injected fault")

type memFS struct {
	root    *mInode
	ops     int
	failAt  int
	crashAt int
	crashed bool
	short   bool
}

func (m *memFS) step(what string) error {
	if m.crashed {
		return fmt.Errorf("%s after the crash: %w", what, errInjected)
	}
	m.ops++
	if m.ops == m.crashAt {
		m.crashed = true
		return fmt.Errorf("%s: crash: %w", what, errInjected)
	}
	if m.ops == m.failAt {
		return fmt.Errorf("%s: %w", what, errInjected)
	}
	return nil
}

func (m *memFS) lookup(p string) (*mInode, *mInode, string, error) {
	p = path.Clean(p)
	if p == "/" {
		return m.root, nil, "", nil
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	cur := m.root
	for i, seg := range parts {
		if !cur.dir {
			return nil, nil, "", fs.ErrNotExist
		}
		next := cur.entries[seg]
		if i == len(parts)-1 {
			return next, cur, seg, nil
		}
		if next == nil {
			return nil, nil, "", fs.ErrNotExist
		}
		cur = next
	}
	return nil, nil, "", fs.ErrNotExist
}

func (m *memFS) Mkdir(p string) error {
	n, parent, name, err := m.lookup(p)
	if err != nil {
		return err
	}
	if n != nil {
		return fs.ErrExist
	}
	if err := m.step("mkdir " + p); err != nil {
		return err
	}
	parent.entries[name] = newDir()
	return nil
}

type mFile struct {
	m      *memFS
	n      *mInode
	append bool
}

func (f *mFile) Write(p []byte) (int, error) {
	if err := f.m.step("write"); err != nil {
		return 0, err
	}
	if f.m.short && len(p) > 1 {
		f.n.data = append(f.n.data, p[:len(p)/2]...)
		return len(p) / 2, nil
	}
	f.n.data = append(f.n.data, p...)
	return len(p), nil
}

func (f *mFile) Sync() error {
	if err := f.m.step("fsync"); err != nil {
		return err
	}
	if f.n.dir {
		f.n.dEntries = map[string]*mInode{}
		for k, v := range f.n.entries {
			f.n.dEntries[k] = v
		}
		return nil
	}
	f.n.durable = slices.Clone(f.n.data)
	return nil
}

func (f *mFile) Close() error { return nil }

func (m *memFS) Create(p string) (vfile, error) {
	n, parent, name, err := m.lookup(p)
	if err != nil {
		return nil, err
	}
	if n != nil {
		return nil, fs.ErrExist
	}
	if parent == nil || !parent.dir {
		return nil, fs.ErrNotExist
	}
	if err := m.step("create " + p); err != nil {
		return nil, err
	}
	n = &mInode{}
	parent.entries[name] = n
	return &mFile{m: m, n: n}, nil
}

func (m *memFS) OpenAppend(p string) (vfile, bool, error) {
	n, parent, name, err := m.lookup(p)
	if err != nil {
		return nil, false, err
	}
	if n != nil {
		if n.dir {
			return nil, false, errors.New("is a directory")
		}
		return &mFile{m: m, n: n, append: true}, false, nil
	}
	if parent == nil || !parent.dir {
		return nil, false, fs.ErrNotExist
	}
	if err := m.step("create " + p); err != nil {
		return nil, false, err
	}
	n = &mInode{}
	parent.entries[name] = n
	return &mFile{m: m, n: n, append: true}, true, nil
}

func (m *memFS) Open(p string) (vfile, error) {
	n, _, _, err := m.lookup(p)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, fs.ErrNotExist
	}
	return &mFile{m: m, n: n}, nil
}

func (m *memFS) Rename(from, to string) error {
	n, fp, fname, err := m.lookup(from)
	if err != nil || n == nil {
		return fs.ErrNotExist
	}
	_, tp, tname, err := m.lookup(to)
	if err != nil || tp == nil {
		return fs.ErrNotExist
	}
	if err := m.step("rename " + from); err != nil {
		return err
	}
	delete(fp.entries, fname)
	tp.entries[tname] = n
	return nil
}

func (m *memFS) Remove(p string) error {
	n, parent, name, err := m.lookup(p)
	if err != nil || n == nil {
		return fs.ErrNotExist
	}
	if err := m.step("remove " + p); err != nil {
		return err
	}
	delete(parent.entries, name)
	return nil
}

func (m *memFS) ReadFile(p string) ([]byte, error) {
	n, _, _, err := m.lookup(p)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, fs.ErrNotExist
	}
	if n.dir {
		return nil, errors.New("is a directory")
	}
	return slices.Clone(n.data), nil
}

type mEntry struct {
	name string
	dir  bool
}

func (e mEntry) Name() string               { return e.name }
func (e mEntry) IsDir() bool                { return e.dir }
func (e mEntry) Type() fs.FileMode          { return 0 }
func (e mEntry) Info() (fs.FileInfo, error) { return nil, errors.New("no info") }

func (m *memFS) WalkDir(root string, fn fs.WalkDirFunc) error {
	n, _, _, err := m.lookup(root)
	if err != nil || n == nil {
		return fn(root, nil, fs.ErrNotExist)
	}
	var walk func(p string, n *mInode) error
	walk = func(p string, n *mInode) error {
		if err := fn(p, mEntry{name: path.Base(p), dir: n.dir}, nil); err != nil {
			if errors.Is(err, fs.SkipDir) {
				return nil
			}
			return err
		}
		if !n.dir {
			return nil
		}
		names := make([]string, 0, len(n.entries))
		for k := range n.entries {
			names = append(names, k)
		}
		slices.Sort(names)
		for _, k := range names {
			if err := walk(path.Join(p, k), n.entries[k]); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(path.Clean(root), n)
}

func (m *memFS) afterCrash(keepMetadata bool) *memFS {
	var copyDir func(n *mInode) *mInode
	copyNode := func(n *mInode) *mInode {
		if n.dir {
			return copyDir(n)
		}
		return &mInode{data: slices.Clone(n.durable), durable: slices.Clone(n.durable)}
	}
	copyDir = func(n *mInode) *mInode {
		out := newDir()
		src := n.dEntries
		if keepMetadata {
			src = n.entries
		}
		for k, v := range src {
			c := copyNode(v)
			out.entries[k], out.dEntries[k] = c, c
		}
		return out
	}
	return &memFS{root: copyDir(m.root)}
}

func freshMemFS() *memFS {
	m := &memFS{root: newDir()}
	m.root.entries["d"] = newDir()
	m.root.dEntries["d"] = m.root.entries["d"]
	return m
}

type crashCase struct {
	name     string
	appends  bool
	setup    func(d *fileDisk) error
	op       func(d *fileDisk) error
	blob     string
	old, new []byte
}

func crashCases() []crashCase {
	old, nw := []byte("old bytes"), []byte("new bytes, longer than the old")
	return []crashCase{
		{name: "overwrite a synced blob", blob: "occ/state",
			setup: func(d *fileDisk) error { return errors.Join(d.Write("occ/state", old), d.Sync("occ/state")) },
			op:    func(d *fileDisk) error { return errors.Join(d.Write("occ/state", nw), d.Sync("occ/state")) },
			old:   old, new: nw},
		{name: "create a blob under new directories", blob: "shards/b/00/v/0.0",
			setup: func(d *fileDisk) error { return nil },
			op: func(d *fileDisk) error {
				return errors.Join(d.Write("shards/b/00/v/0.0", nw), d.Sync("shards/b/00/v/0.0"))
			},
			old: nil, new: nw},
		{name: "delete a synced blob", blob: "a/b",
			setup: func(d *fileDisk) error { return errors.Join(d.Write("a/b", old), d.Sync("a/b")) },
			op:    func(d *fileDisk) error { return errors.Join(d.Delete("a/b"), d.Sync("a/b")) },
			old:   old, new: nil},
		{name: "append to a synced log", blob: "saga/log", appends: true,
			setup: func(d *fileDisk) error { return errors.Join(d.Append("saga/log", old), d.Sync("saga/log")) },
			op:    func(d *fileDisk) error { return errors.Join(d.Append("saga/log", nw), d.Sync("saga/log")) },
			old:   old, new: append(slices.Clone(old), nw...)},
		{name: "start a new log under new directories", blob: "saga/x/log", appends: true,
			setup: func(d *fileDisk) error { return nil },
			op:    func(d *fileDisk) error { return errors.Join(d.Append("saga/x/log", nw), d.Sync("saga/x/log")) },
			old:   nil, new: nw},
	}
}

func readBack(m *memFS, blob string) ([]byte, bool, error) {
	d, err := openFileDisk(m, "/d")
	if err != nil {
		return nil, false, err
	}
	b, err := d.Read(blob)
	if errors.Is(err, env.ErrNotFound) {
		return nil, false, nil
	}
	return b, err == nil, err
}

func TestFileDiskCrashLeavesOldOrNewBytes(t *testing.T) {
	for _, c := range crashCases() {
		for _, mode := range []string{"crash", "fail-then-crash", "short-write"} {
			for k := 1; ; k++ {
				m := freshMemFS()
				d, err := openFileDisk(m, "/d")
				if err != nil {
					t.Fatal(err)
				}
				if err := c.setup(d); err != nil {
					t.Fatalf("%s: set-up: %v", c.name, err)
				}
				m.ops = 0
				switch mode {
				case "crash":
					m.crashAt = k
				case "fail-then-crash":
					m.failAt = k
				case "short-write":
					m.short = k == 1
				}
				opErr := c.op(d)
				ran := m.ops
				if !m.crashed {
					m.crashed = true
				}
				for _, keep := range []bool{false, true} {
					got, ok, err := readBack(m.afterCrash(keep), c.blob)
					if err != nil {
						t.Fatalf("%s, %s at step %d, metadata kept=%v: reopen: %v", c.name, mode, k, keep, err)
					}
					isOld := (c.old == nil && !ok) || (c.old != nil && ok && bytes.Equal(got, c.old))
					isNew := (c.new == nil && !ok) || (c.new != nil && ok && bytes.Equal(got, c.new))
					if c.appends && ok && c.old != nil && len(got) > len(c.old) && bytes.HasPrefix(c.new, got) {
						isOld = true
					}
					if opErr == nil && !isNew {
						t.Fatalf("%s, %s at step %d, metadata kept=%v: Sync returned nil but after the crash the blob is %q (present %v), want the new bytes", c.name, mode, k, keep, got, ok)
					}
					if !isOld && !isNew {
						t.Fatalf("%s, %s at step %d, metadata kept=%v: after the crash the blob is %q (present %v): neither the old nor the new bytes", c.name, mode, k, keep, got, ok)
					}
				}
				if mode == "short-write" || k > ran {
					break
				}
			}
		}
	}
}

var _ = time.Second
