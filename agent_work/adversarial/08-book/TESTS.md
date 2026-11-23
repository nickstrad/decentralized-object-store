# Named test and benchmark audit


106 complete names resolve. `TestConformance` is a deliberate -run prefix matching TestConformanceOnTheSimulator and TestConformanceOnLoopback, not a missing test. Definitions were inspected with their assertion sites; scenario entrypoints delegate to the run/helper named in the body. Full extracted definitions are in named-test-bodies.txt. Historical execution totals come from build notes/reviews, not from these declarations.


## BenchmarkChaos100

Mentioned in: book/chapters/01-deterministic-simulation.md

internal/sim/meta_test.go:251

```go

func BenchmarkChaos100(b *testing.B) {
	run := chaos(100)
	var steps uint64
	for i := 0; b.Loop(); i++ {
		s := run(int64(i + 1))
		steps += s.Steps()
	}
	b.ReportMetric(float64(steps)/b.Elapsed().Seconds(), "events/s")
}


```

## FuzzSimSchedule

Mentioned in: book/chapters/01-deterministic-simulation.md, book/front/topic-map.md

internal/sim/fuzz_test.go:34

```go

func FuzzSimSchedule(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add([]byte{1, 1, 0, 0})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0})
	f.Add([]byte{255, 255, 255, 255, 255, 255, 255, 255, 255})
	for k := 0; k < 20; k++ {
		f.Add([]byte{3, byte(k), byte(k * 7), byte(k * 13), 9, byte(k + 1), 2, 4})
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 {
			data = data[:4096]
		}
		simtest.RequireDeterministic(t, 1, scheduleRun(data))
	})
}

// TestScheduleFromBytes proves that decoding bytes into TimedFaults is
// deterministic, produces non-decreasing timestamps from start, and handles empty input.

```

## TestCanaryDroppedInvariant

Mentioned in: book/chapters/01-deterministic-simulation.md

internal/sim/meta_test.go:240

```go

func TestCanaryDroppedInvariant(t *testing.T) {
	clean := simtoy.ChaosRun(simtoy.ChaosOptions{Nodes: 20})
	if _, err := simtest.CheckDeterministic(1, clean); err != nil {
		t.Fatalf("clean run failed: %v", err)
	}
	run := simtoy.ChaosRun(simtoy.ChaosOptions{Nodes: 20, Configure: sim.PlantDroppedInvariants})
	requireCanaryCaught(t, run, sim.ErrInvariantNotRun)
}

// BenchmarkChaos100 measures simulator throughput in events/s on a 100-node
// full chaos run including invariants and digest computation.

```

## TestCanaryMapOrder

Mentioned in: book/chapters/01-deterministic-simulation.md

internal/sim/meta_test.go:227

```go

func TestCanaryMapOrder(t *testing.T) {
	run := simtoy.ChaosRun(simtoy.ChaosOptions{Nodes: 20, Wrap: func(e env.Env, p env.Process) env.Process {
		peers := map[env.NodeID]bool{}
		for _, id := range sim.NodeIDs(20) {
			peers[id] = true
		}
		return &mapOrder{Process: p, e: e, peers: peers}
	}})
	requireCanaryCaught(t, run, simtest.ErrNondeterministic)
}

// TestCanaryDroppedInvariant proves that Verify catches silent invariant
// skipping by planting PlantDroppedInvariants so scheduled invariants never run.

```

## TestCanaryUnseededDraw

Mentioned in: book/chapters/01-deterministic-simulation.md

internal/sim/meta_test.go:217

```go

func TestCanaryUnseededDraw(t *testing.T) {
	run := simtoy.ChaosRun(simtoy.ChaosOptions{Nodes: 20, Wrap: func(e env.Env, p env.Process) env.Process {
		return &unseeded{Process: p, e: e}
	}})
	requireCanaryCaught(t, run, simtest.ErrNondeterministic)
}

// TestCanaryMapOrder proves that CheckDeterministic catches map-order
// dependence by planting a wrapper that forwards datagrams to the first key
// yielded by a Go map iteration.

```

## TestDifferentSeedsDiffer

Mentioned in: book/chapters/01-deterministic-simulation.md

internal/sim/meta_test.go:155

```go

func TestDifferentSeedsDiffer(t *testing.T) {
	for _, n := range metaSizes {
		t.Run(fmt.Sprintf("nodes=%d", n), func(t *testing.T) {
			a, b := chaos(n)(1).Digests(), chaos(n)(2).Digests()
			if a.Events == b.Events || a.State == b.State {
				t.Fatalf("seeds 1 and 2 share a digest: %s vs %s", a, b)
			}
		})
	}
}

type unseeded struct {
	env.Process
	e env.Env
}


```

## TestGoldenDigests

Mentioned in: book/chapters/01-deterministic-simulation.md

internal/sim/meta_test.go:99

```go

func TestGoldenDigests(t *testing.T) {
	path := filepath.Join("testdata", "golden.txt")
	type row struct {
		n    int
		seed int64
		want string
	}
	var rows []row
	if *updateGolden {
		var b strings.Builder
		b.WriteString("# nodes seed digests\n")
		for _, n := range metaSizes {
			for _, seed := range []int64{1, 2, 3} {
				s := chaos(n)(seed)
				mustNil(t, s.Verify())
				fmt.Fprintf(&b, "%d %d %s\n", n, seed, s.Digests())
			}
		}
		mustNil(t, os.MkdirAll("testdata", 0o755))
		mustNil(t, os.WriteFile(path, []byte(b.String()), 0o644))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden file (regenerate with -update-golden): %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.SplitN(line, " ", 3)
		if len(f) != 3 {
			t.Fatalf("bad golden line %q", line)
		}
		n, err1 := strconv.Atoi(f[0])
		seed, err2 := strconv.ParseInt(f[1], 10, 64)
		if err := errors.Join(err1, err2); err != nil {
			t.Fatalf("bad golden line %q: %v", line, err)
		}
		rows = append(rows, row{n, seed, f[2]})
	}
	if len(rows) != 9 {
		t.Fatalf("golden file has %d rows, want 9", len(rows))
	}
	for _, r := range rows {
		t.Run(fmt.Sprintf("nodes=%d/seed=%d", r.n, r.seed), func(t *testing.T) {
			s := chaos(r.n)(r.seed)
			mustNil(t, s.Verify())
			if got := s.Digests().String(); got != r.want {
				t.Fatalf("digests changed:\n got  %s\n want %s", got, r.want)
			}
		})
	}
}

// TestDifferentSeedsDiffer proves that distinct seeds drive distinct event logs
// and final cluster states.

```

## TestPropDiskModel

Mentioned in: book/chapters/01-deterministic-simulation.md

internal/sim/prop_test.go:481

```go

func TestPropDiskModel(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := &diskMachine{model: map[string]*diskModelBlob{}, names: []string{"a", "b", "c/d"}}
		m.s = sim.New(sim.Config{Seed: rapid.Int64().Draw(t, "seed")})
		if err := m.s.AddNode(sim.NodeSpec{ID: "a"}, func(e env.Env) env.Process {
			m.cur = e
			return &recorder{e: e}
		}); err != nil {
			t.Fatal(err)
		}
		if err := m.s.RunUntil(0); err != nil {
			t.Fatal(err)
		}
		name := rapid.SampledFrom(m.names)
		data := rapid.SliceOfN(rapid.Byte(), 0, 12)
		t.Repeat(map[string]func(*rapid.T){
			"write": func(t *rapid.T) {
				n, d := name.Draw(t, "name"), data.Draw(t, "data")
				if err := m.cur.Disk.Write(n, d); err != nil {
					t.Fatal(err)
				}
				b := m.blob(n)
				b.written, b.wExists, b.dirty, b.appendOnly = append([]byte{}, d...), true, true, false
				b.wDamaged = false
			},
			"append": func(t *rapid.T) {
				n, d := name.Draw(t, "name"), data.Draw(t, "data")
				if err := m.cur.Disk.Append(n, d); err != nil {
					t.Fatal(err)
				}
				b := m.blob(n)
				if !b.dirty {
					b.appendOnly = true
				}
				if !b.wExists {
					b.wDamaged = false
				}
				b.written = append(append([]byte{}, b.written...), d...)
				b.wExists, b.dirty = true, true
			},
			"sync": func(t *rapid.T) {
				n := name.Draw(t, "name")
				err := m.cur.Disk.Sync(n)
				b := m.model[n]
				if b == nil || !b.wExists && !b.dExists {
					if !errors.Is(err, env.ErrNotFound) {
						t.Fatalf("Sync of missing = %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				b.durable, b.dExists, b.dirty, b.appendOnly = append([]byte{}, b.written...), b.wExists, false, false
				b.dDamaged = b.wExists && b.wDamaged
			},
			"delete": func(t *rapid.T) {
				n := name.Draw(t, "name")
				err := m.cur.Disk.Delete(n)
				b := m.model[n]
				if b == nil || !b.wExists {
					if !errors.Is(err, env.ErrNotFound) {
						t.Fatalf("Delete of missing = %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				b.written, b.wExists, b.dirty, b.appendOnly = nil, false, true, false
				b.wDamaged = false
			},
			"crash": func(t *rapid.T) {
				if err := m.s.Crash("a"); err != nil {
					t.Fatal(err)
				}
				for _, b := range m.model {
					b.written, b.wExists, b.dirty, b.appendOnly = append([]byte{}, b.durable...), b.dExists, false, false
					b.wDamaged = b.dDamaged && b.dExists
				}
				m.restart(t)
			},
			"crashTorn": func(t *rapid.T) {
				if err := m.s.CrashTorn("a"); err != nil {
					t.Fatal(err)
				}
				for _, n := range m.names {
					b := m.model[n]
					if b == nil {
						continue
					}
					if b.dirty && b.appendOnly && b.wExists {
						got, ok := m.s.Disk("a").Peek(n)
						switch {
						case !ok && !b.dExists:
						case !ok || len(got) < len(b.durable) || !bytes.Equal(got[:len(b.durable)], b.durable) ||
							!bytes.HasPrefix(b.written[len(b.durable):], got[len(b.durable):]):
							t.Fatalf("torn %s = %q %v, written %q durable %q", n, got, ok, b.written, b.durable)
						default:
							b.durable, b.dExists = append([]byte{}, got...), true
						}
					}
					b.written, b.wExists, b.dirty, b.appendOnly = append([]byte{}, b.durable...), b.dExists, false, false
					b.wDamaged = b.dDamaged && b.dExists
				}
				m.restart(t)
			},
			"bitrot": func(t *rapid.T) {
				n := name.Draw(t, "name")
				b := m.model[n]
				err := m.s.BitRot("a", n)
				if b == nil || !b.dExists || len(b.durable) == 0 {
					if err == nil {
						t.Fatalf("BitRot of %s with no durable bytes succeeded", n)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				got, _ := m.s.Disk("a").PeekDurable(n)
				flips := 0
				for i := range got {
					flips += bits.OnesCount8(got[i] ^ b.durable[i])
				}
				if flips != 1 {
					t.Fatalf("bit-rot flipped %d bits", flips)
				}
				if !b.dirty {
					b.written = append([]byte{}, got...)
					b.wDamaged = true
				}
				b.durable = append([]byte{}, got...)
				b.dDamaged = true
			},
			"wipe": func(t *rapid.T) {
				if err := m.s.WipeDisk("a"); err != nil {
					t.Fatal(err)
				}
				for _, b := range m.model {
					if b.wExists || b.dExists {
						b.dDamaged = true
					}
					b.wDamaged = false
					b.written, b.wExists, b.durable, b.dExists, b.dirty, b.appendOnly = nil, false, nil, false, false, false
				}
			},
			"failRead": func(t *rapid.T) {
				n := name.Draw(t, "name")
				if err := m.s.FailReads("a", n, 1); err != nil {
					t.Fatal(err)
				}
				if _, err := m.cur.Disk.Read(n); !errors.Is(err, env.ErrIO) {
					t.Fatalf("Read after FailReads = %v", err)
				}
			},
			"": func(t *rapid.T) { m.check(t) },
		})
	})
}

type streamMachine struct {
	s    *sim.Sim
	ids  []env.NodeID
	recs map[env.NodeID]*recorder
	sent map[[2]env.NodeID]int
}


```

## TestPropHealedNetworkDelivers

Mentioned in: book/chapters/01-deterministic-simulation.md

internal/sim/prop_test.go:172

```go

func TestPropHealedNetworkDelivers(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(2, 6).Draw(t, "n")
		nc := sim.DefaultNetConfig()
		s := sim.New(sim.Config{Seed: rapid.Int64().Draw(t, "seed"), Net: nc})
		ids := sim.NodeIDs(n)
		recs := map[env.NodeID]*recorder{}
		for i, id := range ids {
			if err := s.AddNode(sim.NodeSpec{ID: id, Region: env.Region(fmt.Sprintf("r%d", i%2))}, func(e env.Env) env.Process {
				r := &recorder{e: e}
				recs[e.ID] = r
				return r
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.RunUntil(0); err != nil {
			t.Fatal(err)
		}
		pick := rapid.SampledFrom(ids)
		faults := rapid.IntRange(0, 8).Draw(t, "faults")
		for i := 0; i < faults; i++ {
			a, b := pick.Draw(t, "a"), pick.Draw(t, "b")
			var err error
			switch rapid.IntRange(0, 6).Draw(t, "kind") {
			case 0:
				mustNil(t, s.Partition([]env.NodeID{a}, []env.NodeID{b}))
			case 1:
				mustNil(t, s.PartitionOneWay([]env.NodeID{a}, []env.NodeID{b}))
			case 2:
				err = s.Isolate(a)
			case 3:
				err = s.PartitionRegion(s.Region(a))
			case 4:
				err = s.SetLinkLoss(a, b, rapid.Uint32Range(0, 1_000_000).Draw(t, "ppm"))
			case 5:
				err = s.Clog(a, b)
			case 6:
				_ = recs[a].e.Net.Send(b, []byte("pre"))
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := s.RunFor(time.Second); err != nil {
			t.Fatal(err)
		}
		s.HealNet()
		msgs := rapid.IntRange(1, 30).Draw(t, "msgs")
		wantDatagrams := map[env.NodeID]int{}
		wantReplies := map[env.NodeID]int{}
		for i := 0; i < msgs; i++ {
			a, b := pick.Draw(t, "from"), pick.Draw(t, "to")
			if rapid.Bool().Draw(t, "request") {
				recs[a].request(b, fmt.Sprintf("post%d", i), time.Minute)
				wantReplies[a]++
				continue
			}
			if err := recs[a].e.Net.Send(b, []byte(fmt.Sprintf("post%d", i))); err != nil {
				t.Fatal(err)
			}
			wantDatagrams[b]++
		}
		if err := s.RunFor(10 * time.Second); err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			got := 0
			for _, d := range recs[id].datagrams {
				if strings.HasPrefix(d.msg, "post") {
					got++
				}
			}
			if got != wantDatagrams[id] {
				t.Fatalf("%s received %d post-heal datagrams, want %d", id, got, wantDatagrams[id])
			}
			ok := 0
			for _, r := range recs[id].replies {
				if r.err != nil {
					t.Fatalf("%s post-heal request failed: %v", id, r.err)
				}
				if strings.HasPrefix(r.reply, "echo:post") {
					ok++
				}
			}
			if ok != wantReplies[id] {
				t.Fatalf("%s got %d replies, want %d", id, ok, wantReplies[id])
			}
		}
	})
}

type withUngroupedBlobs struct {
	env.Process
	e      env.Env
	shards time.Duration
}


```

## TestPropNemesisRespectsBudget

Mentioned in: book/chapters/01-deterministic-simulation.md, book/front/topic-map.md

internal/sim/prop_test.go:347

```go

func TestPropNemesisRespectsBudget(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(3, 8).Draw(t, "n")
		s := sim.New(sim.Config{Seed: rapid.Int64().Draw(t, "seed")})
		ids := sim.NodeIDs(n)
		toy := simtoy.Factory(simtoy.Config{Peers: ids})
		var shards time.Duration
		if rapid.Bool().Draw(t, "newGroups") {
			shards = time.Duration(rapid.IntRange(50, 400).Draw(t, "shardEveryMs")) * time.Millisecond
		}
		if err := s.AddCluster(ids, 3, func(e env.Env) env.Process {
			return &withUngroupedBlobs{Process: toy(e), e: e, shards: shards}
		}); err != nil {
			t.Fatal(err)
		}
		b := sim.Budget{
			MinLive:            rapid.IntRange(0, n).Draw(t, "minLive"),
			MaxDamagedDisks:    rapid.IntRange(0, n).Draw(t, "maxDisks"),
			MaxDamagedPerGroup: rapid.IntRange(0, 3).Draw(t, "perGroup"),
		}
		if rapid.Bool().Draw(t, "groups") {
			b.GroupOf = shardGroups
		}
		kinds := sim.ChaosKinds()
		if rapid.Bool().Draw(t, "diskHeavy") {
			kinds = []sim.FaultKind{sim.FaultBitRot, sim.FaultReadError, sim.FaultWipeDisk, sim.FaultWipeRejoin, sim.FaultCrash, sim.FaultRestart}
		}
		cfg := sim.NemesisConfig{
			Start: 0, Phases: rapid.IntRange(1, 3).Draw(t, "phases"),
			Chaos: 2 * time.Second, Heal: time.Second, MaxGap: 50 * time.Millisecond, Budget: b, Kinds: kinds,
		}
		nem, err := s.StartNemesis(cfg)
		if err != nil {
			t.Fatal(err)
		}
		s.AddInvariant("budget", 1, func() error { return b.Check(s) })
		s.AddInvariant("budget/independent", 1, func() error { return budgetHolds(s, b) })
		if err := s.RunUntil(nem.QuietAt()); err != nil {
			t.Fatal(err)
		}
		if err := s.Verify(); err != nil {
			t.Fatal(err)
		}
		applied := 0
		for _, st := range nem.History() {
			if st.Outcome == sim.OutcomeApplied {
				applied++
			}
		}
		if applied == 0 && b.MaxDamagedDisks > 0 && b.MinLive < n {
			t.Fatalf("nemesis applied nothing: %+v", nem.History())
		}
	})
}

type diskModelBlob struct {
	written, durable  []byte
	wExists, dExists  bool
	dirty, appendOnly bool
	dDamaged          bool
	wDamaged          bool
}

type diskMachine struct {
	s     *sim.Sim
	cur   env.Env
	model map[string]*diskModelBlob
	names []string
}


```

## TestPropNemesisTimingIgnoresInventory

Mentioned in: book/chapters/01-deterministic-simulation.md

internal/sim/prop_test.go:834

```go

func TestPropNemesisTimingIgnoresInventory(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.Int64().Draw(t, "seed")
		n := rapid.IntRange(2, 6).Draw(t, "n")
		budget := sim.Budget{Lifted: true}
		if rapid.Bool().Draw(t, "budget") {
			budget = sim.Budget{
				MinLive:            rapid.IntRange(0, n).Draw(t, "minLive"),
				MaxDamagedDisks:    rapid.IntRange(0, n).Draw(t, "maxDisks"),
				MaxDamagedPerGroup: rapid.IntRange(0, 2).Draw(t, "perGroup"),
			}
			if rapid.Bool().Draw(t, "groups") {
				budget.GroupOf = scribbleGroups
			}
		}
		plan := func(label string) []scribblePlan {
			out := make([]scribblePlan, n)
			for i := range out {
				out[i].blobs = rapid.IntRange(0, 3).Draw(t, fmt.Sprintf("%s/blobs/%d", label, i))
				if rapid.Bool().Draw(t, fmt.Sprintf("%s/ticks/%d", label, i)) {
					out[i].every = time.Duration(rapid.IntRange(20, 300).Draw(t, fmt.Sprintf("%s/everyMs/%d", label, i))) * time.Millisecond
				}
			}
			return out
		}
		run := func(p []scribblePlan) []sim.NemesisStep {
			s := sim.New(sim.Config{Seed: seed})
			ids := sim.NodeIDs(n)
			for i, id := range ids {
				w := p[i]
				if err := s.AddNode(sim.NodeSpec{ID: id, Region: env.Region(fmt.Sprintf("r%d", i%2))}, func(e env.Env) env.Process {
					return &scribbler{e: e, plan: w}
				}); err != nil {
					t.Fatal(err)
				}
			}
			nem, err := s.StartNemesis(sim.NemesisConfig{
				Start: 0, Phases: 2, Chaos: 2 * time.Second, Heal: time.Second, MaxGap: 50 * time.Millisecond, Budget: budget,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RunUntil(nem.QuietAt()); err != nil {
				t.Fatal(err)
			}
			return nem.History()
		}
		a, b := run(plan("a")), run(plan("b"))
		if len(a) != len(b) {
			t.Fatalf("nemesis lengths %d vs %d", len(a), len(b))
		}
		for i := range a {
			fa, fb := a[i].Fault, b[i].Fault
			fa.Blob, fb.Blob = "", ""
			if a[i].At != b[i].At || !bytes.Equal(fa.Encode(), fb.Encode()) {
				t.Fatalf("step %d: %v vs %v", i, a[i], b[i])
			}
		}
	})
}


```

## TestPropNetworkDrawsDoNotShiftTheNemesis

Mentioned in: book/chapters/01-deterministic-simulation.md

internal/sim/prop_test.go:55

```go

func TestPropNetworkDrawsDoNotShiftTheNemesis(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.Int64().Draw(t, "seed")
		drop := rapid.Uint32Range(1, 900_000).Draw(t, "drop")
		run := func(dropPPM uint32) []sim.NemesisStep {
			nc := sim.DefaultNetConfig()
			nc.DropPPM = dropPPM
			s := sim.New(sim.Config{Seed: seed, Net: nc})
			ids := sim.NodeIDs(5)
			if err := s.AddCluster(ids, 2, simtoy.Factory(simtoy.Config{Peers: ids})); err != nil {
				t.Fatal(err)
			}
			n, err := s.StartNemesis(sim.NemesisConfig{
				Start: 0, Phases: 2, Chaos: 2 * time.Second, Heal: time.Second, MaxGap: 100 * time.Millisecond,
				Kinds:  []sim.FaultKind{sim.FaultCrash, sim.FaultRestart, sim.FaultPause, sim.FaultResume, sim.FaultPartition, sim.FaultPartitionOneWay, sim.FaultIsolate, sim.FaultLinkLoss, sim.FaultClog, sim.FaultUnclog},
				Budget: sim.Budget{MinLive: 2},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RunUntil(n.QuietAt()); err != nil {
				t.Fatal(err)
			}
			return n.History()
		}
		a, b := run(0), run(drop)
		if len(a) != len(b) {
			t.Fatalf("nemesis lengths %d vs %d", len(a), len(b))
		}
		for i := range a {
			if a[i].At != b[i].At || a[i].Fault.String() != b[i].Fault.String() || a[i].Outcome != b[i].Outcome {
				t.Fatalf("step %d: %v vs %v", i, a[i], b[i])
			}
		}
	})
}

type ticker struct {
	e     env.Env
	peers []env.NodeID
	left  int
	last  env.Time
	bad   error
}


```

## TestPropStreamOrderPerConnection

Mentioned in: book/chapters/01-deterministic-simulation.md

