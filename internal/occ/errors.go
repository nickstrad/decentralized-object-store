package occ

import (
	"errors"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

// Sentinel errors returned by OCC store, decoder, quota, clock, and node operations:
//   - ErrPreconditionFailed: a conditional write's If-Match or If-None-Match did not hold.
//   - ErrMalformed: an encoded document, state blob, key, ETag, usage, or wire message was invalid.
//   - ErrNotFound: the requested key is absent or holds a tombstone.
//   - ErrQuotaExceeded: an additive quota update would exceed a configured byte or object limit.
//   - ErrClockExhausted: the HLC or a key's stamp reached (MaxInt64, MaxUint32) with no successor.
//   - ErrClockAhead: a remote stamp exceeded the local clock by more than MaxClockLead.
//   - ErrNotReady: a Node has not finished loading its durable state yet.
//   - ErrUsageOverflow: summing live object metadata sizes or counts overflowed uint64.
var (
	ErrPreconditionFailed = errors.New("occ: precondition failed")
	ErrMalformed          = errors.New("occ: malformed encoding")
	ErrNotFound           = errors.New("occ: not found")
	ErrQuotaExceeded      = errors.New("occ: quota exceeded")
	ErrClockExhausted     = errors.New("occ: hybrid logical clock exhausted")
	ErrClockAhead         = errors.New("occ: remote stamp too far ahead of the local clock")
	ErrNotReady           = errors.New("occ: replica state not loaded")
	ErrUsageOverflow      = errors.New("occ: usage total does not fit in 64 bits")
)

// Wire status codes allocated to package occ in [CodeBlockMin, CodeBlockMax].
// Code 131 is deliberately left unassigned.
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
	CodeUsageOverflow      env.Code = 136
)

// CodeEntries returns the wire code mappings for all package occ sentinels.
func CodeEntries() []env.CodeEntry {
	return []env.CodeEntry{
		{Code: CodePreconditionFailed, Err: ErrPreconditionFailed},
		{Code: CodeMalformed, Err: ErrMalformed},
		{Code: CodeNotFound, Err: ErrNotFound},
		{Code: CodeQuotaExceeded, Err: ErrQuotaExceeded},
		{Code: CodeClockExhausted, Err: ErrClockExhausted},
		{Code: CodeClockAhead, Err: ErrClockAhead},
		{Code: CodeNotReady, Err: ErrNotReady},
		{Code: CodeUsageOverflow, Err: ErrUsageOverflow},
	}
}

// Codes builds an env.CodeTable containing the occ sentinels and the built-in
// env sentinels.
func Codes() (*env.CodeTable, error) {
	return env.NewCodeTable(CodeEntries()...)
}
