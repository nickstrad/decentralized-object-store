package sagasim

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
)

type mapDisk struct {
	blobs map[string][]byte
	fail  error
}

func (d *mapDisk) Read(name string) ([]byte, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	b, ok := d.blobs[name]
	if !ok {
		return nil, env.ErrNotFound
	}
	return slices.Clone(b), nil
}
func (d *mapDisk) Write(name string, data []byte) error {
	if d.fail != nil {
		return d.fail
	}
	d.blobs[name] = slices.Clone(data)
	return nil
}
func (d *mapDisk) Append(name string, data []byte) error {
	return d.Write(name, append(d.blobs[name], data...))
}
func (d *mapDisk) Sync(string) error             { return d.fail }
func (d *mapDisk) Delete(name string) error      { delete(d.blobs, name); return nil }
func (d *mapDisk) List(string) ([]string, error) { return nil, nil }

type nopClock struct{ fired []func() }

func (c *nopClock) Now() env.Time { return 0 }
func (c *nopClock) AfterFunc(d time.Duration, fn func()) env.Timer {
	c.fired = append(c.fired, fn)
	return nopTimer{}
}

type nopTimer struct{}

func (nopTimer) Stop() bool               { return false }
func (nopTimer) Reset(time.Duration) bool { return false }

type heldNet struct {
	reqs []func([]byte, error)
}

func (n *heldNet) Send(env.NodeID, []byte) error { return nil }
func (n *heldNet) Request(_ env.NodeID, _ []byte, _ time.Duration, cb func([]byte, error)) {
	n.reqs = append(n.reqs, cb)
}

func newTestStore(id env.NodeID, d *mapDisk) (*Store, *nopClock) {
	clk := &nopClock{}
	seeds := []BucketSeed{{Name: Bucket, Loc: "a", Objs: objects(2)}}
	s := NewStore(seeds)(env.Env{ID: id, Clock: clk, Disk: d, Net: &heldNet{}}).(*Store)
	s.Start()
	return s, clk
}

func ask(s *Store, op byte, fields ...string) ([]byte, error) {
	var fs [][]byte
	for _, f := range fields {
		fs = append(fs, []byte(f))
	}
	var resp []byte
	var rerr error
	called := 0
	s.OnRequest("x", encodeFields(op, fs...), func(r []byte, err error) {
		called++
		resp, rerr = r, err
	})
	if called != 1 {
		return nil, fmt.Errorf("reply called %d times", called)
	}
	return resp, rerr
}

func stamp(ts int64) string { return string(binary.BigEndian.AppendUint64(nil, uint64(ts))) }

func TestWireRejectsMalformed(t *testing.T) {
	good := encodeFields(opRoot, []byte("bk"))
	bad := [][]byte{
		nil,
		{opRoot},
		{opRoot, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x01},
		{opRoot, 65},
		{opRoot, 1, 5, 'a'},
		append(slices.Clone(good), 0),
	}
	for i, b := range bad {
		if _, _, err := decodeFields(b); !errors.Is(err, ErrMalformed) {
			t.Fatalf("case %d (%x): %v", i, b, err)
		}
	}
	if _, err := decodeObjs(encodeFields(0, []byte("k"))); !errors.Is(err, ErrMalformed) {
		t.Fatalf("odd object fields: %v", err)
	}
	for i, b := range [][]byte{encodeFields(1, []byte("x")), encodeFields(1, encodeFields(0, []byte("x")), encodeFields(0)),
		encodeFields(1, encodeFields(0), encodeFields(0, encodeFields(0, []byte("b"))))} {
		if _, err := decodeState(b); !errors.Is(err, ErrMalformed) {
			t.Fatalf("state %d: %v", i, err)
		}
	}
	st := storeState{Buckets: []*bucketState{{Name: "b", Status: statusLive, Regs: []string{"r1", "r2"}, Objs: objects(3)}}, Ptrs: []pointerState{{Bucket: "b", Loc: "a", TS: -5, Writer: "w"}}}
	back, err := decodeState(st.encode())
	if err != nil || !slices.Equal(back.Buckets[0].Regs, []string{"r1", "r2"}) || back.Ptrs[0] != st.Ptrs[0] || string(Root(back.Buckets[0].Objs)) != string(Root(st.Buckets[0].Objs)) {
		t.Fatalf("state round trip: %+v %v", back, err)
	}
}