internal/sim/prop_test.go:694

```go

func TestPropStreamOrderPerConnection(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(2, 4).Draw(t, "n")
		m := &streamMachine{s: sim.New(sim.Config{Seed: rapid.Int64().Draw(t, "seed")}), ids: sim.NodeIDs(n),
			recs: map[env.NodeID]*recorder{}, sent: map[[2]env.NodeID]int{}}
		for _, id := range m.ids {
			if err := m.s.AddNode(sim.NodeSpec{ID: id, Region: "r"}, func(e env.Env) env.Process {
				r := &recorder{e: e}
				m.recs[e.ID] = r
				return r
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := m.s.RunUntil(0); err != nil {
			t.Fatal(err)
		}
		pick := rapid.SampledFrom(m.ids)
		pair := func(t *rapid.T) (env.NodeID, env.NodeID) {
			a := pick.Draw(t, "a")
			b := rapid.SampledFrom(m.ids).Filter(func(x env.NodeID) bool { return x != a }).Draw(t, "b")
			return a, b
		}
		t.Repeat(map[string]func(*rapid.T){
			"request": func(t *rapid.T) {
				a, b := pair(t)
				k := m.sent[[2]env.NodeID{a, b}]
				m.sent[[2]env.NodeID{a, b}] = k + 1
				m.recs[a].request(b, streamLabel(a, b, k), time.Hour)
			},
			"pause": func(t *rapid.T) {
				id := pick.Draw(t, "id")
				if m.s.Status(id).Paused {
					return
				}
				if err := m.s.Pause(id); err != nil {
					t.Fatal(err)
				}
			},
			"resume": func(t *rapid.T) {
				id := pick.Draw(t, "id")
				if !m.s.Status(id).Paused {
					return
				}
				if err := m.s.Resume(id); err != nil {
					t.Fatal(err)
				}
			},
			"clog": func(t *rapid.T) {
				a, b := pair(t)
				if err := m.s.Clog(a, b); err != nil {
					t.Fatal(err)
				}
			},
			"unclog": func(t *rapid.T) {
				a, b := pair(t)
				if err := m.s.Unclog(a, b); err != nil {
					t.Fatal(err)
				}
			},
			"healAll": func(t *rapid.T) { m.s.HealAll() },
			"healNet": func(t *rapid.T) { m.s.HealNet() },
			"run": func(t *rapid.T) {
				d := time.Duration(rapid.IntRange(0, 6).Draw(t, "ms")) * time.Millisecond
				if err := m.s.RunFor(d); err != nil {
					t.Fatal(err)
				}
				m.checkOrder(t)
			},
		})
		m.s.HealAll()
		if err := m.s.RunFor(time.Minute); err != nil {
			t.Fatal(err)
		}
		m.checkOrder(t)
		for _, a := range m.ids {
			for _, b := range m.ids {
				got := 0
				for _, req := range m.recs[b].requests {
					if from, _, _ := parseStreamLabel(t, req); from == a {
						got++
					}
				}
				if want := m.sent[[2]env.NodeID{a, b}]; got != want {
					t.Fatalf("%s received %d requests from %s, sent %d", b, got, a, want)
				}
			}
			total := 0
			for _, b := range m.ids {
				total += m.sent[[2]env.NodeID{a, b}]
			}
			if len(m.recs[a].replies) != total {
				t.Fatalf("%s got %d replies for %d requests", a, len(m.recs[a].replies), total)
			}
		}
	})
}

type scribblePlan struct {
	blobs int
	every time.Duration
}

type scribbler struct {
	e    env.Env
	plan scribblePlan
}


```

## TestPropStreamsIndependent

Mentioned in: book/chapters/01-deterministic-simulation.md

internal/sim/prop_test.go:23

```go

func TestPropStreamsIndependent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.Int64().Draw(t, "seed")
		labels := []string{sim.StreamNet, sim.StreamDisk, sim.StreamNemesis, sim.StreamWorkload, sim.NodeStream("n000"), sim.NodeStream("n001")}
		a := rapid.IntRange(0, len(labels)-1).Draw(t, "a")
		b := rapid.IntRange(0, len(labels)-1).Filter(func(i int) bool { return i != a }).Draw(t, "b")
		extra := rapid.IntRange(0, 1000).Draw(t, "extra")

		ra, rb := sim.NewStream(seed, labels[a]), sim.NewStream(seed, labels[b])
		for i := 0; i < extra; i++ {
			ra.Uint64()
		}
		fresh := sim.NewStream(seed, labels[b])
		for i := 0; i < 16; i++ {
			if rb.Uint64() != fresh.Uint64() {
				t.Fatalf("draws on %q changed %q", labels[a], labels[b])
			}
		}
		x, y := sim.NewStream(seed, labels[a]), sim.NewStream(seed, labels[b])
		same := true
		for i := 0; i < 4; i++ {
			same = same && x.Uint64() == y.Uint64()
		}
		if same {
			t.Fatalf("streams %q and %q start identically", labels[a], labels[b])
		}
	})
}

// TestPropNetworkDrawsDoNotShiftTheNemesis proves that changing network drop
// rates (and thus net-stream draws and protocol traffic) leaves a non-disk
// nemesis schedule and its outcomes unchanged.

```

## TestPropTimeMonotonic

Mentioned in: book/chapters/01-deterministic-simulation.md

internal/sim/prop_test.go:125

```go

func TestPropTimeMonotonic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(t, "n")
		nc := sim.DefaultNetConfig()
		lo := rapid.Int64Range(0, 10_000_000).Draw(t, "lo")
		hi := rapid.Int64Range(lo, 50_000_000).Draw(t, "hi")
		nc.Latency = sim.Range{Min: time.Duration(lo), Max: time.Duration(hi)}
		nc.DuplicatePPM = rapid.Uint32Range(0, 1_000_000).Draw(t, "dup")
		s := sim.New(sim.Config{Seed: rapid.Int64().Draw(t, "seed"), Net: nc})
		ids := sim.NodeIDs(n)
		var procs []*ticker
		for _, id := range ids {
			sp := sim.NodeSpec{
				ID:       id,
				Skew:     time.Duration(rapid.Int64Range(-int64(time.Second), int64(time.Second)).Draw(t, "skew")),
				DriftPPM: rapid.Int64Range(-sim.MaxDriftPPM, sim.MaxDriftPPM).Draw(t, "drift"),
			}
			left := rapid.IntRange(0, 40).Draw(t, "ticks")
			if err := s.AddNode(sp, func(e env.Env) env.Process {
				k := &ticker{e: e, peers: ids, left: left, last: -1 << 62}
				procs = append(procs, k)
				return k
			}); err != nil {
				t.Fatal(err)
			}
		}
		var last env.Time
		s.AddInvariant("global time monotonic", 1, func() error {
			if s.Now() < last {
				return fmt.Errorf("global time went from %d to %d", last, s.Now())
			}
			last = s.Now()
			return nil
		})
		if err := s.RunUntilQuiet(); err != nil {
			t.Fatal(err)
		}
		for _, k := range procs {
			if k.bad != nil {
				t.Fatal(k.bad)
			}
		}
	})
}

// TestPropHealedNetworkDelivers proves that after arbitrary topology faults
// and HealNet, every datagram and request sent afterward is delivered.

```

## TestReplayAcrossProcesses

Mentioned in: book/chapters/01-deterministic-simulation.md, book/front/topic-map.md

internal/sim/meta_test.go:50

```go

func TestReplayAcrossProcesses(t *testing.T) {
	if spec := os.Getenv(childEnv); spec != "" {
		var n int
		var seed int64
		if _, err := fmt.Sscanf(spec, "%d:%d", &n, &seed); err != nil {
			t.Fatalf("bad %s=%q: %v", childEnv, spec, err)
		}
		s := chaos(n)(seed)
		if err := s.Verify(); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("DIGESTS %s\n", s.Digests())
		return
	}
	if testing.Short() {
		t.Skip("re-executes the test binary")
	}
	seed := simtest.Seeds(t)[0]
	variants := [][]string{{"GOMAXPROCS=1", "GOGC=1"}, {"GOMAXPROCS=4", "GOGC=off"}}
	for _, n := range metaSizes {
		t.Run(fmt.Sprintf("nodes=%d", n), func(t *testing.T) {
			s := chaos(n)(seed)
			mustNil(t, s.Verify())
			want := s.Digests().String()
			for _, v := range variants {
				cmd := exec.Command(os.Args[0], "-test.run=^TestReplayAcrossProcesses$", "-test.count=1")
				cmd.Env = append(append(os.Environ(), fmt.Sprintf("%s=%d:%d", childEnv, n, seed)), v...)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("child %v: %v\n%s", v, err, out)
				}
				got := ""
				sc := bufio.NewScanner(strings.NewReader(string(out)))
				for sc.Scan() {
					if rest, ok := strings.CutPrefix(sc.Text(), "DIGESTS "); ok {
						got = rest
					}
				}
				if got != want {
					t.Fatalf("seed %d nodes %d: child %v digests %q, parent %q\n%s",
						seed, n, v, got, want, simtest.ReplayCommand(t, seed))
				}
			}
		})
	}
}

// TestGoldenDigests proves that pinned seeds 1..3 at 20, 50, and 100 nodes
// match testdata/golden.txt so accidental schedule shifts are caught.

```

## TestReplaySameProcess

Mentioned in: book/chapters/01-deterministic-simulation.md, book/front/topic-map.md

internal/sim/meta_test.go:33

```go

func TestReplaySameProcess(t *testing.T) {
	for _, n := range metaSizes {
		t.Run(fmt.Sprintf("nodes=%d", n), func(t *testing.T) {
			simtest.Sweep(t, func(t *testing.T, seed int64) {
				s := simtest.RequireDeterministic(t, seed, chaos(n))
				if !simtoy.Converged(s) {
					t.Fatalf("not converged at %d", s.Now())
				}
			})
		})
	}
}

const childEnv = "SIM_REPLAY_CHILD"

// TestReplayAcrossProcesses proves byte-identical event and state digests across
// separate OS processes with differing GOMAXPROCS and GOGC settings.

```

## FuzzGossipWire

Mentioned in: book/chapters/02-swim-gossip.md, book/front/topic-map.md

internal/gossip/fuzz_test.go:17

```go

func FuzzGossipWire(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{gossip.Version})
	f.Add([]byte{gossip.Version, byte(gossip.KindPing), 0, 0, 0, 1, 0, 0})
	f.Add([]byte{gossip.Version, byte(gossip.KindSyncResp), 0, 0, 0, 0, 0xff, 0xff})
	for _, c := range wireCases() {
		b, err := c.msg.Encode()
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	self, _ := gossip.Message{Kind: gossip.KindGossip, Updates: []gossip.Member{mem("self", 1<<32-1, gossip.StateDead)}}.Encode()
	f.Add(self)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzMaxInput {
			return
		}
		m, err := gossip.Decode(data)
		if err != nil {
			if !errors.Is(err, gossip.ErrMalformed) {
				t.Fatalf("Decode error %v is not ErrMalformed", err)
			}
		} else {
			again, err := m.Encode()
			if err != nil {
				t.Fatalf("decoded message does not re-encode: %v", err)
			}
			if !bytes.Equal(again, data) {
				t.Fatalf("decode then encode is not the identity:\n in  %x\n out %x", data, again)
			}
		}

		h := newHarness(t, "self", gossip.Config{})
		announce(h, "p01", "p02", "p03", "p04")
		h.node.Start()
		before := h.node.Self().Incarnation
		h.node.OnDatagram("p01", data)
		replied := 0
		h.node.OnRequest("p02", data, func(resp []byte, err error) {
			replied++
			if err == nil {
				if r, derr := gossip.Decode(resp); derr != nil || r.Kind != gossip.KindSyncResp {
					t.Fatalf("sync reply %x does not decode as a sync response: %v", resp, derr)
				}
			} else if !errors.Is(err, gossip.ErrMalformed) {
				t.Fatalf("request refused with %v", err)
			}
		})
		if replied != 1 {
			t.Fatalf("request answered %d times", replied)
		}
		h.clock.advance(3 * gossip.DefaultProbeInterval)
		self := h.node.Self()
		if self.State != gossip.StateAlive || self.Incarnation < before {
			t.Fatalf("invariant broken: self %+v, incarnation before %d", self, before)
		}
		all := h.node.All()
		if !slices.IsSortedFunc(all, func(a, b gossip.Member) int { return compareID(a.ID, b.ID) }) {
			t.Fatalf("view not sorted: %+v", all)
		}
		for i := 1; i < len(all); i++ {
			if all[i].ID == all[i-1].ID {
				t.Fatalf("duplicate member %s", all[i].ID)
			}
		}
		for _, s := range h.net.sent {
			if len(s.msg) > gossip.DefaultMaxPacket {
				t.Fatalf("sent %d bytes", len(s.msg))
			}
			if _, err := gossip.Decode(s.msg); err != nil {
				t.Fatalf("node sent an undecodable packet: %v", err)
			}
		}
	})
}


```

## FuzzViewMergeLaws

Mentioned in: book/chapters/02-swim-gossip.md

internal/gossip/prop_test.go:139

```go

func FuzzViewMergeLaws(f *testing.F) {
	for _, b := range viewMergeSeeds(f) {
		f.Add(b)
	}
	f.Fuzz(rapid.MakeFuzz(propViewMergeCommutative))
}

// TestFuzzSeedsRunWithoutSkipping proves that every corpus seed of
// FuzzViewMergeLaws executes to completion under plain go test without skipping.

```

## TestCanaryNoIndirectProbe

Mentioned in: book/chapters/02-swim-gossip.md

internal/gossip/dst_test.go:515

```go

func TestCanaryNoIndirectProbe(t *testing.T) {
	t.Parallel()
	var sus, died tally
	t.Cleanup(func() {
		t.Logf("MEASURE canary suspicions %s", sus.summary())
		t.Logf("MEASURE canary evictions %s", died.summary())
	})
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		var out asymOut
		simtest.RequireDeterministic(t, seed, asymRunFn(gossip.WithoutIndirectProbes(gossip.Config{}), &out))
		sus.add(int64(out.suspected))
		died.add(int64(out.died))
		if out.suspected == 0 && out.died == 0 {
			t.Fatal("with indirect probes off the asymmetric-link check still passed: the test cannot see the bug")
		}
	})
}

type crashOut struct {
	detect    time.Duration
	falseDead int
}


```

## TestDeadEntriesStayPurgedDST

Mentioned in: book/chapters/02-swim-gossip.md

internal/gossip/dst_test.go:913

```go

func TestDeadEntriesStayPurgedDST(t *testing.T) {
	t.Parallel()
	for _, leave := range []bool{false, true} {
		name := "after a crash"
		if leave {
			name = "after a leave"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			simtest.Sweep(t, func(t *testing.T, seed int64) {
				simtest.RequireDeterministic(t, seed, lifetimeRun(leave))
			})
		})
	}
}


```

## TestDeadEntryLifetime

Mentioned in: book/chapters/02-swim-gossip.md

internal/gossip/repair_test.go:80

```go

func TestDeadEntryLifetime(t *testing.T) {
	t.Run("two nodes that purge a dead member at different times never resurrect it in each other", func(t *testing.T) {
		cfg := gossip.Config{DeadRetention: 10 * time.Second}
		a, b := newHarness(t, "a", cfg), newHarness(t, "b", cfg)
		a.deliver("b", gossipMsg(mem("b", 0, gossip.StateAlive), mem("c", 0, gossip.StateAlive)))
		b.deliver("a", gossipMsg(mem("a", 0, gossip.StateAlive), mem("c", 0, gossip.StateAlive)))
		a.node.Start()
		b.node.Start()
		a.deliver("b", gossipMsg(mem("c", 0, gossip.StateDead)))
		step := func(d time.Duration) {
			a.clock.advance(d)
			b.clock.advance(d)
			drainRequests(a, b)
			a.net.take()
			b.net.take()
		}
		step(3 * time.Second)
		b.deliver("a", gossipMsg(mem("c", 0, gossip.StateDead)))
		step(8 * time.Second)
		if _, err := a.node.Lookup("c"); !errors.Is(err, gossip.ErrNotMember) {
			t.Fatalf("a did not purge c at 11s: %v", err)
		}
		exchange(t, a, "a", b, "b")
		exchange(t, b, "b", a, "a")
		if _, err := a.node.Lookup("c"); !errors.Is(err, gossip.ErrNotMember) {
			t.Fatalf("a took c back from b's retained entry: %v", err)
		}
		step(3 * time.Second)
		if _, err := b.node.Lookup("c"); !errors.Is(err, gossip.ErrNotMember) {
			t.Fatalf("b did not purge c at 14s: %v", err)
		}
		for range 50 {
			exchange(t, a, "a", b, "b")
			exchange(t, b, "b", a, "a")
			a.deliver("b", gossip.Message{Kind: gossip.KindPing, Seq: 1})
			b.deliver("a", gossip.Message{Kind: gossip.KindPing, Seq: 1})
			step(time.Second)
		}
		for _, h := range []*harness{a, b} {
			if m, err := h.node.Lookup("c"); err == nil {
				t.Fatalf("c came back as %+v", m)
			}
			if h.node.Stats().Purged != 1 {
				t.Fatalf("c purged %d times", h.node.Stats().Purged)
			}
			if m, ok := tombstone(h.node, "c"); !ok || m != mem("c", 0, gossip.StateDead) {
				t.Fatalf("tombstone of c: %+v %v", m, ok)
			}
		}
	})
	t.Run("a stale alive below a purged death brings back the death, never the stale entry", func(t *testing.T) {
		for _, c := range []struct {
			name    string
			history []gossip.Member
			replay  gossip.Member
			want    gossip.Member
		}{
			{"dead at 9, alive at 0 replayed", []gossip.Member{mem("x", 9, gossip.StateDead)}, mem("x", 0, gossip.StateAlive), mem("x", 9, gossip.StateDead)},
			{"alive then dead at 7, alive at 0 replayed", []gossip.Member{mem("x", 7, gossip.StateAlive), mem("x", 7, gossip.StateDead)}, mem("x", 0, gossip.StateAlive), mem("x", 7, gossip.StateDead)},
			{"dead at 7, alive at 7 replayed", []gossip.Member{mem("x", 7, gossip.StateDead)}, mem("x", 7, gossip.StateAlive), mem("x", 7, gossip.StateDead)},
			{"left at 3, suspect at 2 replayed", []gossip.Member{mem("x", 3, gossip.StateLeft)}, mem("x", 2, gossip.StateSuspect), mem("x", 3, gossip.StateLeft)},
		} {
			t.Run(c.name, func(t *testing.T) {
				h := newHarness(t, "self", gossip.Config{DeadRetention: time.Second})
				announce(h, "p01")
				h.node.Start()
				for _, u := range c.history {
					h.deliver("p01", gossipMsg(u))
				}
				h.clock.advance(2 * time.Second)
				if _, err := h.node.Lookup("x"); !errors.Is(err, gossip.ErrNotMember) {
					t.Fatalf("not purged: %v", err)
				}
				h.takeEvents()
				h.net.take()
				h.deliver("p01", gossipMsg(c.replay))
				got, err := h.node.Lookup("x")
				if err != nil || got != c.want {
					t.Fatalf("after replay Lookup = %+v %v, want %+v", got, err, c.want)
				}
				if ev := h.takeEvents(); len(ev) != 0 {
					t.Fatalf("reinstating the verdict emitted %+v", ev)
				}
				if _, ok := tombstone(h.node, "x"); ok {
					t.Fatal("x is both in the view and in the tombstones")
				}
				if h.node.Stats().Reinstated != 1 {
					t.Fatalf("Reinstated = %d", h.node.Stats().Reinstated)
				}
				h.deliver("p01", gossip.Message{Kind: gossip.KindPing, Seq: 5})
				if carries(t, h.net.take(), "x") {
					t.Fatal("a reinstated verdict was queued for gossip")
				}
				h.clock.advance(2 * time.Second)
				if _, err := h.node.Lookup("x"); !errors.Is(err, gossip.ErrNotMember) {
					t.Fatalf("reinstated verdict not purged again: %v", err)
				}
				if m, ok := tombstone(h.node, "x"); !ok || m != c.want {
					t.Fatalf("tombstone after second purge %+v %v", m, ok)
				}
			})
		}
	})
	t.Run("an equal or older verdict about a purged member is dropped", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{DeadRetention: time.Second})
		h.node.Start()
		h.deliver("p01", gossipMsg(mem("x", 7, gossip.StateDead)))
		h.clock.advance(2 * time.Second)
		for _, u := range []gossip.Member{mem("x", 7, gossip.StateDead), mem("x", 3, gossip.StateLeft), mem("x", 6, gossip.StateDead)} {
			h.deliver("p01", gossipMsg(u))
			if m, err := h.node.Lookup("x"); err == nil {
				t.Fatalf("%+v brought back %+v", u, m)
			}
		}
		if h.node.Stats().Reinstated != 0 {
			t.Fatalf("Reinstated = %d", h.node.Stats().Reinstated)
		}
	})
	t.Run("a higher incarnation after the purge is a new life", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{DeadRetention: time.Second})
		h.node.Start()
		h.deliver("p01", gossipMsg(mem("x", 7, gossip.StateDead)))
		h.clock.advance(2 * time.Second)
		h.takeEvents()
		h.deliver("p01", gossipMsg(mem("x", 8, gossip.StateAlive)))
		if m, err := h.node.Lookup("x"); err != nil || m != mem("x", 8, gossip.StateAlive) {
			t.Fatalf("Lookup = %+v %v", m, err)
		}
		if ev := h.takeEvents(); len(ev) != 1 || ev[0].Kind != gossip.EventJoined {
			t.Fatalf("events %+v", ev)
		}
		if _, ok := tombstone(h.node, "x"); ok {
			t.Fatal("tombstone kept after a new life")
		}
	})
	t.Run("a wiped node rejoining at incarnation 0 is told its purged death and refutes above it", func(t *testing.T) {
		seed := newHarness(t, "s", gossip.Config{DeadRetention: time.Second})
		seed.node.Start()
		seed.deliver("p01", gossipMsg(mem("w", 9, gossip.StateDead)))
		seed.clock.advance(2 * time.Second)
		drainRequests(seed)
		w := newHarness(t, "w", gossip.Config{})
		exchange(t, w, "w", seed, "s")
		if self := w.node.Self(); self.Incarnation != 10 || self.State != gossip.StateAlive {
			t.Fatalf("rejoined as %+v", self)
		}
		seed.takeEvents()
		w.deliver("s", gossip.Message{Kind: gossip.KindPing, Seq: 1})
		acks := w.net.take()
		for _, s := range acks {
			seed.node.OnDatagram("w", s.msg)
		}
		if m, err := seed.node.Lookup("w"); err != nil || m.State != gossip.StateAlive || m.Incarnation != 10 {
			t.Fatalf("seed holds %+v %v", m, err)
		}
		if ev := seed.takeEvents(); len(ev) != 1 || ev[0].Kind != gossip.EventJoined {
			t.Fatalf("seed events %+v", ev)
		}
	})
	t.Run("a death learned about an unknown member is kept but not gossiped", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		announce(h, "p01", "p02")
		h.deliver("p01", gossipMsg(mem("m", 3, gossip.StateDead), mem("n", 1, gossip.StateLeft)))
		if m, err := h.node.Lookup("m"); err != nil || m.State != gossip.StateDead {
			t.Fatalf("Lookup(m) = %+v %v", m, err)
		}
		h.net.take()
		for i := range 20 {
			h.deliver("p02", gossip.Message{Kind: gossip.KindPing, Seq: uint32(i)})
		}
		ss := h.net.take()
		if carries(t, ss, "m") || carries(t, ss, "n") {
			t.Fatal("a second-hand verdict about an unknown member was gossiped")
		}
		h.deliver("p01", gossipMsg(mem("p02", 0, gossip.StateDead)))
		h.net.take()
		h.deliver("p01", gossip.Message{Kind: gossip.KindPing, Seq: 99})
		if !carries(t, h.net.take(), "p02") {
			t.Fatal("the death of a known member was not gossiped")
		}
	})
	t.Run("the tombstones are capped and the oldest is evicted first", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{DeadRetention: time.Second})
		h.node.Start()
		var ms []gossip.Member
		for i := range gossip.MaxTombstones + 1 {
			ms = append(ms, mem(fmt.Sprintf("d%05d", i), 4, gossip.StateDead))
		}
		h.node.OnRequest("p01", syncReq(t, ms...), func([]byte, error) {})
		h.clock.advance(2 * time.Second)
		drainRequests(h)
		if n := len(h.node.Tombstones()); n != gossip.MaxTombstones {
			t.Fatalf("%d tombstones, cap %d", n, gossip.MaxTombstones)
		}
		if h.node.Stats().TombstonesEvicted != 1 {
			t.Fatalf("TombstonesEvicted = %d", h.node.Stats().TombstonesEvicted)
		}
		if _, ok := tombstone(h.node, "d00000"); ok {
			t.Fatal("the oldest tombstone was not the one evicted")
		}
		h.takeEvents()
		h.deliver("p01", gossipMsg(mem("d00000", 0, gossip.StateAlive), mem("d00001", 0, gossip.StateAlive)))
		if m, err := h.node.Lookup("d00000"); err != nil || m.State != gossip.StateAlive {
			t.Fatalf("an evicted ID is not accepted as new: %+v %v", m, err)
		}
		if m, err := h.node.Lookup("d00001"); err != nil || m.State != gossip.StateDead {
			t.Fatalf("a retained tombstone did not answer the stale alive: %+v %v", m, err)
		}
	})
	t.Run("one member buried and reinstated many times never grows the tombstone backlog past twice the cap", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{DeadRetention: time.Second})
		h.node.Start()
		h.deliver("p01", gossipMsg(mem("x", 5, gossip.StateDead)))
		for i := range 2*gossip.MaxTombstones + 10 {
			h.clock.advance(2 * time.Second)
			drainRequests(h)
			h.net.take()
			if _, ok := tombstone(h.node, "x"); !ok {
				t.Fatalf("cycle %d: x not buried", i)
			}
			h.deliver("p01", gossipMsg(mem("x", 1, gossip.StateAlive)))
			if n := gossip.TombBacklog(h.node); n > 2*gossip.MaxTombstones+1 {
				t.Fatalf("cycle %d: tombstone backlog %d", i, n)
			}
		}
		if n := len(h.node.Tombstones()); n != 0 {
			t.Fatalf("%d tombstones while x is reinstated", n)
		}
	})
	t.Run("the heal sync contacts a purged dead member but never a purged left one", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{DeadRetention: time.Second})
		announce(h, "p01")
		h.node.Start()
		h.deliver("p01", gossipMsg(mem("gone", 2, gossip.StateDead), mem("bye", 2, gossip.StateLeft)))
		h.clock.advance(2 * time.Second)
		drainRequests(h)
		contacted := map[env.NodeID]int{}
		for range 40 {
			h.clock.advance(gossip.DefaultSyncInterval)
			for _, r := range h.net.takeRequests() {
				contacted[r.to]++
				r.cb(nil, env.ErrTimeout)
			}
		}
		if contacted["gone"] == 0 || contacted["bye"] != 0 {
			t.Fatalf("contacted %v", contacted)
		}
	})
}

// TestJoinSeedsAreRemembered proves that seeds passed to Join are retried while
// alone and used by heal syncs, remembered seeds are capped at MaxSeeds, and a
// self-only seed list triggers no join retries.

```

