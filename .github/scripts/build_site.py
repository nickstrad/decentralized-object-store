#!/usr/bin/env python3
"""Build the GitHub Pages site: front page, the book, the C4 section, the plan.

Usage: python3 .github/scripts/build_site.py [--root REPO_DIR] [output_dir]
  output_dir defaults to _site; --root defaults to the repository holding this script.
Needs: the `markdown` package (pip install markdown). Nothing else.

Sources and outputs (all links relative, so the site works under any path prefix):
  docs/index.md          -> index.html  (if absent: the README title and the navigation)
  book/manifest.json     -> book/index.html, one book/<slug>.html per entry
  docs/c4/README.md      -> c4/index.html; docs/c4/NN-*.md -> c4/NN-*.html;
  docs/c4/model.json     -> c4/model.json
  PLAN.md                -> plan.html
Never published: docs/knowledge/, agent_work/, STATE.md, kb/.

book/manifest.json schema:
  {"title": str,
   "chapters":    [{"n": int, "slug": str, "title": str, "file": str, "status": str}, ...],
   "front_matter": [{"slug": str, "title": str, "file": str, "status": str}, ...]}
  file is relative to book/ and ends in .md; slugs are unique across both lists;
  chapter numbers are unique and ascending in list order. Reading order is
  front_matter, then chapters.
Exit status: 0 on success; 1 with a message on a broken manifest or missing source.
"""

import html
import json
import posixpath
import re
import shutil
import sys
from pathlib import Path

import markdown

SCRIPT_ROOT = Path(__file__).resolve().parents[2]
REPO_URL = "https://github.com/nickstrad/decentralized-object-store"
MERMAID_CDN = "https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs"
NEVER_PUBLISH = ("docs/knowledge/", "agent_work/", "STATE.md", "kb/", ".kb/")

SLUG_RE = re.compile(r"^[a-z0-9][a-z0-9-]*$")
STATUS_RE = re.compile(r"^[a-z][a-z-]*$")
CHAPTER_KEYS = {"n", "slug", "title", "file", "status"}
FRONT_KEYS = {"slug", "title", "file", "status"}

