package saga_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

const (
	sagaCodeMin env.Code = env.CodeAppMin + 80
	sagaCodeMax env.Code = env.CodeAppMin + 95
)

// TestCodesLieInTheSagaBlock proves that every wire code in saga.Codes falls in
// the allocated 144..159 block with its expected value.
func TestCodesLieInTheSagaBlock(t *testing.T) {
	if sagaCodeMin != 144 || sagaCodeMax != 159 {
		t.Fatalf("block %d..%d, want 144..159", sagaCodeMin, sagaCodeMax)
	}
	want := map[error]env.Code{saga.ErrPermanent: 144, saga.ErrSagaNotFound: 145, saga.ErrLogCorrupt: 146}
	for _, row := range saga.Codes() {
		if row.Code < sagaCodeMin || row.Code > sagaCodeMax {
			t.Fatalf("%v has code %d outside %d..%d", row.Err, row.Code, sagaCodeMin, sagaCodeMax)
		}
		if want[row.Err] != row.Code {
			t.Fatalf("%v has code %d, want %d", row.Err, row.Code, want[row.Err])
		}
	}
	if len(saga.Codes()) != len(want) {
		t.Fatalf("%d codes, want %d", len(saga.Codes()), len(want))
	}
}

type faultyDisk struct {
	env.Disk
	failAppend bool
	failSync   bool
}

func (d *faultyDisk) Append(name string, data []byte) error {
	if d.failAppend {
		return fmt.Errorf("append %s: %w", name, env.ErrIO)
	}
	return d.Disk.Append(name, data)
}

func (d *faultyDisk) Sync(name string) error {
	if d.failSync {
		return fmt.Errorf("sync %s: %w", name, env.ErrIO)
	}
	return d.Disk.Sync(name)
}

// TestBeginWriteFailureOnSimDiskIsNotNotFound proves that when Begin fails
// during Append or Sync on sim.Disk, cleanup removes any unsynced blob and the
// returned error matches env.ErrIO without a spurious env.ErrNotFound.
func TestBeginWriteFailureOnSimDiskIsNotNotFound(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failAppend bool
		failSync   bool
	}{
		{"the append of the begin record fails", true, false},
		{"the sync of the begin record fails", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := sim.New(sim.Config{Seed: 1})
			var fd *faultyDisk
			var eng *saga.Engine
			f := newFx(2, 1)
			err := s.AddNode(sim.NodeSpec{ID: hostID, Region: "r"}, func(e env.Env) env.Process {
				fd = &faultyDisk{Disk: e.Disk, failAppend: tc.failAppend, failSync: tc.failSync}
				e.Disk = fd
				h := &host{e: e}
				return &starter{h: h, start: func() {
					eng, h.err = saga.New(e, saga.Config{Policy: testPolicy()}, f.def(e))
					h.eng = eng
				}}
			})
			if err != nil {
				t.Fatal(err)
			}
			var berr, again error
			var leftover error
			s.At(beginAt, "begin", func() {
				berr = eng.Begin("w", testDef, []byte("in"))
				_, leftover = fd.Disk.Read(saga.DefaultPrefix + "w")
				fd.failAppend, fd.failSync = false, false
				again = eng.Begin("w2", testDef, []byte("in"))
			})
			if err := s.RunUntil(env.Time(time.Second)); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(berr, env.ErrIO) {
				t.Fatalf("begin: %v, want ErrIO", berr)
			}
			if errors.Is(berr, env.ErrNotFound) {
				t.Fatalf("begin reports a spurious ErrNotFound: %v", berr)
			}
			if !errors.Is(leftover, env.ErrNotFound) {
				t.Fatalf("the failed begin left its log behind: %v", leftover)
			}
			if again != nil {
				t.Fatalf("a begin after the disk recovered: %v", again)
			}
		})
	}
}

type mutatingStep struct {
	seen *[]string
	i    int
}

func (m *mutatingStep) Name() string { return stepName(m.i) }

func (m *mutatingStep) Do(c saga.Call, done func([]byte, error)) {
	if m.i == 0 {
		done([]byte("out0"), nil)
		return
	}
	*m.seen = append(*m.seen, fmt.Sprintf("attempt=%d input=%q out0=%q", c.Attempt, c.Input, c.Outputs[0]))
	if c.Attempt == 1 {
		c.Input[0] ^= 0xFF
		c.Outputs[0][0] ^= 0xFF
		done(nil, errFlaky)
		return
	}
	done([]byte("out1"), nil)
}

func (m *mutatingStep) Compensate(c saga.Call, done func(error)) {
	if len(c.Input) > 0 {
		c.Input[0] ^= 0xFF
	}
	for _, o := range c.Outputs {
		if len(o) > 0 {
			o[0] ^= 0xFF
		}
	}
	done(nil)
}

// TestStepCannotCorruptTheEngineSnapshot proves that in-place mutations of
// Call.Input or Call.Outputs by a step do not affect subsequent retries or
// Engine.Status.
func TestStepCannotCorruptTheEngineSnapshot(t *testing.T) {
	d := newMemDisk()
	clk := &manualClock{now: 10}
	var seen []string
	def := saga.Definition{Name: testDef, Steps: []saga.Step{&mutatingStep{seen: &seen, i: 0}, &mutatingStep{seen: &seen, i: 1}}, Pivot: 2}
	eng, err := saga.New(memEnv(d, clk), saga.Config{Policy: testPolicy()}, def)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Recover(); err != nil {
		t.Fatal(err)
	}
	if err := eng.Begin("m", testDef, []byte("input")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20 && clk.fireNext(); i++ {
	}
	want := []string{`attempt=1 input="input" out0="out0"`, `attempt=2 input="input" out0="out0"`}
	if len(seen) != 2 || seen[0] != want[0] || seen[1] != want[1] {
		t.Fatalf("calls saw %q, want %q", seen, want)
	}
	st, err := eng.Status("m")
	if err != nil || st.State != saga.StateCompleted || !bytes.Equal(st.Outputs[0], []byte("out0")) {
		t.Fatalf("status %+v err %v", st, err)
	}
}

// TestMigrationInputValidateMatchesTheCodec proves that MigrationInput.Validate
// enforces the same 1024-byte per-field bound as DecodeMigrationInput.
func TestMigrationInputValidateMatchesTheCodec(t *testing.T) {
	long := strings.Repeat("x", 1025)
	edge := strings.Repeat("y", 1024)
	for _, in := range []saga.MigrationInput{
		{Bucket: long, From: "a", To: "b"},
		{Bucket: "b", From: long, To: "b"},
		{Bucket: "b", From: "a", To: long},
	} {
		if err := in.Validate(); !errors.Is(err, env.ErrInvalidArgument) {
			t.Fatalf("an input the codec refuses passed Validate: %d/%d/%d bytes: %v", len(in.Bucket), len(in.From), len(in.To), err)
		}
	}
	for _, in := range []saga.MigrationInput{{Bucket: edge, From: edge, To: "b"}, {Bucket: "b", From: "a", To: edge}} {
		if err := in.Validate(); err != nil {
			t.Fatalf("a field of 1024 bytes was refused: %v", err)
		}
		out, err := saga.DecodeMigrationInput(in.Encode())
		if err != nil || out != in {
			t.Fatalf("an input that passed Validate does not round-trip: %v", err)
		}
	}
}
