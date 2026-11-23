package saga_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/saga"
	"github.com/nickstrad/object-store/internal/sim"
)

func sampleLog(t testing.TB) ([]saga.Record, []byte) {
	recs := []saga.Record{
		{Kind: saga.RecBegin, Def: testDef, Time: 77, Data: []byte("input")},
		intent(0, 1), done(0, 1), intent(1, 1),
		{Kind: saga.RecAbort, Step: 1, Data: []byte("why")},
		cintent(1, 1), cdone(1, 1), cintent(0, 1), cdone(0, 1),
		end(saga.StateCompensated),
	}
	return recs, encodeLog(t, recs...)
}

func sameRecords(a, b []saga.Record) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.Kind != y.Kind || x.Step != y.Step || x.Attempt != y.Attempt || x.Time != y.Time ||
			x.Def != y.Def || !bytes.Equal(x.Data, y.Data) || x.State != y.State {
			return false
		}
	}
	return true
}

func recordEnds(t testing.TB, recs []saga.Record) []int {
	var ends []int
	var b []byte
	for _, r := range recs {
		var err error
		b, err = saga.AppendRecord(b, r)
		if err != nil {
			t.Fatal(err)
		}
		ends = append(ends, len(b))
	}
	return ends
}

// TestLogAppendRecover proves log record round-trip, torn-tail prefix decoding,
// ErrLogCorrupt on damaged middle/last/oversized/non-canonical frames, torn-tail
// truncation and empty-log deletion on recovery, and recovery from torn crashes
// at every record boundary in the simulator.
func TestLogAppendRecover(t *testing.T) {
	recs, blob := sampleLog(t)
	ends := recordEnds(t, recs)
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"an empty blob decodes to no records and no tail", func(t *testing.T) {
			got, valid, err := saga.DecodeLog(nil)
			if err != nil || valid != 0 || len(got) != 0 {
				t.Fatalf("got %d records, valid %d, err %v", len(got), valid, err)
			}
		}},
		{"records round-trip in order with every field", func(t *testing.T) {
			got, valid, err := saga.DecodeLog(blob)
			if err != nil || valid != len(blob) {
				t.Fatalf("valid %d of %d, err %v", valid, len(blob), err)
			}
			if !sameRecords(got, recs) {
				t.Fatalf("records differ:\n got %s\nwant %s", kinds(got), kinds(recs))
			}
		}},
		{"every strict prefix decodes to the whole records inside it and reports the rest as a torn tail", func(t *testing.T) {
			for cut := 0; cut < len(blob); cut++ {
				got, valid, err := saga.DecodeLog(blob[:cut])
				if err != nil {
					t.Fatalf("cut %d: %v", cut, err)
				}
				whole := 0
				for _, e := range ends {
					if e <= cut {
						whole++
					}
				}
				if len(got) != whole {
					t.Fatalf("cut %d: %d records, want %d", cut, len(got), whole)
				}
				wantValid := 0
				if whole > 0 {
					wantValid = ends[whole-1]
				}
				if valid != wantValid {
					t.Fatalf("cut %d: valid %d, want %d", cut, valid, wantValid)
				}
			}
		}},
		{"a flipped bit in any byte of a middle record is ErrLogCorrupt", func(t *testing.T) {
			for pos := ends[2]; pos < ends[3]; pos++ {
				for bit := 0; bit < 8; bit++ {
					bad := slices.Clone(blob)
					bad[pos] ^= 1 << bit
					got, valid, err := saga.DecodeLog(bad)
					if !errors.Is(err, saga.ErrLogCorrupt) {
						t.Fatalf("pos %d bit %d: err %v", pos, bit, err)
					}
					if valid != ends[2] || len(got) != 3 {
						t.Fatalf("pos %d bit %d: valid %d records %d, want %d and 3", pos, bit, valid, len(got), ends[2])
					}
				}
			}
		}},
		{"a flipped bit in the last whole record is ErrLogCorrupt, not a torn tail", func(t *testing.T) {
			last := len(ends) - 1
			for pos := ends[last-1]; pos < ends[last]; pos++ {
				bad := slices.Clone(blob)
				bad[pos] ^= 0x10
				if _, _, err := saga.DecodeLog(bad); !errors.Is(err, saga.ErrLogCorrupt) {
					t.Fatalf("pos %d: err %v", pos, err)
				}
			}
		}},
		{"a record longer than MaxRecord is refused on encode", func(t *testing.T) {
			_, err := saga.AppendRecord(nil, saga.Record{Kind: saga.RecDone, Data: make([]byte, saga.MaxRecord)})
			if !errors.Is(err, env.ErrTooLarge) {
				t.Fatalf("err %v", err)
			}
		}},
		{"a record with an unknown kind is refused on encode", func(t *testing.T) {
			_, err := saga.AppendRecord(nil, saga.Record{Kind: 0})
			if !errors.Is(err, env.ErrInvalidArgument) {
				t.Fatalf("err %v", err)
			}
		}},
		{"a header with a valid checksum over an oversized length is ErrLogCorrupt", func(t *testing.T) {
			bad := saga.FrameForTest(saga.MaxRecord+1, nil)
			if _, _, err := saga.DecodeLog(bad); !errors.Is(err, saga.ErrLogCorrupt) {
				t.Fatalf("err %v", err)
			}
		}},
		{"a checksummed payload with trailing or non-canonical bytes is ErrLogCorrupt", func(t *testing.T) {
			good := encodeLog(t, begin())
			payload := saga.PayloadForTest(good)
			for _, p := range [][]byte{append(slices.Clone(payload), 0), {}, {0xFF}, {byte(saga.RecIntent), 0x80, 0x00}} {
				bad := saga.FrameForTest(len(p), p)
				if _, _, err := saga.DecodeLog(bad); !errors.Is(err, saga.ErrLogCorrupt) {
					t.Fatalf("payload %x: err %v", p, err)
				}
			}
		}},
		{"recovery truncates a torn tail so later appends decode cleanly", func(t *testing.T) {
			d := newMemDisk()
			name := saga.DefaultPrefix + "x"
			whole := encodeLog(t, begin(), intent(0, 1))
			torn := encodeLog(t, done(0, 1))
			d.put(name, append(slices.Clone(whole), torn[:len(torn)-3]...))
			f := newFx(2, 2)
			clk := &manualClock{}
			eng, err := saga.New(memEnv(d, clk), saga.Config{Policy: testPolicy()}, f.def(memEnv(d, clk)))
			if err != nil {
				t.Fatal(err)
			}
			if err := eng.Recover(); err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(d.ops, "write "+name) {
				t.Fatalf("no truncating write: %v", d.ops)
			}
			for clk.fireNext() {
			}
			got, valid, err := saga.DecodeLog(d.blobs[name])
			if err != nil || valid != len(d.blobs[name]) {
				t.Fatalf("after recovery: valid %d of %d, err %v (%s)", valid, len(d.blobs[name]), err, kinds(got))
			}
			if got[len(got)-1].Kind != saga.RecEnd {
				t.Fatalf("saga did not finish: %s", kinds(got))
			}
		}},
		{"a torn first record leaves no saga and removes the blob", func(t *testing.T) {
			d := newMemDisk()
			name := saga.DefaultPrefix + "y"
			b := encodeLog(t, begin())
			d.put(name, b[:4])
			clk := &manualClock{}
			eng, err := saga.New(memEnv(d, clk), saga.Config{Policy: testPolicy()}, newFx(1, 1).def(memEnv(d, clk)))
			if err != nil {
				t.Fatal(err)
			}
			if err := eng.Recover(); err != nil {
				t.Fatal(err)
			}
			if _, present := d.blobs[name]; present {
				t.Fatal("torn-only blob was kept")
			}
			if _, err := eng.Status("y"); !errors.Is(err, saga.ErrSagaNotFound) {
				t.Fatalf("status err %v", err)
			}
		}},
		{"a torn crash in the simulator at every record boundary recovers and finishes", func(t *testing.T) {
			for k := 1; k <= 8; k++ {
				for seed := int64(1); seed <= 4; seed++ {
					f := newFx(3, 3)
					n := 0
					var s *single
					cfg := saga.Config{Policy: testPolicy(), Hook: func(p saga.Point) bool {
						n++
						if n != k {
							return false
						}
						s.s.At(s.s.Now(), "crash", func() { _ = s.s.CrashTorn(hostID) })
						s.s.At(s.s.Now().Add(time.Second), "restart", func() { _ = s.s.Restart(hostID) })
						return true
					}}
					s = newSingle(t, seed, f, cfg)
					s.begin(t, beginAt, "t", nil)
					s.run(t, 10*time.Second)
					st, err := hostOf(s.s).eng.Status("t")
					if k == 1 && errors.Is(err, saga.ErrSagaNotFound) {
						if len(f.calls) != 0 {
							t.Fatalf("seed=%d: a saga whose begin record was lost ran steps", seed)
						}
						continue
					}
					if err != nil || st.State != saga.StateCompleted {
						t.Fatalf("k=%d seed=%d: state %v err %v", k, seed, st.State, err)
					}
					for i, on := range f.effect {
						if !on {
							t.Fatalf("k=%d seed=%d: step %d effect missing", k, seed, i)
						}
					}
				}
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, tc.run)
	}
}