CSS = """
:root { color-scheme: light dark; --fg: #1c1e21; --bg: #ffffff; --muted: #5c6370;
  --line: #d8dce2; --soft: #f5f6f8; --link: #0b5cad;
  --k: #a626a4; --s: #50a14f; --c: #8a8f98; --t: #c18401; --num: #986801; }
@media (prefers-color-scheme: dark) {
  :root { --fg: #e3e5e8; --bg: #15171a; --muted: #9aa1ac; --line: #343941;
    --soft: #1e2126; --link: #7ab7f5;
    --k: #c678dd; --s: #98c379; --c: #7f848e; --t: #e5c07b; --num: #d19a66; } }
* { box-sizing: border-box; }
body { margin: 0; color: var(--fg); background: var(--bg);
  font: 17px/1.65 system-ui, -apple-system, "Segoe UI", sans-serif; }
header.site { border-bottom: 1px solid var(--line); background: var(--soft); }
header.site nav, main, footer.site { max-width: 62rem; margin: 0 auto; padding: 0 1.25rem; }
main.reading { max-width: 46rem; }
header.site nav { display: flex; flex-wrap: wrap; align-items: baseline; gap: 0.25rem 1.25rem;
  padding-top: 0.75rem; padding-bottom: 0.75rem; }
header.site .name { font-weight: 600; margin-right: auto; color: var(--fg); text-decoration: none; }
header.site a[aria-current] { font-weight: 600; text-decoration: none; color: var(--fg); }
main { padding-top: 1.5rem; padding-bottom: 3rem; }
footer.site { border-top: 1px solid var(--line); color: var(--muted); font-size: 0.875rem;
  padding-top: 1rem; padding-bottom: 2rem; }
a { color: var(--link); }
h1, h2, h3, h4 { line-height: 1.25; margin: 2rem 0 0.75rem; }
h1 { font-size: 1.85rem; margin-top: 0.5rem; }
h2 { font-size: 1.35rem; border-bottom: 1px solid var(--line); padding-bottom: 0.25rem; }
h3 { font-size: 1.1rem; }
img { max-width: 100%; height: auto; }
code, pre { font-family: ui-monospace, "SF Mono", Menlo, Consolas, "DejaVu Sans Mono", monospace;
  font-size: 0.85em; }
code { background: var(--soft); padding: 0.1em 0.3em; border-radius: 3px; }
pre { background: var(--soft); border: 1px solid var(--line); border-radius: 6px;
  padding: 0.9rem 1rem; overflow-x: auto; line-height: 1.35; white-space: pre; }
pre code { background: none; padding: 0; font-size: inherit; white-space: pre; }
pre.diagram { line-height: 1.2; }
pre.mermaid { background: #ffffff; color: #1c1e21; text-align: center; line-height: 1.4; }
.tok-k { color: var(--k); } .tok-s { color: var(--s); } .tok-c { color: var(--c); font-style: italic; }
.tok-t { color: var(--t); } .tok-n { color: var(--num); }
.table-wrap { overflow-x: auto; margin: 1rem 0; }
table { border-collapse: collapse; font-size: 0.9rem; }
th, td { border: 1px solid var(--line); padding: 0.4rem 0.6rem; vertical-align: top; text-align: left; }
th { background: var(--soft); }
hr { border: 0; border-top: 1px solid var(--line); margin: 2rem 0; }
ol.toc { padding-left: 0; list-style: none; }
ol.toc li { padding: 0.35rem 0; border-bottom: 1px solid var(--line); display: flex; gap: 0.75rem; }
ol.toc .num { color: var(--muted); min-width: 1.5rem; text-align: right; }
.status { color: var(--muted); font-size: 0.8rem; border: 1px solid var(--line);
  border-radius: 999px; padding: 0 0.5rem; margin-left: auto; align-self: center; }
nav.pager { display: flex; justify-content: space-between; gap: 1rem; flex-wrap: wrap;
  border-top: 1px solid var(--line); margin-top: 3rem; padding-top: 1rem; }
nav.pager .next { margin-left: auto; text-align: right; }
@media (max-width: 40rem) { body { font-size: 16px; } h1 { font-size: 1.5rem; } }
"""

TEMPLATE = """<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{title}</title>
<style>{css}</style>
</head>
<body>
<header class="site"><nav>
<a class="name" href="{up}index.html">{project}</a>
{links}
</nav></header>
<main class="{width}">
{body}
</main>
<footer class="site"><a href="{repo}">{repo_label}</a></footer>
{script}</body>
</html>
"""

NAV = [("home", "Home", "index.html"), ("book", "Book", "book/index.html"),
       ("c4", "C4 diagrams", "c4/index.html"), ("plan", "Plan", "plan.html")]

MERMAID_SCRIPT = """<script type="module">
import mermaid from "%s";
mermaid.initialize({ startOnLoad: false, theme: "neutral", securityLevel: "strict" });
await mermaid.run({ querySelector: "pre.mermaid" });
</script>
""" % MERMAID_CDN

GO_KEYWORDS = set("""break case chan const continue default defer else fallthrough for func go
goto if import interface map package range return select struct switch type var""".split())
GO_TYPES = set("""bool byte complex64 complex128 error float32 float64 int int8 int16 int32 int64
rune string uint uint8 uint16 uint32 uint64 uintptr any comparable true false nil iota
append cap clear close copy delete len make max min new panic print println recover""".split())
GO_TOKEN = re.compile(
    r"(?P<c>//[^\n]*|/\*.*?\*/)"
    r"|(?P<s>\"(?:\\.|[^\"\\\n])*\"|`[^`]*`|'(?:\\.|[^'\\\n])+')"
    r"|(?P<n>\b(?:0[xX][0-9a-fA-F_]+|0[bB][01_]+|0[oO][0-7_]+|\d[\d_]*(?:\.\d+)?(?:[eE][+-]?\d+)?)\b)"
    r"|(?P<w>[A-Za-z_]\w*)",
    re.S,
)


