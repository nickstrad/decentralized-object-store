package e2e

import (
	"testing"
	"time"
)

var e1SizesS = []int{0, 1, 2, 3, 7, 13, 4093, simStripe - 1, simStripe, simStripe + 1, 3*simStripe + 7, 2<<20 + 29}

func TestE1MultiRegionPutGetS(t *testing.T) {
	t.Parallel()
	var m measure
	t.Cleanup(func() { m.report(t) })
	sweep(t, seedsFor(t, 1), func(t *testing.T, seed int64) {
		var res e1Result
		runSim(t, seed, defaultSimOptions(), func(c *simCluster) error {
			var err error
			res, err = scenarioE1(c, simBounds, e1Params{sizes: e1SizesS})
			return err
		})
		m.add("e1/objects", int64(res.objects))
		m.add("e1/reads", int64(res.reads))
		m.add("e1/stripes_spread_checked", int64(res.stripes))
		m.ms("e1/put_max", res.putTime)
		m.ms("e1/get_max", res.getTime)
	})
}

func TestE2PartitionAndHealS(t *testing.T) {
	t.Parallel()
	var m measure
	t.Cleanup(func() { m.report(t) })
	sweep(t, seedsFor(t, 1), func(t *testing.T, seed int64) {
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

func TestE3BitRotRepairS(t *testing.T) {
	t.Parallel()
	var m measure
	t.Cleanup(func() { m.report(t) })
	sweep(t, seedsFor(t, 1), func(t *testing.T, seed int64) {
		var res e3Result
		runSim(t, seed, defaultSimOptions(), func(c *simCluster) error {
			var err error
			res, err = scenarioE3(c, simBounds, e3Params{objects: 16, damaged: 2, size: 3*simStripe + 100})
			return err
		})
		m.ms("e3/repair_under_reads", res.repairUnderReads)
		m.ms("e3/repair", res.repair)
		m.add("e3/shards_rotted", int64(res.shardsRotted))
		m.add("e3/damaged_bytes", int64(res.damagedBytes))
		m.add("e3/bucket_bytes", int64(res.bucketBytes))
		if res.damagedBytes > 0 {
			m.add("e3/rebuilt_per_damaged_ppm", int64(res.rebuiltBytes*1_000_000/uint64(res.damagedBytes)))
			m.add("e3/moved_per_damaged_ppm", int64(res.movedBytes*1_000_000/uint64(res.damagedBytes)))
		}
	})
}

func TestE6CrashRestartWipeS(t *testing.T) {
	t.Parallel()
	var m measure
	t.Cleanup(func() { m.report(t) })
	sweep(t, seedsFor(t, 1), func(t *testing.T, seed int64) {
		var res e6Result
		var oc outcome
		runSim(t, seed, defaultSimOptions(), func(c *simCluster) error {
			var err error
			res, err = scenarioE6(c, simBounds, e6Params{perWave: 8, size: 20000, down: 4 * time.Second, settle: 30 * time.Millisecond})
			if err != nil {
				return err
			}
			_, oc, err = c.finalChecks(true)
			return err
		})
		m.add("e6/acked", int64(res.acked))
		m.add("e6/unavailable", int64(res.failed))
		m.ms("e6/rejoin", res.rejoin)
		m.ms("e6/catch_up", res.catchUp)
		m.ms("e6/refill", res.refill)
		m.add("e6/victim_shards", int64(res.victimShards))
		m.add("e6/wiped_shards", int64(res.wipedShards))
		m.add("e6/inc_before", int64(res.incBefore))
		m.add("e6/inc_after", int64(res.incAfter))
		m.add("e6/live_lost", int64(oc.lost))
	})
}

func TestE8LocalFirstMetadataSyncS(t *testing.T) {
	t.Parallel()
	var m measure
	t.Cleanup(func() { m.report(t) })
	sweep(t, seedsFor(t, 1), func(t *testing.T, seed int64) {
		var res e8Result
		var tally map[string]int
		opts := defaultSimOptions()
		opts.skew, opts.driftP = 2*time.Second, 500
		runSim(t, seed, opts, func(c *simCluster) error {
			var err error
			res, err = scenarioE8(c, simBounds, e8Params{objects: 6, detect: 500 * time.Millisecond})
			if err != nil {
				return err
			}
			tally, _, err = c.finalChecks(false)
			return err
		})
		m.ms("e8/converge", res.converge)
		m.add("e8/conditional_writes", int64(res.conditional))
		m.add("e8/contested_current", int64(res.contested.current))
		m.add("e8/contested_discarded", int64(res.contested.discarded))
		m.add("e8/discards_max_node", int64(res.discards))
		m.add("e8/stale_refused", int64(res.staleRefused))
		m.add("e8/oracle_discarded", int64(tally["discarded"]))
	})
}