// TestLogCorruptMiddleRecordFailsRecovery proves that a damaged middle record
// fails Recover and Status with ErrLogCorrupt, leaves the blob untouched on
// disk, schedules no work, and blocks Begin from reusing the ID.
func TestLogCorruptMiddleRecordFailsRecovery(t *testing.T) {
	d := newMemDisk()
	name := saga.DefaultPrefix + "m"
	b := encodeLog(t, begin(), intent(0, 1), done(0, 1), intent(1, 1))
	mid := len(encodeLog(t, begin())) + 3
	b[mid] ^= 0x01
	d.put(name, b)
	clk := &manualClock{}
	eng, err := saga.New(memEnv(d, clk), saga.Config{Policy: testPolicy()}, newFx(2, 2).def(memEnv(d, clk)))
	if err != nil {
		t.Fatal(err)
	}
	err = eng.Recover()
	if !errors.Is(err, saga.ErrLogCorrupt) {
		t.Fatalf("Recover err %v, want ErrLogCorrupt", err)
	}
	if _, err := eng.Status("m"); !errors.Is(err, saga.ErrLogCorrupt) {
		t.Fatalf("Status err %v, want ErrLogCorrupt", err)
	}
	if !bytes.Equal(d.blobs[name], b) {
		t.Fatal("recovery rewrote a corrupt log; the evidence must stay as found")
	}
	if clk.pending() != 0 {
		t.Fatal("a corrupt saga scheduled work")
	}
	if err := eng.Begin("m", testDef, nil); !errors.Is(err, env.ErrInvalidArgument) {
		t.Fatalf("Begin over a corrupt log: %v", err)
	}
}

