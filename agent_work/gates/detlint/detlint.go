package main

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"

	"detlint/internal/report"
)

type config struct {
	root  string
	tests bool
	allow string
	pkgs  []string
}

type result struct {
	findings []report.Finding
	files    int
	pkgs     int
}

// run checks the configured packages and returns findings after allowlist
// filtering. An error means the run itself could not proceed (exit 2).
func run(cfg config) (result, error) {
	var res result
	root, err := filepath.Abs(cfg.root)
	if err != nil {
		return res, err
	}
	modPath, err := readModulePath(root)
	if err != nil {
		return res, fmt.Errorf("reading module path: %w", err)
	}
	dirs, err := expand(root, cfg.pkgs)
	if err != nil {
		return res, err
	}
	fset := token.NewFileSet()
	l := &loader{fset: fset, root: root, modPath: modPath,
		cache: map[string]*types.Package{}, errs: map[string]error{}, loading: map[string]bool{}}
	var raw []rawFinding
	for _, rel := range dirs {
		dir := filepath.Join(root, rel)
		files, xtest, err := parseDir(fset, dir, cfg.tests)
		if err != nil {
			raw = append(raw, rawFinding{report.Finding{Path: filepath.ToSlash(rel), Line: 1, Col: 1, Rule: "parse", Msg: err.Error()}, "-"})
			continue
		}
		if len(files) == 0 && len(xtest) == 0 {
			continue
		}
		path := l.importPath(rel)
		var under *types.Package
		if len(files) > 0 {
			res.pkgs++
			res.files += len(files)
			var pf []rawFinding
			under, pf = checkAndLint(l, root, path, files)
			raw = append(raw, pf...)
		}
		if len(xtest) > 0 {
			res.pkgs++
			res.files += len(xtest)
			if under != nil {
				l.override = map[string]*types.Package{path: under}
			}
			_, pf := checkAndLint(l, root, path+"_test", xtest)
			l.override = nil
			raw = append(raw, pf...)
		}
	}
	res.findings = dedupe(raw)
	if cfg.allow != "" {
		res.findings, err = applyAllow(cfg.allow, res.findings, raw)
		if err != nil {
			return res, err
		}
	}
	report.Sort(res.findings)
	return res, nil
}

type rawFinding struct {
	report.Finding
	fn string
}

