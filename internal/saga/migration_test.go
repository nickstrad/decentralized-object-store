package saga_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/saga"
)

type fakeOps struct {
	log      []string
	fail     map[string][]error
	pointer  string
	retired  bool
	roots    map[string][][]byte
	copyRoot []byte
}

func newFakeOps(to string) *fakeOps {
	return &fakeOps{fail: map[string][]error{}, pointer: to, roots: map[string][][]byte{}, copyRoot: []byte("r1")}
}

func (f *fakeOps) next(op string) error {
	f.log = append(f.log, op)
	q := f.fail[op]
	if len(q) == 0 {
		return nil
	}
	f.fail[op] = q[1:]
	return q[0]
}

func (f *fakeOps) root(loc string) []byte {
	q := f.roots[loc]
	if len(q) == 0 {
		return f.copyRoot
	}
	if len(q) > 1 {
		f.roots[loc] = q[1:]
	}
	return q[0]
}

func (f *fakeOps) Claim(c saga.Call, m saga.MigrationInput, done func(error)) { done(f.next("claim")) }
func (f *fakeOps) Unclaim(c saga.Call, m saga.MigrationInput, done func(error)) {
	done(f.next("unclaim"))
}
func (f *fakeOps) Copy(c saga.Call, m saga.MigrationInput, done func([]byte, error)) {
	if err := f.next("copy"); err != nil {
		done(nil, err)
		return
	}
	done(f.copyRoot, nil)
}
func (f *fakeOps) Discard(c saga.Call, m saga.MigrationInput, done func(error)) {
	done(f.next("discard"))
}
func (f *fakeOps) Root(c saga.Call, bucket, loc string, done func([]byte, error)) {
	if err := f.next("root " + loc); err != nil {
		done(nil, err)
		return
	}
	done(f.root(loc), nil)
}
func (f *fakeOps) Flip(c saga.Call, m saga.MigrationInput, done func(error)) { done(f.next("flip")) }
func (f *fakeOps) Pointer(c saga.Call, bucket string, done func(string, error)) {
	if err := f.next("pointer"); err != nil {
		done("", err)
		return
	}
	done(f.pointer, nil)
}
func (f *fakeOps) Seal(c saga.Call, m saga.MigrationInput, done func(bool, error)) {
	if err := f.next("seal"); err != nil {
		done(false, err)
		return
	}
	done(f.retired, nil)
}
func (f *fakeOps) Promote(c saga.Call, m saga.MigrationInput, done func(error)) {
	done(f.next("promote"))
}
func (f *fakeOps) Retire(c saga.Call, m saga.MigrationInput, done func(error)) {
	done(f.next("retire"))
}

