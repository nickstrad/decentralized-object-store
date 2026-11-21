detlint gate tools (nested module "detlint", stdlib only)

Build once (inside the dev container, from the repo root /src):
  BIN=$(mktemp -d); (cd agent_work/gates/detlint && go build -o "$BIN" ./...)
  -> $BIN/detlint $BIN/nocomments $BIN/astdiff $BIN/paniccheck (+ importcheck, added by the gate author)
Or run one directly: (cd agent_work/gates/detlint && go run . -root /src internal/env internal/sim/...)
                     (cd agent_work/gates/detlint && go run ./cmd/nocomments -root /src)

All tools: findings on stdout, one per line, sorted: path:line:col: <rule-id>: message
Exit: 0 clean, 1 findings, 2 usage/internal error (message on stderr).
Paths in findings are relative to -root (or to the trees, for astdiff).

1. detlint [-tests] [-allow FILE] -root DIR PKGDIR...
   PKGDIR relative to DIR; "dir/..." recurses (skips testdata, vendor, .x, _x, nested modules).
   Missing dirs are skipped silently. Summary on stderr: "detlint: checked N files in M packages".
   Default: non-test files only; -tests adds _test.go (in-package and external _test packages).
   Type-checks with go/types (root module from source, stdlib + module deps via the source
   importer; cgo disabled, so cgo files are skipped). Type errors -> [typecheck], parse/read errors -> [parse].
   Rules: wallclock globalrand cryptorand concurrency procstate realio ptrfmt maprange float
   (also: typecheck parse stale-allow allow-format). One finding per (file, line, rule).
   -allow lines: relative/file.go<TAB>Func<TAB>rule-id<TAB>justification
     Func is Func or Type.Method ((*T).M and T[K].M -> T.M); code outside any func is "-";
     func literals belong to their enclosing func. '#' and blank lines ignored.
     Empty field/justification -> [allow-format]. Entry that suppressed nothing in THIS run
     -> [stale-allow] (so pass the full package set whenever you pass -allow).
   Example: $BIN/detlint -root /src -allow agent_work/gates/detlint-allowlist.txt internal/env internal/sim/...
   (-allow path is relative to the current directory, not to -root.)

2. nocomments -root DIR [PATH...]
   PATHs relative to DIR (dirs or single files); default ".". Skips dirs named
   agent_work kb .git testdata vendor (anywhere below the start path).
   Allowed: //go:... directives; "// Code generated ... DO NOT EDIT." lines;
   "// Output:" / "// Unordered output:" comment groups inside Example* funcs in _test.go.
   Everything else (incl. //nolint, "// go:x", /* */) -> [comment]. Unparsable file -> [parse].
   Example: $BIN/nocomments -root /src

3. astdiff TREE_A TREE_B
   Every .go file (same dir exclusions) must exist in both and have identical ASTs ignoring
   positions and comments (formatting-only changes pass). Significant comments must match
   in text and placement (placement = before package / before or inside top-level decl i):
   //go:*, // +build, //line, //export, //extern, // Code generated ... DO NOT EDIT.,
   and Example output blocks in _test.go. Non-.go files ignored. Rule [astdiff], one
   finding per differing file (first difference), added/removed files reported at :1:1.
   Example (pre-prose vs post-prose):
     git worktree add /tmp/pre <sha>; $BIN/astdiff /tmp/pre /src
     (or: git archive <sha> | tar -x -C /tmp/pre)

4. paniccheck -root DIR -allow FILE [DIR...]
   DIRs relative to root, default "internal"; non-test .go files; same dir exclusions.
   Allowlist lines: relative/file.go<TAB>EnclosingFunc<TAB>justification
   (EnclosingFunc as for detlint; one entry covers every panic in that function.)
   Unmatched panic -> [panic]; entry matching no panic -> [stale-allow];
   empty justification or wrong field count -> [allow-format].
   A call to a locally declared or package-level user-defined "panic" is not counted.
   Example: $BIN/paniccheck -root /src -allow /src/agent_work/gates/panic-allowlist.txt

Tests: docker compose run --rm dev bash -c 'cd agent_work/gates/detlint && gofmt -l . && go vet ./... && go test ./...'