// dedupe keeps one finding per (path, line, rule), the leftmost.
func dedupe(raw []rawFinding) []report.Finding {
	fs := make([]report.Finding, 0, len(raw))
	for _, r := range raw {
		fs = append(fs, r.Finding)
	}
	report.Sort(fs)
	type key struct {
		path string
		line int
		rule string
	}
	seen := map[key]bool{}
	out := fs[:0]
	for _, f := range fs {
		k := key{f.Path, f.Line, f.Rule}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	return out
}

func applyAllow(path string, fs []report.Finding, raw []rawFinding) ([]report.Finding, error) {
	entries, bad, err := report.ReadAllow(path, 4)
	if err != nil {
		return nil, fmt.Errorf("reading allowlist: %w", err)
	}
	fnOf := map[report.Finding]string{}
	for _, r := range raw {
		fnOf[r.Finding] = r.fn
	}
	var out []report.Finding
	for _, f := range fs {
		allowed := false
		for _, e := range entries {
			if e.Fields[0] == f.Path && e.Fields[1] == fnOf[f] && e.Fields[2] == f.Rule {
				e.Used = true
				allowed = true
			}
		}
		if !allowed {
			out = append(out, f)
		}
	}
	out = append(out, bad...)
	out = append(out, report.Stale(path, entries)...)
	return out, nil
}

// checkAndLint type-checks one package and lints its files. Type errors are
// findings; the package is returned for use by an external test package.
func checkAndLint(l *loader, root, path string, files []*ast.File) (*types.Package, []rawFinding) {
	var out []rawFinding
	relPath := func(pos token.Pos) (string, int, int) {
		p := l.fset.Position(pos)
		rel, err := filepath.Rel(root, p.Filename)
		if err != nil {
			rel = p.Filename
		}
		return filepath.ToSlash(rel), p.Line, p.Column
	}
	fileOf := func(pos token.Pos) *ast.File {
		for _, f := range files {
			if f.FileStart <= pos && pos <= f.FileEnd {
				return f
			}
		}
		return nil
	}
	add := func(pos token.Pos, rule, msg string) {
		p, line, col := relPath(pos)
		fn := "-"
		if f := fileOf(pos); f != nil {
			fn = report.Enclosing(f, pos)
		}
		out = append(out, rawFinding{report.Finding{Path: p, Line: line, Col: col, Rule: rule, Msg: msg}, fn})
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	typeErr := false
	conf := types.Config{Importer: l, Error: func(err error) {
		typeErr = true
		if te, ok := err.(types.Error); ok {
			add(te.Pos, "typecheck", te.Msg)
			return
		}
		out = append(out, rawFinding{report.Finding{Path: path, Line: 1, Col: 1, Rule: "typecheck", Msg: err.Error()}, "-"})
	}}
	pkg, err := conf.Check(path, l.fset, files, info)
	if err != nil && !typeErr {
		out = append(out, rawFinding{report.Finding{Path: path, Line: 1, Col: 1, Rule: "typecheck", Msg: err.Error()}, "-"})
	}
	for _, f := range files {
		lintFile(f, info, pkg, add)
	}
	return pkg, out
}

var wallclock = set("Now", "Since", "Until", "Sleep", "After", "AfterFunc", "Tick", "NewTimer", "NewTicker")
var ctxTimer = set("WithTimeout", "WithDeadline", "WithTimeoutCause", "WithDeadlineCause")
var randCtors = set("New", "NewPCG", "NewChaCha8", "NewSource", "NewZipf")
var osProc = set("Getpid", "Getppid", "Hostname", "Getenv", "LookupEnv", "Environ", "Args", "Getwd")
var runtimeProc = set("NumGoroutine", "NumCPU", "GOMAXPROCS", "Stack", "Caller", "Callers")
var reflectPtr = set("Pointer", "UnsafePointer", "UnsafeAddr")
var sortFuncs = set("Strings", "Ints", "Float64s", "Slice", "SliceStable", "Sort", "Stable")
var mapsIter = set("Keys", "Values", "All")

func set(xs ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// bannedImports maps an import path to its rule.
func bannedImport(path string) (string, bool) {
	switch path {
	case "crypto/rand":
		return "cryptorand", true
	case "hash/maphash", "unsafe":
		return "procstate", true
	case "net", "os/exec", "os/signal", "syscall", "plugin":
		return "realio", true
	}
	if strings.HasPrefix(path, "net/http") || strings.HasPrefix(path, "syscall/") {
		return "realio", true
	}
	return "", false
}

func isFloat(t types.Type) bool {
	if t == nil {
		return false
	}
	b, ok := t.Underlying().(*types.Basic)
	return ok && b.Info()&(types.IsFloat|types.IsComplex) != 0
}

func isChan(t types.Type) bool {
	return underIs(t, func(u types.Type) bool { _, ok := u.(*types.Chan); return ok })
}

func isMap(t types.Type) bool {
	return underIs(t, func(u types.Type) bool { _, ok := u.(*types.Map); return ok })
}

// underIs applies pred to t's underlying type; for a type parameter it
// reports true if any term of the constraint satisfies pred.
func underIs(t types.Type, pred func(types.Type) bool) bool {
	if t == nil {
		return false
	}
	tp, ok := t.(*types.TypeParam)
	if !ok {
		return pred(t.Underlying())
	}
	iface, ok := tp.Constraint().Underlying().(*types.Interface)
	if !ok {
		return false
	}
	for i := 0; i < iface.NumEmbeddeds(); i++ {
		switch e := iface.EmbeddedType(i).(type) {
		case *types.Union:
			for j := 0; j < e.Len(); j++ {
				if pred(e.Term(j).Type().Underlying()) {
					return true
				}
			}
		default:
			if pred(e.Underlying()) {
				return true
			}
		}
	}
	return false
}

// pkgFunc returns (pkgpath, name, true) if obj is a package-level function.
func pkgFunc(obj types.Object) (string, string, bool) {
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return "", "", false
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		return "", "", false
	}
	return fn.Pkg().Path(), fn.Name(), true
}

// qualName renders pkg.Name or pkg.Type.Method for messages.
func qualName(obj types.Object) string {
	if fn, ok := obj.(*types.Func); ok {
		if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
			t := sig.Recv().Type()
			if p, ok := t.(*types.Pointer); ok {
				t = p.Elem()
			}
			if n, ok := t.(*types.Named); ok {
				return fn.Pkg().Path() + "." + n.Obj().Name() + "." + fn.Name()
			}
		}
	}
	return obj.Pkg().Path() + "." + obj.Name()
}

// calleeObj resolves the object a call's Fun refers to, if it is a name.
func calleeObj(info *types.Info, fun ast.Expr) types.Object {
	switch f := ast.Unparen(fun).(type) {
	case *ast.Ident:
		return info.Uses[f]
	case *ast.SelectorExpr:
		return info.Uses[f.Sel]
	case *ast.IndexExpr:
		return calleeObj(info, f.X)
	case *ast.IndexListExpr:
		return calleeObj(info, f.X)
	}
	return nil
}

func lintFile(f *ast.File, info *types.Info, pkg *types.Package, add func(token.Pos, string, string)) {
	for _, im := range f.Imports {
		p := strings.Trim(im.Path.Value, "\"`")
		if rule, ok := bannedImport(p); ok {
			add(im.Pos(), rule, "import of "+p)
		}
	}
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		parent := ast.Node(nil)
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		lintNode(n, parent, stack, info, pkg, add)
		stack = append(stack, n)
		return true
	})
}

