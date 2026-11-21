// Command astdiff proves that two trees differ only in non-directive
// comments and formatting.
//
//	astdiff <treeA> <treeB>
//
// Exit status: 0 same code, 1 findings, 2 usage or internal error.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"detlint/internal/report"
)

func main() {
	os.Exit(mainArgs(os.Args[1:]))
}

func mainArgs(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: astdiff <treeA> <treeB>")
		return 2
	}
	fs, err := run(args[0], args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "astdiff:", err)
		return 2
	}
	report.Print(os.Stdout, fs)
	return report.ExitCode(fs, nil)
}

// run compares every .go file of both trees. Finding paths are relative to
// the trees; positions refer to treeB unless the file only exists in treeA.
func run(a, b string) ([]report.Finding, error) {
	fa, err := goFiles(a)
	if err != nil {
		return nil, err
	}
	fb, err := goFiles(b)
	if err != nil {
		return nil, err
	}
	var out []report.Finding
	all := map[string]bool{}
	for r := range fa {
		all[r] = true
	}
	for r := range fb {
		all[r] = true
	}
	rels := make([]string, 0, len(all))
	for r := range all {
		rels = append(rels, r)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		switch {
		case !fb[rel]:
			out = append(out, report.Finding{Path: rel, Line: 1, Col: 1, Rule: "astdiff", Msg: "file removed (only in " + a + ")"})
		case !fa[rel]:
			out = append(out, report.Finding{Path: rel, Line: 1, Col: 1, Rule: "astdiff", Msg: "file added (only in " + b + ")"})
		default:
			if f := compareFile(filepath.Join(a, rel), filepath.Join(b, rel), rel); f != nil {
				out = append(out, *f)
			}
		}
	}
	return out, nil
}

func goFiles(root string) (map[string]bool, error) {
	st, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}
	out := map[string]bool{}
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && report.SkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			out[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	return out, err
}

func compareFile(pa, pb, rel string) *report.Finding {
	fset := token.NewFileSet()
	mode := parser.ParseComments | parser.SkipObjectResolution
	fa, err := parser.ParseFile(fset, pa, nil, mode)
	if err != nil {
		return &report.Finding{Path: rel, Line: 1, Col: 1, Rule: "astdiff", Msg: "parse error in A: " + err.Error()}
	}
	fb, err := parser.ParseFile(fset, pb, nil, mode)
	if err != nil {
		return &report.Finding{Path: rel, Line: 1, Col: 1, Rule: "astdiff", Msg: "parse error in B: " + err.Error()}
	}
	at := func(pos token.Pos, msg string) *report.Finding {
		p := fset.Position(pos)
		if !pos.IsValid() {
			p.Line, p.Column = 1, 1
		}
		return &report.Finding{Path: rel, Line: p.Line, Col: p.Column, Rule: "astdiff", Msg: msg}
	}
	d := &differ{at: fb.Package}
	if !d.eq(reflect.ValueOf(fa), reflect.ValueOf(fb)) {
		return at(d.at, "code differs")
	}
	sa, sb := significant(fa, pa), significant(fb, pb)
	for i := 0; i < len(sa) || i < len(sb); i++ {
		switch {
		case i >= len(sa):
			return at(sb[i].pos, "directive added: "+sb[i].text)
		case i >= len(sb):
			return at(fb.Package, "directive removed: "+sa[i].text)
		case sa[i].text != sb[i].text || sa[i].anchor != sb[i].anchor:
			return at(sb[i].pos, "directive changed or moved: "+sb[i].text)
		}
	}
	return nil
}

// sig is a comment that changes the build or test result: a directive or an
// Example output block. anchor places it: -1 before the package clause,
// otherwise 2*i for "before top-level declaration i" or 2*i+1 for "inside
// declaration i".
type sig struct {
	text   string
	anchor int
	pos    token.Pos
}

func significant(f *ast.File, path string) []sig {
	isTest := strings.HasSuffix(path, "_test.go")
	var out []sig
	for _, cg := range f.Comments {
		anchor := anchorOf(f, cg.Pos())
		if isTest && exampleOutput(f, cg) {
			var lines []string
			for _, c := range cg.List {
				lines = append(lines, strings.TrimRight(c.Text, " \t"))
			}
			out = append(out, sig{strings.Join(lines, "\n"), anchor, cg.Pos()})
			continue
		}
		for _, c := range cg.List {
			if isDirective(c.Text) {
				out = append(out, sig{strings.TrimRight(c.Text, " \t"), anchor, c.Pos()})
			}
		}
	}
	return out
}

func isDirective(t string) bool {
	for _, p := range []string{"//go:", "// +build", "//line ", "/*line ", "//export ", "//extern ", "//sys "} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return strings.HasPrefix(t, "// Code generated ") && strings.HasSuffix(t, " DO NOT EDIT.")
}

func anchorOf(f *ast.File, pos token.Pos) int {
	if pos < f.Package {
		return -1
	}
	for i, d := range f.Decls {
		if pos < d.Pos() {
			return 2 * i
		}
		if pos < d.End() {
			return 2*i + 1
		}
	}
	return 2 * len(f.Decls)
}

func exampleOutput(f *ast.File, cg *ast.CommentGroup) bool {
	first := strings.TrimSpace(strings.TrimPrefix(cg.List[0].Text, "//"))
	if !strings.HasPrefix(cg.List[0].Text, "//") ||
		!(strings.HasPrefix(first, "Output:") || strings.HasPrefix(first, "Unordered output:")) {
		return false
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if ok && fd.Recv == nil && fd.Body != nil && strings.HasPrefix(fd.Name.Name, "Example") &&
			fd.Body.Lbrace < cg.Pos() && cg.End() <= fd.Body.Rbrace {
			return true
		}
	}
	return false
}

var (
	posType   = reflect.TypeOf(token.Pos(0))
	cgType    = reflect.TypeOf((*ast.CommentGroup)(nil))
	cgsType   = reflect.TypeOf([]*ast.CommentGroup(nil))
	objType   = reflect.TypeOf((*ast.Object)(nil))
	scopeType = reflect.TypeOf((*ast.Scope)(nil))
)

// differ compares two ASTs structurally, ignoring positions, comments and
// resolver objects. at is the position of the innermost B-side node visited
// when a difference was found.
type differ struct {
	at token.Pos
}

func skipType(t reflect.Type) bool {
	return t == posType || t == cgType || t == cgsType || t == objType || t == scopeType
}

func (d *differ) eq(a, b reflect.Value) bool {
	if a.Type() != b.Type() {
		return false
	}
	switch a.Kind() {
	case reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		return d.eq(a.Elem(), b.Elem())
	case reflect.Pointer:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		if n, ok := b.Interface().(ast.Node); ok && n.Pos().IsValid() {
			d.at = n.Pos()
		}
		return d.eq(a.Elem(), b.Elem())
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if skipType(a.Type().Field(i).Type) {
				continue
			}
			if !d.eq(a.Field(i), b.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Slice:
		if a.Len() != b.Len() {
			return false
		}
		for i := 0; i < a.Len(); i++ {
			if !d.eq(a.Index(i), b.Index(i)) {
				return false
			}
		}
		return true
	case reflect.String:
		return a.String() == b.String()
	case reflect.Bool:
		return a.Bool() == b.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return a.Int() == b.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return a.Uint() == b.Uint()
	}
	// Any other kind (maps, funcs, chans) does not occur in go/ast; treat it
	// as a difference so nothing passes unexamined.
	return false
}
