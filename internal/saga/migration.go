package saga

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/nickstrad/object-store/internal/env"
)

// Constants for the bucket-migration workflow definition:
//   - MigrationName, MigrationSteps, MigrationPivot: definition identity and shape.
//   - StepAllocate..StepDeleteSource: the five step names in execution order.
//   - OutcomeMoved, OutcomeSuperseded: outputs recorded by StepDeleteSource.
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

// ErrRootMismatch is returned (retryably) when the target Merkle root does not
// match the copied or sealed source root.
var ErrRootMismatch = errors.New("saga: merkle roots differ")

// MigrationInput names the bucket and the source and target locations for one
// bucket-migration saga.
type MigrationInput struct {
	Bucket string
	From   string
	To     string
}

// Validate checks that Bucket, From, and To are non-empty, From != To, and no
// field exceeds 1024 bytes, returning env.ErrInvalidArgument otherwise.
func (m MigrationInput) Validate() error {
	if m.Bucket == "" || m.From == "" || m.To == "" || m.From == m.To ||
		len(m.Bucket) > maxInputField || len(m.From) > maxInputField || len(m.To) > maxInputField {
		return fmt.Errorf("saga: migration %q from %q to %q: %w", m.Bucket, m.From, m.To, env.ErrInvalidArgument)
	}
	return nil
}

// Encode serializes m as three length-prefixed strings for Engine.Begin.
func (m MigrationInput) Encode() []byte {
	var b []byte
	for _, s := range []string{m.Bucket, m.From, m.To} {
		b = appendString(b, s)
	}
	return b
}

// DecodeMigrationInput decodes bytes produced by MigrationInput.Encode,
// returning env.ErrInvalidArgument on truncated, over-long (>1024 bytes), or
// trailing bytes.
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

// Allocator registers a migration at the source and claims/unclaims staging
// capacity at the target.
type Allocator interface {
	Claim(c Call, m MigrationInput, done func(error))
	Unclaim(c Call, m MigrationInput, done func(error))
}

// Copier transfers bucket data from source to target and discards staged copies
// on compensation or supersession.
type Copier interface {
	Copy(c Call, m MigrationInput, done func(root []byte, err error))
	Discard(c Call, m MigrationInput, done func(error))
}

// Verifier reads the Merkle root of a bucket at a given location.
type Verifier interface {
	Root(c Call, bucket, loc string, done func(root []byte, err error))
}

// Router updates and reads the bucket's routing pointer. Flip stamps the update
// with (c.Begun, c.Key.Saga); Pointer must return one agreed location or an error.
type Router interface {
	Flip(c Call, m MigrationInput, done func(error))
	Pointer(c Call, bucket string, done func(loc string, err error))
}

// Retirer seals the source against new writes, retires the sealed source, and
// promotes the staged target to live. Seal returns retired=true when this saga
// has already retired the source.
type Retirer interface {
	Seal(c Call, m MigrationInput, done func(retired bool, err error))
	Promote(c Call, m MigrationInput, done func(error))
	Retire(c Call, m MigrationInput, done func(error))
}

// MigrationOps combines the five step interfaces required by Migration.
//
// Implementations must key ownership, claim, registration, discard, seal, and
// retire checks on Call.Key.Saga alone, because StepDeleteSource calls Discard
// and Unclaim with Step 4 while steps 0 and 1 call them with Steps 0 and 1. Any
// idempotency cache for Copy must key on the full Call.Key so StepDeleteSource's
// catch-up copy does not reuse StepCopy's cached result.
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

// Migration returns the five-step bucket-migration Definition (allocate, copy,
// verify, flip as pivot, delete-source) backed by ops.
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

// finish runs the post-pivot phase: check the converged routing pointer, clean
// up and report superseded if another migration won, or seal the source, run a
// catch-up copy and second Merkle check, retire the source, and promote the target.
func finish(ops MigrationOps, c Call, m MigrationInput, done func([]byte, error)) {
	fail := func(err error) bool {
		if err != nil {
			done(nil, err)
		}
		return err != nil
	}
	promote := func() {
		ops.Promote(c, m, func(err error) {
			if fail(err) {
				return
			}
			done([]byte(OutcomeMoved), nil)
		})
	}
	ops.Pointer(c, m.Bucket, func(loc string, err error) {
		if fail(err) {
			return
		}
		// Another migration won the LWW pointer; discard our target and deregister
		// at the source so the winner can seal it.
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
			// Source is already retired by this saga (crash between Retire and
			// Promote); skip catch-up so we do not overwrite live target writes.
			if retired {
				promote()
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
						// Retire before Promote: promoting first would allow a crash
						// and retry to copy the sealed source over acknowledged writes
						// accepted by the newly live target.
						ops.Retire(c, m, func(err error) {
							if fail(err) {
								return
							}
							promote()
						})
					})
				})
			})
		})
	})
}
