#!/usr/bin/env python3
"""site-lint (gate G9): build the Pages site into a temp dir and check it.

Usage:
  site_lint.py <repo_root>    lint that tree with this repo's .github/scripts/build_site.py
  site_lint.py --selftest     run against the fixture trees in testdata/site/

Rules (findings print one per line as `path: rule: message`):
  manifest          book/manifest.json is not valid JSON or not the expected shape
  chapter-missing   a manifest entry's file does not exist
  chapter-empty     a manifest entry's file is empty or whitespace
  chapter-title     a manifest entry's first heading differs from its manifest title
  chapter-unlisted  a .md file under book/chapters/ is not in the manifest
  build-failed      build_site.py exited nonzero
  absolute-link     a root-absolute href/src (breaks under the project-site path prefix)
  broken-link       a relative href/src in the built site does not resolve to a built file
  machine-ref       a built page or reader-facing source names this machine or a brief
PLAN.md and plan.html are exempt from machine-ref (the plan legitimately names them),
not from the link check. External http(s) links are not fetched.
Exit: 0 clean, 1 findings, 2 internal error. Needs python3 + markdown only.
"""

import json
import os
import re
import subprocess
import sys
import tempfile
import traceback
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlsplit

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
BUILD = REPO / ".github" / "scripts" / "build_site.py"
FIXTURES = HERE / "testdata" / "site"

MACHINE_REFS = [r"/root/", r"/home/[a-z]", r"\bvm-1\b", r"\bdroplet\b", r"/root/briefs",
                r"objstore-p2-", r"agent_work/[^ ]*brief"]
MACHINE_RE = [(p, re.compile(p)) for p in MACHINE_REFS]
EXEMPT_SOURCES = {"PLAN.md"}
EXEMPT_BUILT = {"plan.html"}
TEXT_SUFFIXES = {".md", ".json", ".html", ".mmd", ".txt", ".css", ".js", ".svg", ".yml", ".yaml"}
SELFTEST = {"good": None, "missing-chapter": "chapter-missing",
            "broken-link": "broken-link", "root-path": "machine-ref"}