## TestFuzzSeedsDrawNonEmptyViews

Mentioned in: book/chapters/02-swim-gossip.md

internal/gossip/prop_test.go:164

```go

func TestFuzzSeedsDrawNonEmptyViews(t *testing.T) {
	seeds := viewMergeSeeds(t)
	large := 0
	for i, b := range seeds {
		n := -1
		t.Run(fmt.Sprintf("seed %d", i), func(t *testing.T) {
			rapid.MakeFuzz(func(rt *rapid.T) { n = len(drawMergeUpdates(rt)) })(t, b)
		})
		if n >= 5 {
			large++
		}
	}
	if large < 2 {
		t.Fatalf("%d of %d FuzzViewMergeLaws seeds draw five or more updates, want at least 2", large, len(seeds))
	}
}


```

## TestJoinNoReachableSeedDST

Mentioned in: book/chapters/02-swim-gossip.md

internal/gossip/dst_test.go:817

```go

func TestJoinNoReachableSeedDST(t *testing.T) {
	t.Parallel()
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		var got error
		simtest.RequireDeterministic(t, seed, joinFailRun(&got))
		if !errors.Is(got, gossip.ErrJoinFailed) || !errors.Is(got, env.ErrUnreachable) {
			t.Fatalf("join error %v, want ErrJoinFailed wrapping env.ErrUnreachable", got)
		}
	})
}


```

## TestJoinSeedsAreRemembered

Mentioned in: book/chapters/02-swim-gossip.md

internal/gossip/repair_test.go:333

```go

func TestJoinSeedsAreRemembered(t *testing.T) {
	t.Run("seeds given to Join are retried while alone and contacted by the heal sync", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		h.node.Start()
		if err := h.node.Join([]env.NodeID{"s1"}, nil); err != nil {
			t.Fatal(err)
		}
		drainRequests(h)
		alone := 0
		for range 5 {
			h.clock.advance(gossip.DefaultProbeInterval)
			for _, r := range h.net.takeRequests() {
				if r.to == "s1" {
					alone++
				}
				r.cb(nil, env.ErrTimeout)
			}
		}
		if alone < 4 {
			t.Fatalf("join seed retried %d times in 5 lonely periods", alone)
		}
		announce(h, "p01")
		healed := 0
		for range 40 {
			h.clock.advance(gossip.DefaultSyncInterval)
			for _, r := range h.net.takeRequests() {
				if r.to == "s1" {
					healed++
				}
				r.cb(nil, env.ErrTimeout)
			}
		}
		if healed == 0 {
			t.Fatal("the heal sync never contacted the join seed")
		}
	})
	t.Run("remembered seeds are capped at MaxSeeds and a longer configured list is refused", func(t *testing.T) {
		var many []env.NodeID
		for i := range gossip.MaxSeeds + 10 {
			many = append(many, env.NodeID(fmt.Sprintf("s%03d", i)))
		}
		h := newHarness(t, "self", gossip.Config{})
		if err := h.node.Join(append(many, many[0], "self"), nil); err != nil {
			t.Fatal(err)
		}
		if n := len(h.net.takeRequests()); n != gossip.MaxSeeds+10 {
			t.Fatalf("join contacted %d seeds", n)
		}
		if n := gossip.SeedCount(h.node); n != gossip.MaxSeeds {
			t.Fatalf("%d seeds remembered", n)
		}
		if err := (gossip.Config{Seeds: many}).WithDefaults().Validate(); !errors.Is(err, env.ErrInvalidArgument) {
			t.Fatalf("%d configured seeds: %v", len(many), err)
		}
	})
	t.Run("a node whose only seed is itself never retries a join", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{Seeds: []env.NodeID{"self"}})
		h.node.Start()
		h.clock.advance(5 * gossip.DefaultProbeInterval)
		if n := h.node.Stats().Rejoins; n != 0 {
			t.Fatalf("Rejoins = %d", n)
		}
		if r := h.net.takeRequests(); len(r) != 0 {
			t.Fatalf("requests %+v", r)
		}
	})
}

// TestRelayBounds proves that ping-req floods stay capped at MaxRelays with
// excess counted in RelaysDropped, expired relays are pruned on arrival without
// a tick, answered relays keep the expiry FIFO bounded at 2*MaxRelays, and
// relays whose outbound ping fails are not retained.

```

## TestProbeAndIndirectProbe

Mentioned in: book/chapters/02-swim-gossip.md

internal/gossip/node_test.go:131

```go

func TestProbeAndIndirectProbe(t *testing.T) {
	setup := func(t *testing.T, cfg gossip.Config) (*harness, env.NodeID, uint32) {
		h := newHarness(t, "self", cfg)
		announce(h, peerIDs(5)...)
		h.node.Start()
		for {
			h.clock.advance(10 * time.Millisecond)
			if p := pings(t, h.net.take()); len(p) == 1 {
				m, _ := gossip.Decode(p[0].msg)
				return h, p[0].to, m.Seq
			}
		}
	}
	t.Run("a ping is answered with an ack carrying the same sequence number", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		h.deliver("p01", gossip.Message{Kind: gossip.KindPing, Seq: 5})
		ms := decodeAll(t, h.net.sent)
		if len(ms) != 1 || ms[0].Kind != gossip.KindAck || ms[0].Seq != 5 || h.net.sent[0].to != "p01" {
			t.Fatalf("reply: %+v", ms)
		}
	})
	t.Run("an unanswered ping triggers ping-req through k other members", func(t *testing.T) {
		h, target, seq := setup(t, gossip.Config{})
		h.clock.advance(gossip.DefaultProbeTimeout)
		var via []env.NodeID
		for i, m := range decodeAll(t, h.net.sent) {
			if m.Kind != gossip.KindPingReq {
				continue
			}
			if m.Target != target || m.Seq != seq {
				t.Fatalf("ping-req %+v, want target %s seq %d", m, target, seq)
			}
			via = append(via, h.net.sent[i].to)
		}
		slices.Sort(via)
		if len(via) != gossip.DefaultIndirectProbes || slices.Contains(via, target) || len(slices.Compact(via)) != gossip.DefaultIndirectProbes {
			t.Fatalf("intermediaries %v for target %s", via, target)
		}
		if h.node.Stats().IndirectProbes != 1 {
			t.Fatalf("IndirectProbes = %d", h.node.Stats().IndirectProbes)
		}
	})
	t.Run("an indirect ack saves the target from suspicion", func(t *testing.T) {
		h, target, seq := setup(t, gossip.Config{})
		h.clock.advance(gossip.DefaultProbeTimeout)
		h.deliver("p05", gossip.Message{Kind: gossip.KindAck, Seq: seq})
		h.clock.advance(gossip.DefaultProbeInterval)
		if m, _ := lookupState(t, h.node, string(target)); m.State != gossip.StateAlive {
			t.Fatalf("target %s is %s after an indirect ack", target, m.State)
		}
	})
	t.Run("no ack at all makes the target suspect, and dead after the suspicion timeout", func(t *testing.T) {
		h, target, _ := setup(t, gossip.Config{})
		h.takeEvents()
		h.clock.advance(gossip.DefaultProbeInterval)
		if m, _ := lookupState(t, h.node, string(target)); m.State != gossip.StateSuspect {
			t.Fatalf("target is %s, want suspect", m.State)
		}
		evs := h.takeEvents()
		if len(evs) != 1 || evs[0].Kind != gossip.EventSuspected || evs[0].Member.ID != target {
			t.Fatalf("events %+v", evs)
		}
		timeout := gossip.Config{}.WithDefaults().SuspicionTimeout(6)
		h.clock.advance(timeout - gossip.DefaultProbeInterval)
		if m, _ := lookupState(t, h.node, string(target)); m.State != gossip.StateSuspect {
			t.Fatalf("target is %s before the timeout", m.State)
		}
		h.clock.advance(gossip.DefaultProbeInterval)
		if m, _ := lookupState(t, h.node, string(target)); m.State != gossip.StateDead {
			t.Fatalf("target is %s after the timeout, want dead", m.State)
		}
		var died bool
		for _, ev := range h.takeEvents() {
			if ev.Kind == gossip.EventDied && ev.Member.ID == target {
				died = true
			}
		}
		if !died {
			t.Fatal("no died event")
		}
	})
	t.Run("a refutation during the suspicion timeout cancels the death", func(t *testing.T) {
		h, target, _ := setup(t, gossip.Config{})
		h.clock.advance(gossip.DefaultProbeInterval)
		h.deliver(target, gossipMsg(mem(string(target), 1, gossip.StateAlive)))
		h.takeEvents()
		h.clock.advance(gossip.Config{}.WithDefaults().SuspicionTimeout(6) * 2)
		for _, ev := range h.takeEvents() {
			if ev.Kind == gossip.EventDied && ev.Member.ID == target && ev.Member.Incarnation == 0 {
				t.Fatal("the suspicion at incarnation 0 killed the member after it refuted")
			}
		}
	})
	t.Run("the indirect stage can be switched off only by the test switch", func(t *testing.T) {
		cfg := gossip.WithoutIndirectProbes(gossip.Config{})
		h, _, _ := setup(t, cfg)
		h.clock.advance(gossip.DefaultProbeTimeout)
		for _, m := range decodeAll(t, h.net.sent) {
			if m.Kind == gossip.KindPingReq {
				t.Fatal("ping-req sent with indirect probes off")
			}
		}
	})
	t.Run("a ping-req is relayed and the target's ack forwarded with the requester's sequence number", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		announce(h, peerIDs(3)...)
		h.deliver("p01", gossip.Message{Kind: gossip.KindPingReq, Seq: 77, Target: "p03"})
		ss := h.net.take()
		ms := decodeAll(t, ss)
		if len(ms) != 1 || ms[0].Kind != gossip.KindPing || ss[0].to != "p03" {
			t.Fatalf("relay sent %+v", ms)
		}
		h.deliver("p03", gossip.Message{Kind: gossip.KindAck, Seq: ms[0].Seq + 1000})
		if len(h.net.sent) != 0 {
			t.Fatal("an unknown ack was forwarded")
		}
		h.deliver("p03", gossip.Message{Kind: gossip.KindAck, Seq: ms[0].Seq})
		ss = h.net.take()
		ms = decodeAll(t, ss)
		if len(ms) != 1 || ms[0].Kind != gossip.KindAck || ms[0].Seq != 77 || ss[0].to != "p01" {
			t.Fatalf("forwarded %+v to %v", ms, ss)
		}
		h.deliver("p03", gossip.Message{Kind: gossip.KindAck, Seq: ms[0].Seq})
		if len(h.net.sent) != 0 {
			t.Fatal("a duplicate ack was forwarded twice")
		}
	})
	t.Run("a relay expires and a late ack is not forwarded", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		announce(h, peerIDs(3)...)
		h.node.Start()
		h.deliver("p01", gossip.Message{Kind: gossip.KindPingReq, Seq: 9, Target: "p03"})
		var relaySeq uint32
		for i, m := range decodeAll(t, h.net.take()) {
			_ = i
			relaySeq = m.Seq
		}
		h.clock.advance(3 * gossip.DefaultProbeInterval)
		h.net.take()
		h.deliver("p03", gossip.Message{Kind: gossip.KindAck, Seq: relaySeq})
		for _, m := range decodeAll(t, h.net.take()) {
			if m.Kind == gossip.KindAck && m.Seq == 9 {
				t.Fatal("expired relay forwarded")
			}
		}
	})
	t.Run("the suspicion timeout and retransmit limit scale with log2 of the member count", func(t *testing.T) {
		c := gossip.Config{}.WithDefaults()
		for _, r := range []struct {
			n        int
			wantLog  int
			wantSusp time.Duration
		}{{0, 1, 4 * time.Second}, {1, 1, 4 * time.Second}, {2, 1, 4 * time.Second}, {8, 3, 12 * time.Second}, {9, 4, 16 * time.Second}, {100, 7, 28 * time.Second}} {
			if got := c.SuspicionTimeout(r.n); got != r.wantSusp {
				t.Errorf("SuspicionTimeout(%d) = %v, want %v", r.n, got, r.wantSusp)
			}
			if got := c.RetransmitLimit(r.n); got != gossip.DefaultRetransmitMult*r.wantLog {
				t.Errorf("RetransmitLimit(%d) = %d, want %d", r.n, got, gossip.DefaultRetransmitMult*r.wantLog)
			}
		}
	})
}


```

## TestPropIncarnationMonotonic

Mentioned in: book/chapters/02-swim-gossip.md

internal/gossip/prop_test.go:296

```go

func TestPropIncarnationMonotonic(t *testing.T) {
	rapid.Check(t, propIncarnationMonotonic)
	rapid.Check(t, propRecordMonotonicAcrossPurge)
	rapid.Check(t, func(t *rapid.T) {
		ups := rapid.SliceOfN(genUpdate(propIDs, 6), 0, 40).Draw(t, "updates")
		v := gossip.NewView()
		last := map[env.NodeID]uint32{}
		for _, u := range ups {
			v.Apply(u)
			for _, m := range v.All() {
				if m.Incarnation < last[m.ID] {
					t.Fatalf("view incarnation of %s went from %d to %d", m.ID, last[m.ID], m.Incarnation)
				}
				last[m.ID] = m.Incarnation
			}
		}
	})
}


```

## TestPropViewMergeIsCommutative

Mentioned in: book/chapters/02-swim-gossip.md

internal/gossip/prop_test.go:94

```go

func TestPropViewMergeIsCommutative(t *testing.T) {
	rapid.Check(t, propViewMergeCommutative)
}

// viewMergeSeeds returns byte buffers for FuzzViewMergeLaws: three synthetic
// patterns plus non-empty rapid draw buffers from testdata/merge-seeds.txt.

```

## TestRelayBounds

Mentioned in: book/chapters/02-swim-gossip.md

internal/gossip/repair_test.go:405

```go

func TestRelayBounds(t *testing.T) {
	const flood = 10_000
	t.Run("a ping-req flood keeps at most MaxRelays relays and counts the rest", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		for i := range flood {
			h.deliver("p01", gossip.Message{Kind: gossip.KindPingReq, Seq: uint32(i), Target: "victim"})
		}
		ss := h.net.take()
		if len(ss) != gossip.MaxRelays {
			t.Fatalf("%d pings relayed, cap %d", len(ss), gossip.MaxRelays)
		}
		if n := gossip.RelayCount(h.node); n != gossip.MaxRelays {
			t.Fatalf("%d relays held", n)
		}
		if n := h.node.Stats().RelaysDropped; n != flood-gossip.MaxRelays {
			t.Fatalf("RelaysDropped = %d", n)
		}
		for i := range flood {
			h.deliver("victim", gossip.Message{Kind: gossip.KindAck, Seq: uint32(1<<31 + i)})
		}
		if len(h.net.sent) != 0 {
			t.Fatal("an unknown ack was forwarded")
		}
		for _, m := range decodeAll(t, ss) {
			h.deliver("victim", gossip.Message{Kind: gossip.KindAck, Seq: m.Seq})
		}
		if n := len(h.net.take()); n != gossip.MaxRelays {
			t.Fatalf("%d acks forwarded", n)
		}
		if n := gossip.RelayCount(h.node); n != 0 {
			t.Fatalf("%d relays left after every ack", n)
		}
	})
	t.Run("expired relays are pruned when a ping-req arrives, without a tick", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		for i := range gossip.MaxRelays {
			h.deliver("p01", gossip.Message{Kind: gossip.KindPingReq, Seq: uint32(i), Target: "victim"})
		}
		h.clock.advance(gossip.DefaultProbeInterval + time.Millisecond)
		h.net.take()
		h.deliver("p01", gossip.Message{Kind: gossip.KindPingReq, Seq: 7, Target: "victim"})
		if n := len(h.net.take()); n != 1 {
			t.Fatalf("%d pings after the old relays expired", n)
		}
		if n := gossip.RelayCount(h.node); n != 1 {
			t.Fatalf("%d relays held", n)
		}
	})
	t.Run("relays answered at once never let the expiry list grow past twice the cap", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		for i := range 3 * gossip.MaxRelays {
			h.deliver("p01", gossip.Message{Kind: gossip.KindPingReq, Seq: uint32(i), Target: "victim"})
			ms := decodeAll(t, h.net.take())
			h.deliver("victim", gossip.Message{Kind: gossip.KindAck, Seq: ms[0].Seq})
			h.net.take()
			if n := gossip.RelayBacklog(h.node); n > 2*gossip.MaxRelays+1 {
				t.Fatalf("expiry list holds %d entries after %d relays", n, i+1)
			}
		}
		if h.node.Stats().RelaysDropped != 0 || gossip.RelayCount(h.node) != 0 {
			t.Fatalf("dropped %d, held %d", h.node.Stats().RelaysDropped, gossip.RelayCount(h.node))
		}
	})
	t.Run("a relay whose ping could not be sent is not kept", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		h.net.sendErr = env.ErrUnreachable
		h.deliver("p01", gossip.Message{Kind: gossip.KindPingReq, Seq: 3, Target: "victim"})
		if n := gossip.RelayCount(h.node); n != 0 {
			t.Fatalf("%d relays kept for an unsent ping", n)
		}
	})
}

// TestViewBounds proves that Config.MaxMembers caps view growth (recording
// ErrViewFull while allowing known members to transition and purges to free
// room), a view filled to MaxUpdates still encodes valid syncs and datagrams,
// and out-of-range MaxMembers values are rejected.

```

## TestSWIMAsymmetricLinkDST

Mentioned in: book/chapters/02-swim-gossip.md, book/front/topic-map.md

internal/gossip/dst_test.go:495

```go

func TestSWIMAsymmetricLinkDST(t *testing.T) {
	t.Parallel()
	var sus tally
	t.Cleanup(func() { t.Logf("MEASURE asymmetric suspicions %s", sus.summary()) })
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		var out asymOut
		simtest.RequireDeterministic(t, seed, asymRunFn(gossip.Config{}, &out))
		sus.add(int64(out.suspected))
		if out.died != 0 {
			t.Fatalf("%d evictions under a single one-way link", out.died)
		}
		if out.suspected != 0 {
			t.Fatalf("%d suspicions under a single one-way link with indirect probes", out.suspected)
		}
	})
}

// TestCanaryNoIndirectProbe plants a bug—disabling indirect probes via
// WithoutIndirectProbes—in the asymmetric-link scenario and proves that the
// scenario catches it on every seed (suspicions occur instead of zero).

```

## TestSWIMConvergenceDST

Mentioned in: book/chapters/02-swim-gossip.md, book/front/topic-map.md

internal/gossip/dst_test.go:443

