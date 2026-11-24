#!/usr/bin/env python3
"""Claims-discipline check for a verbiage-only prose pass.

Usage: agent_work/09-cleanup/check-prose.py <base-commit> [<head-commit>]
       (head defaults to the working tree)

Compares every reader-facing Markdown file and docs/c4/model.json between the two trees:
  fences    every fenced block (code excerpts, sh commands, Mermaid, terminal renderings)
            must be byte-identical and in the same order; a changed fence is printed
  numbers   the multiset of numeric tokens outside fences; removed and added tokens are
            printed per file for a reader to judge (a removed date or plan section number
            is expected; a changed measurement is a finding)
  model     docs/c4/model.json must have the same structure: same keys, same list lengths
            (a `notes`-like list of strings may shrink), same non-string values; changed
            strings are printed with their JSON path; a changed id/name/status/url/
            technology value is a finding
Exit 0 when there is no finding (printed number changes still need reading), 1 otherwise.
"""
import collections, json, re, subprocess, sys

FILES_GLOB = ["README.md", "docs/index.md", "book/front", "book/chapters", "docs/c4"]
FROZEN_KEYS = {"id", "name", "status", "url", "technology", "kind", "type", "source", "target",
               "from", "to", "schemaVersion"}


def git(*a):
    return subprocess.run(["git", *a], capture_output=True, text=True, check=True).stdout


def read(rev, path):
    if rev is None:
        try:
            return open(path, encoding="utf-8").read()
        except FileNotFoundError:
            return None
    r = subprocess.run(["git", "show", "%s:%s" % (rev, path)], capture_output=True, text=True)
    return r.stdout if r.returncode == 0 else None


def split(text):
    fences, prose, cur, inside = [], [], [], False
    for line in text.splitlines():
        if re.match(r"^\s*(```|~~~)", line):
            if inside:
                cur.append(line); fences.append("\n".join(cur)); cur = []; inside = False
            else:
                inside = True; cur = [line]
            continue
        (cur if inside else prose).append(line)
    return fences, "\n".join(prose)


NUM = re.compile(r"(?<![A-Za-z_])\d+(?:[.,:]\d+)*(?:\s?%|\s?[kKMG]i?B|\s?ms|\s?s\b|\s?min)?")


def main():
    base = sys.argv[1]; head = sys.argv[2] if len(sys.argv) > 2 else None
    files = [f for f in git("ls-files", *FILES_GLOB).split() if f.endswith(".md")]
    bad = 0
    for f in files:
        a, b = read(base, f), read(head, f)
        if a is None or b is None:
            print("%s: FINDING: file added or removed" % f); bad += 1; continue
        if a == b:
            continue
        fa, pa = split(a); fb, pb = split(b)
        if fa != fb:
            if len(fa) != len(fb):
                print("%s: FINDING: %d fenced blocks before, %d after" % (f, len(fa), len(fb)))
            for i, (x, y) in enumerate(zip(fa, fb)):
                if x != y:
                    print("%s: FINDING: fenced block %d changed" % (f, i + 1))
            bad += 1
        ca, cb = collections.Counter(NUM.findall(pa)), collections.Counter(NUM.findall(pb))
        gone, new = ca - cb, cb - ca
        if gone or new:
            print("%s: numbers removed %s | added %s" % (f, sorted(gone.elements()), sorted(new.elements())))
    a, b = read(base, "docs/c4/model.json"), read(head, "docs/c4/model.json")
    if a != b:
        def walk(x, y, path):
            nonlocal bad
            if type(x) is not type(y):
                print("model.json %s: FINDING: type changed" % path); bad += 1
            elif isinstance(x, dict):
                if list(x) != list(y):
                    print("model.json %s: FINDING: keys changed" % path); bad += 1
                for k in x:
                    if k in y:
                        walk(x[k], y[k], path + "." + k)
            elif isinstance(x, list):
                if len(x) != len(y):
                    if all(isinstance(v, str) for v in x + y) and len(y) < len(x):
                        print("model.json %s: strings removed: %r" % (path, [v for v in x if v not in y]))
                        rest = [v for v in x if v in y]
                        if rest != [v for v in y if v in x] or len(rest) != len(y):
                            print("model.json %s: remaining strings also changed:\n   - %r\n   + %r" % (path, x, y))
                    else:
                        print("model.json %s: FINDING: list length %d -> %d" % (path, len(x), len(y))); bad += 1
                else:
                    for i, (p, q) in enumerate(zip(x, y)):
                        walk(p, q, "%s[%d]" % (path, i))
            elif x != y:
                key = path.rsplit(".", 1)[-1].split("[")[0]
                if not isinstance(x, str) or key in FROZEN_KEYS:
                    print("model.json %s: FINDING: frozen value changed %r -> %r" % (path, x, y)); bad += 1
                else:
                    print("model.json %s:\n   - %s\n   + %s" % (path, x, y))
        walk(json.loads(a), json.loads(b), "$")
    print("check-prose: %s" % ("FINDINGS: %d" % bad if bad else "no structural finding"))
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
