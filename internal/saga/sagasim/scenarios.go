package sagasim

import (
	"fmt"
	"slices"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/saga"
	"github.com/nickstrad/object-store/internal/sim"
)

// NemesisKinds returns sim.ChaosKinds excluding bit-rot, wipe-disk, and
// wipe-rejoin, because coordinator logs are local and unrecoverable disk loss
// leaves a saga broken or unfinished by design.
func NemesisKinds() []sim.FaultKind {
	var out []sim.FaultKind
	for _, k := range sim.ChaosKinds() {
		switch k {
		case sim.FaultBitRot, sim.FaultWipeDisk, sim.FaultWipeRejoin:
			continue
		}
		out = append(out, k)
	}
	return out
}

// NemesisParams configures a chained-migration chaos run in NemesisRun.
// A zero Objects defaults to MatrixObjects.
type NemesisParams struct {
	Start    env.Time
	Phases   int
	Chaos    time.Duration
	Heal     time.Duration
	MaxGap   time.Duration
	Converge time.Duration
	Writes   int
	WriteGap time.Duration
	Tick     time.Duration
	Objects  int
}

// NemesisReport captures summary counts and settle timing from NemesisRun.
type NemesisReport struct {
	Sagas       int
	Completed   int
	Compensated int
	Settled     time.Duration
	Attempts    []int
	Acked       int
	Stored      int
	Final       string
}

func threeStores() []sim.NodeSpec {
	return []sim.NodeSpec{{ID: "a", Region: "ra"}, {ID: "b", Region: "rb"}, {ID: "c", Region: "rc"}}
}

// NemesisRun returns a deterministic simulation function that chains bucket
// migrations around three stores while a client writes concurrently under
// nemesis chaos, verifying convergence and write durability after healing.
// res may be nil; each invocation uses its own report when res is nil so the
// returned closure is safe to call concurrently.
func NemesisRun(p NemesisParams, res *NemesisReport) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		// Shadow res per invocation so concurrent callers with nil res never race.
		res := res
		if res == nil {
			res = &NemesisReport{}
		}
		*res = NemesisReport{}
		objs := p.Objects
		if objs == 0 {
			objs = MatrixObjects
		}
		s, m, err := Build(seed, Options{
			Coordinators: []sim.NodeSpec{{ID: "c1", Region: "ra"}},
			Stores:       threeStores(),
			Client:       &sim.NodeSpec{ID: "cl", Region: "rb"},
			Source:       "a",
			Objects:      objs,
			Policy:       MatrixPolicy(),
		})
		if err != nil {
			s.Fail(err)
			return s
		}
		m.AddInvariants(s)
		nem, err := s.StartNemesis(sim.NemesisConfig{
			Start: p.Start, Phases: p.Phases, Chaos: p.Chaos, Heal: p.Heal, MaxGap: p.MaxGap,
			Kinds:  NemesisKinds(),
			Budget: sim.Budget{MinLive: 3, MaxDamagedDisks: 1},
		})
		if err != nil {
			s.Fail(err)
			return s
		}
		stores := m.Stores()
		cur := string(stores[0])
		var last saga.ID
		var lastIn saga.MigrationInput
		next := 0
		var tick func()
		tick = func() {
			if last != "" {
				st, ok := m.Status("c1", last)
				if !ok || !st.State.Terminal() {
					s.At(s.Now().Add(p.Tick), "migrate", tick)
					return
				}
				if st.State == saga.StateCompleted {
					cur = lastIn.To
					res.Completed++
				} else {
					res.Compensated++
				}
				last = ""
			}
			if s.Now() >= nem.QuietAt() {
				return
			}
			next++
			to := stores[(slices.Index(stores, env.NodeID(cur))+1)%len(stores)]
			in := saga.MigrationInput{Bucket: Bucket, From: cur, To: string(to)}
			id := saga.ID(fmt.Sprintf("m%d", next))
			if err := m.Begin("c1", id, in); err == nil {
				last, lastIn = id, in
				res.Sagas++
			}
			s.At(s.Now().Add(p.Tick), "migrate", tick)
		}
		s.At(p.Start/2, "migrate", tick)
		s.Workload(p.Start/2, p.Writes, p.WriteGap, func(i int) { m.Write(i) })
		if err := s.RunUntil(nem.QuietAt()); err != nil {
			return s
		}
		done := func() bool { return last == "" && m.WritesIdle() }
		if err := s.RunUntilConverged(nem.QuietAt().Add(p.Converge), done); err != nil {
			return s
		}
		res.Settled = s.Now().Sub(nem.QuietAt())
		res.Attempts = m.Attempts("c1")
		res.Acked = m.Acked()
		res.Final = cur
		if st, ok := m.view(cur); ok {
			if bk := st.bucket(Bucket); bk != nil {
				res.Stored = len(bk.Objs)
			}
		}
		cerr := m.CheckAt(Bucket, cur)
		if cerr == nil {
			cerr = m.CheckWrites(Bucket, cur)
		}
		if cerr != nil {
			s.Fail(cerr)
		}
		return s
	}
}