```go

func TestSWIMConvergenceDST(t *testing.T) {
	t.Parallel()
	for _, n := range []int{20, 50, 100} {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			t.Parallel()
			var periods, msgs, falseDead tally
			t.Cleanup(func() {
				t.Logf("MEASURE converge N=%d log2N=%d bound=%d periods: periods-to-converge %s", n, ceilLog2(n), convergeC*ceilLog2(n), periods.summary())
				t.Logf("MEASURE converge N=%d milli-datagrams per node per period %s", n, msgs.summary())
				t.Logf("MEASURE converge N=%d false Died events (healthy nodes, any observer) %s", n, falseDead.summary())
			})
			shardedSweep(t, convergeShards, func(t *testing.T, seed int64) {
				var out convergeOut
				simtest.RequireDeterministic(t, seed, convergeRun(n, &out))
				periods.add(out.periods)
				msgs.add(out.milliMsgs)
				falseDead.add(int64(out.falseDead))
			})
		})
	}
}

type asymOut struct {
	suspected int
	died      int
}


```

## TestSWIMCrashDetectionDST

Mentioned in: book/chapters/02-swim-gossip.md

internal/gossip/dst_test.go:578

```go

func TestSWIMCrashDetectionDST(t *testing.T) {
	t.Parallel()
	var det tally
	t.Cleanup(func() {
		t.Logf("MEASURE crash detection ms (N=%d, loss %d ppm, bound %v) %s", crashNodes, crashLossPPM, detectBound, det.summary())
	})
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		var out crashOut
		simtest.RequireDeterministic(t, seed, crashRun(&out))
		det.add(out.detect.Milliseconds())
	})
}


```

## TestSWIMLongPartitionHealDST

Mentioned in: book/chapters/02-swim-gossip.md, book/front/topic-map.md

internal/gossip/dst_test.go:1002

```go

func TestSWIMLongPartitionHealDST(t *testing.T) {
	t.Parallel()
	for _, seedCrashed := range []bool{false, true} {
		name := "members joined through Join and the seed is across the cut"
		if seedCrashed {
			name = "the only seed crashed, so only tombstones point across the cut"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var heal tally
			t.Cleanup(func() {
				t.Logf("MEASURE long partition heal ms (%s, N=%d, retention %v, bound %v) %s", name, longNodes, longRetention, longHealBound, heal.summary())
			})
			simtest.Sweep(t, func(t *testing.T, seed int64) {
				var out time.Duration
				simtest.RequireDeterministic(t, seed, longPartitionRun(seedCrashed, &out))
				heal.add(out.Milliseconds())
			})
		})
	}
}


```

## TestSWIMPartitionHealDST

Mentioned in: book/chapters/02-swim-gossip.md

internal/gossip/dst_test.go:643

```go

func TestSWIMPartitionHealDST(t *testing.T) {
	t.Parallel()
	var heal tally
	t.Cleanup(func() {
		t.Logf("MEASURE partition heal ms (N=%d, bound %v) %s", partitionNodes, healBound, heal.summary())
	})
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		var out time.Duration
		simtest.RequireDeterministic(t, seed, partitionRun(&out))
		heal.add(out.Milliseconds())
	})
}

type refuteOut struct {
	suspected int
	died      int
	inc       uint32
}


```

## TestSWIMRefutationDST

Mentioned in: book/chapters/02-swim-gossip.md

internal/gossip/dst_test.go:699

```go

func TestSWIMRefutationDST(t *testing.T) {
	t.Parallel()
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		var out refuteOut
		simtest.RequireDeterministic(t, seed, refuteRunFn(&out))
		if out.suspected == 0 {
			t.Fatal("the paused node was never suspected: the scenario did not exercise refutation")
		}
		if out.died != 0 {
			t.Fatalf("the falsely suspected node was declared dead %d times", out.died)
		}
		if out.inc == 0 {
			t.Fatal("the suspected node never raised its incarnation")
		}
	})
}

type restartOut struct {
	before gossip.Member
	after  gossip.Member
}


```

## TestSWIMRestartSameIDDST

Mentioned in: book/chapters/02-swim-gossip.md

internal/gossip/dst_test.go:768

```go

func TestSWIMRestartSameIDDST(t *testing.T) {
	t.Parallel()
	for _, wait := range []bool{true, false} {
		name := "restarted before anyone noticed the crash"
		if wait {
			name = "restarted after every node declared it dead"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			simtest.Sweep(t, func(t *testing.T, seed int64) {
				var out restartOut
				simtest.RequireDeterministic(t, seed, restartRun(wait, &out))
				if out.after.Incarnation < out.before.Incarnation || (out.before.State != gossip.StateAlive && out.after.Incarnation == out.before.Incarnation) {
					t.Fatalf("rejoined as %+v over the old entry %+v", out.after, out.before)
				}
			})
		})
	}
}


```

## TestViewBounds

Mentioned in: book/chapters/02-swim-gossip.md

internal/gossip/repair_test.go:482

```go

func TestViewBounds(t *testing.T) {
	t.Run("a sync request cannot grow the view past MaxMembers", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{MaxMembers: 10, DeadRetention: time.Second})
		h.node.Start()
		var ms []gossip.Member
		for i := range 20 {
			ms = append(ms, mem(fmt.Sprintf("m%02d", i), 0, gossip.StateAlive))
		}
		var resp []byte
		var rerr error
		h.node.OnRequest("m00", syncReq(t, ms...), func(b []byte, err error) { resp, rerr = b, err })
		if rerr != nil {
			t.Fatal(rerr)
		}
		if n := len(h.node.All()); n != 10 {
			t.Fatalf("view holds %d members, cap 10", n)
		}
		if h.node.Stats().ViewFull != 11 || !errors.Is(h.node.LastError(), gossip.ErrViewFull) {
			t.Fatalf("ViewFull = %d, last %v", h.node.Stats().ViewFull, h.node.LastError())
		}
		if m, err := gossip.Decode(resp); err != nil || len(m.Updates) != 10 {
			t.Fatalf("reply %+v %v", m, err)
		}
		h.deliver("m00", gossipMsg(mem("m01", 0, gossip.StateDead)))
		if m, _ := h.node.Lookup("m01"); m.State != gossip.StateDead {
			t.Fatalf("a known member could not change while the view was full: %+v", m)
		}
		h.clock.advance(2 * time.Second)
		drainRequests(h)
		h.deliver("m00", gossipMsg(mem("m15", 0, gossip.StateAlive)))
		if _, err := h.node.Lookup("m15"); err != nil {
			t.Fatalf("a purge did not free room: %v", err)
		}
	})
	t.Run("a view filled to MaxUpdates still encodes as a sync and its packets stay decodable", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{MaxPacket: gossip.MaxPacketLimit})
		ms := make([]gossip.Member, 0, gossip.MaxUpdates)
		for i := range gossip.MaxUpdates {
			ms = append(ms, gossip.Member{ID: env.NodeID(fmt.Sprintf("%05d", i)), Incarnation: 0, State: gossip.StateAlive})
		}
		var rerr error
		replied := false
		h.node.OnRequest("00000", syncReq(t, ms...), func(_ []byte, err error) { replied, rerr = true, err })
		if !replied || rerr != nil {
			t.Fatalf("reply %v %v", replied, rerr)
		}
		if n := len(h.node.All()); n != gossip.MaxUpdates {
			t.Fatalf("view holds %d, want %d", n, gossip.MaxUpdates)
		}
		if err := h.node.Join([]env.NodeID{"00001"}, nil); err != nil {
			t.Fatal(err)
		}
		reqs := h.net.takeRequests()
		if len(reqs) != 1 {
			t.Fatalf("%d join requests", len(reqs))
		}
		if m, err := gossip.Decode(reqs[0].req); err != nil || len(m.Updates) != gossip.MaxUpdates {
			t.Fatalf("join request does not decode: %v", err)
		}
		h.deliver("00002", gossip.Message{Kind: gossip.KindPing, Seq: 1})
		for _, m := range decodeAll(t, h.net.take()) {
			if len(m.Updates) > gossip.MaxUpdates {
				t.Fatalf("%d updates in one packet", len(m.Updates))
			}
		}
	})
	t.Run("MaxMembers outside 1..MaxUpdates is refused", func(t *testing.T) {
		for _, v := range []int{-1, gossip.MaxUpdates + 1} {
			if err := (gossip.Config{MaxMembers: v}).WithDefaults().Validate(); !errors.Is(err, env.ErrInvalidArgument) {
				t.Fatalf("MaxMembers %d: %v", v, err)
			}
		}
		if got := (gossip.Config{}).WithDefaults().MaxMembers; got != gossip.MaxUpdates {
			t.Fatalf("default MaxMembers %d", got)
		}
	})
}

// TestCallbacksNeverRunInsideTheRegisteringCall proves that Join and Subscribe
// never invoke their callbacks synchronously and that Join callbacks report
// ErrClosed (merging nothing) if Leave happens before completion.

```

## BenchmarkEncode1MiBStripe

Mentioned in: book/chapters/03-reed-solomon-erasure-coding.md

internal/erasure/bench_test.go:29

```go

func BenchmarkEncode1MiBStripe(b *testing.B) {
	codec, shards := benchStripe(b)
	b.SetBytes(1 << 20)
	for b.Loop() {
		if err := codec.Encode(shards); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkReconstructTwoDataShards1MiBStripe measures ReconstructData
// throughput when two data shards of a 1 MiB (4+2) stripe are missing.

```

## BenchmarkReconstructAllMissing1MiBStripe

Mentioned in: book/chapters/03-reed-solomon-erasure-coding.md

internal/erasure/bench_test.go:54

```go

func BenchmarkReconstructAllMissing1MiBStripe(b *testing.B) {
	codec, shards := benchStripe(b)
	b.SetBytes(1 << 20)
	for b.Loop() {
		shards[1], shards[4] = nil, nil
		if err := codec.Reconstruct(shards); err != nil {
			b.Fatal(err)
		}
	}
}


```

## BenchmarkReconstructTwoDataShards1MiBStripe

Mentioned in: book/chapters/03-reed-solomon-erasure-coding.md

internal/erasure/bench_test.go:41

```go

func BenchmarkReconstructTwoDataShards1MiBStripe(b *testing.B) {
	codec, shards := benchStripe(b)
	b.SetBytes(1 << 20)
	for b.Loop() {
		shards[0], shards[2] = nil, nil
		if err := codec.ReconstructData(shards); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkReconstructAllMissing1MiBStripe measures Reconstruct throughput when
// one data shard and one parity shard of a 1 MiB (4+2) stripe are missing.

```

## BenchmarkStripedPutGet8MiB

Mentioned in: book/chapters/03-reed-solomon-erasure-coding.md

internal/storage/bench_test.go:15

```go

func BenchmarkStripedPutGet8MiB(b *testing.B)  { benchPutGet(b, 8<<20) }

```

## FuzzErasureCoding

Mentioned in: book/chapters/03-reed-solomon-erasure-coding.md, book/front/topic-map.md

internal/erasure/codec_test.go:533

```go

func FuzzErasureCoding(f *testing.F) {
	for _, seed := range codingFuzzSeeds() {
		f.Add(seed)
	}
	f.Fuzz(rapid.MakeFuzz(propErasureCoding))
}

// TestPropErasureCodingAnyLossSet checks that any erasure set of size <= m
// reconstructs exact bytes and any set of size > m returns ErrTooFewShards.

```

## FuzzGF256

Mentioned in: book/chapters/03-reed-solomon-erasure-coding.md

internal/erasure/gf256_test.go:224

```go

func FuzzGF256(f *testing.F) {
	for _, seed := range gf256FuzzSeeds() {
		f.Add(seed)
	}
	f.Fuzz(rapid.MakeFuzz(propFieldAxioms))
}


```

## FuzzShardDecode

Mentioned in: book/chapters/03-reed-solomon-erasure-coding.md, book/front/topic-map.md

internal/storage/shard_test.go:189

```go

func FuzzShardDecode(f *testing.F) {
	good, err := storage.EncodeShard(sampleHeader(), []byte("payload bytes"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add(good)
	f.Add(good[:storage.HeaderSize])
	f.Add(good[:len(good)-1])
	f.Add(append(append([]byte{}, good...), 1))
	empty, err := storage.EncodeShard(storage.ShardHeader{K: 1, M: 0}, nil)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(empty)
	f.Fuzz(func(t *testing.T, blob []byte) {
		h, payload, err := storage.DecodeShard(blob)
		if err != nil {
			if !errors.Is(err, storage.ErrCorrupt) {
				t.Fatalf("decode error %v is not ErrCorrupt", err)
			}
			return
		}
		if h.PayloadLen != len(payload) || len(blob) != storage.HeaderSize+len(payload) {
			t.Fatalf("accepted a blob whose length disagrees with its header")
		}
		sum := sha256.Sum256(append(append([]byte{}, blob[:32]...), payload...))
		if sum != h.Checksum {
			t.Fatal("accepted a blob whose checksum does not match: checksum bypass")
		}
		again, err := storage.EncodeShard(storage.ShardHeader{K: h.K, M: h.M, Index: h.Index, Stripe: h.Stripe, Version: h.Version}, payload)
		if err != nil || !bytes.Equal(again, blob) {
			t.Fatalf("an accepted blob does not re-encode to itself: %v", err)
		}
	})
}


```

## TestCanaryChecksumSkipBreaksCorruptionProperty

Mentioned in: book/chapters/03-reed-solomon-erasure-coding.md

internal/storage/stripe_test.go:289

```go

func TestCanaryChecksumSkipBreaksCorruptionProperty(t *testing.T) {
	if runPlanted(t, propCorruptionBecomesErasure) {
		t.Fatal("the property fails without the planted bug")
	}
	restore := storage.PlantSkipChecksum()
	defer restore()
	codec, err := erasure.New(2, 1)
	mustNil(t, err)
	blobs, _ := encodeStripe(t, codec, 1, 0, []byte("abcd"))
	blobs[0][storage.HeaderSize] ^= 1
	if _, _, err := storage.DecodeShard(blobs[0]); err != nil {
		t.Fatalf("the planted bug did not take effect: %v", err)
	}
	if !runPlanted(t, propCorruptionBecomesErasure) {
		t.Fatal("TestPropCorruptionBecomesErasure passes with checksum verification skipped: the property cannot see corruption")
	}
}

// TestDecodeStripeRejectsForgedShards proves header and manifest-checksum
// checks reject a self-consistent forged shard, a shard from another code, and
// a wrong-length shard, and shows that without manifest checksums a forged
// shard would decode wrong bytes.

```

## TestCanaryChecksumSkipFailsStripedDST

Mentioned in: book/chapters/03-reed-solomon-erasure-coding.md

internal/storage/dst_test.go:290

```go

func TestCanaryChecksumSkipFailsStripedDST(t *testing.T) {
	restore := storage.PlantSkipChecksum()
	defer restore()
	var outs []*dstOutcome
	_, err := simtest.CheckDeterministic(1, stripedRun(dstMode{}, &outs))
	if err == nil {
		t.Fatalf("the striped DST passes with checksum verification skipped (%d gets, %d ok)", outs[0].gets, outs[0].ok)
	}
	if !errors.Is(err, storage.ErrCorrupt) {
		t.Fatalf("the striped DST failed for another reason: %v", err)
	}
}


```

## TestNodePutReleasesTheObjectAtTheAck

Mentioned in: book/chapters/03-reed-solomon-erasure-coding.md

internal/storage/node_repair_test.go:184

```go

func TestNodePutReleasesTheObjectAtTheAck(t *testing.T) {
	codes, err := storage.Codes()
	mustNil(t, err)
	lat := sim.Range{Min: time.Microsecond, Max: time.Microsecond}
	s := sim.New(sim.Config{Seed: 220211, Codes: codes, Net: sim.NetConfig{Latency: lat, InterRegionLatency: lat}})
	ids := sim.NodeIDs(6)
	cfg := storage.Config{Layout: storage.Layout{K: 4, M: 2, StripeSize: 1 << 10}, Members: clusterMembers(ids, 3), Window: 1}
	mustNil(t, s.AddCluster(ids, 3, nodeFactory(cfg)))
	mustNil(t, s.RunUntilQuiet())
	mustNil(t, s.Pause(ids[5]))
	n := nodeOf(t, s, ids[0])
	data := bytes.Repeat([]byte{9}, 16<<10)
	held := weak.Make(&data[0])
	done := false
	n.Put("b", "k", data, func(_ *storage.Manifest, err error) {
		mustNil(t, err)
		done = true
	})
	data = nil
	mustNil(t, s.RunFor(storage.DefaultRequestTimeout/2))
	if !done {
		t.Fatal("the put did not ack with one paused holder")
	}
	if n.Stats().PutOutstanding == 0 {
		t.Fatal("no request is outstanding after the ack; the test needs late requests")
	}
	runtime.GC()
	if held.Value() != nil {
		t.Fatalf("the object buffer is still reachable after the ack with %d late requests outstanding", n.Stats().PutOutstanding)
	}
	mustNil(t, s.Resume(ids[5]))
	mustNil(t, s.RunUntilQuiet())
}


```

## TestPropChunkStoreModel

Mentioned in: book/chapters/03-reed-solomon-erasure-coding.md, book/front/topic-map.md

internal/storage/chunkstore_test.go:323

```go

func TestPropChunkStoreModel(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		rd := newRestartableDisk(rt)
		st := storage.NewChunkStore(rd.disk)
		m := &chunkModel{written: map[storage.ShardID][]byte{}, durable: map[storage.ShardID][]byte{}, rotW: map[storage.ShardID]bool{}, rotD: map[storage.ShardID]bool{}, dirty: map[storage.ShardID]bool{}}
		ids := make([]storage.ShardID, 0, 8)
		for v := 1; v <= 2; v++ {
			for idx := 0; idx < 4; idx++ {
				ids = append(ids, storage.ShardID{Bucket: "m", Version: storage.VersionID(v), Stripe: uint32(idx % 2), Index: uint16(idx)})
			}
		}
		genID := rapid.SampledFrom(ids)
		genPayload := rapid.SampledFrom([][]byte{{}, {1}, []byte("two"), []byte("payload four")})
		durableIs := func(rt *rapid.T, id storage.ShardID, want []byte) {
			got, ok := rd.s.Disk(rd.id).PeekDurable(id.BlobName())
			if want == nil {
				if ok {
					rt.Fatalf("%v is durable after a call that reported it absent", id)
				}
				return
			}
			if !ok || !bytes.Equal(got, shardFor(rt, id, want)) {
				rt.Fatalf("%v is not durable with the acknowledged bytes (present %v)", id, ok)
			}
		}
		rt.Repeat(map[string]func(*rapid.T){
			"put": func(rt *rapid.T) {
				id := genID.Draw(rt, "id")
				p := genPayload.Draw(rt, "payload")
				err := st.Put(id, shardFor(rt, id, p))
				prev, ok := m.written[id]
				switch {
				case !ok || m.rotW[id]:
					if err != nil {
						rt.Fatalf("put %v: %v", id, err)
					}
					m.setBoth(id, p)
					durableIs(rt, id, p)
				case bytes.Equal(prev, p):
					if err != nil {
						rt.Fatalf("idempotent put %v: %v", id, err)
					}
					m.setBoth(id, p)
					durableIs(rt, id, p)
				default:
					if !errors.Is(err, storage.ErrExists) {
						rt.Fatalf("conflicting put %v: %v", id, err)
					}
				}
			},
			"delete": func(rt *rapid.T) {
				id := genID.Draw(rt, "id")
				err := st.Delete(id)
				if !m.has(id) {
					if !errors.Is(err, storage.ErrNotFound) {
						rt.Fatalf("delete of missing %v: %v", id, err)
					}
				} else if err != nil {
					rt.Fatalf("delete %v: %v", id, err)
				}
				m.dropBoth(id)
				durableIs(rt, id, nil)
			},
			"rot": func(rt *rapid.T) {
				id := genID.Draw(rt, "id")
				if !m.clean(id) {
					rt.Skip("nothing clean to rot")
				}
				if err := rd.s.BitRot(rd.id, id.BlobName()); err != nil {
					rt.Fatal(err)
				}
				m.rotW[id], m.rotD[id] = true, true
			},
			"unsynced write": func(rt *rapid.T) {
				id := genID.Draw(rt, "id")
				p := genPayload.Draw(rt, "payload")
				if err := rd.disk.Write(id.BlobName(), shardFor(rt, id, p)); err != nil {
					rt.Fatal(err)
				}
				m.written[id] = p
				m.dirty[id] = true
				delete(m.rotW, id)
			},
			"unsynced delete": func(rt *rapid.T) {
				id := genID.Draw(rt, "id")
				if !m.has(id) {
					rt.Skip("nothing written to delete")
				}
				if err := rd.disk.Delete(id.BlobName()); err != nil {
					rt.Fatal(err)
				}
				delete(m.written, id)
				m.dirty[id] = true
				delete(m.rotW, id)
			},
			"crash": func(rt *rapid.T) {
				rd.crash(rt)
				st = storage.NewChunkStore(rd.disk)
				m.crash()
			},
			"": func(rt *rapid.T) {
				for _, id := range ids {
					_, p, err := st.Get(id)
					want, ok := m.written[id]
					switch {
					case !ok:
						if !errors.Is(err, storage.ErrNotFound) {
							rt.Fatalf("get missing %v: %v", id, err)
						}
					case m.rotW[id]:
						if !errors.Is(err, storage.ErrCorrupt) {
							rt.Fatalf("get rotted %v: %v %q", id, err, p)
						}
					default:
						if err != nil || !bytes.Equal(p, want) {
							rt.Fatalf("get %v = %q, %v; want %q", id, p, err, want)
						}
					}
				}
				listed, err := st.List("m")
				if err != nil {
					rt.Fatal(err)
				}
				if len(listed) != len(m.written) {
					rt.Fatalf("List has %d shards, model %d", len(listed), len(m.written))
				}
				for _, id := range listed {
					if !m.has(id) {
						rt.Fatalf("List has %v, not in the model", id)
					}
				}
			},
		})
	})
}

// TestChunkStorePutOfUnsyncedIdenticalBytesIsDurable proves Put syncs even
// when matching bytes already exist in the unsynced written layer.

```

## TestStripedPutGetDST

Mentioned in: book/chapters/03-reed-solomon-erasure-coding.md, book/front/topic-map.md

internal/storage/dst_test.go:249

```go

func TestStripedPutGetDST(t *testing.T) {
	for _, mode := range []dstMode{{lifted: false}, {lifted: true}} {
		name := "within-budget"
		if mode.lifted {
			name = "budget-lifted"
		}
		t.Run(name, func(t *testing.T) {
			agg := dstOutcome{minDurableAtAck: dstK + dstM}
			simtest.Sweep(t, func(t *testing.T, seed int64) {
				var outs []*dstOutcome
				simtest.RequireDeterministic(t, seed, stripedRun(mode, &outs))
				r := outs[0]
				if r.gets < dstGets/2 {
					t.Fatalf("only %d gets completed", r.gets)
				}
				if mode.lifted && r.unavailable == 0 {
					t.Fatal("the lifted run never saw ErrUnavailable")
				}
				agg.putMax = max(agg.putMax, r.putMax)
				agg.getMax = max(agg.getMax, r.getMax)
				agg.maxRequests = max(agg.maxRequests, r.maxRequests)
				agg.gets += r.gets
				agg.ok += r.ok
				agg.unavailable += r.unavailable
				agg.fallbacks += r.fallbacks
				agg.applied += r.applied
				agg.rejected += r.rejected
				agg.transientOnly += r.transientOnly
				agg.minDurableAtAck = min(agg.minDurableAtAck, r.minDurableAtAck)
			})
			if agg.fallbacks == 0 || agg.applied == 0 {
				t.Fatalf("the faults never reached a read: %d gets fell back, %d faults applied", agg.fallbacks, agg.applied)
			}
			t.Logf("MEASURED %s: put max %v (bound %v), get max %v (bound %v), max mean requests per stripe %d (bound %d), gets %d ok %d unavailable %d fell-back %d, faults applied %d rejected-by-budget %d, unavailable-with-transient-cause %d, fewest durable shards of a stripe at its PUT ack %d (need %d)",
				name, agg.putMax, dstPutBound, agg.getMax, dstGetBound, agg.maxRequests, dstMaxRequests, agg.gets, agg.ok, agg.unavailable, agg.fallbacks, agg.applied, agg.rejected, agg.transientOnly, agg.minDurableAtAck, dstK+1)
		})
	}
}

// TestCanaryChecksumSkipFailsStripedDST plants a bug that skips SHA-256
// verification in DecodeShard and proves the striped DST catches it as ErrCorrupt.

```

