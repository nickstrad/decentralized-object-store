package e2e

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

type parsedReplay struct {
	line string
	run  string
	seed string
}

var replayLineRe = regexp.MustCompile(`REPLAY: go test \S+ -run '([^']+)' -seed=(-?\d+) -count=1`)

func parseReplayLines(out string) []parsedReplay {
	var res []parsedReplay
	for _, line := range strings.Split(out, "\n") {
		m := replayLineRe.FindStringSubmatch(line)
		if len(m) == 3 {
			res = append(res, parsedReplay{line: strings.TrimSpace(line), run: m[1], seed: m[2]})
		}
	}
	return res
}

func cleanReplayEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "DST_SEED=") ||
			strings.HasPrefix(kv, "DST_SEEDS=") ||
			strings.HasPrefix(kv, "DST_SEED_BASE=") ||
			strings.HasPrefix(kv, "E2E_FULL_SEEDS=") ||
			strings.HasPrefix(kv, "E2E_PLANT_E8_FAIL=") ||
			strings.HasPrefix(kv, "E2E_PLANT_E5_FAIL=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

func runSelf(t *testing.T, extraEnv []string, args ...string) (string, error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = cleanReplayEnv(extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestReplayCommandsReproduceEverySeed plants failures across all four sweep
// shards and E5 failure-matrix cells, re-executes every printed REPLAY command,
// and proves explicit -seed runs that match zero seed subtests fail closed.
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
