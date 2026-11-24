package telemetry_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nickstrad/object-store/internal/metrics"
	"github.com/nickstrad/object-store/internal/telemetry"
	"github.com/nickstrad/object-store/internal/telemetry/adaptertest"
)

func TestCSVContract(t *testing.T) {
	adaptertest.Run(t, func(t *testing.T) adaptertest.Fixture {
		dir := filepath.Join(t.TempDir(), "metrics")
		raw, _ := json.Marshal(map[string]any{"directory": dir, "sync": false})
		a, err := telemetry.Open(context.Background(), telemetry.Config{Enabled: true, Adapter: "csv", Settings: raw}, adaptertest.Identity())
		if err != nil {
			t.Fatal(err)
		}
		failing := &recoveringCSV{Adapter: a}
		return adaptertest.Fixture{Adapter: failing, Readback: func(context.Context) (metrics.Batch, error) {
			batches, err := telemetry.ReadCSVDir(dir)
			if err != nil {
				return metrics.Batch{}, err
			}
			if len(batches) == 0 {
				return metrics.Batch{}, fmt.Errorf("no batch")
			}
			return batches[len(batches)-1], nil
		}, FailNext: func() {
			// The next publish fails because the directory was replaced with a file.
			moved := dir + "-saved"
			if err := os.Rename(dir, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dir, []byte("blocked"), 0600); err != nil {
				t.Fatal(err)
			}
			// A wrapper restores it immediately after observing the actual sink error.
			failing.restore = func() { _ = os.Remove(dir); _ = os.Rename(moved, dir) }
		}}
	})
}

type recoveringCSV struct {
	telemetry.Adapter
	restore func()
}

func (a *recoveringCSV) Publish(ctx context.Context, b metrics.Batch) error {
	err := a.Adapter.Publish(ctx, b)
	if a.restore != nil {
		a.restore()
		a.restore = nil
	}
	return err
}
