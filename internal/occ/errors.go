package occ

import (
	"errors"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

var (
	ErrPreconditionFailed = errors.New("occ: precondition failed")
	ErrMalformed          = errors.New("occ: malformed encoding")
	ErrNotFound           = errors.New("occ: not found")
	ErrQuotaExceeded      = errors.New("occ: quota exceeded")
	ErrClockExhausted     = errors.New("occ: hybrid logical clock exhausted")
	ErrClockAhead         = errors.New("occ: remote stamp too far ahead of the local clock")
	ErrNotReady           = errors.New("occ: replica state not loaded")
)

const (
	CodeBlockMin env.Code = 128
	CodeBlockMax env.Code = 143

	CodePreconditionFailed env.Code = 128
	CodeMalformed          env.Code = 129
	CodeNotFound           env.Code = 130
	CodeQuotaExceeded      env.Code = 132
	CodeClockExhausted     env.Code = 133
	CodeClockAhead         env.Code = 134
	CodeNotReady           env.Code = 135
)

func CodeEntries() []env.CodeEntry {
	return []env.CodeEntry{
		{Code: CodePreconditionFailed, Err: ErrPreconditionFailed},
		{Code: CodeMalformed, Err: ErrMalformed},
		{Code: CodeNotFound, Err: ErrNotFound},
		{Code: CodeQuotaExceeded, Err: ErrQuotaExceeded},
		{Code: CodeClockExhausted, Err: ErrClockExhausted},
		{Code: CodeClockAhead, Err: ErrClockAhead},
		{Code: CodeNotReady, Err: ErrNotReady},
	}
}

func Codes() (*env.CodeTable, error) {
	return env.NewCodeTable(CodeEntries()...)
}
