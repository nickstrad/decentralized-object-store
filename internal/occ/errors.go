package occ

import (
	"errors"
	"strconv"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

var (
	ErrPreconditionFailed = errors.New("occ: precondition failed")
	ErrMalformed          = errors.New("occ: malformed encoding")
	ErrNotFound           = errors.New("occ: not found")
	ErrConflict           = errors.New("occ: conflicting siblings")
	ErrQuotaExceeded      = errors.New("occ: quota exceeded")
	ErrClockExhausted     = errors.New("occ: hybrid logical clock exhausted")
	ErrClockAhead         = errors.New("occ: remote stamp too far ahead of the local clock")
	ErrNotReady           = errors.New("occ: replica state not loaded")
)

const (
	CodePreconditionFailed env.Code = 80
	CodeMalformed          env.Code = 81
	CodeNotFound           env.Code = 82
	CodeConflict           env.Code = 83
	CodeQuotaExceeded      env.Code = 84
	CodeClockExhausted     env.Code = 85
	CodeClockAhead         env.Code = 86
	CodeNotReady           env.Code = 87
)

func CodeEntries() []env.CodeEntry {
	return []env.CodeEntry{
		{Code: CodePreconditionFailed, Err: ErrPreconditionFailed},
		{Code: CodeMalformed, Err: ErrMalformed},
		{Code: CodeNotFound, Err: ErrNotFound},
		{Code: CodeConflict, Err: ErrConflict},
		{Code: CodeQuotaExceeded, Err: ErrQuotaExceeded},
		{Code: CodeClockExhausted, Err: ErrClockExhausted},
		{Code: CodeClockAhead, Err: ErrClockAhead},
		{Code: CodeNotReady, Err: ErrNotReady},
	}
}

func Codes() (*env.CodeTable, error) {
	return env.NewCodeTable(CodeEntries()...)
}

type ConflictError struct {
	Key      string
	ETag     ETag
	Siblings []Version
}

func (e *ConflictError) Error() string {
	return "occ: key " + strconv.Quote(e.Key) + " has " + strconv.Itoa(len(e.Siblings)) + " siblings (etag " + e.ETag.String() + "): " + ErrConflict.Error()
}

func (e *ConflictError) Unwrap() error { return ErrConflict }
