package node

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

type realClock struct{ rt *Runtime }

func (c realClock) Now() env.Time { return env.Time(time.Now().UnixNano()) }

func (c realClock) AfterFunc(d time.Duration, fn func()) env.Timer { return c.rt.afterFunc(d, fn) }

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

type fileDisk struct {
	dir string
	seq uint64
}

const tmpDir = ".tmp"

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

func (d *fileDisk) Read(name string) ([]byte, error) {
	p, err := d.path(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
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
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return ioErr("write", name, err)
	}
	if err := os.MkdirAll(filepath.Join(d.dir, tmpDir), 0o755); err != nil {
		return ioErr("write", name, err)
	}
	d.seq++
	tmp := filepath.Join(d.dir, tmpDir, strconv.FormatUint(d.seq, 10))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return ioErr("write", name, err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return ioErr("write", name, errors.Join(err, os.Remove(tmp)))
	}
	return nil
}

func (d *fileDisk) Append(name string, data []byte) error {
	p, err := d.path(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return ioErr("append", name, err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return ioErr("append", name, err)
	}
	_, werr := f.Write(data)
	if err := errors.Join(werr, f.Close()); err != nil {
		return ioErr("append", name, err)
	}
	return nil
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
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
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	switch {
	case err == nil:
		if err := errors.Join(f.Sync(), f.Close()); err != nil {
			return ioErr("sync", name, err)
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return ioErr("sync", name, err)
	}
	if err := syncDir(filepath.Dir(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ioErr("sync", name, err)
	}
	return nil
}

func (d *fileDisk) Delete(name string) error {
	p, err := d.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		return ioErr("delete", name, err)
	}
	return nil
}

func (d *fileDisk) List(prefix string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(d.dir, func(p string, e fs.DirEntry, err error) error {
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
