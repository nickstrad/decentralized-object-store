package noderuntime

import (
	"io/fs"
	"os"
	"path/filepath"
)

// vfile is the minimal file handle required by fileDisk so crash-model tests
// can substitute an in-memory POSIX filesystem.
type vfile interface {
	Write(p []byte) (int, error)
	Sync() error
	Close() error
}

// vfs abstracts OS filesystem calls under fileDisk.
type vfs interface {
	Mkdir(path string) error
	Create(path string) (vfile, error)
	OpenAppend(path string) (vfile, bool, error)
	Open(path string) (vfile, error)
	Rename(from, to string) error
	Remove(path string) error
	ReadFile(path string) ([]byte, error)
	WalkDir(root string, fn fs.WalkDirFunc) error
}

// osFS implements vfs directly against the host operating system.
type osFS struct{}

func (osFS) Mkdir(path string) error { return os.Mkdir(path, 0o755) }

func (osFS) Create(path string) (vfile, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
}

func (osFS) OpenAppend(path string) (vfile, bool, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err == nil {
		return f, false, nil
	}
	if !os.IsNotExist(err) {
		return nil, false, err
	}
	f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	return f, err == nil, err
}

func (osFS) Open(path string) (vfile, error)      { return os.Open(path) }
func (osFS) Rename(from, to string) error         { return os.Rename(from, to) }
func (osFS) Remove(path string) error             { return os.Remove(path) }
func (osFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (osFS) WalkDir(root string, fn fs.WalkDirFunc) error { return filepath.WalkDir(root, fn) }