## FuzzMerkleDiff

Mentioned in: book/chapters/04-merkle-anti-entropy.md, book/front/topic-map.md

internal/merkle/prop_test.go:188

```go

func FuzzMerkleDiff(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte{255, 255, 255, 255, 0, 0, 0, 0, 1})
	f.Fuzz(rapid.MakeFuzz(propDiffComplete))
}


```

## FuzzMerkleWire

Mentioned in: book/chapters/04-merkle-anti-entropy.md, book/front/topic-map.md

internal/merkle/wire_test.go:169

```go

func FuzzMerkleWire(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x04})
	f.Add(encodeRequest(request{depth: 2, level: 0, root: h32(1)}))
	f.Add(encodeRequest(request{depth: 2, level: 1, paths: []uint64{0, 1 << 60}}))
	f.Add(encodeRequest(request{depth: 2, level: 2, paths: []uint64{0x12 << 56}}))
	f.Add(encodeRequest(request{depth: 2, level: 2, paths: []uint64{0x12 << 56}, resume: true, after: "a"}))
	f.Add(encodeResponse(response{kind: msgInner, level: 0, bitmaps: []uint16{0xc000}, hashes: []Hash{h32(1), h32(2)}}))
	f.Add(encodeResponse(response{kind: msgLeaves, level: 2, leaves: [][]Entry{{{Key: "a", Hash: h32(3)}}}}))
	f.Add(encodeResponse(response{kind: msgLeaves, level: 2, more: true, leaves: [][]Entry{nil, {{Key: "a", Hash: h32(3)}}}}))
	f.Add([]byte{0x02, 0, 0xff, 0xff, 0x03})
	tr, err := New(2)
	if err != nil {
		f.Fatal(err)
	}
	for i := 0; i < 64; i++ {
		if err := tr.Set(string(rune('a'+i%26))+string(rune('a'+i/26)), Sum([]byte{byte(i)})); err != nil {
			f.Fatal(err)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			return
		}
		if req, err := decodeRequest(data); err == nil {
			if !bytes.Equal(encodeRequest(req), data) {
				t.Fatalf("request % x does not re-encode to itself", data)
			}
		} else if !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrDepth) {
			t.Fatalf("decodeRequest error %v", err)
		}
		if resp, err := decodeResponse(data); err == nil {
			if !bytes.Equal(encodeResponse(resp), data) {
				t.Fatalf("response % x does not re-encode to itself", data)
			}
		} else if !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrDepth) {
			t.Fatalf("decodeResponse error %v", err)
		}
		if out, err := tr.Answer(data); err == nil {
			if _, err := decodeResponse(out); err != nil {
				t.Fatalf("Answer produced an undecodable reply: %v", err)
			}
			if len(out) > MaxReplyBytes {
				t.Fatalf("Answer produced %d bytes, over MaxReplyBytes", len(out))
			}
		} else if !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrDepth) {
			t.Fatalf("Answer error %v", err)
		}
		for steps := 0; steps < 3; steps++ {
			s := tr.Diff(Limits{Frontier: 4})
			for i := 0; i < steps; i++ {
				req, ok := s.Request()
				if !ok {
					break
				}
				other := tr.Snapshot()
				other.Delete("aa")
				resp, err := other.Answer(req)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Feed(resp); err != nil {
					t.Fatal(err)
				}
			}
			if _, ok := s.Request(); !ok {
				continue
			}
			if err := s.Feed(data); err != nil && !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrDepth) {
				t.Fatalf("Feed error %v", err)
			}
			for n := 0; n < 16; n++ {
				if _, ok := s.Request(); !ok {
					break
				}
				s.Fail(ErrMalformed)
			}
			if !s.Done() {
				t.Fatal("session not done")
			}
		}
	})
}


```

## TestBitRotRepairDST

Mentioned in: book/chapters/04-merkle-anti-entropy.md, book/front/topic-map.md

internal/merkle/dst_test.go:367

```go

func TestBitRotRepairDST(t *testing.T) {
	t.Run("rot during traffic is detected and repaired", func(t *testing.T) {
		simtest.Sweep(t, func(t *testing.T, seed int64) {
			s := simtest.RequireDeterministic(t, seed, bitRotRun(true))
			w := take(s)
			if w.convergeAt == 0 {
				t.Fatalf("replicas did not converge within %v of quiet", BitRotConvergeBound)
			}
			if len(w.rotFirst) != 0 {
				t.Fatalf("%d damaged blobs never repaired", len(w.rotFirst))
			}
			if w.rotEvents == 0 || w.repairs == 0 {
				t.Fatalf("no rot happened (events %d, repairs %d)", w.rotEvents, w.repairs)
			}
			if w.maxRepair > BitRotRepairBound {
				t.Fatalf("slowest repair %v over the bound %v", w.maxRepair, BitRotRepairBound)
			}
			moved := w.fetchedBytes()
			if moved > RepairCopiesPerRot*w.rotBytes {
				t.Fatalf("fetched %d bytes for %d rotted bytes", moved, w.rotBytes)
			}
			var rot uint64
			for _, st := range w.stats() {
				rot += st.Changed
			}
			if rot == 0 {
				t.Fatal("the scrub never saw changed bytes")
			}
			t.Logf("rot events %d rotted bytes %d fetched %d slowest repair %v converged %v after quiet",
				w.rotEvents, w.rotBytes, moved, w.maxRepair, w.convergeAt.Sub(w.quietAt))
		})
	})
	t.Run("without damage no chunk bytes move", func(t *testing.T) {
		simtest.Sweep(t, func(t *testing.T, seed int64) {
			s := simtest.RequireDeterministic(t, seed, cleanRun)
			w := take(s)
			if w.convergeAt == 0 {
				t.Fatal("did not converge")
			}
			if n := w.fetchedBytes(); n != w.settledFetched {
				t.Fatalf("fetched %d bytes after the writes settled (%d before) with no damage", n-w.settledFetched, w.settledFetched)
			}
		})
	})
}


```

## TestCanarySkippedBranch

Mentioned in: book/chapters/04-merkle-anti-entropy.md

internal/merkle/canary_test.go:21

```go

func TestCanarySkippedBranch(t *testing.T) {
	local := map[string]merkle.Hash{}
	remote := map[string]merkle.Hash{}
	for i, k := range keysInLeaf(t, 1, 15, 8) {
		remote[k] = sum(fmt.Sprint(i))
	}
	for i := 0; i < 50; i++ {
		k := fmt.Sprintf("shared-%d", i)
		local[k], remote[k] = sum(k), sum(k)
	}
	compare := func() error {
		res, err := merkle.Compare(treeOf(t, 2, local), treeOf(t, 2, remote), merkle.Limits{})
		if err != nil {
			return err
		}
		return checkComplete(local, remote, res)
	}
	if err := compare(); err != nil {
		t.Fatalf("the completeness check fails without the planted bug: %v", err)
	}
	t.Run("the completeness check catches the skipped branch", func(t *testing.T) {
		defer merkle.SetSkipBranch(true)()
		if err := compare(); err == nil {
			t.Fatal("a diff that skips branch f passed the completeness check")
		}
	})
	for _, seed := range canarySeeds {
		s, err := simtest.CheckDeterministic(seed, canaryRun)
		if w := take(s); err != nil || w.convergeAt == 0 {
			t.Fatalf("seed %d: the scenario does not converge without the planted bug: %v", seed, err)
		}
	}
	t.Run("the missed-write scenario fails to converge", func(t *testing.T) {
		defer merkle.SetSkipBranch(true)()
		for _, seed := range canarySeeds {
			s, err := simtest.CheckDeterministic(seed, canaryRun)
			w := take(s)
			if !errors.Is(err, sim.ErrDeadline) || w.convergeAt != 0 {
				t.Fatalf("seed %d: want a convergence deadline stall, got err=%v convergeAt=%v", seed, err, w.convergeAt)
			}
		}
	})
}


```

## TestLyingPeerDST

Mentioned in: book/chapters/04-merkle-anti-entropy.md, book/chapters/08-end-to-end-and-hardening.md, book/front/topic-map.md

internal/merkle/dst_test.go:605

```go

func TestLyingPeerDST(t *testing.T) {
	t.Run("honest replicas converge beside a liar and never store its bytes", func(t *testing.T) {
		var seeds int
		var sweepInvalid uint64
		simtest.Sweep(t, func(t *testing.T, seed int64) {
			s := simtest.RequireDeterministic(t, seed, lyingRun)
			w := take(s)
			if w.convergeAt == 0 {
				t.Fatalf("honest replicas did not converge within %v", LyingConvergeBound)
			}
			var failed, invalid uint64
			for _, st := range w.stats() {
				if st.MaxRoundTrips > uint64(w.depth+1) {
					t.Fatalf("a round took %d round trips at depth %d", st.MaxRoundTrips, w.depth)
				}
				failed += st.Failed
				invalid += st.Invalid
			}
			l, ok := s.Process(w.ids[3]).(*liar)
			if !ok || l.n == 0 || failed == 0 {
				t.Fatalf("the liar was never engaged: contacted %v failed %d", ok && l.n > 0, failed)
			}
			seeds++
			sweepInvalid += invalid
		})
		if seeds >= lyingSweepFloor && sweepInvalid == 0 {
			t.Fatalf("no fabricated entry was refused across %d seeds", seeds)
		}
	})
	t.Run("a lone liar cannot plant bytes", func(t *testing.T) {
		simtest.Sweep(t, func(t *testing.T, seed int64) {
			s := simtest.RequireDeterministic(t, seed, loneLiarRun)
			take(s)
			st := storeOf(s, s.Nodes()[0])
			stats := st.w.Stats()
			if stats.Rejected == 0 || !errors.Is(st.w.LastErr(), merkle.ErrRejected) {
				t.Fatalf("no fetched bytes were rejected: %+v, last error %v", stats, st.w.LastErr())
			}
			if stats.Repaired != 0 || len(s.Disk(s.Nodes()[0]).Names()) != 0 {
				t.Fatalf("the liar planted %d chunks", stats.Repaired)
			}
			if stats.MaxRoundTrips > uint64(merkle.DepthFor(dstChunks)+1) {
				t.Fatalf("a round took %d round trips", stats.MaxRoundTrips)
			}
		})
	})
}


```

## TestMeasureDiffCost

Mentioned in: book/chapters/04-merkle-anti-entropy.md

internal/merkle/canary_test.go:83

```go

func TestMeasureDiffCost(t *testing.T) {
	if os.Getenv(measureEnv) == "" {
		t.Skip("set " + measureEnv + "=1 to print the cost table")
	}
	for _, n := range []int{1_000, 10_000, 100_000} {
		depth := merkle.DepthFor(n)
		entries := make([]merkle.Entry, 0, n)
		for i := 0; i < n; i++ {
			k := fmt.Sprintf("object-%07d", i)
			entries = append(entries, merkle.Entry{Key: k, Hash: sum(k)})
		}
		base, err := merkle.Build(depth, entries)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range []int{1, 10, 100} {
			other := base.Snapshot()
			for i := 0; i < d; i++ {
				k := fmt.Sprintf("object-%07d", (i*7919)%n)
				switch i % 3 {
				case 0:
					err = other.Set(k, sum("changed"+k))
				case 1:
					other.Delete(k)
				default:
					err = other.Set(fmt.Sprintf("new-%d", i), sum(k))
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			start := time.Now()
			res, err := merkle.Compare(base, other, merkle.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			el := time.Since(start)
			fmt.Printf("MEASURE N=%d depth=%d d=%d found=%d roundtrips=%d hashes=%d bound16d(depth+1)=%d exactbound=%d leafload=%d bytes=%d+%d time=%v\n",
				n, depth, d, len(res.Diffs), res.Cost.RoundTrips, res.Cost.Hashes, 16*d*(depth+1), costBound(d, depth, other.LeafLoad()),
				other.LeafLoad(), res.Cost.BytesSent, res.Cost.BytesReceived, el)
		}
	}
}


```

## TestMissedWriteRepairDST

Mentioned in: book/chapters/04-merkle-anti-entropy.md

internal/merkle/dst_test.go:463

```go

func TestMissedWriteRepairDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		s := simtest.RequireDeterministic(t, seed, missedWriteRun)
		w := take(s)
		if w.convergeAt == 0 {
			t.Fatalf("not converged within %v of heal", MissedWriteBound)
		}
		var repaired, replErrs uint64
		for _, st := range w.stats() {
			repaired += st.Repaired
		}
		for _, id := range w.honest {
			replErrs += storeOf(s, id).replErrs
		}
		if replErrs == 0 || repaired == 0 {
			t.Fatalf("the partition dropped %d replica writes and anti-entropy repaired %d", replErrs, repaired)
		}
		t.Logf("dropped %d repaired %d converged %v after heal", replErrs, repaired, w.convergeAt.Sub(w.quietAt))
	})
}


```

## TestRepairUnderLossDST

Mentioned in: book/chapters/04-merkle-anti-entropy.md

internal/merkle/dst_test.go:512

```go

func TestRepairUnderLossDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		s := simtest.RequireDeterministic(t, seed, lossRun)
		w := take(s)
		if w.convergeAt == 0 {
			t.Fatalf("not converged within %v under %d ppm loss", LossConvergeBound, LossPPM)
		}
		var failed, completed uint64
		for _, st := range w.stats() {
			inflight := uint64(0)
			if st.InFlight {
				inflight = 1
			}
			if st.Rounds != st.Completed+st.Failed+inflight {
				t.Fatalf("rounds %d != completed %d + failed %d + in flight %d", st.Rounds, st.Completed, st.Failed, inflight)
			}
			failed += st.Failed
			completed += st.Completed
		}
		if failed == 0 || completed == 0 {
			t.Fatalf("failed %d completed %d: loss did not bite", failed, completed)
		}
		t.Logf("rounds failed %d completed %d converged %v after the last write", failed, completed, w.convergeAt.Sub(w.quietAt))
	})
}


```

## TestScrubRefillsAWipedDiskDST

Mentioned in: book/chapters/04-merkle-anti-entropy.md

internal/merkle/worker_test.go:359

```go

func TestScrubRefillsAWipedDiskDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		s := simtest.RequireDeterministic(t, seed, scrubRun)
		w := take(s)
		if w.convergeAt == 0 {
			t.Fatalf("wiped replica not refilled within %v", refillBound)
		}
		victim := storeOf(s, w.ids[0]).w.Stats()
		if victim.Dropped < uint64(len(w.keys)) || victim.Repaired < uint64(len(w.keys)) {
			t.Fatalf("victim dropped %d repaired %d of %d", victim.Dropped, victim.Repaired, len(w.keys))
		}
		if storeOf(s, w.ids[1]).w.Stats().ReadErrors == 0 {
			t.Fatal("injected read errors were not seen by the scrub")
		}
		t.Logf("refilled %d chunks in %v", victim.Repaired, w.convergeAt.Sub(wipeAt))
	})
}


```

## BenchmarkMergeDoc

Mentioned in: book/chapters/05-optimistic-concurrency-control.md

internal/occ/bench_test.go:30

```go

func BenchmarkMergeDoc(b *testing.B) {
	for _, n := range []int{1_000, 10_000} {
		b.Run(fmt.Sprintf("records=%d", n), func(b *testing.B) {
			x, y := benchDocs(b, n)
			b.ResetTimer()
			for range b.N {
				z := x.Clone()
				z.Merge(y)
			}
		})
		b.Run(fmt.Sprintf("store merge with discards records=%d", n), func(b *testing.B) {
			x, y := benchDocs(b, n)
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				cfg := storeConfig("z", 1, 3, &fakeClock{now: 1_000_000})
				cfg.Doc = x.Clone()
				s, err := occ.NewStore(cfg)
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if _, err := s.Merge(y); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("encode+decode records=%d", n), func(b *testing.B) {
			x, y := benchDocs(b, n)
			x.Merge(y)
			b.ReportMetric(float64(len(x.Encode())), "bytes")
			b.ResetTimer()
			for range b.N {
				if _, err := occ.Decode(x.Clone().Encode()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}


```

## FuzzOCCConditionalWrite

Mentioned in: book/chapters/05-optimistic-concurrency-control.md, book/front/topic-map.md

internal/occ/prop_test.go:252

```go

func FuzzOCCConditionalWrite(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(rapid.MakeFuzz(propEveryLossRecorded))
}

// TestPropCheckAndSetModel proves single-replica conditional Put and Delete
// against a reference model: operations succeed iff the precondition holds,
// every new version gets a distinct ETag, and no discards occur.

```

## FuzzOCCDecode

Mentioned in: book/chapters/05-optimistic-concurrency-control.md

internal/occ/encoding_test.go:408

```go

func FuzzOCCDecode(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("OCC\x02"))
	f.Add([]byte("OCC\x02\x00"))
	f.Add(goldenDoc().Encode())
	for _, h := range hostileDocs() {
		f.Add(h.Encode())
	}
	f.Add(occ.EncodeStateBlob(3, 1, 0, goldenDoc().Encode()))
	f.Add(occ.EncodeOpRequest(occ.OpPut, "k", occ.IfMatch(occ.ETag{1}), []byte("v")))
	honest := goldenDoc()
	hostile := hostileDocs()[0]
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			return
		}
		_, _ = occ.DecodeStateBlob(data)
		_, _, _ = occ.ParseKey(string(data))
		_, _ = occ.DecodeUsage(data)
		_, _ = occ.DecodeObjectMeta(data)
		_, _ = occ.DecodeResult(data)
		d, work, err := occ.DecodeWork(data)
		if work > 4*len(data)+8 {
			t.Fatalf("decode work %d for %d bytes", work, len(data))
		}
		if err != nil {
			if !errors.Is(err, occ.ErrMalformed) || d != nil {
				t.Fatalf("decode error %v is not ErrMalformed", err)
			}
			return
		}
		if !bytes.Equal(d.Encode(), data) {
			t.Fatal("encode(decode(b)) != b")
		}
		again, err := occ.Decode(d.Encode())
		if err != nil || !again.Equal(d) {
			t.Fatalf("decode(encode(d)) != d: %v", err)
		}
		if err := checkLaws(d, honest, d); err != nil {
			t.Fatal(err)
		}
		if err := checkLaws(honest, d, hostile); err != nil {
			t.Fatal(err)
		}
		s, _ := newTestStore(t, "a", 1, 1)
		mustPut(t, s, "k", "mine", occ.IfNoneMatch())
		if _, err := s.Merge(d); err != nil {
			t.Fatal(err)
		}
	})
}


```

## FuzzOCCMergeLaws

Mentioned in: book/chapters/05-optimistic-concurrency-control.md

internal/occ/prop_test.go:414

```go

func FuzzOCCMergeLaws(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(rapid.MakeFuzz(propMergeLaws))
}

// TestPropConvergence proves that delivering full states and single-key deltas
// in any permuted, duplicated order converges to byte-identical docs and ETags.

```

## TestConditionalWriteRaceDST

Mentioned in: book/chapters/05-optimistic-concurrency-control.md

internal/occ/dst_test.go:713

```go

func TestConditionalWriteRaceDST(t *testing.T) {
	t.Run("two writers on one node: exactly one wins", func(t *testing.T) {
		simtest.Sweep(t, func(t *testing.T, seed int64) {
			simtest.RequireDeterministic(t, seed, raceRun(false))
		})
	})
	t.Run("two writers across a partition: both acknowledged, one survives, the other's discard is recorded", func(t *testing.T) {
		simtest.Sweep(t, func(t *testing.T, seed int64) {
			simtest.RequireDeterministic(t, seed, raceRun(true))
		})
	})
}


```

## TestDiscardGapNeverClaimed

Mentioned in: book/chapters/05-optimistic-concurrency-control.md

internal/occ/lww2_test.go:20

```go

func TestDiscardGapNeverClaimed(t *testing.T) {
	for _, secondWriter := range []string{"a", "b"} {
		t.Run("next2 written by "+secondWriter, func(t *testing.T) {
			a, ca, la := newLoggedStore(t, "a", 1, 1)
			b, cb, _ := newLoggedStore(t, "b", 1, 2)
			c, _, lc := newLoggedStore(t, "c", 1, 3)
			mustPut(t, a, "k", "one", occ.IfNoneMatch())
			mustMerge(t, b, a.Doc())
			ca.now += 10
			cb.now += 20
			lost := mustPut(t, a, "k", "lost", occ.IfMatch(a.Record("k").ETag()))
			mustMerge(t, c, a.Doc())
			mustPut(t, b, "k", "winner", occ.IfMatch(b.Record("k").ETag()))
			mustMerge(t, a, b.Doc())
			if len(la.discards) != 1 || la.discards[0].Loser.ID() != lost.ID() {
				t.Fatal("setup: a must discard its own write")
			}
			ca.now += 100
			next1 := mustPut(t, a, "k", "next1", occ.IfMatch(a.Record("k").ETag()))
			w := a
			if secondWriter == "b" {
				mustMerge(t, b, a.Doc())
				cb.now += 1000
				w = b
			} else {
				ca.now += 100
			}
			next2 := mustPut(t, w, "k", "next2", occ.IfMatch(w.Record("k").ETag()))
			for _, v := range []occ.Version{next1, next2} {
				if v.Supersedes(lost) {
					t.Fatalf("%s claims the discarded dot %s:%d it never saw: clock %s", value(occ.NewRecord(v)), lost.Dot().Actor, lost.Dot().Counter, v.Clock())
				}
			}
			mustMerge(t, c, w.Doc())
			if c.LWWDiscards() != 1 || len(lc.discards) != 1 || lc.discards[0].Loser.ID() != lost.ID() || len(lc.lines) != 1 {
				t.Fatalf("c held the lost version and replaced it with next2 that never saw it: discards %d, hooks %d, lines %d", c.LWWDiscards(), len(lc.discards), len(lc.lines))
			}
		})
	}
}

// TestDotsAreNeverReused proves that a discarded write's dot is never reissued
// or falsely claimed across six follow-up writes by two nodes, and a replica
// holding the lost version records a discard on replacing it after each write.

```

## TestDotsAreNeverReused

Mentioned in: book/chapters/05-optimistic-concurrency-control.md

internal/occ/lww2_test.go:64