// TestRecoverRetriesReadErrors proves that transient disk read failures during
// Recover are reported on startup and retried on a timer until the saga finishes.
func TestRecoverRetriesReadErrors(t *testing.T) {
	f := newFx(2, 2)
	s := newSingle(t, 3, f, saga.Config{Policy: testPolicy()})
	s.begin(t, beginAt, "r", nil)
	s.s.At(beginAt.Add(stepLatency/2), "crash", func() { _ = s.s.Crash(hostID) })
	s.s.At(env.Time(time.Second), "fail reads", func() { _ = s.s.FailReads(hostID, "", 3) })
	s.s.At(env.Time(time.Second), "restart", func() { _ = s.s.Restart(hostID) })
	s.run(t, 10*time.Second)
	if st := s.status(t, "r"); st.State != saga.StateCompleted {
		t.Fatalf("state %v", st.State)
	}
	if h := hostOf(s.s); !errors.Is(h.err, env.ErrIO) {
		t.Fatalf("first Recover error %v, want ErrIO reported", h.err)
	}
}

// TestDecodeLogOnSimDiskAfterTornCrash proves that DecodeLog cleanly decodes
// the durable prefix left by sim.Disk after CrashTorn across 20 seeds.
func TestDecodeLogOnSimDiskAfterTornCrash(t *testing.T) {
	for seed := int64(1); seed <= 20; seed++ {
		s := sim.New(sim.Config{Seed: seed})
		var e env.Env
		if err := s.AddNode(sim.NodeSpec{ID: hostID, Region: "r"}, func(x env.Env) env.Process {
			e = x
			return &host{e: x}
		}); err != nil {
			t.Fatal(err)
		}
		first := encodeLog(t, begin())
		second := encodeLog(t, intent(0, 1))
		s.At(1, "write", func() {
			if err := e.Disk.Append("l", first); err != nil {
				s.Fail(err)
			}
			if err := e.Disk.Sync("l"); err != nil {
				s.Fail(err)
			}
			if err := e.Disk.Append("l", second); err != nil {
				s.Fail(err)
			}
			_ = s.CrashTorn(hostID)
		})
		if err := s.RunUntil(10); err != nil {
			t.Fatal(err)
		}
		b, _ := s.Disk(hostID).PeekDurable("l")
		recs, valid, err := saga.DecodeLog(b)
		if err != nil || valid < len(first) || (len(recs) != 1 && len(recs) != 2) {
			t.Fatalf("seed %d: %d records, valid %d, err %v", seed, len(recs), valid, err)
		}
	}
}
