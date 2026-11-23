#!/usr/bin/env python3
"""Link check on the DEPLOYED site (Chunk 8's publishing checks; site_lint.py checks the
built tree before a push, this checks what Pages actually serves after one).

Usage:
  site-links.py [<base-url>] [--external]
      default base: https://nickstrad.github.io/decentralized-object-store/
  site-links.py --selftest
      serves two fixture sites from a temporary directory on loopback and checks that a
      clean one passes and that a dead link, a dead anchor and a missing image are found

Crawls every page reachable from the base URL that lives under it, and reports, one finding
per line as `<page>: <rule>: <detail>`:
  dead-link     an internal href/src that does not answer 200
  dead-anchor   an internal link whose #fragment matches no id=/name= on the target page
  escapes-site  a root-relative or same-host link that leaves the base path (it would break
                under the project-site prefix, or points at another project's pages)
  dead-external (only with --external) an http(s) link to another host that answers 4xx/5xx
                or does not answer; without the flag external links are counted, not fetched
Exit: 0 clean, 1 findings, 2 usage or the base URL itself does not answer.
Needs python3 only (standard library). Sends a generic User-Agent and nothing else.
"""

import http.server
import sys
import tempfile
import threading
import urllib.error
import urllib.request
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import urldefrag, urljoin, urlsplit

DEFAULT_BASE = "https://nickstrad.github.io/decentralized-object-store/"
AGENT = "site-link-check/1"
TIMEOUT = 30
MAX_PAGES = 2000


class Page(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.links = []  # (attribute, value)
        self.ids = set()

    def handle_starttag(self, tag, attrs):
        for k, v in attrs:
            if v is None:
                continue
            if k in ("id", "name"):
                self.ids.add(v)
            if (k == "href" and tag in ("a", "link")) or (k == "src" and tag in ("img", "script", "source")):
                self.links.append((k, v))


def fetch(url, want_body):
    """Returns (status, content_type, body or b''). status 0 means no answer."""
    req = urllib.request.Request(url, headers={"User-Agent": AGENT}, method="GET")
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT) as r:
            ctype = r.headers.get("Content-Type", "")
            body = r.read() if want_body else b""
            return r.status, ctype, body
    except urllib.error.HTTPError as e:
        return e.code, "", b""
    except (urllib.error.URLError, TimeoutError, OSError):
        return 0, "", b""


def crawl(base, external=False, out=sys.stdout):
    if not base.endswith("/"):
        base += "/"
    b = urlsplit(base)
    findings = []
    status, _, _ = fetch(base, False)
    if status != 200:
        print(f"site-links: base {base} answered {status}", file=sys.stderr)
        return 2, findings
    pages = {}      # url (no fragment) -> Page, for HTML pages under base
    answers = {}    # url (no fragment) -> status
    queue = [base]
    refs = []       # (from_page, target_url, fragment)
    externals = {}  # url -> first page that links to it
    while queue:
        url = queue.pop(0)
        if url in answers:
            continue
        if len(answers) >= MAX_PAGES:
            findings.append((url, "dead-link", f"crawl stopped at {MAX_PAGES} URLs"))
            break
        is_html_candidate = url.endswith("/") or url.endswith(".html") or "." not in url.rsplit("/", 1)[-1]
        status, ctype, body = fetch(url, is_html_candidate)
        answers[url] = status
        if status != 200 or "html" not in ctype:
            continue
        p = Page()
        p.feed(body.decode("utf-8", "replace"))
        pages[url] = p
        for _, raw in p.links:
            raw = raw.strip()
            if not raw or raw.startswith(("mailto:", "javascript:", "data:", "tel:")):
                continue
            target, frag = urldefrag(urljoin(url, raw))
            t = urlsplit(target)
            if t.scheme not in ("http", "https"):
                continue
            if t.netloc != b.netloc:
                externals.setdefault(target, url)
                continue
            if not t.path.startswith(b.path):
                findings.append((url, "escapes-site", raw))
                continue
            refs.append((url, target, frag))
            if target not in answers and target not in queue:
                queue.append(target)
    for page, target, frag in refs:
        st = answers.get(target, 0)
        if st != 200:
            findings.append((page, "dead-link", f"{target} answered {st}"))
        elif frag and target in pages and frag not in pages[target].ids:
            findings.append((page, "dead-anchor", f"{target}#{frag}"))
    if external:
        for target in sorted(externals):
            st, _, _ = fetch(target, False)
            if st == 0 or st >= 400:
                findings.append((externals[target], "dead-external", f"{target} answered {st}"))
    for page, rule, detail in sorted(set(findings)):
        print(f"{page}: {rule}: {detail}", file=out)
    print(f"site-links: {len(pages)} pages, {len(answers)} internal URLs, {len(refs)} internal links, "
          f"{len(externals)} external links {'checked' if external else 'not fetched'}, "
          f"{len(set(findings))} findings", file=out)
    return (1 if findings else 0), findings


def selftest():
    good = {
        "index.html": '<a href="a.html#top">a</a> <img src="img/x.png"> <a href="https://example.invalid/">x</a>',
        "a.html": '<h1 id="top">a</h1> <a href="./">home</a>',
        "img/x.png": "png",
    }
    bad = dict(good)
    bad["index.html"] = ('<a href="a.html#nope">a</a> <a href="gone.html">g</a> <img src="img/missing.png">'
                         ' <a href="/elsewhere/">e</a>')
    want = {"dead-anchor", "dead-link", "escapes-site"}
    ok = True
    with tempfile.TemporaryDirectory() as tmp:
        for name, tree in (("good", good), ("bad", bad)):
            for rel, text in tree.items():
                f = Path(tmp, "site", name, rel)
                f.parent.mkdir(parents=True, exist_ok=True)
                f.write_text(text)

        class Quiet(http.server.SimpleHTTPRequestHandler):
            def __init__(self, *a, **kw):
                super().__init__(*a, directory=tmp, **kw)

            def log_message(self, *a):
                pass

        srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Quiet)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        try:
            port = srv.server_address[1]
            sink = open("/dev/null", "w")
            rc, f = crawl(f"http://127.0.0.1:{port}/site/good/", out=sink)
            if rc != 0:
                print(f"selftest: clean fixture reported {f}")
                ok = False
            rc, f = crawl(f"http://127.0.0.1:{port}/site/bad/", out=sink)
            rules = {r for _, r, _ in f}
            dead = [d for _, r, d in f if r == "dead-link"]
            if rc != 1 or rules != want or len(dead) != 2:
                print(f"selftest: bad fixture: rc={rc} rules={sorted(rules)} dead-links={dead}, want {sorted(want)} and 2 dead links")
                ok = False
            rc, _ = crawl(f"http://127.0.0.1:{port}/site/none/", out=sink)
            if rc != 2:
                print(f"selftest: a base that does not answer returned {rc}, want 2")
                ok = False
        finally:
            srv.shutdown()
    print("site-links selftest: " + ("ok" if ok else "FAILED"))
    return 0 if ok else 1


def main(argv):
    args = [a for a in argv if not a.startswith("--")]
    flags = {a for a in argv if a.startswith("--")}
    if flags - {"--external", "--selftest"} or len(args) > 1:
        print(__doc__.split("\n\n")[1], file=sys.stderr)
        return 2
    if "--selftest" in flags:
        return selftest()
    rc, _ = crawl(args[0] if args else DEFAULT_BASE, external="--external" in flags)
    return rc


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