class BuildError(Exception):
    pass


def highlight_go(code_html):
    """Dependency-free Go highlighting: keywords, builtins, strings, comments, numbers."""
    code = html.unescape(code_html)
    out, pos = [], 0
    for m in GO_TOKEN.finditer(code):
        out.append(html.escape(code[pos:m.start()], quote=False))
        tok, kind = m.group(0), m.lastgroup
        if kind == "w":
            kind = "k" if tok in GO_KEYWORDS else "t" if tok in GO_TYPES else None
        text = html.escape(tok, quote=False)
        out.append('<span class="tok-%s">%s</span>' % (kind, text) if kind else text)
        pos = m.end()
    out.append(html.escape(code[pos:], quote=False))
    return "".join(out)


def render_markdown(text):
    md = markdown.Markdown(extensions=["tables", "fenced_code", "sane_lists", "toc"])
    body = md.convert(text)
    # Mermaid blocks: mermaid.js transforms <pre class="mermaid"> elements.
    body = re.sub(
        r'<pre><code class="language-mermaid">(.*?)</code></pre>',
        lambda m: '<pre class="mermaid">' + m.group(1) + "</pre>",
        body,
        flags=re.S,
    )
    # Unicode text diagrams: monospace, never wrapped.
    body = body.replace('<pre><code class="language-text">',
                        '<pre class="diagram"><code class="language-text">')
    body = re.sub(
        r'(<pre><code class="language-go">)(.*?)(</code></pre>)',
        lambda m: m.group(1) + highlight_go(m.group(2)) + m.group(3),
        body,
        flags=re.S,
    )
    body = body.replace("<table>", '<div class="table-wrap"><table>')
    body = body.replace("</table>", "</table></div>")
    return body


class Site:
    def __init__(self, root, out):
        self.root = root
        self.out = out
        self.pages = {}    # repo-relative source path -> site-relative output path
        self.assets = {}   # repo-relative asset path -> site-relative output path
        self.written = []

    def add(self, src, out):
        if src.startswith(NEVER_PUBLISH):
            raise BuildError("refusing to publish %s (never-published path)" % src)
        if out in self.pages.values():
            raise BuildError("two sources map to %s" % out)
        self.pages[src] = out

    def rewrite_links(self, body, src, out):
        """Point relative links at site pages, book assets, or GitHub for other repo files."""
        src_dir = posixpath.dirname(src)
        out_dir = posixpath.dirname(out) or "."

        def fix(match):
            attr, value = match.group(1), html.unescape(match.group(2))
            if re.match(r"^([a-z][a-z0-9+.-]*:|#|//)", value, flags=re.I):
                return match.group(0)
            path, sep, frag = value.partition("#")
            if not path:
                return match.group(0)
            target = posixpath.normpath(posixpath.join(src_dir, path))
            if target in self.pages:
                new = posixpath.relpath(self.pages[target], out_dir)
            elif (target.startswith("book/") and (self.root / target).is_file()
                  and target != "book/manifest.json"):
                self.assets[target] = target
                new = posixpath.relpath(target, out_dir)
            elif not target.startswith("..") and (self.root / target).exists():
                kind = "tree" if (self.root / target).is_dir() else "blob"
                new = "%s/%s/main/%s" % (REPO_URL, kind, target)
            else:
                return match.group(0)
            return '%s="%s"' % (attr, html.escape(new + sep + frag, quote=True))

        return re.sub(r'\b(href|src)="([^"]*)"', fix, body)

    def write(self, out, title, body, section, reading=False):
        up = "../" * out.count("/")
        links = "\n".join(
            '<a href="%s%s"%s>%s</a>' % (up, href, ' aria-current="page"' if key == section else "",
                                         label)
            for key, label, href in NAV
        ) + '\n<a href="%s">GitHub</a>' % REPO_URL
        page = TEMPLATE.format(
            title=html.escape(title),
            css=CSS,
            project=html.escape(self.project),
            up=up,
            links=links,
            width="reading" if reading else "wide",
            repo=REPO_URL,
            repo_label=html.escape(REPO_URL.split("://", 1)[1]),
            body=body,
            script=MERMAID_SCRIPT if 'class="mermaid"' in body else "",
        )
        dest = self.out / out
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_text(page, encoding="utf-8")
        self.written.append(out)

    def render_source(self, src, section, reading=False, extra=""):
        text = read(self.root / src)
        out = self.pages[src]
        body = self.rewrite_links(render_markdown(text), src, out) + extra
        self.write(out, title_of(text, src) + " — " + self.project, body, section, reading)


