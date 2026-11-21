#!/usr/bin/env python3
"""Build the kickoff review site into _site/ (GitHub Pages scaffold).

Renders PLAN.md and the docs/c4 diagram set to static HTML so the kickoff
proposal can be reviewed through GitHub Pages. Chunk 0 supersedes this
scaffold with the full book site.

Usage: python3 .github/scripts/build_site.py [output_dir]   (default: _site)
Needs: the `markdown` package (pip install markdown).
"""

import html
import posixpath
import re
import shutil
import sys
from pathlib import Path

import markdown

ROOT = Path(__file__).resolve().parents[2]
REPO_URL = "https://github.com/nickstrad/decentralized-object-store"
PROJECT = "Decentralized Object Store & Local-First Sync Engine"
MERMAID_CDN = "https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs"

CSS = """
:root { color-scheme: light dark; --fg: #1c1e21; --bg: #ffffff; --muted: #5c6370;
  --line: #d8dce2; --soft: #f5f6f8; --link: #0b5cad; }
@media (prefers-color-scheme: dark) {
  :root { --fg: #e3e5e8; --bg: #15171a; --muted: #9aa1ac; --line: #343941;
    --soft: #1e2126; --link: #7ab7f5; } }
* { box-sizing: border-box; }
body { margin: 0; color: var(--fg); background: var(--bg);
  font: 16px/1.6 system-ui, -apple-system, "Segoe UI", sans-serif; }
header.site { border-bottom: 1px solid var(--line); background: var(--soft); }
header.site nav, main, footer.site { max-width: 62rem; margin: 0 auto; padding: 0 1.25rem; }
header.site nav { display: flex; flex-wrap: wrap; gap: 0.25rem 1.25rem;
  padding-top: 0.75rem; padding-bottom: 0.75rem; }
header.site .name { font-weight: 600; margin-right: auto; }
main { padding-top: 1.5rem; padding-bottom: 3rem; }
footer.site { color: var(--muted); font-size: 0.875rem; padding-bottom: 2rem; }
a { color: var(--link); }
h1, h2, h3, h4 { line-height: 1.25; margin: 2rem 0 0.75rem; }
h1 { font-size: 1.75rem; margin-top: 0.5rem; }
h2 { font-size: 1.35rem; border-bottom: 1px solid var(--line); padding-bottom: 0.25rem; }
h3 { font-size: 1.1rem; }
code, pre { font-family: ui-monospace, "SF Mono", Menlo, Consolas, "DejaVu Sans Mono", monospace;
  font-size: 0.875em; }
code { background: var(--soft); padding: 0.1em 0.3em; border-radius: 3px; }
pre { background: var(--soft); border: 1px solid var(--line); border-radius: 6px;
  padding: 0.9rem 1rem; overflow-x: auto; line-height: 1.25; }
pre code { background: none; padding: 0; font-size: inherit; }
pre.mermaid { background: #ffffff; color: #1c1e21; text-align: center; line-height: 1.4; }
.table-wrap { overflow-x: auto; margin: 1rem 0; }
table { border-collapse: collapse; font-size: 0.9rem; }
th, td { border: 1px solid var(--line); padding: 0.4rem 0.6rem; vertical-align: top; text-align: left; }
th { background: var(--soft); }
hr { border: 0; border-top: 1px solid var(--line); margin: 2rem 0; }
.banner { border: 1px solid var(--line); border-left: 4px solid #c9820a; background: var(--soft);
  padding: 0.6rem 0.9rem; border-radius: 4px; margin: 0 0 1.5rem; }
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
<span class="name">{project}</span>
<a href="{up}index.html">Home</a>
<a href="{up}plan.html">Plan</a>
<a href="{up}c4/index.html">C4 diagrams</a>
<a href="{repo}">Repository</a>
</nav></header>
<main>
{banner}{body}
</main>
<footer class="site">Kickoff review site, built from the repository at each push to main.</footer>
{script}</body>
</html>
"""

MERMAID_SCRIPT = """<script type="module">
import mermaid from "%s";
mermaid.initialize({ startOnLoad: false, theme: "neutral", securityLevel: "strict" });
await mermaid.run({ querySelector: "pre.mermaid" });
</script>
""" % MERMAID_CDN

BANNER = ('<p class="banner">Kickoff proposal: <strong>target architecture</strong>, '
          "awaiting Nick's sign-off. No product code exists yet.</p>\n")


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
    body = body.replace("<table>", '<div class="table-wrap"><table>')
    body = body.replace("</table>", "</table></div>")
    return body


