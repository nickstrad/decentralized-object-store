package noderuntime

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nickstrad/object-store/internal/env"
)

type realClock struct{ rt *Runtime }

func (c realClock) Now() env.Time { return env.Time(time.Now().UnixNano()) }

func (c realClock) AfterFunc(d time.Duration, fn func()) env.Timer { return c.rt.afterFunc(d, fn) }

// realTimer wraps time.Timer so callbacks run on the Runtime loop and a
// stopped or reset timer's already-queued event is ignored via generation gen.
type realTimer struct {
	rt      *Runtime
	t       *time.Timer
	gen     uint64
	pending bool
	fn      func()
}

func (rt *Runtime) afterFunc(d time.Duration, fn func()) *realTimer {
	tm := &realTimer{rt: rt, fn: fn}
	rt.timers[tm] = struct{}{}
	tm.arm(d)
	return tm
}

func (tm *realTimer) arm(d time.Duration) {
	tm.gen++
	g := tm.gen
	tm.pending = true
	if tm.t != nil {
		tm.t.Stop()
	}
	rt := tm.rt
	tm.t = time.AfterFunc(max(d, 0), func() { rt.post(func() { tm.fire(g) }) })
}

func (tm *realTimer) fire(g uint64) {
	if g != tm.gen || !tm.pending {
		return
	}
	tm.pending = false
	delete(tm.rt.timers, tm)
	tm.fn()
}

func (tm *realTimer) Stop() bool {
	was := tm.pending
	tm.pending = false
	tm.gen++
	if tm.t != nil {
		tm.t.Stop()
	}
	delete(tm.rt.timers, tm)
	return was
}

func (tm *realTimer) Reset(d time.Duration) bool {
	was := tm.pending
	tm.rt.timers[tm] = struct{}{}
	tm.arm(d)
	return was
}

// fileDisk implements env.Disk over a directory tree using temp-file write,
// fsync, atomic rename, and directory/ancestor fsync. It runs synchronously on
// the node loop. Under POSIX semantics a crash leaves either the old or new
// bytes (or a prefix of an unsynced append) and synced writes survive; unsynced
// writes may also survive if flushed from the OS page cache.
type fileDisk struct {
	fs    vfs
	dir   string
	seq   uint64
	fresh map[string]bool
}

const tmpDir = ".tmp"

// openFileDisk initializes dir and removes any temp files left in .tmp/ by a
// prior crash.
func openFileDisk(fsys vfs, dir string) (*fileDisk, error) {
	d := &fileDisk{fs: fsys, dir: filepath.Clean(dir), fresh: map[string]bool{}}
	tmp := filepath.Join(d.dir, tmpDir)
	if err := d.mkdirs(tmp); err != nil {
		return nil, ioErr("open", dir, err)
	}
	var stale []string
	err := fsys.WalkDir(tmp, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != tmp {
			stale = append(stale, p)
			if e.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	if err != nil {
		return nil, ioErr("open", dir, err)
	}
	for _, p := range stale {
		if err := fsys.Remove(p); err != nil {
			return nil, ioErr("open", dir, err)
		}
	}
	if err := d.syncChain(tmp); err != nil {
		return nil, ioErr("open", dir, err)
	}
	return d, nil
}

// path validates name as a relative slash-separated blob key outside .tmp and
// maps it under d.dir.
func (d *fileDisk) path(name string) (string, error) {
	if name == "" || len(name) > 4096 || strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) || strings.Contains(name, "\\") {
		return "", fmt.Errorf("node: disk name %q: %w", name, env.ErrInvalidArgument)
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." || seg == tmpDir {
			return "", fmt.Errorf("node: disk name %q: %w", name, env.ErrInvalidArgument)
		}
	}
	return filepath.Join(d.dir, filepath.FromSlash(name)), nil
}

func ioErr(op, name string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("node: disk %s %q: %w", op, name, env.ErrNotFound)
	}
	return fmt.Errorf("node: disk %s %q: %v: %w", op, name, err, env.ErrIO)
}

// mkdirs creates dir and any missing parents under d.dir, recording every newly
// created directory in d.fresh so syncChain fsyncs its parent.
func (d *fileDisk) mkdirs(dir string) error {
	rel, err := filepath.Rel(d.dir, dir)
	if err != nil {
		return err
	}
	cur := d.dir
	if rel == "." {
		rel = ""
	}
	for _, seg := range append([]string{""}, strings.Split(rel, string(filepath.Separator))...) {
		if seg != "" {
			cur = filepath.Join(cur, seg)
		}
		err := d.fs.Mkdir(cur)
		switch {
		case err == nil:
			d.fresh[cur] = true
		case !errors.Is(err, fs.ErrExist):
			return err
		}
	}
	return nil
}