```go

func TestDotsAreNeverReused(t *testing.T) {
	a, ca, la := newLoggedStore(t, "a", 1, 1)
	b, cb, _ := newLoggedStore(t, "b", 1, 2)
	c, _, _ := newLoggedStore(t, "c", 1, 3)
	dots := map[occ.Dot]occ.VersionID{}
	note := func(v occ.Version) {
		t.Helper()
		if id, ok := dots[v.Dot()]; ok && id != v.ID() {
			t.Fatalf("dot %s:%d issued twice", v.Dot().Actor, v.Dot().Counter)
		}
		dots[v.Dot()] = v.ID()
	}
	note(mustPut(t, a, "k", "one", occ.IfNoneMatch()))
	mustMerge(t, b, a.Doc())
	ca.now += 10
	cb.now += 20
	lost := mustPut(t, a, "k", "lost", occ.IfMatch(a.Record("k").ETag()))
	note(lost)
	mustMerge(t, c, a.Doc())
	holding := c.Doc()
	note(mustPut(t, b, "k", "winner", occ.IfMatch(b.Record("k").ETag())))
	mustMerge(t, a, b.Doc())
	if len(la.discards) != 1 || la.discards[0].Loser.ID() != lost.ID() {
		t.Fatal("setup: a must discard its own write")
	}
	for i := range 6 {
		w, clk := a, ca
		if i%3 == 2 {
			mustMerge(t, b, a.Doc())
			w, clk = b, cb
		}
		clk.now += 100
		next := mustPut(t, w, "k", fmt.Sprintf("next%d", i+1), occ.IfMatch(w.Record("k").ETag()))
		note(next)
		if w == b {
			mustMerge(t, a, b.Doc())
		}
		if next.Supersedes(lost) {
			t.Fatalf("write %d claims the discarded dot: clock %s", i+1, next.Clock())
		}
		fresh, _, lf := newLoggedStore(t, "c", 1, 3)
		mustMerge(t, fresh, holding)
		mustMerge(t, fresh, w.Doc())
		if len(lf.discards) != 1 || lf.discards[0].Loser.ID() != lost.ID() {
			t.Fatalf("after write %d a node that held the lost version replaced it with no record", i+1)
		}
	}
}

// TestIncomingConcurrentLoserIsRecorded proves that rejecting an incoming
// concurrent loser records it once (deduplicating re-sends), ignores causal
// old news, and leaves no trace on Persist failure.

```

## TestHostileMessagesBoundedWork

Mentioned in: book/chapters/05-optimistic-concurrency-control.md

internal/occ/encoding_test.go:286

```go

func TestHostileMessagesBoundedWork(t *testing.T) {
	limit := sim.DefaultMaxMessage - 1
	if raceBuild() {
		limit = 1 << 20
	}
	perKey := len(manyKeys(2, 0)) - len(manyKeys(1, 0))
	perEntry := len(bigContexts(2, 1, 1, 0)) - len(bigContexts(1, 1, 1, 0))
	shapes := []struct {
		name       string
		held, push func(scale int) []byte
	}{
		{"many keys", func(scale int) []byte { return manyKeys(fitCount(limit/scale, perKey), 1) },
			func(scale int) []byte { return manyKeys(fitCount(limit/scale, perKey), 2) }},
		{"big contexts", func(scale int) []byte { return bigContexts(fitCount(limit/scale/4, perEntry), 4, 1, 1) },
			func(scale int) []byte { return bigContexts(fitCount(limit/scale/4, perEntry), 4, 1, 2) }},
	}
	for _, sh := range shapes {
		t.Run(sh.name, func(t *testing.T) {
			var perByte [2]float64
			for i, scale := range []int{8, 1} {
				held, push := sh.held(scale), sh.push(scale)
				if scale == 1 && (len(push) > sim.DefaultMaxMessage || len(push) < limit*9/10) {
					t.Fatalf("the hostile message is %d bytes, want close to %d", len(push), limit)
				}
				hd, hwork, err := occ.DecodeWork(held)
				if err != nil {
					t.Fatal(err)
				}
				pd, pwork, err := occ.DecodeWork(push)
				if err != nil {
					t.Fatal(err)
				}
				if hwork > 4*len(held) || pwork > 4*len(push) {
					t.Fatalf("decode work %d / %d for %d / %d bytes is not linear", hwork, pwork, len(held), len(push))
				}
				c := &fakeClock{now: 1_000_000}
				cfg := storeConfig("a", 1, 1, c)
				discards := 0
				cfg.OnDiscard = func(string, occ.Version, occ.Version) { discards++ }
				s, err := occ.NewStore(cfg)
				if err != nil {
					t.Fatal(err)
				}
				mustMerge(t, s, hd)
				w0 := s.Work()
				mustMerge(t, s, pd)
				merge := s.Work() - w0
				if merge > len(held)+len(push) {
					t.Fatalf("merge work %d for %d + %d bytes is not linear", merge, len(held), len(push))
				}
				if discards != pd.Len() || s.LWWDiscards() != uint64(pd.Len()) || !s.Doc().Equal(pd) {
					t.Fatalf("every forged replacement must be decided and recorded: %d of %d", discards, pd.Len())
				}
				perByte[i] = float64(pwork+merge) / float64(len(push))
				t.Logf("MEASURE %s scale 1/%d: %d bytes, decode work %d, merge work %d", sh.name, scale, len(push), pwork, merge)
			}
			if perByte[1] > 1.5*perByte[0] {
				t.Fatalf("work per byte grew from %.2f to %.2f with message size", perByte[0], perByte[1])
			}
		})
	}
}


```

## TestMultiRegionConvergenceDST

Mentioned in: book/chapters/05-optimistic-concurrency-control.md, book/front/topic-map.md

internal/occ/dst_test.go:476

```go

func TestMultiRegionConvergenceDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		simtest.RequireDeterministic(t, seed, multiRegionRun)
		if lastWorld.acks == 0 {
			t.Fatal("the workload acknowledged nothing")
		}
		logWorld(t, lastWorld)
	})
}


```

## TestOfflineRegionDST

Mentioned in: book/chapters/05-optimistic-concurrency-control.md, book/front/topic-map.md

internal/occ/dst_test.go:563

```go

func TestOfflineRegionDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		simtest.RequireDeterministic(t, seed, offlineRun)
		logWorld(t, lastWorld)
	})
}

type raceClient struct {
	e    env.Env
	c    *occ.Client
	etag occ.ETag
	res  occ.Result
	err  error
	done bool
}


```

## TestPropClassificationMatchesOracle

Mentioned in: book/chapters/05-optimistic-concurrency-control.md

internal/occ/lww2_prop_test.go:128

```go

func TestPropClassificationMatchesOracle(t *testing.T) {
	rapid.Check(t, propClassificationMatchesOracle)
}


```

## TestQuotaOvershootUnderPartitionDST

Mentioned in: book/chapters/05-optimistic-concurrency-control.md, book/front/topic-map.md

internal/occ/dst_test.go:841

```go

func TestQuotaOvershootUnderPartitionDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		simtest.RequireDeterministic(t, seed, quotaRun)
		logWorld(t, lastWorld)
	})
}


```

## TestWriterLostAfterDeliveryDST

Mentioned in: book/chapters/05-optimistic-concurrency-control.md

internal/occ/lww2_dst_test.go:80

```go

func TestWriterLostAfterDeliveryDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		simtest.RequireDeterministic(t, seed, writerLossRun)
		if lastWorld.tally["discarded"] == 0 {
			t.Fatal("no delivered version lost to a concurrent write: the scenario did not exercise the hole")
		}
		logWorld(t, lastWorld)
	})
}

// TestCanaryIncomingLoserSilentDST plants the bug of silently dropping rejected
// incoming concurrent losers and proves TestWriterLostAfterDeliveryDST fails.

```

## FuzzSagaLogRecover

Mentioned in: book/chapters/06-sagas.md, book/front/topic-map.md

internal/saga/prop_test.go:531

```go

func FuzzSagaLogRecover(f *testing.F) {
	recs, blob := sampleLog(f)
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add(blob)
	f.Add(blob[:len(blob)-1])
	f.Add(blob[:7])
	bad := slices.Clone(blob)
	bad[20] ^= 4
	f.Add(bad)
	f.Add(encodeLog(f, recs[:4]...))
	f.Add(encodeLog(f, saga.Record{Kind: saga.RecBegin, Def: "other"}))
	f.Add(encodeLog(f, begin(), done(0, 1)))
	f.Add(append(encodeLog(f, begin()), 0xA5, 0, 0, 0, 1))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 64<<10 {
			return
		}
		recs, valid, err := saga.DecodeLog(b)
		if valid < 0 || valid > len(b) {
			t.Fatalf("valid %d of %d", valid, len(b))
		}
		if err != nil && !errors.Is(err, saga.ErrLogCorrupt) {
			t.Fatalf("untyped error %v", err)
		}
		var re []byte
		for _, r := range recs {
			var eerr error
			re, eerr = saga.AppendRecord(re, r)
			if eerr != nil {
				t.Fatalf("decoded record does not encode: %v", eerr)
			}
		}
		if !bytes.Equal(re, b[:valid]) {
			t.Fatalf("records re-encode to %d bytes, valid prefix is %d", len(re), valid)
		}
		if err == nil {
			again, v2, err2 := saga.DecodeLog(b[:valid])
			if err2 != nil || v2 != valid || len(again) != len(recs) {
				t.Fatalf("valid prefix does not decode alone: %v", err2)
			}
		}
		if _, rerr := saga.Resume(recs, 3, 2, 3); rerr != nil && !errors.Is(rerr, saga.ErrLogCorrupt) {
			t.Fatalf("Resume: untyped error %v", rerr)
		}
		d := newMemDisk()
		name := saga.DefaultPrefix + "f"
		d.put(name, b)
		clk := &manualClock{}
		e := memEnv(d, clk)
		eng, nerr := saga.New(e, saga.Config{Policy: testPolicy()}, newFx(3, 2).def(e))
		if nerr != nil {
			t.Fatal(nerr)
		}
		rerr := eng.Recover()
		if rerr != nil && !errors.Is(rerr, saga.ErrLogCorrupt) && !errors.Is(rerr, env.ErrInvalidArgument) {
			t.Fatalf("Recover: untyped error %v", rerr)
		}
		for i := 0; i < 1000 && clk.fireNext(); i++ {
		}
		after, present := d.blobs[name]
		switch {
		case err != nil || rerr != nil:
			if !present || !bytes.Equal(after, b) {
				t.Fatalf("a refused log was changed")
			}
		case len(recs) == 0:
			if present {
				t.Fatalf("a log with no whole record was kept")
			}
		default:
			if !present || !bytes.HasPrefix(after, b[:valid]) {
				t.Fatalf("recovery did not keep the valid prefix")
			}
			final, _, ferr := saga.DecodeLog(after)
			if ferr != nil {
				t.Fatalf("recovered log is corrupt: %v", ferr)
			}
			snap, serr := saga.Resume(final, 3, 2, testPolicy().MaxAttempts)
			if serr != nil || !snap.State.Terminal() {
				t.Fatalf("recovered saga did not finish: %+v %v", snap, serr)
			}
		}
	})
}


```

## TestCanarySkippedCompensation

Mentioned in: book/chapters/06-sagas.md

internal/saga/dst_test.go:105

```go

func TestCanarySkippedCompensation(t *testing.T) {
	h := sagasim.ModelHarness()
	cell := sagasim.Cell{Step: saga.MigrationPivot, Kind: sagasim.FailBeforeAction}
	if cell.Want() != saga.StateCompensated {
		t.Fatalf("canary cell %s does not compensate", cell.Name())
	}
	plant := func(cfg *saga.Config) { saga.PlantSkippedCompensation(cfg, 1) }
	for _, step := range []int{0, 2} {
		other := func(cfg *saga.Config) { saga.PlantSkippedCompensation(cfg, step) }
		_, err := simtest.CheckDeterministic(1, sagasim.Run(h, cell, sagasim.RunOptions{Configure: other}))
		if step == 0 && !errors.Is(err, sagasim.ErrCheck) {
			t.Fatalf("skipping the allocate compensation went unnoticed: %v", err)
		}
		if step == 2 && err != nil {
			t.Fatalf("skipping the verify compensation, which undoes nothing, failed: %v", err)
		}
	}
	for _, seed := range []int64{1, 2, 3} {
		if _, err := simtest.CheckDeterministic(seed, sagasim.Run(h, cell, sagasim.RunOptions{})); err != nil {
			t.Fatalf("seed %d: the clean cell fails: %v", seed, err)
		}
		_, err := simtest.CheckDeterministic(seed, sagasim.Run(h, cell, sagasim.RunOptions{Configure: plant}))
		if err == nil {
			t.Fatalf("seed %d: skipping the copy compensation went unnoticed", seed)
		}
		if !errors.Is(err, sagasim.ErrCheck) {
			t.Fatalf("seed %d: failed for another reason: %v", seed, err)
		}
	}
}

// TestMigrationUnderNemesisDST proves that chained bucket migrations under
// network, pause, crash, and read-error chaos converge after healing with the
// bucket in exactly one location, more than 32 objects stored, and zero lost
// acknowledged writes.

```

## TestDuelingMigrationsDST

Mentioned in: book/chapters/06-sagas.md, book/front/topic-map.md

internal/saga/dst_test.go:165

```go

func TestDuelingMigrationsDST(t *testing.T) {
	p := sagasim.DuelParams{Converge: duelConverge, MaxStagger: duelMaxStagger, MinPartition: duelMinPart, MaxPartition: duelMaxPart}
	winners := map[string]int{}
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		res := &sagasim.DuelReport{}
		simtest.RequireDeterministic(t, seed, sagasim.DuelRun(p, res))
		winners[res.Pointer]++
		if after := res.Finished.Sub(res.Healed); after > duelAfterHeal {
			t.Fatalf("finished %v after the heal, bound %v", after, duelAfterHeal)
		}
		t.Logf("DUEL seed=%d pointer=%s states=%v outcomes=%q afterheal=%v", seed, res.Pointer, res.States, res.Outcomes, res.Finished.Sub(res.Healed))
	})
	t.Logf("DUEL winners %v", winners)
}

// TestModelClusterReportsBadInput proves that invalid migration inputs
// (self-move, missing bucket, unknown target) compensate cleanly without
// disturbing the existing bucket.

```

## TestMatrixCellsCoverEveryFailurePoint

Mentioned in: book/chapters/06-sagas.md

internal/saga/dst_test.go:68

```go

func TestMatrixCellsCoverEveryFailurePoint(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range sagasim.Cells() {
		if seen[c.Name()] {
			t.Fatalf("duplicate cell %s", c.Name())
		}
		seen[c.Name()] = true
	}
	for step := 0; step < sagasim.MigrationSteps; step++ {
		for _, k := range []sagasim.FailureKind{sagasim.FailBeforeAction, sagasim.FailAfterAction, sagasim.CrashBeforeAction, sagasim.CrashAfterAction} {
			for _, torn := range []bool{false, true} {
				c := sagasim.Cell{Step: step, Kind: k, Torn: torn}
				if !seen[c.Name()] {
					t.Fatalf("missing cell %s", c.Name())
				}
			}
		}
		if step < saga.MigrationPivot {
			for _, k := range []sagasim.FailureKind{sagasim.FailMidCompensation, sagasim.CrashMidCompensation} {
				c := sagasim.Cell{Step: step, Kind: k, Torn: true}
				if !seen[c.Name()] {
					t.Fatalf("missing cell %s", c.Name())
				}
			}
		}
	}
	for _, k := range []sagasim.FailureKind{0, sagasim.FailBeforeAction, sagasim.CrashMidCompensation, 99} {
		if k.String() == "" {
			t.Fatalf("kind %d has no name", k)
		}
	}
}

// TestCanarySkippedCompensation plants saga.PlantSkippedCompensation to skip
// step 1 (copy) or step 0 (allocate) compensation without calling Compensate,
// proving the matrix checks catch the leaked orphans or claim with ErrCheck
// while skipping verify's no-op compensation passes.

```

## TestMigrationFailurePointsDST

Mentioned in: book/chapters/06-sagas.md, book/front/topic-map.md

internal/saga/dst_test.go:41

```go

func TestMigrationFailurePointsDST(t *testing.T) {
	h := sagasim.ModelHarness()
	cells := sagasim.Cells()
	if len(cells) != sagasim.MatrixCells {
		t.Fatalf("%d cells, want %d", len(cells), sagasim.MatrixCells)
	}
	for _, c := range cells {
		t.Run(c.Name(), func(t *testing.T) {
			t.Parallel()
			simtest.Sweep(t, func(t *testing.T, seed int64) {
				rep := &sagasim.Report{}
				simtest.RequireDeterministic(t, seed, sagasim.Run(h, c, sagasim.RunOptions{Report: rep}))
				if rep.State != c.Want() {
					t.Fatalf("terminal state %v, want %v", rep.State, c.Want())
				}
				if rep.Elapsed > sagasim.MatrixBound {
					t.Fatalf("took %v, bound %v", rep.Elapsed, sagasim.MatrixBound)
				}
				t.Logf("MATRIX %s seed=%d state=%v elapsed=%v crashes=%d torn=%d calls=%v", c.Name(), seed, rep.State, rep.Elapsed, rep.Crashes, rep.Torn, rep.Calls)
			})
		})
	}
}

// TestMatrixCellsCoverEveryFailurePoint proves that sagasim.Cells has no
// duplicates and includes every step and failure-kind combination (with
// mid-compensation cells restricted to pre-pivot steps).

```

## TestMigrationUnderNemesisDST

Mentioned in: book/chapters/06-sagas.md, book/front/topic-map.md

internal/saga/dst_test.go:140

```go

func TestMigrationUnderNemesisDST(t *testing.T) {
	p := sagasim.NemesisParams{
		Start: nemesisStart, Phases: nemesisPhases, Chaos: nemesisChaos, Heal: nemesisHeal, MaxGap: nemesisMaxGap,
		Converge: nemesisConverge, Writes: nemesisWrites, WriteGap: nemesisWriteGap, Tick: nemesisTick,
		Objects: nemesisObjects,
	}
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		res := &sagasim.NemesisReport{}
		simtest.RequireDeterministic(t, seed, sagasim.NemesisRun(p, res))
		if res.Sagas < nemesisMinSagas {
			t.Fatalf("only %d migrations ran under the nemesis", res.Sagas)
		}
		if res.Stored <= oldObjectCliff {
			t.Fatalf("the bucket ended with %d objects, not above the old %d-object limit", res.Stored, oldObjectCliff)
		}
		if res.Settled > nemesisConverge {
			t.Fatalf("settled %v after the last heal, bound %v", res.Settled, nemesisConverge)
		}
		t.Logf("NEMESIS seed=%d sagas=%d completed=%d compensated=%d settled=%v acked=%d stored=%d final=%s attempts=%v", seed, res.Sagas, res.Completed, res.Compensated, res.Settled, res.Acked, res.Stored, res.Final, res.Attempts)
	})
}

// TestDuelingMigrationsDST proves that two coordinators migrating the same
// bucket to different targets across a partition converge after healing to one
// live location, an agreed pointer across all replicas, and a cleaned-up loser.

```

## TestPropNoStepAfterTerminal

Mentioned in: book/chapters/06-sagas.md

internal/saga/prop_test.go:325

```go

func TestPropNoStepAfterTerminal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		sc := drawScenario(t)
		seed := rapid.Int64().Draw(t, "seed")
		extra := rapid.IntRange(0, 3).Draw(t, "extraCrashes")
		slowFirst := rapid.Bool().Draw(t, "slowFirst")
		s, f, err := runScenario(seed, sc, func(s *sim.Sim, f *fx) {
			if slowFirst {
				f.latency = propSlow
				s.At(env.Time(propSlow), "speed up", func() { f.latency = stepLatency })
			}
			for j := 0; j < extra; j++ {
				at := env.Time(time.Minute + time.Duration(j)*10*time.Second)
				s.At(at, "late crash", func() { _ = s.CrashTorn(hostID) })
				s.At(at.Add(time.Second), "late restart", func() { _ = s.Restart(hostID) })
			}
		})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(f.afterEnd) != 0 {
			t.Fatalf("calls after the end record was durable: %v", f.afterEnd)
		}
		recs := durableRecords(t, s, "t")
		if len(recs) == 0 {
			return
		}
		snap, err := saga.Resume(recs, sc.n, sc.pivot, propMaxAttempts)
		if err != nil {
			t.Fatalf("log %s: %v", kinds(recs), err)
		}
		if !snap.State.Terminal() {
			t.Fatalf("saga not terminal: %+v", snap)
		}
	})
}


```

## TestPropRecoveryIdempotent

Mentioned in: book/chapters/06-sagas.md

internal/saga/prop_test.go:467

```go

func TestPropRecoveryIdempotent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		steps := rapid.IntRange(1, 4).Draw(t, "steps")
		pivot := rapid.IntRange(0, steps).Draw(t, "pivot")
		recs := genLog(t, steps, pivot, propMaxAttempts)
		var blob []byte
		for _, r := range recs {
			var err error
			blob, err = saga.AppendRecord(blob, r)
			if err != nil {
				t.Fatal(err)
			}
		}
		cut := rapid.IntRange(0, len(blob)).Draw(t, "cut")
		name := saga.DefaultPrefix + "t"
		once := recoverOnce(map[string][]byte{name: blob[:cut]}, steps, pivot)
		if once.err != nil {
			t.Fatalf("recover: %v", once.err)
		}
		twice := recoverOnce(once.disk, steps, pivot)
		if twice.err != nil {
			t.Fatalf("second recover: %v", twice.err)
		}
		if !sameDisk(once.disk, twice.disk) {
			t.Fatalf("recovering twice changed the disk")
		}
		if !sameStatus(once.status, twice.status) || once.first != twice.first || once.acted != twice.acted {
			t.Fatalf("recovering twice differs: %+v %+v / %+v %+v", once.status, once.first, twice.status, twice.first)
		}
		prefix, valid, err := saga.DecodeLog(blob[:cut])
		if err != nil {
			t.Fatal(err)
		}
		got, present := once.disk[name]
		if len(prefix) == 0 {
			if present {
				t.Fatalf("a log with no whole record was kept")
			}
			return
		}
		if !bytes.Equal(got, blob[:valid]) {
			t.Fatalf("recovered blob is not the valid prefix")
		}
		snap, err := saga.Resume(prefix, steps, pivot, propMaxAttempts)
		if err != nil {
			t.Fatal(err)
		}
		wantKind := map[saga.Action]saga.RecordKind{saga.ActDo: saga.RecIntent, saga.ActAbort: saga.RecAbort, saga.ActCompensate: saga.RecCompIntent, saga.ActEnd: saga.RecEnd}
		if snap.Next == saga.ActNone {
			if once.acted {
				t.Fatalf("a terminal saga acted: %+v", once.first)
			}
			return
		}
		if !once.acted || once.first.Kind != wantKind[snap.Next] {
			t.Fatalf("first action %+v, want %v from %+v", once.first, wantKind[snap.Next], snap)
		}
	})
}

// FuzzSagaLogRecover proves that arbitrary byte slices never panic DecodeLog,
// Resume, or Engine.Recover: every input either returns ErrLogCorrupt/
// ErrInvalidArgument without modifying disk, deletes an empty/torn-only log, or
// preserves the valid prefix and finishes cleanly.

```

