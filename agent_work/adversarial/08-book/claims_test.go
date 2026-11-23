package bookaudit

import (
	"errors"
	"fmt"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/erasure"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

// This test passes when it demonstrates the chapter's universal claim is false.
func TestSquareSubmatrixCounterexample(t *testing.T) {
	g, err := erasure.CodingMatrix(4, 2)
	if err != nil {
		t.Fatal(err)
	}
	rows := g.Rows()
	z, err := erasure.NewMatrix([][]byte{{rows[0][1]}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = z.Invert(); !errors.Is(err, erasure.ErrSingular) {
		t.Fatalf("1x1 off-diagonal submatrix: %v", err)
	}
	t.Log("G[0:1,1:2] is a singular square submatrix; the MDS guarantee is k-by-k")
}

// A successful early-quorum PUT does not imply m future losses are tolerable.
func TestPermanentDamageBudgetNeedsCompleteInitialStripe(t *testing.T) {
	codes, err := storage.Codes()
	if err != nil {
		t.Fatal(err)
	}
	s := sim.New(sim.Config{Seed: 1, Codes: codes})
	ids := sim.NodeIDs(6)
	members := make(storage.StaticMembership, 6)
	for i, id := range ids {
		members[i] = storage.Member{ID: id, Region: env.Region(fmt.Sprintf("r%d", i%3))}
	}
	nodes := map[env.NodeID]*storage.Node{}
	cfg := storage.Config{Layout: storage.Layout{K: 4, M: 2, StripeSize: 64}, Members: members}
	err = s.AddCluster(ids, 3, func(e env.Env) env.Process {
		n, e2 := storage.NewNode(e, cfg)
		if e2 != nil {
			t.Fatal(e2)
		}
		nodes[e.ID] = n
		return n
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RunUntilQuiet(); err != nil {
		t.Fatal(err)
	}
	if err = s.SetLinkLoss(ids[0], ids[5], 1_000_000); err != nil {
		t.Fatal(err)
	}
	var man *storage.Manifest
	var putErr error
	nodes[ids[0]].Put("b", "k", []byte("one stripe"), func(m *storage.Manifest, e error) { man, putErr = m, e })
	if err = s.RunUntilQuiet(); err != nil {
		t.Fatal(err)
	}
	if putErr != nil || man == nil {
		t.Fatalf("PUT: %v, manifest %v", putErr, man)
	}
	s.HealNet()
	durable, rot := 0, 0
	for i, ref := range man.Stripes[0].Shards {
		blob := man.ShardID(0, i).BlobName()
		if _, ok := s.Disk(ref.Node).PeekDurable(blob); !ok {
			continue
		}
		durable++
		if rot < 2 {
			if err = s.BitRot(ref.Node, blob); err != nil {
				t.Fatal(err)
			}
			rot++
		}
	}
	if durable != 5 || rot != 2 {
		t.Fatalf("setup: durable=%d rot=%d", durable, rot)
	}
	budget := sim.Budget{MinLive: 6, MaxDamagedDisks: 6, MaxDamagedPerGroup: 2, GroupOf: storage.StripeGroup}
	if err = budget.Check(s); err != nil {
		t.Fatal(err)
	}
	var getErr error
	done := false
	nodes[ids[0]].Get(man, func(_ storage.GetResult, e error) { getErr = e; done = true })
	if err = s.RunUntilQuiet(); err != nil {
		t.Fatal(err)
	}
	if !done || !errors.Is(getErr, storage.ErrUnavailable) || errors.Is(getErr, env.ErrIO) {
		t.Fatalf("GET: done=%v err=%v", done, getErr)
	}
	if err = budget.Check(s); err != nil {
		t.Fatal(err)
	}
	t.Logf("PUT acked with 5 durable shards; healed network; 2 permanent corruptions within m=2 budget; GET unavailable without transient IO: %v", getErr)
}
