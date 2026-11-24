package occ_test

import (
	"bytes"
	"errors"
	"github.com/nickstrad/object-store/internal/metrics"
	"github.com/nickstrad/object-store/internal/occ"
	"testing"
)

func obsCounter(s metrics.Snapshot, suffix string) uint64 {
	for _, p := range s.Points {
		if p.Name == "objstore_occ_"+suffix {
			return p.Counter
		}
	}
	return 0
}
func TestMetricsCommitRollbackReplay(t *testing.T) {
	clock := &fakeClock{now: 1000000}
	cfg := storeConfig("a", 1, 1, clock)
	cfg.MetricsEnabled = true
	fail := false
	drainFail := false
	var durable []byte
	cfg.Persist = func(b []byte) error {
		if fail {
			return errors.New("disk unavailable")
		}
		if drainFail && len(durable) > 0 {
			return errors.New("drain unavailable")
		}
		durable = bytes.Clone(b)
		return nil
	}
	a, err := occ.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newTestStore(t, "b", 1, 2)
	key := occ.ManifestKey("b", "key")
	mustPut(t, a, key, "a", occ.Unconditional())
	mustPut(t, b, key, "b", occ.Unconditional())
	fail = true
	if _, err := a.Merge(b.Doc()); err == nil {
		t.Fatal("failed persist accepted")
	}
	if a.LWWDiscards() != 0 || obsCounter(a.Snapshot(), "lww_discards_total") != 0 {
		t.Fatal("rollback leaked discard")
	}
	fail = false
	durable = nil
	drainFail = true
	mustMerge(t, a, b.Doc())
	if obsCounter(a.Snapshot(), "lww_discards_total") != 1 {
		t.Fatal("committed loss not counted")
	}
	mustMerge(t, a, b.Doc())
	if obsCounter(a.Snapshot(), "lww_discards_total") != 1 {
		t.Fatal("repeated merge counted loss twice")
	}
	cfg.Persist = nil
	replayed := 0
	cfg.Log = func(string) { replayed++ }
	reopened, err := occ.OpenStore(cfg, durable)
	if err != nil {
		t.Fatal(err)
	}
	if replayed != 1 || reopened.LWWDiscards() != 1 || obsCounter(reopened.Snapshot(), "lww_discards_total") != 0 {
		t.Fatalf("replay changed lifetime delta: %d %+v", replayed, reopened.Snapshot())
	}
	snap := a.Snapshot()
	batch := metrics.Batch{SchemaVersion: 1, NodeID: "n", Region: "r", BootID: "b", Sequence: 1, StartUnixNano: 1, TimeUnixNano: 1, Snapshots: []metrics.Snapshot{snap}}
	if err := metrics.Validate(batch); err != nil {
		t.Fatal(err)
	}
	// Off and on writers preserve identical protocol state and durable audit results.
	offCfg := storeConfig("x", 1, 3, clock)
	onCfg := storeConfig("x", 1, 3, clock)
	onCfg.MetricsEnabled = true
	off, _ := occ.NewStore(offCfg)
	on, _ := occ.NewStore(onCfg)
	for _, s := range []*occ.Store{off, on} {
		mustPut(t, s, key, "value", occ.Unconditional())
		mustMerge(t, s, b.Doc())
	}
	if !bytes.Equal(off.EncodeState(), on.EncodeState()) {
		t.Fatal("metrics changed persisted state")
	}
	if len(off.Snapshot().Points) != 0 {
		t.Fatal("disabled points")
	}
}
