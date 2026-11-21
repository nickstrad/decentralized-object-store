detlint gate tools (nested module "detlint", stdlib only)

Build once (inside the dev container, from the repo root /src):
  BIN=$(mktemp -d); (cd agent_work/gates/detlint && go build -o "$BIN" ./...)
  -> $BIN/detlint $BIN/nocomments $BIN/astdiff $BIN/paniccheck $BIN/importcheck
Or run one directly: (cd agent_work/gates/detlint && go run . -root /src internal/env internal/sim/...)
                     (cd agent_work/gates/detlint && go run ./cmd/nocomments -root /src)

All tools: findings on stdout, one per line, sorted: path:line:col: <rule-id>: message
Exit: 0 clean, 1 findings, 2 usage/internal error (message on stderr).
Paths in findings are relative to -root (or to the trees, for astdiff).

1. detlint [-tests] [-allow FILE] [-allow-import P1,P2,...] -root DIR PKGDIR...
   PKGDIR relative to DIR; "dir/..." recurses (skips testdata, vendor, .x, _x, nested modules).
   Missing dirs are skipped silently. Summary on stderr: "detlint: checked N files in M packages".
   Default: non-test files only; -tests adds _test.go (in-package and external _test packages).
   Type-checks with go/types (root module from source, stdlib + module deps via the source
   importer). Type errors -> [typecheck], parse/read errors -> [parse].
   In a checked directory NO .go file is dropped: every non-test .go file (and with -tests
   every _test.go file) is type-checked and linted whatever its build constraints or name.
   Module packages that a checked package imports but that are not themselves checked are
   loaded as the default build sees them (build.Default.MatchFile, cgo off).
   One finding per (file, line, rule).

   Rules:
   [import]   A non-test file may import only packages of the root module (path == module
              path from DIR/go.mod, or that path + "/...") and these standard packages:
              bufio bytes cmp container/heap container/list container/ring crypto/sha256
              crypto/sha512 crypto/subtle embed encoding/binary encoding/hex encoding/base64
              errors fmt hash hash/crc32 hash/crc64 hash/fnv io iter maps math math/bits
              math/rand/v2 slices sort strconv strings time unicode unicode/utf8 unicode/utf16
              plus whatever -allow-import adds. Anything else is a finding at the import spec,
              used or not (blank and dot imports included). _test.go files (with -tests) are
              not subject to the list; there an import of crypto/rand -> [cryptorand],
              hash/maphash or unsafe -> [procstate], net, net/http..., os/exec, os/signal,
              syscall..., plugin -> [realio].
   [buildtag] In each checked directory (test files included, -tests or not): a .go file with
              a //go:build or // +build line (any parsed comment; for test files without
              -tests only the header up to the imports is parsed); a .go file whose name has a
              GOOS/GOARCH suffix (_linux.go, _amd64.go, _windows_arm64_test.go, ... decided by
              go/build's own MatchFile with an unknown GOOS/GOARCH); a .go file importing "C";
              any other .go file build.Default.MatchFile would exclude; a file the go tool
              would compile or link (.c .cc .cxx .cpp .m .h .hh .hpp .hxx .f .F .for .f90 .s .S
              .sx .swig .swigcxx .syso); a .go or such source file whose name starts with "_" or
              "." ("file is invisible to the go tool"). Position 1:1 except constraint lines
              and import "C" specs.
   [wallclock]  uses of time.Now Since Until Sleep After AfterFunc Tick NewTimer NewTicker
              (calls, values, aliased and dot imports); context.WithTimeout/WithDeadline(+Cause).
   [globalrand] package-level functions of math/rand/v2 and math/rand other than the
              constructors New NewPCG NewChaCha8 NewSource NewZipf.
   [concurrency] go, select, channel types/variables/send/receive/range/close, any use of
              sync or sync/atomic.
   [procstate] os.Getpid Getppid Hostname Getenv LookupEnv Environ Args Getwd; runtime
              NumGoroutine NumCPU GOMAXPROCS Stack Caller Callers; reflect.Value.Pointer,
              UnsafePointer, UnsafeAddr.   [realio] any other use of package os.
              (os, sync, runtime, reflect, context are not importable in non-test files, so in
              those these use rules fire next to the [import] finding.)
   [ptrfmt]   (a) In a call of any function or method of package fmt (resolved with go/types)
              except the Scan family, an argument bound to an `any`/`...any` parameter whose
              STATIC type can print an address: pointer, chan, func, unsafe.Pointer, uintptr,
              or a map/slice/array/struct containing one at any depth (recursive types are
              followed once), or a type parameter with such a type term. The io.Writer of
              Fprint*, the format string and the []byte of Append* are not printed and not
              checked. A multi-value call argument f() is checked per result.
              (b) Any use of a fmt function or method as a value (not the callee of a direct
              call), e.g. f := fmt.Sprintf, since its arguments can then not be checked.
              (c) A %p verb in a constant string argument of any call to a function or method
              whose name ends in "f".
              LIMIT: an argument whose static type is an interface (error, any, fmt.Stringer,
              ...) is not flagged: its dynamic type is unknown. So a wrapper
              `func logf(f string, a ...any) { fmt.Printf(f, a...) }` hides the types its
              callers pass. A type parameter whose constraint has no type terms (any,
              comparable, method-only) is treated the same. Structs with pointer fields are
              flagged even if they implement String() (fmt does not call it for %d, %#v, ...),
              e.g. time.Time (it has a *Location field). Builtin print/println are not checked.
   [maprange] A range over a map (or a type parameter with a map type term) is accepted only
              if (1) its body is empty, or (2) its body consists only of `s = append(s, k)` /
              `s = append(s, k, v)` statements (k, v: the range key/value identifiers, s: one
              slice variable) and the very next statement of the same block (case clause,
              select clause; a labeled range counts as the labeled statement) is exactly
              slices.Sort(s), sort.Strings(s) or sort.Ints(s). Comparator sorts (sort.Sort,
              sort.Slice, sort.SliceStable, slices.SortFunc, slices.SortStableFunc) do not
              qualify: a comparator can leave ties, and ties expose map order -> allowlist it.
              Function literals follow the same rule. maps.Keys/Values/All is accepted only as
              the single direct argument of slices.Sorted(...) (not SortedFunc/
              SortedStableFunc); any other use is a [maprange] finding.
   [float]    any expression, variable or type expression of float/complex type; and a type
              parameter whose constraint's type terms (unions, ~T, embedded constraint
              interfaces at any depth, e.g. cmp.Ordered) include a float or complex type,
              reported at the type parameter. LIMIT: a type parameter constrained by any or
              comparable is not flagged (its instantiation with float64 in a checked package is
              caught at the call site as a float type expression).
   Also: [typecheck] [parse] [stale-allow] [allow-format].

   -allow lines: relative/file.go<TAB>Func<TAB>rule-id<TAB>justification
     Func is Func or Type.Method ((*T).M and T[K].M -> T.M); code outside any func is "-"
     (so [import] and [buildtag] findings use "-"); func literals belong to their enclosing
     func. '#' and blank lines ignored. Empty field/justification -> [allow-format]. Entry
     that suppressed nothing in THIS run -> [stale-allow] (so pass the full package set
     whenever you pass -allow).
   -allow-import: comma-separated import paths added to the [import] allowlist (exact paths,
     no prefixes).
   Example: $BIN/detlint -root /src -allow agent_work/gates/detlint-allow.txt internal/env internal/sim/...
   (-allow path is relative to the current directory, not to -root.)

2. nocomments -root DIR [-baseline OLD] [PATH...]
   PATHs relative to DIR (dirs or single files); default ".". Skips dirs named
   agent_work kb .git testdata vendor (anywhere below the start path).
   Allowed: comments matching exactly ^//go:(build|embed|generate|noinline)( |$), where a
   //go:build line must also sit before the package clause; "// Output:" / "// Unordered
   output:" comment groups inside the body of an Example* func in a _test.go file.
   Everything else -> [comment]: other //go: directives (//go:linkname, //go:nosplit,
   //go:embedx, ...), //line, //nolint, "// go:x", /* */, "// Code generated ... DO NOT EDIT."
   (no exception: the project has no generated code). Unparsable file -> [parse].
   -baseline OLD: per file, a non-directive comment is accepted as many times as its exact text
   occurs in the same file (same relative path) under OLD; each further copy is a finding.
   A misplaced //go:build is a finding even if OLD had it.
   Example: $BIN/nocomments -root /src

3. astdiff TREE_A TREE_B
   Every .go file (same dir exclusions as nocomments) must exist in both and have identical
   ASTs ignoring positions and comments (formatting-only changes pass). Significant comments
   must match in text, order and placement: //go:* lines, // +build lines, //line and /*line,
   //export, //extern, the cgo preamble (the whole comment group documenting import "C",
   text identical), and Example output blocks in _test.go. Placement is structural: the path
   of child indices from the file down to the innermost node whose source range contains the
   comment, ending in "gap before child i" or "after the last child", so moving //go:embed
   from one ValueSpec to another inside one var ( ... ) block is a difference.
   Also compared byte for byte, and required to exist in both trees: every file named
   go.mod, go.sum, go.work, go.work.sum, or with extension .c .cc .cxx .cpp .m .h .hh .hpp
   .hxx .f .F .for .f90 .s .S .sx .swig .swigcxx .syso. All other non-Go files (.txt, .md,
   embedded data, ...) are ignored.
   Rule [astdiff], one finding per differing file (first difference); added/removed files and
   differing byte-compared files reported at :1:1.
   LIMIT: where a node's children overlap in source range (a method's receiver lies inside
   its FuncType's range), two gaps can share an anchor; no directive is meaningful there.
   Example (pre-prose vs post-prose):
     git worktree add /tmp/pre <sha>; $BIN/astdiff /tmp/pre /src
     (or: git archive <sha> | tar -x -C /tmp/pre)