func runMigration(t *testing.T, ops *fakeOps, input []byte) saga.Status {
	t.Helper()
	clk := &manualClock{}
	e := memEnv(newMemDisk(), clk)
	eng, err := saga.New(e, saga.Config{Policy: testPolicy()}, saga.Migration(ops))
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Begin("m", saga.MigrationName, input); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000 && clk.fireNext(); i++ {
	}
	st, err := eng.Status("m")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// TestMigrationWorkflow proves every bucket-migration control path against fake
// ops: clean move, verification mismatch, catch-up mismatch retry, superseded
// cleanup, already-retired promotion, invalid input, and retried forward and
// compensation errors.
func TestMigrationWorkflow(t *testing.T) {
	in := saga.MigrationInput{Bucket: "bk", From: "a", To: "b"}
	happy := []string{"claim", "copy", "root b", "flip", "pointer", "seal", "copy", "root a", "root b", "retire", "promote"}
	tests := []struct {
		name     string
		setup    func(f *fakeOps)
		input    []byte
		state    saga.State
		outcome  string
		ops      []string
		wantErr  error
		checkLog func(t *testing.T, log []string)
	}{
		{"a clean migration claims, copies, verifies, flips, catches up, retires the source and promotes the target",
			func(f *fakeOps) {}, nil, saga.StateCompleted, saga.OutcomeMoved, happy, nil, nil},
		{"a target whose root differs from the copied root fails verification until compensation",
			func(f *fakeOps) { f.roots["b"] = [][]byte{[]byte("bad")} }, nil, saga.StateCompensated, "",
			[]string{"claim", "copy", "root b", "root b", "root b", "root b", "discard", "unclaim"}, saga.ErrRootMismatch, nil},
		{"a catch-up whose roots differ is retried until they agree",
			func(f *fakeOps) { f.roots["a"] = [][]byte{[]byte("r2"), []byte("r1")} }, nil, saga.StateCompleted, saga.OutcomeMoved, nil, nil,
			func(t *testing.T, log []string) {
				if n := strings.Count(strings.Join(log, ","), "seal"); n != 2 {
					t.Fatalf("seal ran %d times: %v", n, log)
				}
			}},
		{"a migration that lost the pointer cleans up its target and reports superseded",
			func(f *fakeOps) { f.pointer = "c" }, nil, saga.StateCompleted, saga.OutcomeSuperseded,
			[]string{"claim", "copy", "root b", "flip", "pointer", "discard", "unclaim"}, nil, nil},
		{"a source already retired by this migration only promotes",
			func(f *fakeOps) { f.retired = true }, nil, saga.StateCompleted, saga.OutcomeMoved,
			[]string{"claim", "copy", "root b", "flip", "pointer", "seal", "promote"}, nil, nil},
		{"an input that does not decode is refused and compensated",
			func(f *fakeOps) {}, []byte{0xFF}, saga.StateCompensated, "", nil, env.ErrInvalidArgument, nil},
		{"an input that moves a bucket onto itself is refused",
			func(f *fakeOps) {}, saga.MigrationInput{Bucket: "bk", From: "a", To: "a"}.Encode(), saga.StateCompensated, "", nil, saga.ErrPermanent, nil},
		{"every forward operation that fails once is retried and the migration completes",
			func(f *fakeOps) {
				for _, op := range []string{"claim", "copy", "root b", "flip", "pointer", "seal", "root a", "retire", "promote"} {
					f.fail[op] = []error{errFlaky}
				}
			}, nil, saga.StateCompleted, saga.OutcomeMoved, nil, nil, nil},
		{"a failing catch-up copy and a failing clean-up are retried after the pivot",
			func(f *fakeOps) {
				f.fail["copy"] = []error{nil, errFlaky}
				f.pointer = "c"
				f.fail["discard"] = []error{errFlaky}
				f.fail["unclaim"] = []error{errFlaky}
			}, nil, saga.StateCompleted, saga.OutcomeSuperseded, nil, nil, nil},
		{"failed compensations are retried until they succeed",
			func(f *fakeOps) {
				f.fail["flip"] = []error{saga.ErrPermanent}
				f.fail["discard"] = []error{errFlaky, errFlaky}
				f.fail["unclaim"] = []error{errFlaky}
			}, nil, saga.StateCompensated, "",
			[]string{"claim", "copy", "root b", "flip", "discard", "discard", "discard", "unclaim", "unclaim"}, errFlaky,
			func(t *testing.T, log []string) {}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeOps(in.To)
			tc.setup(f)
			input := tc.input
			if input == nil {
				input = in.Encode()
			}
			st := runMigration(t, f, input)
			if st.State != tc.state {
				t.Fatalf("state %v, want %v (ops %v, err %v)", st.State, tc.state, f.log, st.Err)
			}
			if tc.outcome != "" && (len(st.Outputs) != saga.MigrationSteps || string(st.Outputs[saga.MigrationSteps-1]) != tc.outcome) {
				t.Fatalf("outputs %q, want outcome %q", st.Outputs, tc.outcome)
			}
			if tc.ops != nil && !slices.Equal(f.log, tc.ops) {
				t.Fatalf("ops %v\nwant %v", f.log, tc.ops)
			}
			if tc.wantErr != nil && !errors.Is(st.Err, tc.wantErr) {
				t.Fatalf("status err %v, want %v", st.Err, tc.wantErr)
			}
			if tc.checkLog != nil {
				tc.checkLog(t, f.log)
			}
		})
	}
}
