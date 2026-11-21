package main

import (
	"fmt"
	"slices"
	"testing"
)

// short renders a finding as path:line: rule for comparison.
func shortFindings(t *testing.T, cfg config) []string {
	t.Helper()
	res, err := run(cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var out []string
	for _, f := range res.findings {
		out = append(out, fmt.Sprintf("%s:%d: %s", f.Path, f.Line, f.Rule))
	}
	return out
}

func TestDetlint(t *testing.T) {
	cases := []struct {
		name  string
		pkgs  []string
		tests bool
		allow string
		want  []string
	}{
		// wallclock
		{name: "time aliased import", pkgs: []string{"wallclock/alias"},
			want: []string{"wallclock/alias/a.go:6: wallclock"}},
		{name: "time dot import", pkgs: []string{"wallclock/dot"},
			want: []string{"wallclock/dot/a.go:6: wallclock"}},
		{name: "time.Now method value", pkgs: []string{"wallclock/methodvalue"},
			want: []string{"wallclock/methodvalue/a.go:6: wallclock"}},
		{name: "time.Now passed as argument", pkgs: []string{"wallclock/funcarg"},
			want: []string{"wallclock/funcarg/a.go:10: wallclock"}},
		{name: "context.WithTimeout", pkgs: []string{"wallclock/ctxtimeout"},
			want: []string{"wallclock/ctxtimeout/a.go:9: wallclock"}},
		{name: "time.Duration arithmetic passes", pkgs: []string{"clean/duration"}},
		{name: "local module import resolves", pkgs: []string{"clean/localimport"}},

		// globalrand / cryptorand
		{name: "global rand via aliased math/rand/v2", pkgs: []string{"globalrand/aliasv2"},
			want: []string{"globalrand/aliasv2/a.go:6: globalrand", "globalrand/aliasv2/a.go:10: globalrand"}},
		{name: "global rand math/rand v1", pkgs: []string{"globalrand/v1"},
			want: []string{"globalrand/v1/a.go:6: globalrand"}},
		{name: "seeded *rand.Rand methods pass", pkgs: []string{"clean/seeded"}},
		{name: "crypto/rand", pkgs: []string{"cryptorand"},
			want: []string{"cryptorand/a.go:3: cryptorand"}},

		// concurrency
		{name: "select", pkgs: []string{"concurrency/selectstmt"},
			want: []string{"concurrency/selectstmt/a.go:4: concurrency"}},
		{name: "go statement", pkgs: []string{"concurrency/gostmt"},
			want: []string{"concurrency/gostmt/a.go:6: concurrency"}},
		{name: "channel make send receive close", pkgs: []string{"concurrency/channel"},
			want: []string{
				"concurrency/channel/a.go:4: concurrency",
				"concurrency/channel/a.go:5: concurrency",
				"concurrency/channel/a.go:6: concurrency",
				"concurrency/channel/a.go:7: concurrency",
			}},
		{name: "sync/atomic", pkgs: []string{"concurrency/atomic"},
			want: []string{"concurrency/atomic/a.go:5: concurrency", "concurrency/atomic/a.go:8: concurrency"}},
		{name: "sync.Mutex", pkgs: []string{"concurrency/mutex"},
			want: []string{
				"concurrency/mutex/a.go:6: concurrency",
				"concurrency/mutex/a.go:11: concurrency",
				"concurrency/mutex/a.go:13: concurrency",
			}},

		// procstate
		{name: "hash/maphash", pkgs: []string{"procstate/maphash"},
			want: []string{"procstate/maphash/a.go:3: procstate"}},
		{name: "os.Getpid", pkgs: []string{"procstate/getpid"},
			want: []string{"procstate/getpid/a.go:6: procstate"}},
		{name: "unsafe runtime reflect", pkgs: []string{"procstate/misc"},
			want: []string{
				"procstate/misc/a.go:6: procstate",
				"procstate/misc/a.go:10: procstate",
				"procstate/misc/a.go:14: procstate",
			}},

		// realio
		{name: "os file function", pkgs: []string{"realio/osfile"},
			want: []string{"realio/osfile/a.go:6: realio"}},
		{name: "net import", pkgs: []string{"realio/netimport"},
			want: []string{"realio/netimport/a.go:3: realio"}},

		// ptrfmt
		{name: "%p in format", pkgs: []string{"ptrfmt/pos"},
			want: []string{"ptrfmt/pos/a.go:11: ptrfmt", "ptrfmt/pos/a.go:15: ptrfmt"}},
		{name: "no %p passes", pkgs: []string{"ptrfmt/clean"}},

		// maprange
		{name: "map range feeding output", pkgs: []string{"maprange/output"},
			want: []string{"maprange/output/a.go:7: maprange", "maprange/output/a.go:15: maprange"}},
		{name: "collect without sort", pkgs: []string{"maprange/unsortedcollect"},
			want: []string{"maprange/unsortedcollect/a.go:5: maprange"}},
		{name: "maps.Keys iteration", pkgs: []string{"maprange/mapskeys"},
			want: []string{"maprange/mapskeys/a.go:6: maprange"}},
		{name: "collect and sort passes", pkgs: []string{"clean/sorted"}},

		// float
		{name: "float in a decision", pkgs: []string{"float/decision"},
			want: []string{"float/decision/a.go:4: float", "float/decision/a.go:5: float"}},
		{name: "float field", pkgs: []string{"float/field"},
			want: []string{"float/field/a.go:4: float", "float/field/a.go:8: float"}},

		// allowlist
		{name: "unallowed finding", pkgs: []string{"allowed"},
			want: []string{"allowed/a.go:5: maprange"}},
		{name: "allowlisted finding passes", pkgs: []string{"allowed"}, allow: "testdata/allow-ok.txt"},
		{name: "stale allowlist entry fails", pkgs: []string{"allowed"}, allow: "testdata/allow-stale.txt",
			want: []string{"testdata/allow-stale.txt:2: stale-allow"}},
		{name: "empty justification fails", pkgs: []string{"allowed"}, allow: "testdata/allow-empty.txt",
			want: []string{"allowed/a.go:5: maprange", "testdata/allow-empty.txt:1: allow-format"}},

		// loading
		{name: "type error is a finding", pkgs: []string{"typeerr"},
			want: []string{"typeerr/a.go:4: typecheck"}},
		{name: "missing dir skipped", pkgs: []string{"does/not/exist"}},
		{name: "test files skipped by default", pkgs: []string{"withtests"}},
		{name: "test files with -tests", pkgs: []string{"withtests"}, tests: true,
			want: []string{
				"withtests/a_test.go:9: wallclock",
				"withtests/x_test.go:11: concurrency",
				"withtests/x_test.go:12: concurrency",
			}},
		{name: "recursive clean tree", pkgs: []string{"clean/..."}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := shortFindings(t, config{root: "testdata/mod", pkgs: c.pkgs, tests: c.tests, allow: c.allow})
			if !slices.Equal(got, c.want) {
				t.Errorf("findings:\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestDetlintCounts(t *testing.T) {
	res, err := run(config{root: "testdata/mod", pkgs: []string{"clean/...", "nope"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.pkgs != 4 || res.files != 4 {
		t.Errorf("got %d files in %d packages, want 4 in 4", res.files, res.pkgs)
	}
}

func TestDetlintExitCodes(t *testing.T) {
	cases := []struct {
		args []string
		want int
	}{
		{[]string{"-root", "testdata/mod", "clean/..."}, 0},
		{[]string{"-root", "testdata/mod", "wallclock/alias"}, 1},
		{[]string{"-root", "testdata/mod"}, 2},
		{[]string{"-root", "testdata/nomodule", "x"}, 2},
	}
	for _, c := range cases {
		if got := mainArgs(c.args); got != c.want {
			t.Errorf("mainArgs(%q) = %d, want %d", c.args, got, c.want)
		}
	}
}

func TestHasPtrVerb(t *testing.T) {
	cases := map[string]bool{
		"%p": true, "%-8p": true, "%[1]p": true, "%#p": true, "x %d %p": true,
		"%%p": false, "%v": false, "%5.2s": false, "p%": false, "": false, "%%%p": true,
	}
	for in, want := range cases {
		if got := hasPtrVerb(in); got != want {
			t.Errorf("hasPtrVerb(%q) = %v, want %v", in, got, want)
		}
	}
}