## TestPropTerminalState

Mentioned in: book/chapters/06-sagas.md

internal/saga/prop_test.go:299

```go

func TestPropTerminalState(t *testing.T) {
	var exhausted, postPerm int
	rapid.Check(t, func(t *rapid.T) {
		sc := drawScenario(t)
		seed := rapid.Int64().Draw(t, "seed")
		s, f, err := runScenario(seed, sc, nil)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		checkTerminal(t, s, f, sc)
		for i := 0; i < sc.n; i++ {
			if sc.exhausts(i) && f.doCalls[i] > 0 {
				exhausted++
			}
			if sc.postPerm[i] > 0 && f.doCalls[i] > sc.postPerm[i] {
				postPerm++
			}
		}
	})
	if exhausted == 0 || postPerm == 0 {
		t.Fatalf("the draws never reached retry exhaustion (%d) or a post-pivot ErrPermanent (%d)", exhausted, postPerm)
	}
}

// TestPropNoStepAfterTerminal proves that no forward step or compensation runs
// once RecEnd is durable, including under slow callbacks and late torn crashes.

```

## FuzzFrame

Mentioned in: book/chapters/07-object-store-and-sync-engine.md

internal/noderuntime/frame_test.go:121

```go

func FuzzFrame(f *testing.F) {
	good, _ := encodeRequest(requestFrame{ID: 3, From: "n:1", Body: []byte("x")})
	framed, _ := appendFrame(nil, good)
	f.Add(framed)
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 1})
	f.Add(append([]byte{0, 0, 0, 13}, encodeResponse(responseFrame{ID: 1, Code: 0})...))
	f.Add([]byte{1, 3, 'a', 'b', 'c', 9})
	f.Fuzz(func(t *testing.T, b []byte) {
		if p, err := readFrame(bytes.NewReader(b), 1<<16); err == nil {
			if len(p) > 1<<16 || len(b) < frameLenSize+len(p) {
				t.Fatalf("frame of %d bytes from %d input bytes", len(p), len(b))
			}
			if r, err := decodeRequest(p); err == nil {
				again, err := encodeRequest(r)
				if err != nil || !bytes.Equal(again, p) {
					t.Fatalf("request does not re-encode to its bytes: %v", err)
				}
			}
			if r, err := decodeResponse(p); err == nil && len(r.Msg) <= env.MaxErrorMessage {
				if again := encodeResponse(r); !bytes.Equal(again, p) {
					t.Fatalf("response does not re-encode to its bytes")
				}
			}
		} else if !errors.Is(err, node.ErrFrame) && !errors.Is(err, env.ErrTooLarge) {
			t.Fatalf("frame error %v is neither node.ErrFrame nor ErrTooLarge", err)
		}
		if from, body, err := decodeDatagram(b); err == nil {
			again, err := encodeDatagram(from, body)
			if err == nil && !bytes.Equal(again, b) {
				t.Fatalf("datagram does not re-encode to its bytes")
			}
		}
	})
}


```

## FuzzObjectAPI

Mentioned in: book/chapters/07-object-store-and-sync-engine.md, book/front/topic-map.md

internal/storage/httpapi/http_test.go:833

```go

func FuzzObjectAPI(f *testing.F) {
	f.Add([]byte("\x01/b\x00\x00\x00\x01/b/k\x00\x00hello\x00\x00/b/k\x00\x00\x00"))
	f.Add([]byte("\x01/b/k\x00*\x00x\x00"))
	f.Add([]byte{})
	f.Add([]byte("\x05/_migrations\x00\x00{\"bucket\":\"b\",\"to\":\"r\"}\x00"))
	f.Add([]byte("\x00/b?tags\x00\"etag\"\x00\x00\x03/b/k\x00\x00\x00"))
	f.Add([]byte("\x01/b\x00\x00\x00\x01/b/k\x00\x00v1\x00\x01/b/k\x00\x01\"\"\x00v2\x00"))
	f.Add([]byte("\x01/b\x00\x00\x00\x01/b/k\x00\x00v1\x00\x01/b/k\x00\x01   \x00v2\x00\x03/b/k\x00\x01W/\"@\"\x00\x00"))
	f.Add([]byte("\x01/b\x00\x00\x00\x01/b/k\x00\x00v1\x00\x01/b/k\x00\x01\"x\", \"@\"\x00v2\x00\x01/b/k\x00\x02\x00v3\x00"))
	f.Add([]byte("\x01/b\x00\x00\x00\x00\x01/b/k\x00\x00v1\x00\x00\x01/b/k\x00\x01\"a,b\", \"@\"\x00v2\x00\x00\x03/b/k\x00\x01\"a,@\"\x00\x00\x00\x03/b/k\x00\x01\"a,b\",\"@\"\x00\x00"))
	f.Add([]byte("\x01/b\x00\x00\x00\x00\x01/b/k\x00\x00v1\x00\x00\x01/b/k\x00\x01, W/\"a,\" ,,\"@\",\x00v2\x00\x00\x03/b/k\x00\x01\"@\" \"@\"\x00\x00\x00\x01/b/k\x00\x01\"a, \"@\"\x00v3\x00"))
	f.Fuzz(func(t *testing.T, in []byte) {
		m := newModel()
		h := httpapi.NewHandler(m, httpapi.Limits{MaxObjectSize: 4096, MaxSmallBody: 1024})
		puts := map[string][]byte{}
		etags := map[string]string{}
		parts := bytes.Split(in, []byte{0})
		for i := 0; i+3 < len(parts) && i < 4*12; i += 4 {
			if len(parts[i]) == 0 {
				continue
			}
			method := fuzzMethods[int(parts[i][0])%len(fuzzMethods)]
			path := string(parts[i][1:])
			if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, " \r\n\t#") {
				path = "/" + strings.Map(func(r rune) rune {
					if r < 0x21 || r == 0x7f || r == '#' || r == '%' {
						return -1
					}
					return r
				}, path)
			}
			req, err := http.NewRequest(method, "http://h"+path, bytes.NewReader(parts[i+2]))
			if err != nil {
				continue
			}
			key := req.URL.Path
			name, value := "", string(parts[i+1])
			if len(value) > 0 && value[0] < 4 {
				name, value = fuzzHeaders[value[0]], value[1:]
			} else if value != "" {
				name = "If-Match"
				if value == "*" {
					name = "If-None-Match"
				}
			}
			value = strings.ReplaceAll(value, "@", etags[key])
			if got, want := parsed(value), referenceEntityTags(value); !sameTagList(got, want) {
				t.Fatalf("ParseEntityTags(%q) = %+v, the reference tokeniser gives %+v", value, got, want)
			}
			if name != "" && strings.ContainsAny(value, "\r\n\x00") {
				name = ""
			}
			if name != "" {
				req.Header[name] = []string{value}
			}
			cur, present := etags[key]
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if !fuzzStatus[w.Code] {
				t.Fatalf("%s %s: status %d outside the table", method, path, w.Code)
			}
			applied := req.URL.RawQuery == "" && strings.Count(key, "/") == 2 &&
				(method == "PUT" && w.Code == 200 || method == "DELETE" && w.Code == 204)
			if applied && name != "" && !mayApply(name, value, present, cur) {
				t.Fatalf("%s %s with %s: %q applied (status %d) though the precondition cannot hold (exists=%v, etag %s)", method, key, name, value, w.Code, present, cur)
			}
			if method == "PUT" && w.Code == 200 && strings.Count(key, "/") == 2 && req.URL.RawQuery == "" {
				puts[key] = parts[i+2]
				if w.Header().Get("ETag") == "" {
					t.Fatalf("PUT %s: no ETag", key)
				}
				etags[key] = strings.Trim(w.Header().Get("ETag"), `"`)
			}
			if method == "DELETE" && w.Code == 204 {
				delete(puts, key)
				delete(etags, key)
			}
			if method == "GET" && w.Code == 200 && req.URL.RawQuery == "" {
				if want, ok := puts[key]; ok && !bytes.Equal(w.Body.Bytes(), want) {
					t.Fatalf("GET %s returned %q, last acknowledged PUT wrote %q", key, w.Body.Bytes(), want)
				}
			}
		}
	})
}


```

## TestAStaleShardWriteToAWipedHolderOfADeclaredLostVersionIsCollected

Mentioned in: book/chapters/07-object-store-and-sync-engine.md

internal/node/lost_test.go:611

```go

func TestAStaleShardWriteToAWipedHolderOfADeclaredLostVersionIsCollected(t *testing.T) {
	h, _ := lossHarness(t, 10, storage.Layout{K: 2, M: 2, StripeSize: 1024}, 0)
	h.gc.off = true
	w, a := h.ids[9], h.ids[0]
	if err := h.s.Clog(w, a); err != nil {
		t.Fatal(err)
	}
	oi, err := h.syncPut(t, w, storage.PutRequest{Bucket: "g", Key: "k", Data: make([]byte, 700)})
	if err != nil {
		t.Fatal(err)
	}
	man := h.mans[oi.Version]
	if _, ok := holderOf(man, 0, w); ok {
		t.Fatal("the case needs a writer outside the placement")
	}
	name, ok := holderOf(man, 0, a)
	if !ok {
		t.Fatalf("the case needs %s in the placement", a)
	}
	rotted := 0
	for _, ref := range man.Stripes[0].Shards {
		if ref.Node == a || rotted == 2 {
			continue
		}
		n, _ := holderOf(man, 0, ref.Node)
		if err := h.s.BitRot(ref.Node, n); err != nil {
			t.Fatal(err)
		}
		rotted++
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(30*time.Second), func() bool { return len(h.declared) > 0 && h.converged() }); err != nil {
		t.Fatal(err)
	}
	if err := h.s.WipeRejoin(a); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Unclog(w, a); err != nil {
		t.Fatal(err)
	}
	stored := func() bool {
		_, ok := h.s.Disk(a).Peek(name)
		return ok
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(time.Second), stored); err != nil {
		t.Fatalf("the case needs the stale shard write to land on the wiped holder: %v", err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(60*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(h.cfg.GCGrace + 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Err(); err != nil {
		t.Fatal(err)
	}
	for _, id := range h.ids {
		if left := shardsOf(h, id, oi.Version); len(left) > 0 {
			t.Fatalf("%s still holds %v of the declared-lost version after the grace period", id, left)
		}
	}
	for _, n := range h.up() {
		rd, err := n.OCC().Reader()
		if err != nil {
			t.Fatal(err)
		}
		if rd.Record(occ.ManifestKey("g", "k")).Live() {
			t.Fatalf("%s: the declared-lost version is live again", n.ID())
		}
		if !rd.Record(occ.TagKey("g", "k", "~lost/"+oi.Version)).Live() {
			t.Fatalf("%s: the loss record is gone", n.ID())
		}
		if _, _, err := h.syncGet(t, n.ID(), "g", "k"); !isNotFoundErr(err) {
			t.Fatalf("%s: get of the declared-lost object returned %v, want not found", n.ID(), err)
		}
	}
	if len(h.declared) != 1 {
		t.Fatalf("declarations %v, want exactly one", h.declared)
	}
}


```

## TestCanaryDeleteResurrected

Mentioned in: book/chapters/07-object-store-and-sync-engine.md

internal/node/dst_crash_test.go:244

```go

func TestCanaryDeleteResurrected(t *testing.T) {
	node.SetCanaryIgnoreTombstones(true)
	t.Cleanup(func() { node.SetCanaryIgnoreTombstones(false) })
	caught := 0
	for _, seed := range []int64{1, 2, 3} {
		var res crashResult
		s := crashRun(&res)(seed)
		if s.Err() != nil && res.resurrected {
			caught++
		}
	}
	if caught == 0 {
		t.Fatal("with tombstones ignored by anti-entropy, no deleted object came back on any seed")
	}
}

var driftSeeds = []int64{7, 383}

// TestCrashScenarioUsageMatchesARecountOnTheSeedsThatDrifted replays the
// specific crash seeds where a partition-era recount left usage drifted,
// proving auditQuotas restores agreement with live manifests.

```

## TestCanaryNodeSkipsDiscardRecount

Mentioned in: book/chapters/07-object-store-and-sync-engine.md

internal/node/dst_partition_test.go:299

```go

func TestCanaryNodeSkipsDiscardRecount(t *testing.T) {
	node.SetCanaryNoDiscardRecount(true)
	node.SetCanaryNoQuotaAudit(true)
	t.Cleanup(func() { node.SetCanaryNoDiscardRecount(false); node.SetCanaryNoQuotaAudit(false) })
	failed := 0
	for _, seed := range []int64{1, 2, 3} {
		var res partitionResult
		s := partitionRun(&res)(seed)
		if s.Err() != nil && !res.quotaOK {
			failed++
		}
	}
	if failed == 0 {
		t.Fatal("with the recount after a quota discard switched off, the quota check still passed on every seed")
	}
	drift := 0
	for _, seed := range []int64{1, 2} {
		if quotaDriftRun(seed).Err() != nil {
			drift++
		}
	}
	if drift == 0 {
		t.Fatal("with the recount after a discard switched off, the unequal-size overwrite across a partition still left usage right")
	}
}


```

## TestCanaryNodeSkipsQuotaAudit

Mentioned in: book/chapters/07-object-store-and-sync-engine.md

internal/node/dst_crash_test.go:274

```go

func TestCanaryNodeSkipsQuotaAudit(t *testing.T) {
	node.SetCanaryNoQuotaAudit(true)
	t.Cleanup(func() { node.SetCanaryNoQuotaAudit(false) })
	failed := 0
	for _, seed := range driftSeeds {
		var res crashResult
		if crashRun(&res)(seed).Err() != nil {
			failed++
		}
	}
	if failed != len(driftSeeds) {
		t.Fatalf("with the quota audit switched off, %d of %d drift seeds still matched a recount", len(driftSeeds)-failed, len(driftSeeds))
	}
}


```

## TestCanaryShutdownLeak

Mentioned in: book/chapters/07-object-store-and-sync-engine.md

internal/noderuntime/runtime_net_test.go:202

```go

func TestCanaryShutdownLeak(t *testing.T) {
	before := leakcheck.Take()
	release := make(chan struct{})
	noderuntime.SetCanaryUntrackedGoroutine(release)
	t.Cleanup(func() { noderuntime.SetCanaryUntrackedGoroutine(nil) })
	rts, _, err := noderuntime.StartLoopbackCluster(t, 1, nil, func(_ int, ids []env.NodeID, dir string) noderuntime.RuntimeConfig {
		return noderuntime.RuntimeConfig{ID: ids[0], Region: "r0", DataDir: dir, Node: realConfig()}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rts[0].Close(); err != nil {
		t.Fatal(err)
	}
	if err := before.Leaked(200 * time.Millisecond); !errors.Is(err, leakcheck.ErrLeak) {
		t.Fatalf("a goroutine started without the runtime's WaitGroup passed the leak check: %v", err)
	}
	close(release)
	if err := before.Leaked(5 * time.Second); err != nil {
		t.Fatalf("leak check still failing after the canary goroutine exited: %v", err)
	}
}

// TestRealRuntimeLeavesNoGoroutineBehind proves starting a 2-node loopback
// cluster, waiting for join, and closing both nodes leaks zero goroutines.

```

## TestClusterPartitionHealDST

Mentioned in: book/chapters/07-object-store-and-sync-engine.md, book/front/topic-map.md

internal/node/dst_partition_test.go:273

```go

func TestClusterPartitionHealDST(t *testing.T) {
	t.Parallel()
	var m measure
	sweepSharded(t, func(t *testing.T, seed int64) {
		var res partitionResult
		simtest.RequireDeterministic(t, seed, partitionRun(&res))
		m.add("partition/converge_ms", res.converge.Milliseconds())
		m.add("partition/quota_settle_ms", res.quotaSettle.Milliseconds())
		m.add("partition/discard_events", int64(res.events))
		m.add("partition/distinct_losers", int64(res.distinctLosers))
		m.add("partition/object_losers", int64(res.objectLosers))
		m.add("partition/quota_overshoot_bytes", res.overshoot)
		m.add("partition/recount_writes", int64(res.recountWrites))
		m.add("partition/collected_loser_shards", int64(res.collectedLoser))
		m.add("partition/acked", int64(res.acked))
		m.add("partition/quota_refused", int64(res.quotaRefused))
		for k, v := range res.tally {
			m.add("partition/oracle_"+k, int64(v))
		}
	})
	t.Cleanup(func() { m.report(t) })
}

// TestCanaryNodeSkipsDiscardRecount plants a bug disabling both
// discard-triggered recounts and auditQuotas and proves partitionRun and
// quotaDriftRun fail their post-heal usage checks.

```

## TestConformance

Mentioned in: book/chapters/07-object-store-and-sync-engine.md

Regex prefix; see internal/noderuntime/conformance_test.go:279 and :296.

## TestFileDiskCrashLeavesOldOrNewBytes

Mentioned in: book/chapters/07-object-store-and-sync-engine.md, book/front/topic-map.md

internal/noderuntime/crashfs_test.go:348

```go

func TestFileDiskCrashLeavesOldOrNewBytes(t *testing.T) {
	for _, c := range crashCases() {
		for _, mode := range []string{"crash", "fail-then-crash", "short-write"} {
			for k := 1; ; k++ {
				m := freshMemFS()
				d, err := openFileDisk(m, "/d")
				if err != nil {
					t.Fatal(err)
				}
				if err := c.setup(d); err != nil {
					t.Fatalf("%s: set-up: %v", c.name, err)
				}
				m.ops = 0
				switch mode {
				case "crash":
					m.crashAt = k
				case "fail-then-crash":
					m.failAt = k
				case "short-write":
					m.short = k == 1
				}
				opErr := c.op(d)
				ran := m.ops
				if !m.crashed {
					m.crashed = true
				}
				for _, keep := range []bool{false, true} {
					got, ok, err := readBack(m.afterCrash(keep), c.blob)
					if err != nil {
						t.Fatalf("%s, %s at step %d, metadata kept=%v: reopen: %v", c.name, mode, k, keep, err)
					}
					isOld := (c.old == nil && !ok) || (c.old != nil && ok && bytes.Equal(got, c.old))
					isNew := (c.new == nil && !ok) || (c.new != nil && ok && bytes.Equal(got, c.new))
					if c.appends && ok && c.old != nil && len(got) > len(c.old) && bytes.HasPrefix(c.new, got) {
						isOld = true
					}
					if opErr == nil && !isNew {
						t.Fatalf("%s, %s at step %d, metadata kept=%v: Sync returned nil but after the crash the blob is %q (present %v), want the new bytes", c.name, mode, k, keep, got, ok)
					}
					if !isOld && !isNew {
						t.Fatalf("%s, %s at step %d, metadata kept=%v: after the crash the blob is %q (present %v): neither the old nor the new bytes", c.name, mode, k, keep, got, ok)
					}
				}
				if mode == "short-write" || k > ran {
					break
				}
			}
		}
	}
}

var _ = time.Second


```

## FuzzClusterNemesis

Mentioned in: book/chapters/08-end-to-end-and-hardening.md, book/front/topic-map.md

test/e2e/e7_test.go:492

```go

func FuzzClusterNemesis(f *testing.F) {
	for _, b := range fuzzCorpus() {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			t.Skip("input over 64 KiB")
		}
		s, _ := runFuzzInput(fuzzOptions(), fuzzWarmM, data)
		if err := s.Verify(); err != nil {
			simtest.Report(t, s)
			t.Fatalf("seed %d: %v", fuzzSeed(data), err)
		}
	})
}


```

## TestCanaryE2ObjectOpsUnavailableDuringSplitFails

Mentioned in: book/chapters/08-end-to-end-and-hardening.md

test/e2e/canary_test.go:488

```go

func TestCanaryE2ObjectOpsUnavailableDuringSplitFails(t *testing.T) {
	cases := []struct {
		name      string
		blockPut  bool
		blockGet  bool
		wantError string
	}{
		{name: "put and get unavailable during split", blockPut: true, blockGet: true, wantError: "must succeed"},
		{name: "only get unavailable during split", blockPut: false, blockGet: true, wantError: "get e2/"},
		{name: "only put unavailable during split", blockPut: true, blockGet: false, wantError: "put e2/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newSimCluster(1, defaultSimOptions())
			c.apiIntercept = func(n *node.Node, op byte) error {
				if len(n.Gossip().Members()) >= len(c.ids) {
					return nil
				}
				if tc.blockPut && op == opPut {
					return storage.ErrUnavailable
				}
				if tc.blockGet && op == opGet {
					return storage.ErrUnavailable
				}
				return nil
			}
			if err := c.boot(); err != nil {
				t.Fatal(err)
			}
			if _, err := scenarioE2(c, simBounds, e2Params{objects: 6, size: 3000, detect: 500 * time.Millisecond, keepCut: 2 * time.Second}); err != nil {
				c.fail(err)
			}
			expectCanaryFailure(t, c.s, tc.wantError)
		})
	}
}

type faultyDiscardHealthCluster struct {
	*simCluster
	mutate func(h *storage.Health)
}


```

## TestCanaryE7WipingTheOnlyCopyOfAnAcknowledgedWriteIsCaught

Mentioned in: book/chapters/08-end-to-end-and-hardening.md

test/e2e/canary_test.go:346

```go

func TestCanaryE7WipingTheOnlyCopyOfAnAcknowledgedWriteIsCaught(t *testing.T) {
	const seed = 166963932986536
	var st nemesisStats
	s := runNemesis(seed, e7Options(), func(n *nemesis) nemesisPlan {
		n.loneWipe = true
		return randomPlan(n.r, e7Cycles, e7Chaos)
	}, &st)(seed)
	expectCanaryFailure(t, s, "on no discard record")
}

type dropBucketOnCopyStep struct {
	saga.Step
	cluster func() *simCluster
	bucket  string
}


```

## TestCanaryFuzzRoutedBucketCannotPlaceShards

Mentioned in: book/chapters/08-end-to-end-and-hardening.md

test/e2e/canary_test.go:423

```go

func TestCanaryFuzzRoutedBucketCannotPlaceShards(t *testing.T) {
	old := fuzzOptions()
	old.nodes = 9
	old.cfg.Layout = storage.Layout{K: 4, M: 2, StripeSize: simStripe}
	t.Run("warm-up fails immediately on unplaceable routed bucket", func(t *testing.T) {
		_, err := checkFuzzCorpus(old, fuzzWarmM)
		if err == nil {
			t.Fatal("checkFuzzCorpus passed with 9-node 4+2 layout")
		}
		want := "fault-free warm-up put to routed bucket m"
		if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("failed for another reason than unplaceable routed bucket: %v", err)
		}
		t.Logf("caught: %v", err)
	})
	t.Run("corpus check catches zero acknowledged puts to routed bucket without warm-up", func(t *testing.T) {
		_, err := checkFuzzCorpus(old, 0)
		if err == nil {
			t.Fatal("checkFuzzCorpus(warmM=0) passed with 9-node 4+2 layout")
		}
		want := "acknowledged 0 traffic puts to routed bucket m"
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("failed for another reason: %v", err)
		}
		t.Logf("caught: %v", err)
	})
}

// TestCanaryNemesisForgottenAmbiguousMigrationFails enables forgetAmbiguousMig
// and proves a timed-out StartMigration that actually started a saga is caught
// by tracking assertions, a stale next-cycle region, and non-terminal saga
// checks.

```

## TestCanaryNemesisForgottenAmbiguousMigrationFails

Mentioned in: book/chapters/08-end-to-end-and-hardening.md

test/e2e/canary_test.go:455

```go

func TestCanaryNemesisForgottenAmbiguousMigrationFails(t *testing.T) {
	const seed = 702
	t.Run("2-cycle run fails with no migration when timed-out start is forgotten", func(t *testing.T) {
		s, _, err := verifyAmbiguousMigrationTracked(seed, 2, true)
		if err == nil {
			t.Fatal("ambiguous migration run passed with forgetAmbiguousMig enabled")
		}
		if s.Err() == nil || !strings.Contains(err.Error(), "bucket cannot migrate") {
			t.Fatalf("failed for another reason than forgotten ambiguous migration: %v", err)
		}
		t.Logf("caught: %v", err)
	})
	t.Run("1-cycle tracking assertions catch forgotten ambiguous start", func(t *testing.T) {
		_, _, err := verifyAmbiguousMigrationTracked(seed, 1, true)
		if err == nil {
			t.Fatal("1-cycle ambiguous migration check passed with forgetAmbiguousMig enabled")
		}
		want := "ambiguous migration start was not recovered"
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("failed for another reason than untracked ambiguous start: %v", err)
		}
		t.Logf("caught: %v", err)
	})
	t.Run("forgetting slow ambiguous start leaves non-terminal saga into next cycle", func(t *testing.T) {
		var st nemesisStats
		s := slowAmbiguousMigrationRun(seed, true, &st)(seed)
		expectCanaryFailure(t, s, "still holds non-terminal saga")
	})
}

