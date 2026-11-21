package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Trees are written from strings at test time because several variants are
// deliberately not gofmt-formatted.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const base = `//go:build linux

package p

import _ "embed"

//go:embed data.txt
var data string

const greeting = "hello"

//go:noinline
func F(a, b int) int {
	return a + b*0x10
}

func G() []int {
	return []int{1, 2}
}
`

const baseTest = `package p

import "fmt"

func ExampleF() {
	fmt.Println(F(1, 2))
	// Output: 33
}
`

func tree(overrides map[string]string) map[string]string {
	m := map[string]string{"p/p.go": base, "p/p_test.go": baseTest, "p/data.txt": "x"}
	for k, v := range overrides {
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return m
}

func TestAstdiff(t *testing.T) {
	cases := []struct {
		name string
		b    map[string]string
		want []string
	}{
		{name: "identical", b: tree(nil)},
		{name: "comments added and formatting changed", b: tree(map[string]string{"p/p.go": `//go:build linux

// Package p is documented now.
package p

import _ "embed"

// data is embedded.
//
//go:embed data.txt
var data string

const greeting   =   "hello" // trailing

/* block comment */

//go:noinline
func F(a, b int) int {

	// explain
	return a + b*0x10
}

func G() []int {
	return []int{
		1,
		2,
	}
}
`})},
		{name: "non-Go file change ignored", b: tree(map[string]string{"p/data.txt": "changed"})},
		{name: "excluded dir ignored", b: tree(map[string]string{"agent_work/x.go": "package x\n", "p/testdata/y.go": "package y\n"})},
		{name: "changed string literal", b: tree(map[string]string{"p/p.go": replace(base, `"hello"`, `"hellO"`)}),
			want: []string{"p/p.go:10: astdiff"}},
		{name: "changed int literal spelling", b: tree(map[string]string{"p/p.go": replace(base, "0x10", "16")}),
			want: []string{"p/p.go:14: astdiff"}},
		{name: "added parentheses", b: tree(map[string]string{"p/p.go": replace(base, "a + b*0x10", "(a + b*0x10)")}),
			want: []string{"p/p.go:14: astdiff"}},
		{name: "renamed identifier", b: tree(map[string]string{"p/p.go": replace(base, "func G()", "func H()")}),
			want: []string{"p/p.go:17: astdiff"}},
		{name: "changed go:build", b: tree(map[string]string{"p/p.go": replace(base, "//go:build linux", "//go:build darwin")}),
			want: []string{"p/p.go:1: astdiff"}},
		{name: "changed go:embed", b: tree(map[string]string{"p/p.go": replace(base, "//go:embed data.txt", "//go:embed other.txt")}),
			want: []string{"p/p.go:7: astdiff"}},
		{name: "moved go:noinline", b: tree(map[string]string{"p/p.go": replace(replace(base, "//go:noinline\n", ""), "func G()", "//go:noinline\nfunc G()")}),
			want: []string{"p/p.go:16: astdiff"}},
		{name: "removed directive", b: tree(map[string]string{"p/p.go": replace(base, "//go:noinline\n", "")}),
			want: []string{"p/p.go:3: astdiff"}},
		{name: "changed example output", b: tree(map[string]string{"p/p_test.go": replace(baseTest, "Output: 33", "Output: 34")}),
			want: []string{"p/p_test.go:7: astdiff"}},
		{name: "added file", b: tree(map[string]string{"p/q.go": "package p\n"}),
			want: []string{"p/q.go:1: astdiff"}},
		{name: "removed file", b: tree(map[string]string{"p/p_test.go": ""}),
			want: []string{"p/p_test.go:1: astdiff"}},
		{name: "parse error", b: tree(map[string]string{"p/p.go": base + "func {"}),
			want: []string{"p/p.go:1: astdiff"}},
	}
	a := writeTree(t, tree(nil))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := writeTree(t, c.b)
			fs, err := run(a, b)
			if err != nil {
				t.Fatal(err)
			}
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

func TestAstdiffExitCodes(t *testing.T) {
	a := writeTree(t, tree(nil))
	b := writeTree(t, tree(map[string]string{"p/p.go": replace(base, `"hello"`, `"bye"`)}))
	cases := []struct {
		args []string
		want int
	}{
		{[]string{a, a}, 0},
		{[]string{a, b}, 1},
		{[]string{a}, 2},
		{[]string{a, filepath.Join(a, "missing")}, 2},
	}
	for _, c := range cases {
		if got := mainArgs(c.args); got != c.want {
			t.Errorf("mainArgs(%q) = %d, want %d", c.args, got, c.want)
		}
	}
}

func replace(s, old, new string) string {
	i := strings.Index(s, old)
	if i < 0 {
		panic("fixture text not found: " + old)
	}
	return s[:i] + new + s[i+len(old):]
}