def read(path):
    try:
        return path.read_text(encoding="utf-8")
    except FileNotFoundError:
        raise BuildError("missing source file %s" % path)


def title_of(text, fallback):
    m = re.search(r"^#\s+(.+?)\s*#*\s*$", text, flags=re.M)
    return m.group(1).strip() if m else fallback


def load_manifest(root):
    path = root / "book" / "manifest.json"
    where = "book/manifest.json"
    try:
        data = json.loads(read(path))
    except json.JSONDecodeError as e:
        raise BuildError("%s: bad JSON: %s" % (where, e))
    if not isinstance(data, dict):
        raise BuildError("%s: top level must be an object" % where)
    extra = set(data) - {"title", "chapters", "front_matter"}
    if extra:
        raise BuildError("%s: unknown keys %s" % (where, sorted(extra)))
    if not isinstance(data.get("title"), str) or not data["title"].strip():
        raise BuildError("%s: 'title' must be a non-empty string" % where)
    entries, slugs, last_n = [], set(), None
    for kind in ("front_matter", "chapters"):
        items = data.get(kind)
        if not isinstance(items, list):
            raise BuildError("%s: '%s' must be a list" % (where, kind))
        keys = CHAPTER_KEYS if kind == "chapters" else FRONT_KEYS
        for i, e in enumerate(items):
            label = "%s: %s[%d]" % (where, kind, i)
            if not isinstance(e, dict) or set(e) != keys:
                raise BuildError("%s: must be an object with exactly the keys %s"
                                 % (label, sorted(keys)))
            if kind == "chapters":
                n = e["n"]
                if not isinstance(n, int) or isinstance(n, bool) or n < 1:
                    raise BuildError("%s: 'n' must be a positive integer" % label)
                if last_n is not None and n <= last_n:
                    raise BuildError("%s: chapter numbers must be unique and ascending (%d after %d)"
                                     % (label, n, last_n))
                last_n = n
            for k in ("slug", "title", "file", "status"):
                if not isinstance(e[k], str) or not e[k].strip():
                    raise BuildError("%s: '%s' must be a non-empty string" % (label, k))
            slug, file = e["slug"], e["file"]
            if not SLUG_RE.match(slug) or slug == "index":
                raise BuildError("%s: bad slug %r (lowercase letters, digits, hyphens; not 'index')"
                                 % (label, slug))
            if slug in slugs:
                raise BuildError("%s: duplicate slug %r" % (label, slug))
            slugs.add(slug)
            if not STATUS_RE.match(e["status"]):
                raise BuildError("%s: bad status %r" % (label, e["status"]))
            norm = posixpath.normpath(file)
            if (file.startswith("/") or norm.startswith("..") or not norm.endswith(".md")
                    or norm != file):
                raise BuildError("%s: 'file' must be a normalized .md path inside book/, got %r"
                                 % (label, file))
            if not (root / "book" / file).is_file():
                raise BuildError("%s: chapter file book/%s does not exist" % (label, file))
            entries.append(dict(e, kind=kind, src="book/" + file))
    return data["title"], entries