class LinkParser(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.urls = []

    def handle_starttag(self, tag, attrs):
        for name, value in attrs:
            if name in ("href", "src") and value is not None:
                self.urls.append((tag, name, value))


def is_text(path):
    return path.suffix.lower() in TEXT_SUFFIXES


def reader_sources(root):
    """README.md, docs/** (minus docs/knowledge/** and docs/project-source.md), book/**."""
    out = []
    if (root / "README.md").is_file():
        out.append(root / "README.md")
    for top in ("docs", "book"):
        base = root / top
        if not base.is_dir():
            continue
        for p in sorted(base.rglob("*")):
            rel = p.relative_to(root).as_posix()
            if not p.is_file() or not is_text(p):
                continue
            if rel.startswith("docs/knowledge/") or rel == "docs/project-source.md":
                continue
            out.append(p)
    return out


def scan_machine_refs(path, label, findings):
    text = path.read_text(encoding="utf-8", errors="replace")
    for lineno, line in enumerate(text.splitlines(), 1):
        for pat, rx in MACHINE_RE:
            if rx.search(line):
                findings.append("%s:%d: machine-ref: matches %s" % (label, lineno, pat))
                break


def check_manifest(root, findings):
    mpath = root / "book" / "manifest.json"
    label = "book/manifest.json"
    try:
        data = json.loads(mpath.read_text(encoding="utf-8"))
    except FileNotFoundError:
        findings.append("%s: manifest: file missing" % label)
        return
    except json.JSONDecodeError as e:
        findings.append("%s: manifest: bad JSON: %s" % (label, e))
        return
    if not isinstance(data, dict):
        findings.append("%s: manifest: top level is not an object" % label)
        return
    listed = set()
    for kind in ("front_matter", "chapters"):
        items = data.get(kind)
        if not isinstance(items, list):
            findings.append("%s: manifest: '%s' is not a list" % (label, kind))
            continue
        for i, e in enumerate(items):
            if not isinstance(e, dict) or not isinstance(e.get("file"), str) \
                    or not isinstance(e.get("title"), str):
                findings.append("%s: manifest: %s[%d] lacks string 'file' and 'title'"
                                % (label, kind, i))
                continue
            rel = "book/" + e["file"]
            path = root / rel
            listed.add(os.path.normpath(rel))
            if not path.is_file():
                findings.append("%s: chapter-missing: %s[%d] (%s) is not a file"
                                % (rel, kind, i, e.get("slug")))
                continue
            text = path.read_text(encoding="utf-8", errors="replace")
            if not text.strip():
                findings.append("%s: chapter-empty: file is empty" % rel)
                continue
            m = re.search(r"^#{1,6}\s+(.+?)\s*#*\s*$", text, flags=re.M)
            heading = m.group(1).strip() if m else None
            if heading != e["title"].strip():
                findings.append("%s: chapter-title: first heading %r != manifest title %r"
                                % (rel, heading, e["title"]))
    chapters = root / "book" / "chapters"
    if chapters.is_dir():
        for p in sorted(chapters.rglob("*.md")):
            rel = p.relative_to(root).as_posix()
            if os.path.normpath(rel) not in listed:
                findings.append("%s: chapter-unlisted: not in book/manifest.json" % rel)


def check_site(site, findings):
    for page in sorted(site.rglob("*")):
        if not page.is_file():
            continue
        rel = page.relative_to(site).as_posix()
        label = "site/" + rel
        if is_text(page) and rel not in EXEMPT_BUILT:
            scan_machine_refs(page, label, findings)
        if page.suffix != ".html":
            continue
        parser = LinkParser()
        parser.feed(page.read_text(encoding="utf-8", errors="replace"))
        for tag, attr, url in parser.urls:
            parts = urlsplit(url)
            if parts.scheme or url.startswith("//"):
                continue  # external (http, https, mailto, data, ...): not fetched
            if not parts.path:
                continue  # same-page anchor or query only
            if parts.path.startswith("/"):
                findings.append("%s: absolute-link: <%s %s=%r> breaks under the project-site prefix"
                                % (label, tag, attr, url))
                continue
            target = (page.parent / unquote(parts.path)).resolve()
            if parts.path.endswith("/") or target.is_dir():
                target = target / "index.html"
            try:
                target.relative_to(site.resolve())
            except ValueError:
                findings.append("%s: broken-link: <%s %s=%r> leaves the site root"
                                % (label, tag, attr, url))
                continue
            if not target.is_file():
                findings.append("%s: broken-link: <%s %s=%r> does not resolve to a built file"
                                % (label, tag, attr, url))


def lint(root, quiet=False):
    root = Path(root).resolve()
    if not root.is_dir():
        raise RuntimeError("not a directory: %s" % root)
    findings = []
    check_manifest(root, findings)
    for src in reader_sources(root):
        rel = src.relative_to(root).as_posix()
        if rel not in EXEMPT_SOURCES:
            scan_machine_refs(src, rel, findings)
    with tempfile.TemporaryDirectory(prefix="site-lint-") as tmp:
        site = Path(tmp) / "site"
        proc = subprocess.run([sys.executable, str(BUILD), "--root", str(root), str(site)],
                              capture_output=True, text=True)
        if proc.returncode != 0:
            msg = (proc.stderr.strip() or proc.stdout.strip() or "no output").splitlines()[-1]
            findings.append("%s: build-failed: exit %d: %s"
                            % (BUILD.relative_to(REPO).as_posix(), proc.returncode, msg))
        else:
            if not quiet:
                print(proc.stdout.strip())
            check_site(site, findings)
    return findings


def run(root):
    findings = lint(root)
    print("site-lint: note: PLAN.md and plan.html are exempt from machine-ref "
          "(not from the link check)")
    for f in findings:
        print(f)
    print("site-lint: %s (%d finding%s)" % ("FAIL" if findings else "ok", len(findings),
                                           "" if len(findings) == 1 else "s"))
    return 1 if findings else 0


def selftest():
    ok = True
    for name, expected in SELFTEST.items():
        tree = FIXTURES / name
        if not tree.is_dir():
            raise RuntimeError("fixture tree missing: %s" % tree)
        findings = lint(tree, quiet=True)
        rules = sorted({f.split(": ")[1] for f in findings if ": " in f})
        if expected is None:
            good = not findings
            want = "no findings"
        else:
            good = expected in rules
            want = "rule %s" % expected
        ok &= good
        print("selftest %-16s %s: want %s, got %s" % (name, "PASS" if good else "FAIL", want,
                                                     ", ".join(rules) or "no findings"))
        if not good:
            for f in findings:
                print("    " + f)
    print("site-lint selftest: %s" % ("ok" if ok else "FAIL"))
    return 0 if ok else 1


def main(argv):
    if argv == ["--selftest"]:
        return selftest()
    if len(argv) != 1 or argv[0].startswith("-"):
        print("usage: site_lint.py <repo_root> | --selftest", file=sys.stderr)
        return 2
    return run(argv[0])


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1:]))
    except SystemExit:
        raise
    except Exception:
        traceback.print_exc()
        print("site-lint: internal error", file=sys.stderr)
        sys.exit(2)