// TestCanaryE2ObjectOpsUnavailableDuringSplitFails intercepts PUT, GET, or both
// with ErrUnavailable whenever a node's view shrinks, proving E2 requires
// surviving-side object operations to succeed during a partition.

```

## TestDefaultBuildCarriesNoFaultInjection

Mentioned in: book/chapters/08-end-to-end-and-hardening.md

test/e2e/tierr_test.go:636

```go

func TestDefaultBuildCarriesNoFaultInjection(t *testing.T) {
	requireTierR(t)
	plain, err := os.ReadFile(buildObjnode(t, false, ""))
	if err != nil {
		t.Fatal(err)
	}
	tagged, err := os.ReadFile(buildObjnode(t, false, "e2efault"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{blocklistSuffix, "noderuntime.blockedPeer", "noderuntime.BlocklistPath"} {
		if !bytes.Contains(tagged, []byte(marker)) {
			t.Fatalf("the e2efault build lacks %q, so this check could not see it", marker)
		}
		if bytes.Contains(plain, []byte(marker)) {
			t.Fatalf("the default build contains %q", marker)
		}
	}
}

// TestAllocDistinctPortsNeverCollides proves allocDistinctPorts returns 40
// unique valid ports in one reservation batch.

```

## TestE7NemesisHealingCyclesS

Mentioned in: book/chapters/08-end-to-end-and-hardening.md, book/front/topic-map.md

test/e2e/e7_test.go:70

```go

func TestE7NemesisHealingCyclesS(t *testing.T) {
	t.Parallel()
	var m measure
	var mu sync.Mutex
	var seedsRun, totPutsM, totPutsMOK, totMovedObjs, totAvailGets int
	t.Cleanup(func() {
		m.report(t)
		if t.Failed() {
			return
		}
		if seedsRun >= 10 && totPutsM >= 4 && totPutsMOK == 0 {
			t.Fatalf("E7 acknowledged 0 of %d puts to routed bucket %s across %d seeds", totPutsM, bucketM, seedsRun)
		}
		if seedsRun >= 20 && totMovedObjs == 0 {
			t.Fatalf("E7 migrated 0 pre-existing objects of bucket %s across %d seeds", bucketM, seedsRun)
		}
		if err := checkE7AvailabilityFloor(seedsRun, totAvailGets); err != nil {
			t.Fatal(err)
		}
	})
	sweep(t, seedsFor(t, 3, 5), func(t *testing.T, seed int64) {
		var st nemesisStats
		simtest.RequireDeterministic(t, seed, runNemesis(seed, e7Options(), func(n *nemesis) nemesisPlan { return randomPlan(n.r, e7Cycles, e7Chaos) }, &st))
		if st.cycles != e7Cycles {
			t.Fatalf("seed %d ran %d cycles, want %d", seed, st.cycles, e7Cycles)
		}
		if st.applied == 0 {
			t.Fatalf("seed %d applied 0 chaos faults", seed)
		}
		if st.putsOK == 0 || st.getsOK == 0 {
			t.Fatalf("seed %d saw putsOK=%d getsOK=%d, want both > 0", seed, st.putsOK, st.getsOK)
		}
		mu.Lock()
		seedsRun++
		totPutsM += st.putsM
		totPutsMOK += st.putsMOK
		totMovedObjs += st.movedObjects
		totAvailGets += st.availabilityGets
		mu.Unlock()
		m.add("e7/cycles", int64(st.cycles))
		m.add("e7/faults_applied", int64(st.applied))
		m.add("e7/faults_skipped_by_budget", int64(st.skipped))
		m.add("e7/puts", int64(st.puts))
		m.add("e7/puts_ok", int64(st.putsOK))
		m.add("e7/puts_m", int64(st.putsM))
		m.add("e7/puts_m_ok", int64(st.putsMOK))
		m.add("e7/gets", int64(st.gets))
		m.add("e7/gets_ok", int64(st.getsOK))
		m.add("e7/availability_gets", int64(st.availabilityGets))
		m.add("e7/discards", int64(st.discards))
		m.add("e7/declared_lost", int64(st.declared))
		m.add("e7/migrations_completed", int64(st.migrated))
		m.add("e7/migrations_compensated", int64(st.compensated))
		m.add("e7/ambiguous_migrations_recovered", int64(st.ambiguousRecovered))
		m.add("e7/dest_objects", int64(st.destObjects))
		m.add("e7/dest_shards", int64(st.destShards))
		m.add("e7/moved_objects", int64(st.movedObjects))
		m.add("e7/moved_shards", int64(st.movedShards))
		for _, d := range st.converge {
			m.ms("e7/converge_after_heal", d)
		}
		for _, d := range st.repair {
			m.ms("e7/repair_after_converge", d)
		}
	})
}

// fuzzOptions uses 12 nodes (4 per region) with a 2+1 layout so routed bucket m
// keeps >= k+m=3 nodes in its region even under a single-node fault while
// keeping per-execution fuzz cost low.

```

## TestMigrationRetireBelowKRecopiesAndNeverGoesLive

Mentioned in: book/chapters/08-end-to-end-and-hardening.md

internal/node/dst_migstall_test.go:1655

```go

func TestMigrationRetireBelowKRecopiesAndNeverGoesLive(t *testing.T) {
	t.Parallel()
	seeds := []int64{31337, 3, 4}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			simtest.RequireDeterministic(t, seed, migRetireRecheckRun(3, true))
		})
	}
}


```

## TestMigrationRetireBelowQuorumWaitsForRebuild

Mentioned in: book/chapters/08-end-to-end-and-hardening.md

internal/node/dst_migstall_test.go:1640

```go

func TestMigrationRetireBelowQuorumWaitsForRebuild(t *testing.T) {
	t.Parallel()
	seeds := []int64{31337, 3, 4}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			simtest.RequireDeterministic(t, seed, migRetireRecheckRun(2, false))
		})
	}
}

// TestMigrationRetireBelowKRecopiesAndNeverGoesLive proves Retire rejects a
// staged version reduced to 3 verified shards (< k=4) after catch-up Root,
// deletes the damaged staged record, and lets post-pivot retry re-copy from
// the live source before completing.

```

## TestMigrationVerifyBelowKCompensates

Mentioned in: book/chapters/08-end-to-end-and-hardening.md

internal/node/dst_migstall_test.go:1265

```go

func TestMigrationVerifyBelowKCompensates(t *testing.T) {
	t.Parallel()
	seeds := []int64{31337, 3, 4}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			simtest.RequireDeterministic(t, seed, migThresholdRun(3, saga.StateCompensated))
		})
	}
}

// TestMigrationVerifyBelowQuorumWaitsForRebuild proves that leaving 4 verified
// shards (k=4 <= 4 < quorum=5) before StepVerify blocks StepFlip until
// destination holders rebuild to quorum and then completes cleanly.

```

## TestMigrationVerifyBelowQuorumWaitsForRebuild

Mentioned in: book/chapters/08-end-to-end-and-hardening.md

internal/node/dst_migstall_test.go:1279

```go

func TestMigrationVerifyBelowQuorumWaitsForRebuild(t *testing.T) {
	t.Parallel()
	seeds := []int64{31337, 3, 4}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			simtest.RequireDeterministic(t, seed, migThresholdRun(4, saga.StateCompleted))
		})
	}
}

// verifyWaveNet fires onWave right after the coordinator's opVerifyShards
// (channel 5, op 4) wave finishes so tests can wipe destination holders in the
// window between catch-up Root verification and Retire's manifest swap.
type verifyWaveNet struct {
	env.Transport
	self     env.NodeID
	coord    *env.NodeID
	active   *bool
	inflight *int
	onWave   func()
}


```

## TestNemesisAmbiguousMigrationStartIsTracked

Mentioned in: book/chapters/08-end-to-end-and-hardening.md

test/e2e/e7_test.go:396

```go

func TestNemesisAmbiguousMigrationStartIsTracked(t *testing.T) {
	const seed = 702
	for _, cycles := range []int{1, 2} {
		var st nemesisStats
		simtest.RequireDeterministic(t, seed, ambiguousMigrationRun(seed, cycles, false, &st))
		if st.ambiguousRecovered != 1 {
			t.Fatalf("cycles=%d: ambiguousRecovered = %d, want 1", cycles, st.ambiguousRecovered)
		}
		if st.migrated != cycles {
			t.Fatalf("cycles=%d: migrated = %d, want %d", cycles, st.migrated, cycles)
		}
		wantShards := cycles * fuzzWarmM * (fuzzOptions().cfg.Layout.K + fuzzOptions().cfg.Layout.M)
		if st.movedShards < wantShards {
			t.Fatalf("cycles=%d: movedShards = %d, want >= %d", cycles, st.movedShards, wantShards)
		}
		wantRegion := "r1"
		if cycles == 2 {
			wantRegion = "r2"
		}
		if st.finalRegion != wantRegion {
			t.Fatalf("cycles=%d: finalRegion = %q, want %q", cycles, st.finalRegion, wantRegion)
		}
		t.Logf("cycles=%d recovered=%d migrated=%d movedShards=%d destShards=%d finalRegion=%s", cycles, st.ambiguousRecovered, st.migrated, st.movedShards, st.destShards, st.finalRegion)
	}
	t.Run("dropped request timeout starts no saga and next cycle migrates from original region", func(t *testing.T) {
		var st nemesisStats
		simtest.RequireDeterministic(t, seed, runNemesis(seed, fuzzOptions(), func(n *nemesis) nemesisPlan {
			n.noGrace = true
			n.warmM = fuzzWarmM
			n.beforeCycle = func(n *nemesis, ci int) error {
				if ci != 0 {
					return nil
				}
				var clients []env.NodeID
				for _, r := range n.c.regions {
					clients = append(clients, env.NodeID("client-"+r))
				}
				n.markNetFault()
				return n.c.apply(sim.Fault{Kind: sim.FaultPartitionOneWay, Side: clients, Other: n.c.ids})
			}
			return nemesisPlan{cycles: []cycle{
				{chaos: e7Chaos, ops: []chaosOp{{kind: opMigrate, a: 0, b: 0, at: 500 * time.Millisecond}}},
				{chaos: e7Chaos, ops: []chaosOp{{kind: opMigrate, a: 0, b: 0, at: 500 * time.Millisecond}}},
			}}
		}, &st))
		if st.ambiguousRecovered != 0 {
			t.Fatalf("ambiguousRecovered = %d, want 0 when request never reached coordinator", st.ambiguousRecovered)
		}
		if st.migrated != 1 || st.finalRegion != "r1" {
			t.Fatalf("migrated=%d finalRegion=%q, want 1 and r1", st.migrated, st.finalRegion)
		}
	})
	t.Run("ambiguous start finishes to terminal state before next cycle can wipe coordinator", func(t *testing.T) {
		var st nemesisStats
		simtest.RequireDeterministic(t, seed, slowAmbiguousMigrationRun(seed, false, &st))
		if st.ambiguousRecovered != 1 || st.migrated != 1 || st.finalRegion != "r1" {
			t.Fatalf("unexpected stats: recovered=%d migrated=%d finalRegion=%q", st.ambiguousRecovered, st.migrated, st.finalRegion)
		}
		if st.applied < 2 {
			t.Fatalf("expected migration and cycle-1 coordinator wipe to both apply, got applied=%d", st.applied)
		}
	})
}

// TestE7AvailabilityFloorSafeForSingleSeed proves the sweep-level availability
// floor never false-fails single-seed runs and catches under-floor sweeps.

```

## TestReplayCommandsReproduceEverySeed

Mentioned in: book/chapters/08-end-to-end-and-hardening.md

test/e2e/replay_test.go:61

```go

func TestReplayCommandsReproduceEverySeed(t *testing.T) {
	t.Run("sweep replays every seed across all four shards", func(t *testing.T) {
		const planted = "planted-replay-e8"
		out, err := runSelf(t,
			[]string{"DST_SEEDS=8", "E2E_FULL_SEEDS=1", "E2E_PLANT_E8_FAIL=" + planted},
			"-test.v", "-test.count=1", "-test.run", "^TestE8LocalFirstMetadataSyncS$",
		)
		if err == nil {
			t.Fatalf("expected planted E8 failure sweep to fail, output:\n%s", out)
		}
		replays := parseReplayLines(out)
		if len(replays) < 8 {
			t.Fatalf("captured %d REPLAY lines under DST_SEEDS=8, want >= 8\noutput:\n%s", len(replays), out)
		}
		shardsSeen := map[string]bool{}
		for _, rp := range replays {
			for _, sh := range []string{"shard=0", "shard=1", "shard=2", "shard=3"} {
				if strings.Contains(rp.run, sh) {
					shardsSeen[sh] = true
				}
			}
			rout, rerr := runSelf(t,
				[]string{"E2E_PLANT_E8_FAIL=" + planted},
				"-test.v", "-test.count=1", "-test.run", rp.run, "-seed="+rp.seed,
			)
			if rerr == nil {
				t.Fatalf("REPLAY passed instead of reproducing failure:\n  %s\noutput:\n%s", rp.line, rout)
			}
			want := planted + " (seed=" + rp.seed + ")"
			if !strings.Contains(rout, want) {
				t.Fatalf("REPLAY failed without planted message %q:\n  %s\noutput:\n%s", want, rp.line, rout)
			}
		}
		if len(shardsSeen) != 4 {
			t.Fatalf("replays covered shards %v, want all 4 shards", shardsSeen)
		}
	})

	t.Run("e5 cell replays reproduce failing cells and seeds", func(t *testing.T) {
		const planted = "planted-replay-e5"
		out, err := runSelf(t,
			[]string{"DST_SEEDS=4", "E2E_FULL_SEEDS=1", "E2E_PLANT_E5_FAIL=" + planted},
			"-test.v", "-test.count=1", "-test.run", "^TestE5MigrationEveryFailurePointS$/^(allocate-fail-before-action-untorn|copy-fail-after-action-untorn)$",
		)
		if err == nil {
			t.Fatalf("expected planted E5 failure run to fail, output:\n%s", out)
		}
		replays := parseReplayLines(out)
		if len(replays) < 4 {
			t.Fatalf("captured %d E5 REPLAY lines, want >= 4\noutput:\n%s", len(replays), out)
		}
		for _, rp := range replays {
			rout, rerr := runSelf(t,
				[]string{"E2E_PLANT_E5_FAIL=" + planted},
				"-test.v", "-test.count=1", "-test.run", rp.run, "-seed="+rp.seed,
			)
			if rerr == nil {
				t.Fatalf("E5 REPLAY passed instead of reproducing failure:\n  %s\noutput:\n%s", rp.line, rout)
			}
			want := "seed=" + rp.seed + ")"
			if !strings.Contains(rout, planted) || !strings.Contains(rout, want) {
				t.Fatalf("E5 REPLAY failed without planted message %q and %q:\n  %s\noutput:\n%s", planted, want, rp.line, rout)
			}
		}
	})

	t.Run("explicit seed matching no seed subtest fails closed", func(t *testing.T) {
		cases := []struct {
			name string
			run  string
			seed string
		}{
			{"sweep wrong shard for seed=2", "^TestE8LocalFirstMetadataSyncS$/^shard=0$/^seed=2$", "2"},
			{"sweep nonexistent shard", "^TestE8LocalFirstMetadataSyncS$/^shard=99$/^seed=2$", "2"},
			{"e5 nonexistent cell", "^TestE5MigrationEveryFailurePointS$/^no-such-cell$/^seed=2$", "2"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				out, err := runSelf(t, nil, "-test.v", "-test.count=1", "-test.run", tc.run, "-seed="+tc.seed)
				if err == nil {
					t.Fatalf("empty -seed run passed instead of failing closed:\noutput:\n%s", out)
				}
				if !strings.Contains(out, "ran 0 seed subtests") {
					t.Fatalf("empty -seed run failed without expected message:\noutput:\n%s", out)
				}
			})
		}
	})
}


```

## TestTierRHarnessLeavesNothingBehindWhenATestFails

Mentioned in: book/chapters/08-end-to-end-and-hardening.md

test/e2e/tierr_test.go:570

```go

func TestTierRHarnessLeavesNothingBehindWhenATestFails(t *testing.T) {
	requireTierR(t)
	if out := os.Getenv("E2E_CHILD_OUT"); out != "" {
		c := newRealCluster(t, 99, realOptions{nodes: 3, layout: storage.Layout{K: 2, M: 1, StripeSize: realStripe}})
		var rec hygieneRecord
		for _, p := range c.procs {
			rec.PIDs = append(rec.PIDs, p.cmd.Process.Pid)
			rec.Ports = append(rec.Ports, p.ports...)
		}
		binMu.Lock()
		rec.Dirs = append(append(rec.Dirs, c.dir), binDirs...)
		binMu.Unlock()
		b, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, b, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Fatal("planted failure with a running cluster")
	}
	out := filepath.Join(t.TempDir(), "child.json")
	cmd := exec.Command(os.Args[0], "-test.run", "^TestTierRHarnessLeavesNothingBehindWhenATestFails$", "-test.count=1")
	cmd.Env = append(os.Environ(), "E2E_CHILD_OUT="+out, tierREnv+"=1")
	b, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the child passed; its planted failure did not fire:\n%s", b)
	}
	if !strings.Contains(string(b), "planted failure") {
		t.Fatalf("the child failed for another reason: %v\n%s", err, b)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var rec hygieneRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.PIDs) != 3 || len(rec.Ports) != 6 || len(rec.Dirs) < 2 {
		t.Fatalf("child record incomplete: %+v", rec)
	}
	for _, pid := range rec.PIDs {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Errorf("process %d still exists after the failed test: %v", pid, err)
		}
	}
	for _, port := range rec.Ports {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Errorf("tcp port %d still taken: %v", port, err)
			continue
		}
		if err := l.Close(); err != nil {
			t.Error(err)
		}
	}
	for _, d := range rec.Dirs {
		if _, err := os.Stat(d); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("directory %s left behind: %v", d, err)
		}
	}
}

// TestDefaultBuildCarriesNoFaultInjection proves e2efault blocklist symbols
// appear in the tagged objnode binary and are absent from the default binary.

```

## TestConformanceOnLoopback

Mentioned in: book/front/topic-map.md

internal/noderuntime/conformance_test.go:296

```go

func TestConformanceOnLoopback(t *testing.T) {
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			c := startReal(t, 3, realConfig())
			if err := sc.run(realDriver{t: t, c: c}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

var _ = env.ErrClosed


```

## TestE2PartitionAndHealS

Mentioned in: book/front/topic-map.md

test/e2e/tiers_test.go:44

```go

func TestE2PartitionAndHealS(t *testing.T) {
	t.Parallel()
	var m measure
	t.Cleanup(func() { m.report(t) })
	sweep(t, seedsFor(t, 1, 4), func(t *testing.T, seed int64) {
		var res e2Result
		var tally map[string]int
		var oc outcome
		runSim(t, seed, defaultSimOptions(), func(c *simCluster) error {
			var err error
			res, err = scenarioE2(c, simBounds, e2Params{objects: 6, size: 3000, detect: 500 * time.Millisecond, keepCut: 2 * time.Second})
			if err != nil {
				return err
			}
			tally, oc, err = c.finalChecks(true)
			return err
		})
		m.ms("e2/converge", res.converge)
		m.ms("e2/detect", res.detect)
		m.add("e2/puts_acked", int64(res.putsAcked))
		m.add("e2/puts_unavailable", int64(res.putsUnavailable))
		m.add("e2/post_puts_required", int64(res.postPutsRequired))
		m.add("e2/gets_ok", int64(res.getsOK))
		m.add("e2/early_gets_required", int64(res.earlyGetsRequired))
		m.add("e2/post_gets_required", int64(res.postGetsRequired))
		m.add("e2/contested_current", int64(res.contested.current))
		m.add("e2/contested_discarded", int64(res.contested.discarded))
		m.add("e2/discards_max_node", int64(res.discards))
		m.add("e2/declared_lost", int64(res.lost))
		m.add("e2/oracle_superseded", int64(tally["superseded"]))
		m.add("e2/oracle_discarded", int64(tally["discarded"]))
		m.add("e2/live_readable", int64(oc.readable))
		m.add("e2/live_lost", int64(oc.lost))
	})
}

// TestE3BitRotRepairS proves tier S bit-rot reads, proportional anti-entropy
// repair (rebuilt and moved bytes), and absence recording short of loss.

```

## TestE9ShutdownUnderLoadR

Mentioned in: book/front/topic-map.md

test/e2e/tierr_test.go:529

```go

func TestE9ShutdownUnderLoadR(t *testing.T) {
	requireTierR(t)
	o := defaultRealOptions()
	o.putSlots = 4
	c := newRealCluster(t, 9, o)
	res, err := scenarioE9(c, syscall.SIGTERM)
	if err != nil {
		t.Fatal(err)
	}
	logMeasure(t, "e9r/shutdown_under_load", res.shutdown)
	logMeasure(t, "e9r/in_flight_at_sigterm_finished", res.inflightOK)
	logMeasure(t, "e9r/in_flight_at_sigterm_failed_cleanly", res.inflightFailed)
	logMeasure(t, "e9r/requests_ok", res.ok)
	logMeasure(t, "e9r/requests_refused_cleanly", res.refused)
	logMeasure(t, "e9r/acked_read_after_restart", res.readAfterRestart)
	logMeasure(t, "e9r/restart_to_converged", res.restart)
}

// TestCanaryE9AKilledNodeFailsTheCleanShutdownChecks plants SIGKILL instead of
// SIGTERM and proves scenarioE9 catches the non-zero exit status.

```