def rewrite_links(body, src, out, pages):
    """Point relative links at site pages, or at GitHub for files not on the site.

    src: repo-relative source path of this page. out: site-relative output path.
    pages: {repo-relative source path: site-relative output path}.
    """
    src_dir = posixpath.dirname(src)
    out_dir = posixpath.dirname(out)

    def fix(match):
        href = html.unescape(match.group(1))
        if re.match(r"^([a-z][a-z0-9+.-]*:|#|//)", href, flags=re.I):
            return match.group(0)
        path, sep, frag = href.partition("#")
        target = posixpath.normpath(posixpath.join(src_dir, path))
        if target in pages:
            new = posixpath.relpath(pages[target], out_dir or ".")
        elif not target.startswith("..") and (ROOT / target).exists():
            kind = "tree" if (ROOT / target).is_dir() else "blob"
            new = "%s/%s/main/%s" % (REPO_URL, kind, target)
        else:
            return match.group(0)
        return 'href="%s"' % html.escape(new + sep + frag, quote=True)

    return re.sub(r'href="([^"]*)"', fix, body)


def title_of(text, fallback):
    m = re.search(r"^#\s+(.+)$", text, flags=re.M)
    return m.group(1).strip() if m else fallback


def write_page(site, out, title, body, banner=True):
    up = "../" * out.count("/")
    page = TEMPLATE.format(
        title=html.escape(title),
        css=CSS,
        project=html.escape(PROJECT),
        up=up,
        repo=REPO_URL,
        banner=BANNER if banner else "",
        body=body,
        script=MERMAID_SCRIPT if 'class="mermaid"' in body else "",
    )
    dest = site / out
    dest.parent.mkdir(parents=True, exist_ok=True)
    dest.write_text(page, encoding="utf-8")
    print("wrote", out)


def north_star(plan_text):
    """First paragraph of PLAN.md section 1 (the plan's own summary)."""
    m = re.search(r"^## 1\. North Star\s*\n+(.+?)\n\s*\n", plan_text, flags=re.S | re.M)
    if not m:
        sys.exit("build_site: PLAN.md section '## 1. North Star' not found")
    return m.group(1)


def main():
    site = Path(sys.argv[1] if len(sys.argv) > 1 else "_site").resolve()
    if site.exists():
        shutil.rmtree(site)
    site.mkdir(parents=True)

    c4_dir = ROOT / "docs" / "c4"
    diagrams = sorted(p for p in c4_dir.glob("*.md") if p.name != "README.md")
    if not diagrams:
        sys.exit("build_site: no diagram files in docs/c4")

    pages = {"PLAN.md": "plan.html", "docs/c4/README.md": "c4/index.html",
             "docs/c4/model.json": "c4/model.json"}
    for p in diagrams:
        pages["docs/c4/" + p.name] = "c4/" + p.stem + ".html"

    for src, out in pages.items():
        if not out.endswith(".html"):
            continue
        text = (ROOT / src).read_text(encoding="utf-8")
        body = rewrite_links(render_markdown(text), src, out, pages)
        write_page(site, out, title_of(text, src) + " — " + PROJECT, body)

    shutil.copyfile(c4_dir / "model.json", site / "c4" / "model.json")
    print("wrote c4/model.json")

    plan_text = (ROOT / "PLAN.md").read_text(encoding="utf-8")
    summary = render_markdown(north_star(plan_text))
    links = "".join(
        '<li><a href="%s">%s</a></li>\n'
        % (pages["docs/c4/" + p.name], html.escape(title_of(p.read_text(encoding="utf-8"), p.stem)))
        for p in diagrams
    )
    body = (
        "<h1>%s</h1>\n" % html.escape(PROJECT)
        + "<p>This is the kickoff review site. It presents the Phase 1 plan and the "
        "initial C4 diagram set. Both describe the target architecture and await "
        "Nick's sign-off; no product code exists yet.</p>\n"
        + "<h2>Review</h2>\n<ul>\n"
        '<li><a href="plan.html">The plan</a> (PLAN.md)</li>\n'
        '<li><a href="c4/index.html">C4 diagrams</a> (docs/c4): index, nine diagrams, '
        '<a href="c4/model.json">model.json</a></li>\n</ul>\n'
        + "<h2>Summary, from the plan</h2>\n" + summary + "\n"
        + "<h2>Diagrams</h2>\n<ul>\n" + links + "</ul>\n"
    )
    write_page(site, "index.html", PROJECT, body, banner=False)
    (site / ".nojekyll").write_text("", encoding="utf-8")


if __name__ == "__main__":
    main()
