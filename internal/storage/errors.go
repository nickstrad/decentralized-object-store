package storage

import (
	"errors"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

var (
	ErrNotFound    = errors.New("storage: not found")
	ErrCorrupt     = errors.New("storage: corrupt")
	ErrExists      = errors.New("storage: exists")
	ErrUnavailable = errors.New("storage: unavailable")
	ErrInvalid     = errors.New("storage: invalid argument")
)

type ObjectError struct {
	Op     string
	Bucket string
	Key    string
	Err    error
}

func (e *ObjectError) Error() string {
	return "storage: " + e.Op + " " + e.Bucket + "/" + e.Key + ": " + e.Err.Error()
}

func (e *ObjectError) Unwrap() error { return e.Err }

const (
	CodeNotFound    = env.CodeAppMin + 32
	CodeCorrupt     = env.CodeAppMin + 33
	CodeExists      = env.CodeAppMin + 34
	CodeUnavailable = env.CodeAppMin + 35
	CodeInvalid     = env.CodeAppMin + 36
)

func CodeEntries() []env.CodeEntry {
	return []env.CodeEntry{
		{Code: CodeNotFound, Err: ErrNotFound},
		{Code: CodeCorrupt, Err: ErrCorrupt},
		{Code: CodeExists, Err: ErrExists},
		{Code: CodeUnavailable, Err: ErrUnavailable},
		{Code: CodeInvalid, Err: ErrInvalid},
	}
}

func Codes() (*env.CodeTable, error) {
	return env.NewCodeTable(CodeEntries()...)
}

func objectErr(op, bucket, key string, err error) error {
	var e error = &ObjectError{Op: op, Bucket: bucket, Key: key, Err: err}
	return e
}