def build_book(site, title, entries):
    for e in entries:
        site.add(e["src"], "book/%s.html" % e["slug"])
    site.pages["book/manifest.json"] = "book/index.html"
    for i, e in enumerate(entries):
        pager = ['<nav class="pager">']
        if i > 0:
            p = entries[i - 1]
            pager.append('<a class="prev" rel="prev" href="%s.html">&larr; %s</a>'
                         % (p["slug"], html.escape(p["title"])))
        pager.append('<a href="index.html">Contents</a>')
        if i + 1 < len(entries):
            n = entries[i + 1]
            pager.append('<a class="next" rel="next" href="%s.html">%s &rarr;</a>'
                         % (n["slug"], html.escape(n["title"])))
        pager.append("</nav>\n")
        site.render_source(e["src"], "book", reading=True, extra="\n".join(pager))

    body = "<h1>%s</h1>\n" % html.escape(title)
    if entries:
        rows = []
        for e in entries:
            num = str(e["n"]) if e["kind"] == "chapters" else ""
            rows.append('<li><span class="num">%s</span><a href="%s.html">%s</a>'
                        '<span class="status">%s</span></li>'
                        % (num, e["slug"], html.escape(e["title"]), html.escape(e["status"])))
        body += '<ol class="toc">\n%s\n</ol>\n' % "\n".join(rows)
    else:
        body += "<p>No chapters yet</p>\n"
    site.write("book/index.html", title + " — " + site.project, body, "book", reading=True)


def build(root, out):
    readme = read(root / "README.md")
    site = Site(root, out)
    site.project = title_of(readme, "README.md")

    book_title, entries = load_manifest(root)

    c4_dir = root / "docs" / "c4"
    diagrams = sorted(p for p in c4_dir.glob("*.md") if p.name != "README.md")
    if not diagrams:
        raise BuildError("no diagram files in docs/c4")
    if not (c4_dir / "model.json").is_file():
        raise BuildError("docs/c4/model.json is missing")

    has_index = (root / "docs" / "index.md").is_file()
    if has_index:
        site.add("docs/index.md", "index.html")
    site.add("PLAN.md", "plan.html")
    site.add("docs/c4/README.md", "c4/index.html")
    site.pages["docs/c4/model.json"] = "c4/model.json"
    for p in diagrams:
        site.add("docs/c4/" + p.name, "c4/" + p.stem + ".html")

    if out.exists():
        shutil.rmtree(out)
    out.mkdir(parents=True)

    build_book(site, book_title, entries)
    if has_index:
        site.render_source("docs/index.md", "home", reading=True)
    else:
        site.write("index.html", site.project,
                   "<h1>%s</h1>\n" % html.escape(site.project), "home", reading=True)
    site.render_source("PLAN.md", "plan")
    site.render_source("docs/c4/README.md", "c4")
    for p in diagrams:
        site.render_source("docs/c4/" + p.name, "c4")
    shutil.copyfile(c4_dir / "model.json", out / "c4" / "model.json")
    for src, dst in sorted(site.assets.items()):
        (out / dst).parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(root / src, out / dst)
    (out / ".nojekyll").write_text("", encoding="utf-8")

    n_book = sum(1 for e in entries if e["kind"] == "chapters")
    print("build_site: %d pages into %s (front page%s, book index + %d chapters + %d front matter, "
          "c4 index + %d diagrams + model.json, plan; %d assets)"
          % (len(site.written), out, "" if has_index else " [no docs/index.md]", n_book,
             len(entries) - n_book, len(diagrams), len(site.assets)))


def main(argv):
    args = list(argv)
    root = SCRIPT_ROOT
    if args[:1] == ["--root"]:
        if len(args) < 2:
            sys.exit("usage: build_site.py [--root REPO_DIR] [output_dir]")
        root = Path(args[1]).resolve()
        args = args[2:]
    if len(args) > 1 or (args and args[0].startswith("-")):
        sys.exit("usage: build_site.py [--root REPO_DIR] [output_dir]")
    out = Path(args[0] if args else "_site").resolve()
    if out == root or out in root.parents:
        sys.exit("build_site: refusing to write into %s (it would delete the repository)" % out)
    try:
        build(root, out)
    except BuildError as e:
        print("build_site: error: %s" % e, file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main(sys.argv[1:])
