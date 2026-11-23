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
  symlink           a symlink under book/ or docs/, or README.md / PLAN.md is a symlink
  private-link      a reader-facing source or built page links to docs/knowledge/, agent_work/,
                    kb/, .kb/, STATE.md or .git/ (including via ../ and GitHub URLs)
  broken-anchor     an internal link's #fragment matches no id=/name= in the target built page
PLAN.md and plan.html are exempt from machine-ref and private-link (the plan legitimately
names them), not from the link check. External http(s) links are not fetched.
Exit: 0 clean, 1 findings, 2 internal error. Needs python3 + markdown only.
"""

import json
import os
import re
import subprocess
import sys
import shutil
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
NEVER = ("docs/knowledge/", "agent_work/", "kb/", ".kb/", ".git/", "STATE.md")
REPO_URL = "https://github.com/nickstrad/object-store"
# name -> expected rule (None = clean). Trees under testdata/site/<name>.
SELFTEST = {"good": None, "missing-chapter": "chapter-missing",
            "broken-link": "broken-link", "root-path": "machine-ref",
            "root-absolute-link": "absolute-link", "private-link": "private-link",
            "private-link-state": "private-link", "broken-anchor": "broken-anchor",
            "anchor-good": None, "unlisted-chapter": "chapter-unlisted",
            "manifest-abs": "manifest", "manifest-dotdot": "manifest",
            "manifest-backslash": "manifest", "manifest-norm": "manifest"}
# name -> (expected rule, setup(tree) that creates symlinks in a temp copy of testdata/site/good)
SYMLINK_CASES = {}


def _ln(rel, target):
    def setup(tree):
        p = tree / rel
        if p.exists() or p.is_symlink():
            p.unlink()
        p.parent.mkdir(parents=True, exist_ok=True)
        os.symlink(target, p)
    return setup


def _mk(rel, text="# secret\n"):
    def setup(tree):
        p = tree / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text, encoding="utf-8")
    return setup


def _chain(*fs):
    def setup(tree):
        for f in fs:
            f(tree)
    return setup


SYMLINK_CASES = {
    "symlink-chapter-knowledge": ("symlink", _chain(
        _mk("docs/knowledge/x.md", "# Alpha\n"),
        _ln("book/chapters/01-alpha.md", "../../docs/knowledge/x.md"))),
    "symlink-chapter-agentwork": ("symlink", _chain(
        _mk("agent_work/x.md", "# Alpha\n"),
        _ln("book/chapters/01-alpha.md", "../../agent_work/x.md"))),
    "symlink-chapter-kb": ("symlink", _chain(
        _mk("kb/x.md", "# Alpha\n"), _ln("book/chapters/01-alpha.md", "../../kb/x.md"))),
    "symlink-chapter-state": ("symlink", _chain(
        _mk("STATE.md", "# Alpha\n"), _ln("book/chapters/01-alpha.md", "../../STATE.md"))),
    "symlink-asset": ("symlink", _chain(
        _mk("docs/knowledge/x.md"), _ln("book/asset.md", "../docs/knowledge/x.md"),
        _mk("book/chapters/02-beta.md", "# Beta\n\n[a](../asset.md)\n"))),
    "symlink-readme": ("symlink", _chain(
        _mk("STATE.md", "# Fixture Project\n"), _ln("README.md", "STATE.md"))),
    "symlink-plan": ("symlink", _chain(
        _mk("STATE.md", "# Plan\n"), _ln("PLAN.md", "STATE.md"))),
    "symlink-docs-file": ("symlink", _ln("docs/c4/extra.md", "../../STATE.md")),
}


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
            if p.is_symlink() or not p.is_file() or not is_text(p):
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


def check_symlinks(root, findings):
    for name in ("README.md", "PLAN.md", "book", "docs"):
        if (root / name).is_symlink():
            findings.append("%s: symlink: symlinks are never published" % name)
    for top in ("book", "docs"):
        base = root / top
        if base.is_symlink() or not base.is_dir():
            continue
        for dirpath, dirnames, filenames in os.walk(base, followlinks=False):
            for n in sorted(dirnames + filenames):
                p = Path(dirpath) / n
                if p.is_symlink():
                    findings.append("%s: symlink: symlinks are never published"
                                    % p.relative_to(root).as_posix())


def is_never(rel):
    rel = os.path.normpath(rel).replace(os.sep, "/")
    return any((rel + "/").startswith(p if p.endswith("/") else p + "/") for p in NEVER)


MD_LINK = re.compile(r"(?<!\\)\]\(\s*<?([^)\s>]+)|^\s{0,3}\[[^\]]+\]:\s*<?(\S+)|"
                     r"\b(?:href|src)=[\"']([^\"']+)", re.M)


def check_private_links(root, src, findings):
    """Flag links in a reader-facing source that point at never-published files."""
    rel = src.relative_to(root).as_posix()
    text = src.read_text(encoding="utf-8", errors="replace")
    text = re.sub(r"^(```|~~~).*?^\1", "", text, flags=re.S | re.M)
    for m in MD_LINK.finditer(text):
        url = next(g for g in m.groups() if g)
        parts = urlsplit(url)
        if parts.scheme or url.startswith("//") or not parts.path:
            if parts.scheme in ("http", "https") and parts.netloc == "github.com":
                m2 = re.match(r"^/nickstrad/object-store/(?:blob|tree)/[^/]+/(.*)$",
                              unquote(parts.path))
                if m2 and is_never(m2.group(1)):
                    findings.append("%s: private-link: %r points at an operational file" % (rel, url))
            continue
        if parts.path.startswith("/"):
            continue  # reported as absolute-link on the built page
        target = os.path.normpath(os.path.join(os.path.dirname(rel), unquote(parts.path)))
        if target == ".." or target.startswith("../"):
            continue
        if is_never(target):
            findings.append("%s: private-link: %r points at an operational file" % (rel, url))


def check_manifest_path(file, label, findings):
    norm = os.path.normpath(file) if file else ""
    bad = (not file or file.startswith("/") or "\\" in file or "\0" in file
           or norm == ".." or norm.startswith("../") or norm != file
           or not file.endswith(".md"))
    if bad:
        findings.append("book/manifest.json: manifest: %s: bad 'file' %r (must be a normalized "
                        ".md path inside book/)" % (label, file))
    return not bad


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
            if not check_manifest_path(e["file"], "%s[%d]" % (kind, i), findings):
                continue
            real_book = os.path.realpath(root / "book")
            if not os.path.realpath(path).startswith(real_book + os.sep):
                findings.append("%s: manifest: %s[%d] resolves outside book/" % (label, kind, i))
                continue
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


class IdParser(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.ids = set()

    def handle_starttag(self, tag, attrs):
        for name, value in attrs:
            if name in ("id", "name") and value:
                self.ids.add(value)


_ID_CACHE = {}


def page_ids(path):
    if path not in _ID_CACHE:
        p = IdParser()
        p.feed(path.read_text(encoding="utf-8", errors="replace"))
        _ID_CACHE[path] = p.ids
    return _ID_CACHE[path]


def check_site(site, findings):
    _ID_CACHE.clear()
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
                if (parts.scheme in ("http", "https") and parts.netloc == "github.com"
                        and rel not in EXEMPT_BUILT):
                    m2 = re.match(r"^/nickstrad/object-store/(?:blob|tree)/[^/]+/(.*)$",
                                  unquote(parts.path))
                    if m2 and is_never(m2.group(1)):
                        findings.append("%s: private-link: <%s %s=%r> points at an operational file"
                                        % (label, tag, attr, url))
                continue  # external (http, https, mailto, data, ...): not fetched
            frag = unquote(parts.fragment)
            if not parts.path:
                if frag and frag not in page_ids(page):
                    findings.append("%s: broken-anchor: <%s %s=%r> no such id in this page"
                                    % (label, tag, attr, url))
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
            elif frag and target.suffix == ".html" and frag not in page_ids(target):
                findings.append("%s: broken-anchor: <%s %s=%r> no such id in %s"
                                % (label, tag, attr, url, target.name))


def lint(root, quiet=False):
    root = Path(root).resolve()
    if not root.is_dir():
        raise RuntimeError("not a directory: %s" % root)
    findings = []
    check_symlinks(root, findings)
    check_manifest(root, findings)
    for src in reader_sources(root):
        rel = src.relative_to(root).as_posix()
        if rel not in EXEMPT_SOURCES:
            scan_machine_refs(src, rel, findings)
            if src.suffix.lower() == ".md":
                check_private_links(root, src, findings)
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
    """Run every fixture case; exit 0 only if each behaves as expected. No network."""
    ok = True
    cases = []
    for name, expected in SELFTEST.items():
        cases.append((name, expected, FIXTURES / name, None))
    for name, (expected, setup) in SYMLINK_CASES.items():
        cases.append((name, expected, FIXTURES / "good", setup))
    with tempfile.TemporaryDirectory(prefix="site-lint-selftest-") as tmp:
        for name, expected, tree, setup in cases:
            try:
                if not tree.is_dir():
                    raise RuntimeError("fixture tree missing: %s" % tree)
                if setup is not None:  # symlink cases are created at run time, never committed
                    work = Path(tmp) / name
                    shutil.copytree(tree, work, symlinks=True)
                    setup(work)
                    tree = work
                findings = lint(tree, quiet=True)
                rules = sorted({m.group(1) for m in (re.search(r"^\S+?:(?:\d+:)? ([a-z-]+): ", f) for f in findings) if m})
                if expected is None:
                    good = not findings
                    want = "no findings"
                else:
                    good = expected in rules
                    want = "rule %s" % expected
                got = ", ".join(rules) or "no findings"
            except Exception as e:  # a crashing case is a failing case
                findings, good, want, got = [], False, "a result", "error %r" % e
            ok &= good
            print("selftest %-28s %s: want %s, got %s" % (name, "PASS" if good else "FAIL", want, got))
            if not good:
                for f in findings:
                    print("    " + f)
    print("site-lint selftest: %s (%d cases)" % ("ok" if ok else "FAIL", len(cases)))
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