func TestStoreOperations(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"a source registers sagas once and refuses a bucket it does not hold", func(t *testing.T) {
			s, _ := newTestStore("a", &mapDisk{blobs: map[string][]byte{}})
			for i := 0; i < 2; i++ {
				if _, err := ask(s, opRegister, Bucket, "s1"); err != nil {
					t.Fatal(err)
				}
			}
			if bk := s.st.bucket(Bucket); !slices.Equal(bk.Regs, []string{"s1"}) {
				t.Fatalf("regs %v", bk.Regs)
			}
			if _, err := ask(s, opRegister, "other", "s1"); !errors.Is(err, saga.ErrPermanent) {
				t.Fatalf("err %v", err)
			}
			if _, err := ask(s, opDeregister, Bucket, "s1"); err != nil || len(s.st.bucket(Bucket).Regs) != 0 {
				t.Fatalf("deregister: %v", err)
			}
			if _, err := ask(s, opDeregister, Bucket, "s1"); err != nil {
				t.Fatal(err)
			}
		}},
		{"a seal waits while another saga is registered", func(t *testing.T) {
			s, _ := newTestStore("a", &mapDisk{blobs: map[string][]byte{}})
			for _, id := range []string{"s1", "s2"} {
				if _, err := ask(s, opRegister, Bucket, id); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ask(s, opSeal, Bucket, "s1"); !errors.Is(err, ErrBusy) {
				t.Fatalf("err %v", err)
			}
			if _, err := ask(s, opDeregister, Bucket, "s2"); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if r, err := ask(s, opSeal, Bucket, "s1"); err != nil || r[0] != 0 {
					t.Fatalf("seal: %v %v", r, err)
				}
			}
			if _, err := ask(s, opSeal, Bucket, "s2"); !errors.Is(err, ErrBusy) {
				t.Fatalf("a second sealer: %v", err)
			}
			if _, err := ask(s, opPut, Bucket, "k", "v"); !errors.Is(err, ErrMoved) {
				t.Fatalf("put on a sealed source: %v", err)
			}
			if _, err := ask(s, opRetire, Bucket, "s2"); !errors.Is(err, saga.ErrPermanent) {
				t.Fatalf("retire by another saga: %v", err)
			}
			for i := 0; i < 2; i++ {
				if _, err := ask(s, opRetire, Bucket, "s1"); err != nil {
					t.Fatal(err)
				}
			}
			if r, err := ask(s, opSeal, Bucket, "s1"); err != nil || r[0] != 1 {
				t.Fatalf("seal after retire: %v %v", r, err)
			}
			if _, err := ask(s, opRoot, Bucket); !errors.Is(err, env.ErrNotFound) {
				t.Fatalf("root of a retired bucket: %v", err)
			}
			if _, err := ask(s, opDump, Bucket); !errors.Is(err, saga.ErrPermanent) {
				t.Fatalf("dump of a retired bucket: %v", err)
			}
		}},
		{"a target claims once, refuses another saga, and a release with data leaves orphans", func(t *testing.T) {
			s, _ := newTestStore("b", &mapDisk{blobs: map[string][]byte{}})
			for i := 0; i < 2; i++ {
				if _, err := ask(s, opClaim, Bucket, "s1"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ask(s, opClaim, Bucket, "s2"); !errors.Is(err, saga.ErrPermanent) {
				t.Fatalf("err %v", err)
			}
			if _, err := ask(s, opPut, Bucket, "k", "v"); !errors.Is(err, ErrMoved) {
				t.Fatalf("put on staging: %v", err)
			}
			s.st.bucket(Bucket).Objs = objects(1)
			if _, err := ask(s, opRelease, Bucket, "s1"); err != nil {
				t.Fatal(err)
			}
			if bk := s.st.bucket(Bucket); bk == nil || bk.Status != statusRetired || len(bk.Objs) != 1 {
				t.Fatalf("release with data: %+v", bk)
			}
			if _, err := ask(s, opClaim, Bucket, "s3"); err != nil {
				t.Fatalf("claim over a released bucket: %v", err)
			}
			s.st.bucket(Bucket).Objs = objects(1)
			if _, err := ask(s, opDiscard, Bucket, "s9"); err != nil || len(s.st.bucket(Bucket).Objs) != 1 {
				t.Fatalf("discard by another saga: %v", err)
			}
			if _, err := ask(s, opDiscard, Bucket, "s3"); err != nil || len(s.st.bucket(Bucket).Objs) != 0 {
				t.Fatalf("discard: %v", err)
			}
			if _, err := ask(s, opPromote, Bucket, "s9"); !errors.Is(err, saga.ErrPermanent) {
				t.Fatalf("promote by another saga: %v", err)
			}
			for i := 0; i < 2; i++ {
				if _, err := ask(s, opPromote, Bucket, "s3"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ask(s, opPut, Bucket, "k", "v"); err != nil {
				t.Fatal(err)
			}
			if _, err := ask(s, opPut, Bucket, "k", "w"); err != nil || string(s.st.bucket(Bucket).Objs[0].Val) != "w" {
				t.Fatalf("overwrite: %v", err)
			}
			if _, err := ask(s, opRelease, Bucket, "s3"); err != nil || s.st.bucket(Bucket).Status != statusLive {
				t.Fatalf("release of a live bucket: %v", err)
			}
		}},
		{"a copy overtaken by a discard, a release or a newer copy is not applied", func(t *testing.T) {
			for _, between := range []byte{opDiscard, opRelease, opCopy} {
				s, _ := newTestStore("b", &mapDisk{blobs: map[string][]byte{}})
				net := s.e.Net.(*heldNet)
				if _, err := ask(s, opClaim, Bucket, "s1"); err != nil {
					t.Fatal(err)
				}
				var late error
				s.OnRequest("x", encodeFields(opCopy, []byte(Bucket), []byte("s1"), []byte("a")), func(_ []byte, err error) { late = err })
				if between == opCopy {
					s.OnRequest("x", encodeFields(opCopy, []byte(Bucket), []byte("s1"), []byte("a")), func([]byte, error) {})
				} else if _, err := ask(s, between, Bucket, "s1"); err != nil {
					t.Fatal(err)
				}
				net.reqs[0](encodeObjs(objects(3)), nil)
				if !errors.Is(late, ErrBusy) || s.fenced != 1 {
					t.Fatalf("op %d: late copy err %v fenced %d", between, late, s.fenced)
				}
				if bk := s.st.bucket(Bucket); bk != nil && len(bk.Objs) != 0 {
					t.Fatalf("op %d: late copy left %d objects", between, len(bk.Objs))
				}
				if between == opCopy {
					var fresh []byte
					net.reqs[1](encodeObjs(objects(2)), nil)
					fresh = Root(s.st.bucket(Bucket).Objs)
					if string(fresh) != string(Root(objects(2))) {
						t.Fatal("the newest copy was not applied")
					}
				}
			}
		}},
		{"pointer replicas keep the write with the larger stamp", func(t *testing.T) {
			s, _ := newTestStore("c", &mapDisk{blobs: map[string][]byte{}})
			for _, f := range [][4]string{{Bucket, "b", stamp(10), "x"}, {Bucket, "c", stamp(5), "y"}, {Bucket, "c", stamp(10), "a"}, {Bucket, "d", stamp(10), "z"}, {Bucket, "c", stamp(10), "z"}, {Bucket, "a", stamp(10), "z"}} {
				if _, err := ask(s, opFlip, f[0], f[1], f[2], f[3]); err != nil {
					t.Fatal(err)
				}
			}
			r, err := ask(s, opPointer, Bucket)
			_, f, derr := decodeFields(r)
			if err != nil || derr != nil || string(f[0]) != "d" {
				t.Fatalf("pointer %q %v %v", f, err, derr)
			}
			if _, err := ask(s, opFlip, "nb", "b", stamp(1), "x"); err != nil {
				t.Fatal(err)
			}
			if _, err := ask(s, opFlip, Bucket, "b", "short", "x"); !errors.Is(err, ErrMalformed) {
				t.Fatalf("short stamp: %v", err)
			}
		}},
		{"malformed and unknown requests are refused", func(t *testing.T) {
			s, _ := newTestStore("a", &mapDisk{blobs: map[string][]byte{}})
			if _, err := ask(s, 99, "x"); !errors.Is(err, ErrMalformed) {
				t.Fatalf("unknown op: %v", err)
			}
			if _, err := ask(s, opRoot); !errors.Is(err, ErrMalformed) {
				t.Fatalf("missing field: %v", err)
			}
			var got error
			s.OnRequest("x", []byte{}, func(_ []byte, err error) { got = err })
			if !errors.Is(got, ErrMalformed) {
				t.Fatalf("empty request: %v", got)
			}
			if _, err := ask(s, opCopy, Bucket, "s1", "a"); !errors.Is(err, saga.ErrPermanent) {
				t.Fatalf("copy into an unclaimed bucket: %v", err)
			}
			if _, _, err := s.handle(opCopy, [][]byte{[]byte(Bucket), nil, nil}); !errors.Is(err, ErrMalformed) {
				t.Fatalf("copy through handle: %v", err)
			}
		}},
		{"a store that cannot read its state retries and refuses requests meanwhile", func(t *testing.T) {
			d := &mapDisk{blobs: map[string][]byte{}, fail: env.ErrIO}
			s, clk := newTestStore("a", d)
			if _, err := ask(s, opRoot, Bucket); !errors.Is(err, env.ErrIO) {
				t.Fatalf("err %v", err)
			}
			d.fail = nil
			clk.fired[0]()
			if _, err := ask(s, opRoot, Bucket); err != nil {
				t.Fatal(err)
			}
			s2, _ := newTestStore("a", d)
			if !s2.loaded || len(s2.st.bucket(Bucket).Objs) != 2 {
				t.Fatal("state did not survive a restart")
			}
			d.blobs[stateBlob] = []byte{1}
			s3, _ := newTestStore("a", d)
			if s3.loaded || s3.loadErrs != 1 || len(s3.EncodeState()) == 0 {
				t.Fatal("an undecodable state was accepted")
			}
			d.fail = env.ErrIO
			if err := s.persist(); !errors.Is(err, env.ErrIO) {
				t.Fatal(err)
			}
			if _, err := ask(s, opRegister, Bucket, "s1"); !errors.Is(err, env.ErrIO) {
				t.Fatalf("a failed persist was acknowledged: %v", err)
			}
			d.blobs = map[string][]byte{}
			s4, clk4 := newTestStore("a", d)
			if s4.loaded || len(clk4.fired) != 1 {
				t.Fatal("a failed first persist did not schedule a retry")
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, tc.run)
	}
}

type driver struct {
	e env.Env
}

func (d *driver) Start()                        {}
func (d *driver) OnDatagram(env.NodeID, []byte) {}
func (d *driver) OnRequest(_ env.NodeID, _ []byte, reply env.Reply) {
	reply(nil, env.ErrInvalidArgument)
}
func (d *driver) EncodeState() []byte { return nil }

func TestOpsAgainstStores(t *testing.T) {
	table, err := env.NewCodeTable(Codes()...)
	if err != nil {
		t.Fatal(err)
	}
	s := sim.New(sim.Config{Seed: 1, Codes: table})
	seeds := []BucketSeed{{Name: Bucket, Loc: "a", Objs: objects(2)}}
	for _, id := range []env.NodeID{"a", "b"} {
		if err := s.AddNode(sim.NodeSpec{ID: id, Region: "r"}, NewStore(seeds)); err != nil {
			t.Fatal(err)
		}
	}
	var d *driver
	if err := s.AddNode(sim.NodeSpec{ID: "d", Region: "r"}, func(e env.Env) env.Process { d = &driver{e: e}; return d }); err != nil {
		t.Fatal(err)
	}
	var results []error
	var locs []string
	c := saga.Call{Key: saga.Key{Saga: "s1", Step: 0}, Begun: 7}
	s.At(1, "flip one", func() {
		d.e.Net.Request("a", encodeFields(opFlip, []byte(Bucket), []byte("b"), []byte(stamp(9)), []byte("s1")), time.Second, func(_ []byte, err error) {
			results = append(results, err)
		})
	})
	s.At(env.Time(time.Second), "ops", func() {
		o := NewOps(d.e, []env.NodeID{"b", "a"})
		o.Pointer(c, Bucket, func(loc string, err error) { locs = append(locs, loc); results = append(results, err) })
		o.Root(c, Bucket, "zz", func(_ []byte, err error) { results = append(results, err) })
		o.Claim(c, saga.MigrationInput{Bucket: Bucket, From: "a", To: "zz"}, func(err error) { results = append(results, err) })
		o.Unclaim(c, saga.MigrationInput{Bucket: Bucket, From: "zz", To: "yy"}, func(err error) { results = append(results, err) })
		o.Claim(c, saga.MigrationInput{Bucket: "nope", From: "a", To: "b"}, func(err error) { results = append(results, err) })
	})
	if err := s.RunUntil(env.Time(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(results) != 6 || results[0] != nil {
		t.Fatalf("results %v", results)
	}
	got := results[1:]
	if !slices.ContainsFunc(got, func(e error) bool { return errors.Is(e, ErrUnsettled) }) || len(locs) != 1 || locs[0] != "" {
		t.Fatalf("pointer with disagreeing replicas: %v", got)
	}
	perm := 0
	for _, e := range got {
		if errors.Is(e, saga.ErrPermanent) {
			perm++
		}
	}
	if perm != 3 || !slices.Contains(got, nil) {
		t.Fatalf("results %v", got)
	}
	for _, e := range []error{ErrBusy, ErrMoved, ErrUnsettled, ErrMalformed, saga.ErrPermanent} {
		code, msg := table.Encode(e)
		if !errors.Is(table.Decode(code, msg), e) {
			t.Fatalf("%v does not cross the wire", e)
		}
	}
}

func TestRunnerOnTheModel(t *testing.T) {
	h := ModelHarness()
	cells := Cells()
	if len(cells) != MatrixCells {
		t.Fatalf("%d cells", len(cells))
	}
	for _, c := range []Cell{{Step: 1, Kind: CrashAfterAction, Torn: true, Double: true}, {Step: 0, Kind: FailMidCompensation, Torn: true}, {Step: 4, Kind: FailBeforeAction}} {
		rep := &Report{}
		if _, err := simtest.CheckDeterministic(1, Run(h, c, RunOptions{Report: rep})); err != nil {
			t.Fatalf("%s: %v", c.Name(), err)
		}
		if rep.State != c.Want() || rep.Elapsed <= 0 || len(rep.Calls) != MigrationSteps {
			t.Fatalf("%s: report %+v", c.Name(), rep)
		}
	}
	if n := (Cell{Step: 9, Kind: 42}).Name(); !strings.HasPrefix(n, "step9-kind-42") {
		t.Fatalf("name %q", n)
	}
	broken := Run(errHarness{}, cells[0], RunOptions{})
	if _, err := simtest.CheckDeterministic(1, broken); !errors.Is(err, env.ErrInvalidArgument) {
		t.Fatalf("a harness error was not reported: %v", err)
	}
}

type errHarness struct{}

func (errHarness) Build(seed int64, o BuildOptions) (*sim.Sim, Cluster, error) {
	return sim.New(sim.Config{Seed: seed}), nil, env.ErrInvalidArgument
}

func TestModelAccessors(t *testing.T) {
	s, m, err := Build(1, Options{
		Coordinators: []sim.NodeSpec{{ID: "c1", Region: "r"}},
		Stores:       []sim.NodeSpec{{ID: "a", Region: "r"}, {ID: "b", Region: "r"}},
		Client:       &sim.NodeSpec{ID: "cl", Region: "r"},
		Source:       "a", Policy: MatrixPolicy(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RunUntil(env.Time(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if m.Engine("c1") == nil || m.Engine("a") != nil || m.Engine("zz") != nil {
		t.Fatal("engine lookup")
	}
	cp := s.Process("c1").(*coordProcess)
	if cp.Err() != nil || cp.Engine() == nil || len(cp.EncodeState()) == 0 {
		t.Fatalf("coordinator %v", cp.Err())
	}
	var got error
	cp.OnRequest("x", nil, func(_ []byte, err error) { got = err })
	cp.OnDatagram("x", nil)
	cl := s.Process("cl").(*Client)
	cl.OnRequest("x", nil, func(_ []byte, err error) { got = errors.Join(got, err) })
	cl.OnDatagram("x", nil)
	if !errors.Is(got, env.ErrInvalidArgument) {
		t.Fatalf("requests to non-servers: %v", got)
	}
	if _, ok := m.Status("c1", "none"); ok {
		t.Fatal("status of an unknown saga")
	}
	if err := m.Begin("a", "x", saga.MigrationInput{}); !errors.Is(err, env.ErrUnreachable) {
		t.Fatalf("begin on a store: %v", err)
	}
	m.Write(0)
	if m.WritesIdle() {
		t.Fatal("a write in flight is idle")
	}
	if err := s.RunUntil(env.Time(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if !m.WritesIdle() || m.Acked() != 1 || m.Pointer(Bucket) != "a" {
		t.Fatalf("idle %v acked %d pointer %q", m.WritesIdle(), m.Acked(), m.Pointer(Bucket))
	}
	if err := m.CheckWrites(Bucket, "b"); !errors.Is(err, ErrCheck) {
		t.Fatalf("writes at a location without the bucket: %v", err)
	}
	if err := m.CheckDuel(nil, nil, nil); !errors.Is(err, ErrCheck) {
		t.Fatalf("empty duel: %v", err)
	}
	if err := m.CheckMoved(saga.MigrationInput{Bucket: Bucket, From: "a", To: "b"}); !errors.Is(err, ErrCheck) {
		t.Fatalf("a move that did not happen: %v", err)
	}
	_ = s.Crash("cl")
	m.Write(1)
	if !m.WritesIdle() {
		t.Fatal("a down client is busy")
	}
}

func TestScenariosOnTheModel(t *testing.T) {
	nr := &NemesisReport{}
	np := NemesisParams{Start: env.Time(300 * time.Millisecond), Phases: 1, Chaos: 3 * time.Second, Heal: time.Second, MaxGap: 300 * time.Millisecond,
		Converge: time.Minute, Writes: 10, WriteGap: 300 * time.Millisecond, Tick: 100 * time.Millisecond}
	for seed := int64(1); seed <= 3; seed++ {
		if _, err := simtest.CheckDeterministic(seed, NemesisRun(np, nr)); err != nil {
			t.Fatalf("nemesis seed %d: %v", seed, err)
		}
		if nr.Sagas == 0 || nr.Final == "" {
			t.Fatalf("nemesis report %+v", nr)
		}
	}
	dr := &DuelReport{}
	if _, err := simtest.CheckDeterministic(1, DuelRun(DuelParams{Converge: time.Minute, MinPartition: time.Second}, dr)); err != nil {
		t.Fatal(err)
	}
	if dr.Pointer != "b" && dr.Pointer != "c" {
		t.Fatalf("duel report %+v", dr)
	}
	if len(NemesisKinds()) == 0 || slices.Contains(NemesisKinds(), sim.FaultBitRot) {
		t.Fatal("nemesis kinds")
	}
}
