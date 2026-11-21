// Command paniccheck requires every builtin panic call to be allowlisted.
//
//	paniccheck -root <dir> -allow <file> [dirs...]
//
// Allowlist lines: relative/file.go<TAB>EnclosingFunc<TAB>justification.
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
	"sort"
	"strings"

	"detlint/internal/report"
)

func main() {
	os.Exit(mainArgs(os.Args[1:]))
}

func mainArgs(args []string) int {
	fl := flag.NewFlagSet("paniccheck", flag.ContinueOnError)
	root := fl.String("root", "", "module root")
	allow := fl.String("allow", "", "allowlist file")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	if *root == "" || *allow == "" {
		fmt.Fprintln(os.Stderr, "usage: paniccheck -root <dir> -allow <allowlist> [dirs...]")
		return 2
	}
	fs, err := run(*root, *allow, fl.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, "paniccheck:", err)
		return 2
	}
	report.Print(os.Stdout, fs)
	return report.ExitCode(fs, nil)
}

type site struct {
	path, fn string
	pos      token.Position
}

// run finds builtin panic calls in non-test files under root/dirs (default
// "internal"; a missing directory has no panics) and matches them against
// the allowlist.
func run(root, allowPath string, dirs []string) ([]report.Finding, error) {
	if len(dirs) == 0 {
		dirs = []string{"internal"}
	}
	entries, out, err := report.ReadAllow(allowPath, 3)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	byDir := map[string][]string{}
	for _, d := range dirs {
		start := filepath.Join(root, d)
		if _, err := os.Stat(start); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(start, func(p string, de os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if de.IsDir() {
				if p != start && report.SkipDir(de.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				byDir[filepath.Dir(p)] = append(byDir[filepath.Dir(p)], p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	keys := make([]string, 0, len(byDir))
	for k := range byDir {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sites []site
	for _, dir := range keys {
		s, bad, err := scanDir(fset, root, byDir[dir])
		if err != nil {
			return nil, err
		}
		sites = append(sites, s...)
		out = append(out, bad...)
	}
	for _, s := range sites {
		ok := false
		for _, e := range entries {
			if e.Fields[0] == s.path && e.Fields[1] == s.fn {
				e.Used = true
				ok = true
			}
		}
		if !ok {
			out = append(out, report.Finding{Path: s.path, Line: s.pos.Line, Col: s.pos.Column, Rule: "panic",
				Msg: fmt.Sprintf("panic in %s is not in the allowlist (add: %s\\t%s\\t<justification>)", s.fn, s.path, s.fn)})
		}
	}
	out = append(out, report.Stale(allowPath, entries)...)
	return out, nil
}

// scanDir parses one directory's files (grouped by package name) and returns
// the builtin panic call sites. A call to "panic" counts unless the name
// resolves to a local declaration or a package-level declaration named panic.
func scanDir(fset *token.FileSet, root string, paths []string) ([]site, []report.Finding, error) {
	sort.Strings(paths)
	type parsed struct {
		f   *ast.File
		rel string
	}
	var files []parsed
	var bad []report.Finding
	shadowPkg := map[string]bool{}
	for _, p := range paths {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil, nil, err
		}
		rel = filepath.ToSlash(rel)
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			bad = append(bad, report.Finding{Path: rel, Line: 1, Col: 1, Rule: "parse", Msg: err.Error()})
			continue
		}
		files = append(files, parsed{f, rel})
		if declaresPanic(f) {
			shadowPkg[f.Name.Name] = true
		}
	}
	var sites []site
	for _, pf := range files {
		if shadowPkg[pf.f.Name.Name] {
			continue
		}
		for _, d := range pf.f.Decls {
			name := "-"
			if fd, ok := d.(*ast.FuncDecl); ok {
				name = report.FuncName(fd)
			}
			ast.Inspect(d, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := ast.Unparen(call.Fun).(*ast.Ident)
				if ok && id.Name == "panic" && id.Obj == nil {
					sites = append(sites, site{pf.rel, name, fset.Position(call.Pos())})
				}
				return true
			})
		}
	}
	return sites, bad, nil
}

// declaresPanic reports a package-level func, var, const or type named panic.
func declaresPanic(f *ast.File) bool {
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.Name == "panic" {
				return true
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.Name == "panic" {
							return true
						}
					}
				case *ast.TypeSpec:
					if s.Name.Name == "panic" {
						return true
					}
				}
			}
		}
	}
	return false
}
