package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"detlint/internal/report"
)

func TestNocomments(t *testing.T) {
	cases := []struct {
		name  string
		root  string
		paths []string
		want  []string
	}{
		{name: "directives, example output and generated header pass", root: "testdata/clean"},
		{name: "every non-directive comment fails", root: "testdata/dirty", want: []string{
			"doc.go:1: comment",
			"go_space.go:3: comment",
			"inline.go:4: comment",
			"inline.go:5: comment",
			"nolint.go:4: comment",
			"output_nontest.go:4: comment",
			"output_test.go:4: comment",
			"output_test.go:5: comment",
			"sub/sub.go:3: comment",
		}},
		{name: "explicit path limits the walk", root: "testdata/dirty", paths: []string{"sub"},
			want: []string{"sub/sub.go:3: comment"}},
		{name: "explicit file", root: "testdata/dirty", paths: []string{"nolint.go"},
			want: []string{"nolint.go:4: comment"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs, err := run(c.root, c.paths)
			if err != nil {
				t.Fatal(err)
			}
			report.Sort(fs)
			var got []string
			for _, f := range fs {
				got = append(got, fmt.Sprintf("%s:%d: %s", f.Path, f.Line, f.Rule))
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("got %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestNocommentsExitCodes(t *testing.T) {
	cases := []struct {
		args []string
		want int
	}{
		{[]string{"-root", "testdata/clean"}, 0},
		{[]string{"-root", "testdata/dirty"}, 1},
		{[]string{}, 2},
		{[]string{"-root", "testdata/missing"}, 2},
	}
	for _, c := range cases {
		if got := mainArgs(c.args); got != c.want {
			t.Errorf("mainArgs(%q) = %d, want %d", c.args, got, c.want)
		}
	}
}

func TestBaselineAcceptsOnlyExistingComments(t *testing.T) {
	oldTree, newTree := t.TempDir(), t.TempDir()
	write := func(dir, src string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(oldTree, "package a\n\n// Kept explains x.\nvar x = 1\n")
	write(newTree, "package a\n\n// Kept explains x.\nvar x = 1\n\n// Added without the prose seat.\nvar y = 2\n")
	baselineDir = oldTree
	defer func() { baselineDir = "" }()
	fs, err := run(newTree, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].Line != 6 {
		t.Fatalf("want exactly the added comment on line 6, got %v", fs)
	}
	write(newTree, "package a\n\n// Kept explains x.\nvar x = 3\n")
	if fs, err = run(newTree, nil); err != nil || len(fs) != 0 {
		t.Fatalf("a code change that keeps existing comments must pass, got %v %v", fs, err)
	}
}
