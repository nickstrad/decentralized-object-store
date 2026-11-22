package e2e

import (
	"testing"
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