func lintNode(n, parent ast.Node, stack []ast.Node, info *types.Info, pkg *types.Package, add func(token.Pos, string, string)) {
	if e, ok := n.(ast.Expr); ok {
		if tv, ok := info.Types[e]; ok && isFloat(tv.Type) {
			add(e.Pos(), "float", "floating-point expression of type "+tv.Type.String())
		}
	}
	switch x := n.(type) {
	case *ast.Ident:
		if obj := info.Defs[x]; obj != nil {
			if v, ok := obj.(*types.Var); ok {
				if isFloat(v.Type()) {
					add(x.Pos(), "float", "floating-point variable "+x.Name)
				}
				if isChan(v.Type()) {
					add(x.Pos(), "concurrency", "channel variable "+x.Name)
				}
			}
		}
		if obj := info.Uses[x]; obj != nil {
			lintUse(x, obj, parent, stack, info, pkg, add)
		}
	case *ast.GoStmt:
		add(x.Pos(), "concurrency", "go statement")
	case *ast.SelectStmt:
		add(x.Pos(), "concurrency", "select statement")
	case *ast.ChanType:
		add(x.Pos(), "concurrency", "channel type")
	case *ast.SendStmt:
		add(x.Pos(), "concurrency", "channel send")
	case *ast.UnaryExpr:
		if x.Op == token.ARROW {
			add(x.Pos(), "concurrency", "channel receive")
		}
	case *ast.RangeStmt:
		t := info.TypeOf(x.X)
		switch {
		case isChan(t):
			add(x.Pos(), "concurrency", "range over channel")
		case isMap(t):
			if !orderInsensitive(x, stack, info) {
				add(x.Pos(), "maprange", "range over map "+types.ExprString(x.X)+" has nondeterministic order")
			}
		}
	case *ast.CallExpr:
		lintPtrFmt(x, info, add)
	}
}

