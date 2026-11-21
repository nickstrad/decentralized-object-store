package main

import (
	"bufio"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

func init() {
	// Pure-Go file selection everywhere: cgo files are skipped, and the
	// source importer never shells out to cgo for net and friends.
	build.Default.CgoEnabled = false
}

var (
	stdOnce sync.Once
	stdImp  types.ImporterFrom
)

// stdImporter type-checks non-module imports (stdlib and module
// dependencies) from source. It is shared across runs as a cache.
func stdImporter() types.ImporterFrom {
	stdOnce.Do(func() {
		stdImp = importer.ForCompiler(token.NewFileSet(), "source", nil).(types.ImporterFrom)
	})
	return stdImp
}

// readModulePath returns the module path declared in root/go.mod.
func readModulePath(root string) (string, error) {
	f, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "module"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
			p := strings.Trim(strings.TrimSpace(rest), "\"")
			if p != "" {
				return p, nil
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", errors.New("no module line in go.mod")
}

// loader resolves imports of the root module from source and everything else
// through the shared source importer.
type loader struct {
	fset    *token.FileSet
	root    string
	modPath string
	cache   map[string]*types.Package
	errs    map[string]error
	loading map[string]bool
	// override maps an import path to a package to use instead (the package
	// under test, with its in-package test files, for an external test).
	override map[string]*types.Package
}

func (l *loader) Import(path string) (*types.Package, error) {
	return l.ImportFrom(path, l.root, 0)
}

func (l *loader) ImportFrom(path, dir string, mode types.ImportMode) (*types.Package, error) {
	if p, ok := l.override[path]; ok {
		return p, nil
	}
	if rel, ok := l.localRel(path); ok {
		return l.loadLocal(path, rel)
	}
	return stdImporter().ImportFrom(path, dir, mode)
}

func (l *loader) localRel(path string) (string, bool) {
	if path == l.modPath {
		return ".", true
	}
	if rest, ok := strings.CutPrefix(path, l.modPath+"/"); ok {
		return rest, true
	}
	return "", false
}

func (l *loader) importPath(rel string) string {
	if rel == "." || rel == "" {
		return l.modPath
	}
	return l.modPath + "/" + filepath.ToSlash(rel)
}

func (l *loader) loadLocal(path, rel string) (*types.Package, error) {
	if p, ok := l.cache[path]; ok {
		return p, nil
	}
	if err, ok := l.errs[path]; ok {
		return nil, err
	}
	if l.loading[path] {
		return nil, fmt.Errorf("import cycle through %s", path)
	}
	l.loading[path] = true
	defer delete(l.loading, path)
	files, _, err := parseDir(l.fset, filepath.Join(l.root, rel), false)
	if err != nil {
		l.errs[path] = err
		return nil, err
	}
	if len(files) == 0 {
		err := fmt.Errorf("no Go files for %s", path)
		l.errs[path] = err
		return nil, err
	}
	var first error
	conf := types.Config{Importer: l, Error: func(e error) {
		if first == nil {
			first = e
		}
	}}
	pkg, _ := conf.Check(path, l.fset, files, nil)
	if first != nil {
		err := fmt.Errorf("type errors in %s: %v", path, first)
		l.errs[path] = err
		return nil, err
	}
	l.cache[path] = pkg
	return pkg, nil
}

// parseDir parses the buildable .go files of dir. With tests it also returns
// _test.go files; files of an external test package come back separately.
func parseDir(fset *token.FileSet, dir string, tests bool) (pkg, xtest []*ast.File, err error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var names []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") {
			continue
		}
		if strings.HasSuffix(n, "_test.go") && !tests {
			continue
		}
		ok, err := build.Default.MatchFile(dir, n)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, nil, err
		}
		if strings.HasSuffix(n, "_test.go") && strings.HasSuffix(f.Name.Name, "_test") {
			xtest = append(xtest, f)
		} else {
			pkg = append(pkg, f)
		}
	}
	return pkg, xtest, nil
}

// expand turns pkg-dir arguments into a sorted list of existing directories
// relative to root. "dir/..." walks recursively, skipping testdata, vendor
// and hidden or underscore directories. Missing directories are skipped.
func expand(root string, args []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(rel string) {
		rel = filepath.Clean(rel)
		if !seen[rel] {
			seen[rel] = true
			out = append(out, rel)
		}
	}
	for _, a := range args {
		a = filepath.ToSlash(a)
		recursive := false
		if a == "..." || strings.HasSuffix(a, "/...") {
			recursive = true
			a = strings.TrimSuffix(strings.TrimSuffix(a, "..."), "/")
			if a == "" {
				a = "."
			}
		}
		if filepath.IsAbs(a) {
			return nil, fmt.Errorf("package dir %q must be relative to -root", a)
		}
		abs := filepath.Join(root, a)
		st, err := os.Stat(abs)
		if err != nil || !st.IsDir() {
			continue
		}
		if !recursive {
			add(a)
			continue
		}
		err = filepath.WalkDir(abs, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				return nil
			}
			n := d.Name()
			if p != abs && (n == "testdata" || n == "vendor" || strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_")) {
				return filepath.SkipDir
			}
			if p != abs {
				if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			add(rel)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}
