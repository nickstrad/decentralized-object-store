package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

func TestIndependentStartup(t *testing.T) {
	t.Run("C10/failed-node-start/seed=821001", func(t *testing.T) {
		if marker := os.Getenv("CHECK_START_MARKER"); marker != "" {
			bin := buildObjnode(t, true, "e2efault")
			shim := filepath.Join(t.TempDir(), "start-one-less")
			script := "#!/bin/sh\nif [ \"$4\" = \"r2\" ]; then echo failed > " + strconv.Quote(marker) + "; exit 73; fi\nexec " + strconv.Quote(bin) + " \"$@\"\n"
			mustCheck(t, os.WriteFile(shim, []byte(script), 0700))
			binPath["true|e2efault"] = shim
			c := newRealCluster(t, 821001, realOptions{nodes: 3, layout: storage.Layout{K: 2, M: 1, StripeSize: realStripe}})
			fmt.Printf("CHECK_ACCEPTED_FAILED_START up=%d intended=3\n", len(upNodes(c)))
			return
		}
		marker := filepath.Join(t.TempDir(), "failed-node")
		cmd := exec.Command(os.Args[0], "-test.run", "^TestIndependentStartup$", "-test.v", "-test.count=1")
		cmd.Env = append(os.Environ(), "CHECK_START_MARKER="+marker)
		output, err := cmd.CombinedOutput()
		if _, e := os.Stat(marker); e != nil {
			t.Fatalf("SETUP: child did not exercise failed start: %v %s", err, output)
		}
		if strings.Contains(string(output), "CHECK_ACCEPTED_FAILED_START") {
			t.Fatalf("seed=821001 newRealCluster returned after one of three launches exited 73; startup accepted two live nodes as a complete cluster")
		}
		if err == nil {
			t.Fatalf("SETUP: child neither rejected nor accepted startup: %s", output)
		}
		t.Log("cluster construction rejected a launch failure")
	})
}