// DuelParams configures the competing-migrations partition scenario in DuelRun.
type DuelParams struct {
	Converge     time.Duration
	MaxStagger   time.Duration
	MinPartition time.Duration
	MaxPartition time.Duration
}

// DuelReport captures the terminal states, outcomes, winning pointer location,
// and heal/finish times from DuelRun.
type DuelReport struct {
	States   [2]saga.State
	Outcomes [2]string
	Pointer  string
	Healed   env.Time
	Finished env.Time
}

// DuelRun returns a deterministic simulation function in which two coordinators
// migrate the same bucket to different targets across a network partition,
// verifying via CheckDuel that after healing one location holds the bucket, all
// pointer replicas agree, and the loser cleans up its target. res may be nil.
func DuelRun(p DuelParams, res *DuelReport) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		// Shadow res per invocation so concurrent callers with nil res never race.
		res := res
		if res == nil {
			res = &DuelReport{}
		}
		*res = DuelReport{}
		ins := [2]saga.MigrationInput{{Bucket: Bucket, From: "a", To: "b"}, {Bucket: Bucket, From: "a", To: "c"}}
		coords := [2]env.NodeID{"c1", "c2"}
		ids := [2]saga.ID{"x", "y"}
		s, m, err := Build(seed, Options{
			Coordinators: []sim.NodeSpec{{ID: "c1", Region: "ra"}, {ID: "c2", Region: "rc"}},
			Stores:       threeStores(),
			Source:       "a",
			Objects:      MatrixObjects,
			Policy:       MatrixPolicy(),
		})
		if err != nil {
			s.Fail(err)
			return s
		}
		m.AddInvariants(s)
		r := s.WorkloadRand()
		part := p.MinPartition
		if p.MaxPartition > p.MinPartition {
			part += time.Duration(r.Int64N(int64(p.MaxPartition - p.MinPartition)))
		}
		at := env.Time(10 * time.Millisecond)
		s.At(at, "partition", func() {
			if err := s.Partition([]env.NodeID{"c1", "b"}, []env.NodeID{"c2", "c"}); err != nil {
				s.Fail(err)
			}
		})
		res.Healed = at.Add(part)
		s.At(at.Add(part), "heal", s.HealNet)
		for i := range coords {
			begin := at.Add(10 * time.Millisecond)
			if p.MaxStagger > 0 {
				begin = begin.Add(time.Duration(r.Int64N(int64(p.MaxStagger))))
			}
			s.At(begin, "begin", func() {
				if err := m.Begin(coords[i], ids[i], ins[i]); err != nil {
					s.Fail(err)
				}
			})
		}
		done := func() bool {
			for i := range coords {
				st, ok := m.Status(coords[i], ids[i])
				if !ok || !st.State.Terminal() {
					return false
				}
			}
			return true
		}
		if err := s.RunUntilConverged(env.Time(p.Converge), done); err != nil {
			return s
		}
		for i := range coords {
			st, _ := m.Status(coords[i], ids[i])
			res.States[i] = st.State
			if len(st.Outputs) == saga.MigrationSteps {
				res.Outcomes[i] = string(st.Outputs[saga.MigrationSteps-1])
			}
		}
		res.Pointer = m.Pointer(Bucket)
		res.Finished = s.Now()
		if err := m.CheckDuel(ins[:], res.States[:], res.Outcomes[:]); err != nil {
			s.Fail(err)
		}
		return s
	}
}