4. paniccheck -root DIR -allow FILE [DIR...]
   DIRs relative to root, default "internal"; same dir exclusions. Every .go file found there,
   whatever its build constraints and including _x.go / .x.go names, is parsed.
   [panic-shadow]: any declaration of an identifier named panic in ANY of those files, test
   files included: func or method name, import name, const/var/type spec, parameter, result,
   receiver, struct field, type parameter, := variable, range or type-switch variable, label.
   [panic]: every call whose callee is the bare identifier panic in a non-test file, not
   covered by an allowlist line. There is no "user-defined panic" escape any more.
   Allowlist lines: relative/file.go<TAB>EnclosingFunc<TAB>justification
   (EnclosingFunc as for detlint; one entry covers every panic in that function.)
   Entry matching no panic -> [stale-allow]; empty justification or wrong field count ->
   [allow-format]; unparsable file -> [parse].
   Example: $BIN/paniccheck -root /src -allow /src/agent_work/gates/panic-allowlist.txt

5. importcheck -module M [-test-extra P1,P2,...] < file-list
   Reads .go file paths on stdin (one per line) and parses each with parser.ImportsOnly,
   whatever its build tags. Product files may import only the standard library (first path
   element without a dot, "C" excluded) and M/...; _test.go files may also import P/... for
   each -test-extra P. A syntax error after the import block does not hide the imports (they
   are still checked); a file whose package clause or import block does not parse is a
   finding ("cannot parse"). Output lines: path:line:col: import: message.

Tests: docker compose run --rm -T dev bash -c 'cd agent_work/gates/detlint && gofmt -l . ; go vet ./... && go test ./...'
