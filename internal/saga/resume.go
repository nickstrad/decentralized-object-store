package saga

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

type Action uint8

const (
	ActNone Action = iota
	ActDo
	ActAbort
	ActCompensate
	ActEnd
)

type Snapshot struct {
	Def     string
	Begun   env.Time
	Input   []byte
	State   State
	Step    int
	Attempt int
	Open    bool
	Outputs [][]byte
	Cause   string
	Next    Action
	End     State
}

type shape struct {
	steps, pivot, maxAttempts int
}

func (sh shape) validate() error {
	if sh.steps < 1 || sh.pivot < 0 || sh.pivot > sh.steps || sh.maxAttempts < 1 {
		return fmt.Errorf("saga: shape steps=%d pivot=%d attempts=%d: %w", sh.steps, sh.pivot, sh.maxAttempts, env.ErrInvalidArgument)
	}
	return nil
}

func (sh shape) abortFrom(step int) int {
	if step < sh.pivot {
		return step
	}
	return sh.pivot - 1
}

func Resume(recs []Record, steps, pivot, maxAttempts int) (Snapshot, error) {
	sh := shape{steps: steps, pivot: pivot, maxAttempts: maxAttempts}
	if err := sh.validate(); err != nil {
		return Snapshot{}, err
	}
	if len(recs) == 0 {
		return Snapshot{}, fmt.Errorf("saga: no begin record: %w", ErrLogCorrupt)
	}
	var s Snapshot
	for i, r := range recs {
		if err := s.apply(r, sh); err != nil {
			return Snapshot{}, fmt.Errorf("saga: record %d (%s): %w", i, r.Kind, err)
		}
	}
	s.plan(sh)
	return s, nil
}

func bad(why string) error {
	return fmt.Errorf("%s: %w", why, ErrLogCorrupt)
}

func (s *Snapshot) apply(r Record, sh shape) error {
	if s.State == 0 {
		if r.Kind != RecBegin {
			return bad("the first record is not begin")
		}
		*s = Snapshot{Def: r.Def, Begun: r.Time, Input: bytes.Clone(r.Data), State: StateRunning}
		return nil
	}
	if s.State.Terminal() {
		return bad("record after the end")
	}
	switch r.Kind {
	case RecIntent:
		if s.State != StateRunning || r.Step != s.Step || s.Step >= sh.steps || r.Attempt != s.Attempt+1 {
			return bad(fmt.Sprintf("intent %d/%d at step %d attempt %d", r.Step, r.Attempt, s.Step, s.Attempt))
		}
		s.Attempt, s.Open = r.Attempt, true
	case RecDone:
		if s.State != StateRunning || r.Step != s.Step || !s.Open || r.Attempt != s.Attempt {
			return bad(fmt.Sprintf("done %d/%d at step %d attempt %d", r.Step, r.Attempt, s.Step, s.Attempt))
		}
		s.Outputs = append(slices.Clip(s.Outputs), bytes.Clone(r.Data))
		s.Step, s.Attempt, s.Open = s.Step+1, 0, false
	case RecAbort:
		if s.State != StateRunning || !s.Open || s.Step >= sh.steps || s.Step > sh.pivot || r.Step != sh.abortFrom(s.Step) {
			return bad(fmt.Sprintf("abort from %d at step %d", r.Step, s.Step))
		}
		s.State, s.Step, s.Attempt, s.Open, s.Cause = StateCompensating, r.Step, 0, false, string(r.Data)
	case RecCompIntent:
		if s.State != StateCompensating || r.Step != s.Step || s.Step < 0 || r.Attempt != s.Attempt+1 {
			return bad(fmt.Sprintf("compensation intent %d/%d at step %d attempt %d", r.Step, r.Attempt, s.Step, s.Attempt))
		}
		s.Attempt, s.Open = r.Attempt, true
	case RecCompDone:
		if s.State != StateCompensating || r.Step != s.Step || !s.Open || r.Attempt != s.Attempt {
			return bad(fmt.Sprintf("compensation done %d/%d at step %d attempt %d", r.Step, r.Attempt, s.Step, s.Attempt))
		}
		s.Step, s.Attempt, s.Open = s.Step-1, 0, false
	case RecEnd:
		okRun := s.State == StateRunning && s.Step == sh.steps && r.State == StateCompleted
		okComp := s.State == StateCompensating && s.Step == -1 && r.State == StateCompensated
		if !okRun && !okComp {
			return bad(fmt.Sprintf("end %s in %s at step %d", r.State, s.State, s.Step))
		}
		s.State, s.Open = r.State, false
	default:
		return bad("unexpected " + r.Kind.String())
	}
	return nil
}

func (s *Snapshot) plan(sh shape) {
	s.Next, s.End = ActNone, 0
	switch s.State {
	case StateRunning:
		switch {
		case s.Step == sh.steps:
			s.Next, s.End = ActEnd, StateCompleted
		case s.Step < sh.pivot && s.Attempt >= sh.maxAttempts:
			s.Next = ActAbort
		default:
			s.Next = ActDo
		}
	case StateCompensating:
		if s.Step < 0 {
			s.Next, s.End = ActEnd, StateCompensated
		} else {
			s.Next = ActCompensate
		}
	}
}
