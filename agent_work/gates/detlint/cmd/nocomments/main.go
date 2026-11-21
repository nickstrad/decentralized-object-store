// Command nocomments fails on any Go comment that is not a machine directive.
//
//	nocomments -root <dir> [-baseline <older tree>] [paths...]
//
// Exit status: 0 clean, 1 findings, 2 usage or internal error.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"detlint/internal/report"
)

func main() {
	os.Exit(mainArgs(os.Args[1:]))
}

func mainArgs(args []string) int {
	fl := flag.NewFlagSet("nocomments", flag.ContinueOnError)
	root := fl.String("root", "", "tree root")
	baseline := fl.String("baseline", "", "older tree: a comment whose exact text is in the same file there is accepted")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	if *root == "" {
		fmt.Fprintln(os.Stderr, "usage: nocomments -root <dir> [paths...]")
		return 2
	}
	baselineDir = *baseline
	fs, err := run(*root, fl.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, "nocomments:", err)
		return 2
	}
	report.Print(os.Stdout, fs)
	return report.ExitCode(fs, nil)
}

// baselineDir, when set, is an older copy of the tree. A comment already present in the
// same file there is not a finding, so the check then reports only comments that were added.
var baselineDir string

// baselineComments returns the comment texts of rel in the baseline tree (nil if absent).
func baselineComments(rel string) map[string]bool {
	if baselineDir == "" {
		return nil
	}
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(baselineDir, filepath.FromSlash(rel)), nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			set[c.Text] = true
		}
	}
	return set
}

var generated = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// run checks every .go file under root (or under the given paths, relative
// to root; a path may also name a single file). Directories named
// agent_work, kb, .git, testdata and vendor are skipped.
func run(root string, paths []string) ([]report.Finding, error) {
	if len(paths) == 0 {
		paths = []string{"."}
	}
	var out []report.Finding
	fset := token.NewFileSet()
	for _, p := range paths {
		start := filepath.Join(root, p)
		if _, err := os.Stat(start); err != nil {
			return nil, err
		}
		err := filepath.WalkDir(start, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != start && report.SkipDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			fs, err := checkFile(fset, path, filepath.ToSlash(rel))
			if err != nil {
				return err
			}
			out = append(out, fs...)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func checkFile(fset *token.FileSet, path, rel string) ([]report.Finding, error) {
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return []report.Finding{{Path: rel, Line: 1, Col: 1, Rule: "parse", Msg: err.Error()}}, nil
	}
	isTest := strings.HasSuffix(path, "_test.go")
	old := baselineComments(rel)
	var out []report.Finding
	for _, cg := range f.Comments {
		if isTest && exampleOutput(f, cg) {
			continue
		}
		for _, c := range cg.List {
			if allowed(c.Text) || old[c.Text] {
				continue
			}
			p := fset.Position(c.Pos())
			out = append(out, report.Finding{Path: rel, Line: p.Line, Col: p.Column, Rule: "comment", Msg: "comment is not a machine directive: " + clip(c.Text)})
		}
	}
	return out, nil
}

func allowed(text string) bool {
	return strings.HasPrefix(text, "//go:") || generated.MatchString(text)
}

// exampleOutput reports whether cg is an output block inside the body of an
// Example function: its first line is "// Output:" or "// Unordered output:".
func exampleOutput(f *ast.File, cg *ast.CommentGroup) bool {
	first := strings.TrimSpace(strings.TrimPrefix(cg.List[0].Text, "//"))
	if !strings.HasPrefix(cg.List[0].Text, "//") ||
		!(strings.HasPrefix(first, "Output:") || strings.HasPrefix(first, "Unordered output:")) {
		return false
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Body == nil || !strings.HasPrefix(fd.Name.Name, "Example") {
			continue
		}
		if fd.Body.Lbrace < cg.Pos() && cg.End() <= fd.Body.Rbrace {
			return true
		}
	}
	return false
}

func clip(s string) string {
	s = strings.SplitN(s, "\n", 2)[0]
	if len(s) > 60 {
		s = s[:60] + "..."
	}
	return s
}