func lintUse(id *ast.Ident, obj types.Object, parent ast.Node, stack []ast.Node, info *types.Info, pkg *types.Package, add func(token.Pos, string, string)) {
	if b, ok := obj.(*types.Builtin); ok {
		if b.Name() == "close" {
			add(id.Pos(), "concurrency", "close of channel")
		}
		return
	}
	if v, ok := obj.(*types.Var); ok && isChan(v.Type()) {
		add(id.Pos(), "concurrency", "use of channel "+id.Name)
	}
	if obj.Pkg() == nil || obj.Pkg() == pkg {
		return
	}
	path, name := obj.Pkg().Path(), obj.Name()
	fpath, fname, isPkgFunc := pkgFunc(obj)
	switch path {
	case "time":
		if isPkgFunc && wallclock[fname] {
			add(id.Pos(), "wallclock", "use of time."+fname)
		}
	case "math/rand", "math/rand/v2":
		if isPkgFunc && !randCtors[fname] {
			add(id.Pos(), "globalrand", "use of global-source "+fpath+"."+fname)
		}
	case "sync", "sync/atomic":
		add(id.Pos(), "concurrency", "use of "+qualName(obj))
	case "os":
		if osProc[name] {
			add(id.Pos(), "procstate", "use of os."+name)
		} else {
			add(id.Pos(), "realio", "use of os."+name)
		}
	case "context":
		if isPkgFunc && ctxTimer[fname] {
			add(id.Pos(), "wallclock", "use of context."+fname+" (wall-clock deadline)")
		}
	case "runtime":
		if isPkgFunc && runtimeProc[fname] {
			add(id.Pos(), "procstate", "use of runtime."+fname)
		}
	case "reflect":
		if fn, ok := obj.(*types.Func); ok && reflectPtr[name] {
			if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
				add(id.Pos(), "procstate", "use of reflect.Value."+name)
			}
		}
	case "maps":
		if isPkgFunc && mapsIter[fname] && !wrappedInSorted(id, parent, stack, info) {
			add(id.Pos(), "maprange", "maps."+fname+" iteration order is nondeterministic; wrap directly in slices.Sorted")
		}
	}
}

// wrappedInSorted reports whether the use id of maps.Keys/Values/All is the
// callee of a call that is itself the first argument of slices.Sorted or
// slices.SortedFunc / SortedStableFunc.
func wrappedInSorted(id *ast.Ident, parent ast.Node, stack []ast.Node, info *types.Info) bool {
	// stack ends with parent; walk outwards skipping parens.
	i := len(stack) - 1
	var fun ast.Node = id
	if sel, ok := parent.(*ast.SelectorExpr); ok && sel.Sel == id {
		fun = sel
		i--
	}
	for i >= 0 {
		if p, ok := stack[i].(*ast.ParenExpr); ok && p.X == fun {
			fun = p
			i--
			continue
		}
		break
	}
	if i < 0 {
		return false
	}
	call, ok := stack[i].(*ast.CallExpr)
	if !ok || call.Fun != fun {
		return false
	}
	i--
	if i < 0 {
		return false
	}
	outer, ok := stack[i].(*ast.CallExpr)
	if !ok || len(outer.Args) == 0 || outer.Args[0] != call {
		return false
	}
	p, n, ok := pkgFunc(calleeObj(info, outer.Fun))
	return ok && p == "slices" && (n == "Sorted" || n == "SortedFunc" || n == "SortedStableFunc")
}

