package saga

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

const (
	MigrationName     = "bucket-migration"
	MigrationSteps    = 5
	MigrationPivot    = 3
	StepAllocate      = "allocate"
	StepCopy          = "copy"
	StepVerify        = "verify"
	StepFlip          = "flip"
	StepDeleteSource  = "delete-source"
	OutcomeMoved      = "moved"
	OutcomeSuperseded = "superseded"
	maxInputField     = 1 << 10
)

var ErrRootMismatch = errors.New("saga: merkle roots differ")

type MigrationInput struct {
	Bucket string
	From   string
	To     string
}

func (m MigrationInput) Validate() error {
	if m.Bucket == "" || m.From == "" || m.To == "" || m.From == m.To {
		return fmt.Errorf("saga: migration %q from %q to %q: %w", m.Bucket, m.From, m.To, env.ErrInvalidArgument)
	}
	return nil
}

func (m MigrationInput) Encode() []byte {
	var b []byte
	for _, s := range []string{m.Bucket, m.From, m.To} {
		b = appendString(b, s)
	}
	return b
}

func DecodeMigrationInput(b []byte) (MigrationInput, error) {
	var f [3]string
	rest := b
	for i := range f {
		n, k := binary.Uvarint(rest)
		if k <= 0 || n > maxInputField || n > uint64(len(rest)-k) {
			return MigrationInput{}, fmt.Errorf("saga: migration input field %d: %w", i, env.ErrInvalidArgument)
		}
		f[i] = string(rest[k : k+int(n)])
		rest = rest[k+int(n):]
	}
	if len(rest) != 0 {
		return MigrationInput{}, fmt.Errorf("saga: migration input: %d trailing bytes: %w", len(rest), env.ErrInvalidArgument)
	}
	return MigrationInput{Bucket: f[0], From: f[1], To: f[2]}, nil
}

type Allocator interface {
	Claim(c Call, m MigrationInput, done func(error))
	Unclaim(c Call, m MigrationInput, done func(error))
}

type Copier interface {
	Copy(c Call, m MigrationInput, done func(root []byte, err error))
	Discard(c Call, m MigrationInput, done func(error))
}

type Verifier interface {
	Root(c Call, bucket, loc string, done func(root []byte, err error))
}

type Router interface {
	Flip(c Call, m MigrationInput, done func(error))
	Pointer(c Call, bucket string, done func(loc string, err error))
}

type Retirer interface {
	Seal(c Call, m MigrationInput, done func(retired bool, err error))
	Promote(c Call, m MigrationInput, done func(error))
	Retire(c Call, m MigrationInput, done func(error))
}

type MigrationOps interface {
	Allocator
	Copier
	Verifier
	Router
	Retirer
}

type migStep struct {
	name string
	do   func(c Call, m MigrationInput, done func([]byte, error))
	comp func(c Call, m MigrationInput, done func(error))
}

func (s *migStep) Name() string { return s.name }

func (s *migStep) Do(c Call, done func([]byte, error)) {
	m, err := DecodeMigrationInput(c.Input)
	if err == nil {
		err = m.Validate()
	}
	if err != nil {
		done(nil, fmt.Errorf("saga: %s: %w: %w", s.name, err, ErrPermanent))
		return
	}
	s.do(c, m, done)
}

func (s *migStep) Compensate(c Call, done func(error)) {
	m, err := DecodeMigrationInput(c.Input)
	if err == nil {
		err = m.Validate()
	}
	if err != nil {
		done(nil)
		return
	}
	s.comp(c, m, done)
}

func Migration(ops MigrationOps) Definition {
	unit := func(done func([]byte, error)) func(error) {
		return func(err error) { done(nil, err) }
	}
	steps := []Step{
		&migStep{
			name: StepAllocate,
			do:   func(c Call, m MigrationInput, done func([]byte, error)) { ops.Claim(c, m, unit(done)) },
			comp: ops.Unclaim,
		},
		&migStep{
			name: StepCopy,
			do: func(c Call, m MigrationInput, done func([]byte, error)) {
				ops.Copy(c, m, func(root []byte, err error) {
					if err != nil {
						done(nil, err)
						return
					}
					done(root, nil)
				})
			},
			comp: ops.Discard,
		},
		&migStep{
			name: StepVerify,
			do: func(c Call, m MigrationInput, done func([]byte, error)) {
				if len(c.Outputs) < 2 {
					done(nil, fmt.Errorf("saga: verify %s: no copy root: %w", m.Bucket, ErrPermanent))
					return
				}
				want := c.Outputs[1]
				ops.Root(c, m.Bucket, m.To, func(root []byte, err error) {
					switch {
					case err != nil:
						done(nil, err)
					case !bytes.Equal(root, want):
						done(nil, fmt.Errorf("saga: verify %s at %s: %w", m.Bucket, m.To, ErrRootMismatch))
					default:
						done(root, nil)
					}
				})
			},
			comp: func(c Call, m MigrationInput, done func(error)) { done(nil) },
		},
		&migStep{
			name: StepFlip,
			do:   func(c Call, m MigrationInput, done func([]byte, error)) { ops.Flip(c, m, unit(done)) },
			comp: func(c Call, m MigrationInput, done func(error)) { done(nil) },
		},
		&migStep{
			name: StepDeleteSource,
			do:   func(c Call, m MigrationInput, done func([]byte, error)) { finish(ops, c, m, done) },
			comp: func(c Call, m MigrationInput, done func(error)) { done(nil) },
		},
	}
	return Definition{Name: MigrationName, Steps: steps, Pivot: MigrationPivot}
}

func finish(ops MigrationOps, c Call, m MigrationInput, done func([]byte, error)) {
	fail := func(err error) bool {
		if err != nil {
			done(nil, err)
		}
		return err != nil
	}
	moved := func() {
		ops.Promote(c, m, func(err error) {
			if fail(err) {
				return
			}
			ops.Retire(c, m, func(err error) {
				if fail(err) {
					return
				}
				done([]byte(OutcomeMoved), nil)
			})
		})
	}
	ops.Pointer(c, m.Bucket, func(loc string, err error) {
		if fail(err) {
			return
		}
		if loc != m.To {
			ops.Discard(c, m, func(err error) {
				if fail(err) {
					return
				}
				ops.Unclaim(c, m, func(err error) {
					if fail(err) {
						return
					}
					done([]byte(OutcomeSuperseded), nil)
				})
			})
			return
		}
		ops.Seal(c, m, func(retired bool, err error) {
			if fail(err) {
				return
			}
			if retired {
				moved()
				return
			}
			ops.Copy(c, m, func(_ []byte, err error) {
				if fail(err) {
					return
				}
				ops.Root(c, m.Bucket, m.From, func(src []byte, err error) {
					if fail(err) {
						return
					}
					ops.Root(c, m.Bucket, m.To, func(dst []byte, err error) {
						if fail(err) {
							return
						}
						if !bytes.Equal(src, dst) {
							fail(fmt.Errorf("saga: catch-up %s: %s and %s: %w", m.Bucket, m.From, m.To, ErrRootMismatch))
							return
						}
						moved()
					})
				})
			})
		})
	})
}
