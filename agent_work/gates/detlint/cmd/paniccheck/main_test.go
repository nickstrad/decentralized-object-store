package main

import (
	"fmt"
	"slices"
	"testing"

	"detlint/internal/report"
)

func TestPaniccheck(t *testing.T) {
	cases := []struct {
		name  string
		allow string
		dirs  []string
		want  []string
	}{
		{name: "all panics allowlisted", allow: "testdata/allow-ok.txt"},
		{name: "unmatched, stale, empty justification and malformed", allow: "testdata/allow-bad.txt", want: []string{
			"internal/a/a.go:8: panic",
			"internal/a/a.go:25: panic",
			"testdata/allow-bad.txt:3: allow-format",
			"testdata/allow-bad.txt:4: stale-allow",
			"testdata/allow-bad.txt:5: allow-format",
		}},
		{name: "explicit dirs reach cmd", allow: "testdata/allow-ok.txt", dirs: []string{"internal", "cmd"}, want: []string{
			"cmd/tool/main.go:3: panic",
		}},
		{name: "missing dir has no panics, so entries are stale", allow: "testdata/allow-ok.txt", dirs: []string{"nope"}, want: []string{
			"testdata/allow-ok.txt:3: stale-allow",
			"testdata/allow-ok.txt:4: stale-allow",
			"testdata/allow-ok.txt:5: stale-allow",
			"testdata/allow-ok.txt:6: stale-allow",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs, err := run("testdata/mod", c.allow, c.dirs)
			if err != nil {
				t.Fatal(err)
			}
			report.Sort(fs)
			var got []string
			for _, f := range fs {
				got = append(got, fmt.Sprintf("%s:%d: %s", f.Path, f.Line, f.Rule))
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("got %q\nwant %q\nfindings: %v", got, c.want, fs)
			}
		})
	}
}

func TestPaniccheckExitCodes(t *testing.T) {
	cases := []struct {
		args []string
		want int
	}{
		{[]string{"-root", "testdata/mod", "-allow", "testdata/allow-ok.txt"}, 0},
		{[]string{"-root", "testdata/mod", "-allow", "testdata/allow-bad.txt"}, 1},
		{[]string{"-root", "testdata/mod"}, 2},
		{[]string{"-root", "testdata/mod", "-allow", "testdata/missing.txt"}, 2},
	}
	for _, c := range cases {
		if got := mainArgs(c.args); got != c.want {
			t.Errorf("mainArgs(%q) = %d, want %d", c.args, got, c.want)
		}
	}
}