func writeAll(f vfile, data []byte) error {
	for len(data) > 0 {
		n, err := f.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

// syncChain fsyncs dir and the parent of every still-marked fresh ancestor up
// to d.dir, clearing each mark only after its parent fsync succeeds so a retry
// after a mid-chain error finishes the remaining ancestors.
func (d *fileDisk) syncChain(dir string) error {
	if err := d.syncDir(dir); err != nil {
		return err
	}
	for cur := dir; cur != d.dir && cur != filepath.Dir(cur); cur = filepath.Dir(cur) {
		if !d.fresh[cur] {
			continue
		}
		if err := d.syncDir(filepath.Dir(cur)); err != nil {
			return err
		}
		delete(d.fresh, cur)
	}
	if d.fresh[d.dir] {
		if err := d.syncDir(filepath.Dir(d.dir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		delete(d.fresh, d.dir)
	}
	return nil
}

func (d *fileDisk) Read(name string) ([]byte, error) {
	p, err := d.path(name)
	if err != nil {
		return nil, err
	}
	b, err := d.fs.ReadFile(p)
	if err != nil {
		return nil, ioErr("read", name, err)
	}
	return b, nil
}

func (d *fileDisk) Write(name string, data []byte) error {
	p, err := d.path(name)
	if err != nil {
		return err
	}
	if err := d.mkdirs(filepath.Dir(p)); err != nil {
		return ioErr("write", name, err)
	}
	if err := d.mkdirs(filepath.Join(d.dir, tmpDir)); err != nil {
		return ioErr("write", name, err)
	}
	d.seq++
	tmp := filepath.Join(d.dir, tmpDir, strconv.FormatUint(d.seq, 10))
	f, err := d.fs.Create(tmp)
	if err != nil {
		return ioErr("write", name, err)
	}
	// Fsync the temp file before renaming so power loss cannot leave the target
	// pointing at an unallocated or zero-length inode.
	werr := writeAll(f, data)
	if werr == nil {
		werr = f.Sync()
	}
	if err := errors.Join(werr, f.Close()); err != nil {
		return ioErr("write", name, errors.Join(err, d.fs.Remove(tmp)))
	}
	if err := d.fs.Rename(tmp, p); err != nil {
		return ioErr("write", name, errors.Join(err, d.fs.Remove(tmp)))
	}
	return nil
}

func (d *fileDisk) Append(name string, data []byte) error {
	p, err := d.path(name)
	if err != nil {
		return err
	}
	// Route the first append through temp+fsync+rename so a newly created log
	// never survives a crash as a zero-byte file.
	if _, err := d.fs.ReadFile(p); errors.Is(err, fs.ErrNotExist) {
		if err := d.Write(name, data); err != nil {
			return fmt.Errorf("node: disk append %q: first record: %w", name, err)
		}
		return nil
	}
	f, _, err := d.fs.OpenAppend(p)
	if err != nil {
		return ioErr("append", name, err)
	}
	if err := errors.Join(writeAll(f, data), f.Close()); err != nil {
		return ioErr("append", name, err)
	}
	return nil
}

func (d *fileDisk) syncDir(dir string) error {
	f, err := d.fs.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func (d *fileDisk) Sync(name string) error {
	p, err := d.path(name)
	if err != nil {
		return err
	}
	f, err := d.fs.Open(p)
	switch {
	case err == nil:
		if err := errors.Join(f.Sync(), f.Close()); err != nil {
			return ioErr("sync", name, err)
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return ioErr("sync", name, err)
	}
	if err := d.syncChain(filepath.Dir(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ioErr("sync", name, err)
	}
	return nil
}

func (d *fileDisk) Delete(name string) error {
	p, err := d.path(name)
	if err != nil {
		return err
	}
	if err := d.fs.Remove(p); err != nil {
		return ioErr("delete", name, err)
	}
	return nil
}

func (d *fileDisk) List(prefix string) ([]string, error) {
	var out []string
	err := d.fs.WalkDir(d.dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		rel, rerr := filepath.Rel(d.dir, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if e.IsDir() {
			if rel == tmpDir {
				return filepath.SkipDir
			}
			if rel != "." && !strings.HasPrefix(rel+"/", prefix) && !strings.HasPrefix(prefix, rel+"/") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(rel, prefix) {
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		return nil, ioErr("list", prefix, err)
	}
	slices.Sort(out)
	return out, nil
}
