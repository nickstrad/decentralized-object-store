package saga

import (
	"errors"
	"fmt"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

// Sentinel errors returned by the saga engine, log decoder, and step callbacks:
//   - ErrPermanent: definitive step failure; stops retries and triggers abort at
//     or before the pivot, but is retried after the pivot.
//   - ErrSagaNotFound: no active or broken saga exists with the requested ID, or
//     its log is temporarily unreadable while recovery retries.
//   - ErrLogCorrupt: a log frame has a bad magic byte, checksum, length,
//     non-canonical payload, or illegal state-machine transition.
var (
	ErrPermanent    = errors.New("saga: permanent step failure")
	ErrSagaNotFound = errors.New("saga: not found")
	ErrLogCorrupt   = errors.New("saga: log corrupt")
)

// Wire status codes for saga sentinel errors in the allocated 144..159 block.
const (
	CodePermanent    env.Code = env.CodeAppMin + 80
	CodeSagaNotFound env.Code = env.CodeAppMin + 81
	CodeLogCorrupt   env.Code = env.CodeAppMin + 82
)

// Codes returns the env.CodeEntry mappings for this package's sentinel errors.
func Codes() []env.CodeEntry {
	return []env.CodeEntry{
		{Code: CodePermanent, Err: ErrPermanent},
		{Code: CodeSagaNotFound, Err: ErrSagaNotFound},
		{Code: CodeLogCorrupt, Err: ErrLogCorrupt},
	}
}

// StepError wraps the last failure returned by a forward step or compensation
// attempt, recording the saga ID, step name, 1-based attempt number, and phase.
type StepError struct {
	Saga         ID
	Step         string
	Attempt      int
	Compensation bool
	Err          error
}

// Error formats the saga ID, execution phase, step name, attempt number, and cause.
func (e *StepError) Error() string {
	verb := "step"
	if e.Compensation {
		verb = "compensate"
	}
	return fmt.Sprintf("saga %s: %s %s attempt %d: %v", e.Saga, verb, e.Step, e.Attempt, e.Err)
}

// Unwrap returns the underlying step error for errors.Is and errors.As matching.
func (e *StepError) Unwrap() error { return e.Err }