// orderInsensitive accepts the collect-and-sort idiom: the loop body only
// appends the range key and/or value to slice variables, and each such slice
// is passed to a sort.* or slices.Sort* call later in the same function body.
// An empty body is also accepted.
func orderInsensitive(rs *ast.RangeStmt, stack []ast.Node, info *types.Info) bool {
	if len(rs.Body.List) == 0 {
		return true
	}
	objOf := func(e ast.Expr) types.Object {
		id, ok := ast.Unparen(e).(*ast.Ident)
		if !ok || id.Name == "_" {
			return nil
		}
		if o := info.Defs[id]; o != nil {
			return o
		}
		return info.Uses[id]
	}
	iterVars := map[types.Object]bool{}
	if rs.Key != nil {
		if o := objOf(rs.Key); o != nil {
			iterVars[o] = true
		}
	}
	if rs.Value != nil {
		if o := objOf(rs.Value); o != nil {
			iterVars[o] = true
		}
	}
	slices := map[types.Object]bool{}
	for _, st := range rs.Body.List {
		as, ok := st.(*ast.AssignStmt)
		if !ok || as.Tok != token.ASSIGN || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return false
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || call.Ellipsis.IsValid() || len(call.Args) < 2 {
			return false
		}
		if b, ok := calleeObj(info, call.Fun).(*types.Builtin); !ok || b.Name() != "append" {
			return false
		}
		dst := objOf(as.Lhs[0])
		if dst == nil || objOf(call.Args[0]) != dst {
			return false
		}
		for _, a := range call.Args[1:] {
			if o := objOf(a); o == nil || !iterVars[o] {
				return false
			}
		}
		slices[dst] = true
	}
	var body *ast.BlockStmt
	for i := len(stack) - 1; i >= 0 && body == nil; i-- {
		switch fn := stack[i].(type) {
		case *ast.FuncDecl:
			body = fn.Body
		case *ast.FuncLit:
			body = fn.Body
		}
	}
	if body == nil {
		return false
	}
	sorted := map[types.Object]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || call.Pos() < rs.End() || len(call.Args) == 0 {
			return true
		}
		p, name, ok := pkgFunc(calleeObj(info, call.Fun))
		if !ok || !((p == "sort" && sortFuncs[name]) || (p == "slices" && strings.HasPrefix(name, "Sort"))) {
			return true
		}
		arg := ast.Unparen(call.Args[0])
		if conv, ok := arg.(*ast.CallExpr); ok && len(conv.Args) == 1 {
			if tv, ok := info.Types[conv.Fun]; ok && tv.IsType() {
				arg = conv.Args[0]
			}
		}
		if o := objOf(arg); o != nil {
			sorted[o] = true
		}
		return true
	})
	for s := range slices {
		if !sorted[s] {
			return false
		}
	}
	return true
}

// lintPtrFmt flags a %p verb in a constant string argument of a call to a
// function or method whose name ends in "f" (Printf, Sprintf, Errorf, ...).
func lintPtrFmt(call *ast.CallExpr, info *types.Info, add func(token.Pos, string, string)) {
	fn, ok := calleeObj(info, call.Fun).(*types.Func)
	if !ok || !strings.HasSuffix(fn.Name(), "f") {
		return
	}
	for _, a := range call.Args {
		tv, ok := info.Types[a]
		if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
			continue
		}
		if hasPtrVerb(constant.StringVal(tv.Value)) {
			add(a.Pos(), "ptrfmt", "%p verb in format string of "+fn.Name())
		}
	}
}

func hasPtrVerb(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		i++
		if i < len(s) && s[i] == '%' {
			continue
		}
		for i < len(s) && strings.IndexByte("+-# 0", s[i]) >= 0 {
			i++
		}
		skipArg := func() {
			if i < len(s) && s[i] == '[' {
				for i < len(s) && s[i] != ']' {
					i++
				}
				i++
			}
		}
		skipNum := func() {
			if i < len(s) && s[i] == '*' {
				i++
				return
			}
			for i < len(s) && s[i] >= '0' && s[i] <= '9' {
				i++
			}
		}
		skipArg()
		skipNum()
		if i < len(s) && s[i] == '.' {
			i++
			skipArg()
			skipNum()
		}
		skipArg()
		if i < len(s) && s[i] == 'p' {
			return true
		}
		i--
	}
	return false
}
