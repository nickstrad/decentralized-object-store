package saga

import (
	"errors"
	"fmt"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

var (
	ErrPermanent    = errors.New("saga: permanent step failure")
	ErrSagaNotFound = errors.New("saga: not found")
	ErrLogCorrupt   = errors.New("saga: log corrupt")
)

const (
	CodePermanent    env.Code = env.CodeAppMin + 48
	CodeSagaNotFound env.Code = env.CodeAppMin + 49
	CodeLogCorrupt   env.Code = env.CodeAppMin + 50
)

func Codes() []env.CodeEntry {
	return []env.CodeEntry{
		{Code: CodePermanent, Err: ErrPermanent},
		{Code: CodeSagaNotFound, Err: ErrSagaNotFound},
		{Code: CodeLogCorrupt, Err: ErrLogCorrupt},
	}
}

type StepError struct {
	Saga         ID
	Step         string
	Attempt      int
	Compensation bool
	Err          error
}

func (e *StepError) Error() string {
	verb := "step"
	if e.Compensation {
		verb = "compensate"
	}
	return fmt.Sprintf("saga %s: %s %s attempt %d: %v", e.Saga, verb, e.Step, e.Attempt, e.Err)
}

func (e *StepError) Unwrap() error { return e.Err }
