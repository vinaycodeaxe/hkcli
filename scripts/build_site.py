#!/usr/bin/env python3
"""Build the hikecli website from docs/*.md into dist/site (published by GitHub Pages).

    python -m pip install "markdown>=3.5"
    python scripts/build_site.py [--out DIR] [--site-url URL]

The build fails on broken links between pages, unknown #anchors and pages missing
from the sidebar, so CI catches them before they reach the site.
"""

from __future__ import annotations

import argparse
import hashlib
import html
import re
import shutil
import sys
from dataclasses import dataclass, field
from pathlib import Path
from urllib.parse import urlsplit

import markdown

import site_assets as assets

ROOT = Path(__file__).resolve().parent.parent
DOCS = ROOT / "docs"

OWNER = "vinaycodeaxe"
REPO = "hikecli"
REPO_URL = f"https://github.com/{OWNER}/{REPO}"
EDIT_URL = f"{REPO_URL}/edit/main/docs"
DEFAULT_SITE_URL = f"https://{OWNER}.github.io/{REPO}/"

PRODUCT = "hikecli"
TAGLINE = "Hike in your terminal"
SUMMARY = (
    "A single CLI that links to Hike as a web device, keeps a searchable copy of your chats "
    "in SQLite, and sends messages from the terminal, scripts and AI agents like OpenClaw."
)
INSTALL_COMMAND = "uv tool install hikecli"
FEATURES = ["Pair", "Sync", "Search", "Send", "Media", "Contacts", "Groups", "History", "OpenClaw", "Doctor"]
DISCLAIMER = "hikecli is an independent project and is not affiliated with or endorsed by Hike Messenger Ltd."

SECTIONS = [
    ("Start", ["index.md", "install.md", "quickstart.md", "openclaw.md"]),
    ("Using hikecli", ["auth.md", "sync.md", "messages.md", "history.md", "send.md", "media.md", "contacts.md"]),
    ("Reference", ["commands.md", "store.md", "how-it-works.md", "troubleshooting.md", "release.md"]),
]

FRONTMATTER = re.compile(r"\A---[ \t]*\n(.*?)\n---[ \t]*\n", re.S)
HEADING_ID = re.compile(r"<h[1-6][^>]*\sid=\"([^\"]+)\"")
HREF = re.compile(r'href="([^"]*)"')
URL_SCHEME = re.compile(r"^[a-zA-Z][a-zA-Z0-9+.-]*:")


@dataclass
class Page:
    rel: str
    title: str
    description: str
    markdown: str
    section: str = ""
    body: str = ""
    ids: set = field(default_factory=set)
    toc: list = field(default_factory=list)

    @property
    def out(self) -> str:
        return self.rel[:-3] + ".html"


def esc(text) -> str:
    return html.escape(str(text), quote=True)


def read_version() -> str:
    text = (ROOT / "src" / "hikecli" / "__init__.py").read_text(encoding="utf-8")
    match = re.search(r'__version__\s*=\s*"([^"]+)"', text)
    return match.group(1) if match else "0.0.0"


def parse_frontmatter(text: str, rel: str) -> tuple[dict, str]:
    match = FRONTMATTER.match(text)
    if not match:
        return {}, text
    meta = {}
    for line in match.group(1).splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        key, sep, value = line.partition(":")
        if not sep:
            raise SystemExit(f"{rel}: bad frontmatter line {line!r}")
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
            value = value[1:-1]
        meta[key.strip()] = value
    return meta, text[match.end():]


def first_heading(text: str) -> str | None:
    match = re.search(r"^#\s+(.+?)\s*$", text, re.M)
    return match.group(1) if match else None


def load_pages() -> dict[str, Page]:
    pages = {}
    for path in sorted(DOCS.glob("*.md")):
        text = path.read_text(encoding="utf-8").replace("\r\n", "\n")
        meta, body = parse_frontmatter(text, path.name)
        title = meta.get("title") or first_heading(body) or path.stem.replace("-", " ").title()
        pages[path.name] = Page(path.name, title, meta.get("description", ""), body)
    return pages


def collect_toc(tokens: list, out: list) -> list:
    for token in tokens:
        if token["level"] in (2, 3):
            out.append((token["level"], token["id"], html.unescape(token["name"])))
        collect_toc(token.get("children", []), out)
    return out


def render(page: Page) -> None:
    md = markdown.Markdown(
        extensions=["tables", "fenced_code", "sane_lists", "toc"],
        extension_configs={
            "toc": {
                "permalink": "#",
                "permalink_class": "anchor",
                "permalink_title": "Link to this section",
                "toc_depth": "2-3",
            }
        },
    )
    body = md.convert(page.markdown)
    page.ids = set(HEADING_ID.findall(body))
    page.toc = collect_toc(md.toc_tokens, [])
    if page.rel == "index.md":
        body = re.sub(r"<h1[^>]*>.*?</h1>\s*", "", body, count=1, flags=re.S)  # the hero is the page title
    body = body.replace("<table>", '<div class="table-wrap"><table>').replace("</table>", "</table></div>")
    page.body = body


def rewrite_links(page: Page, pages: dict[str, Page], errors: list[str]) -> None:
    def fix(match: re.Match) -> str:
        href = html.unescape(match.group(1))
        if not href or URL_SCHEME.match(href) or href.startswith("//"):
            return match.group(0)
        path, _, fragment = href.partition("#")
        if not path:
            if fragment and fragment not in page.ids:
                errors.append(f"{page.rel}: link to missing anchor #{fragment}")
            return match.group(0)
        if path.startswith("/"):
            errors.append(f"{page.rel}: use relative links, not {href!r}")
            return match.group(0)
        if path.endswith(".md"):
            name = path[2:] if path.startswith("./") else path
            target = pages.get(name) if "/" not in name else None  # docs/ is flat
            if target is None:
                errors.append(f"{page.rel}: link to missing page {path!r}")
                return match.group(0)
            if fragment and fragment not in target.ids:
                errors.append(f"{page.rel}: link to missing anchor {path}#{fragment}")
            new = target.out + (f"#{fragment}" if fragment else "")
            return f'href="{esc(new)}"'
        if not (DOCS / path).exists():
            errors.append(f"{page.rel}: link to missing file {path!r}")
        return match.group(0)

    page.body = HREF.sub(fix, page.body)


def nav_html(current: Page | None, pages: dict[str, Page]) -> str:
    parts = []
    for name, rels in SECTIONS:
        items = []
        for rel in rels:
            page = pages[rel]
            active = current is not None and page.rel == current.rel
            attrs = ' class="active" aria-current="page"' if active else ""
            items.append(f'<li><a href="{esc(page.out)}"{attrs}>{esc(page.title)}</a></li>')
        parts.append(f'<div class="nav-section"><p class="nav-title">{esc(name)}</p><ul>{"".join(items)}</ul></div>')
    return "".join(parts)


def toc_html(page: Page) -> str:
    if len(page.toc) < 2:
        return ""
    items = "".join(f'<li class="l{level}"><a href="#{esc(anchor)}">{esc(name)}</a></li>' for level, anchor, name in page.toc)
    return f'<aside class="toc" aria-label="On this page"><p class="toc-title">On this page</p><ul>{items}</ul></aside>'


def hero_html() -> str:
    chips = "".join(f"<li>{esc(chip)}</li>" for chip in FEATURES)
    return f"""<section class="hero">
  <p class="eyebrow">Hike &middot; One CLI</p>
  <h1>{esc(TAGLINE)}</h1>
  <p class="lede">{esc(SUMMARY)}</p>
  <div class="hero-actions">
    <a class="btn primary" href="quickstart.html">Quickstart</a>
    <a class="btn" href="openclaw.html">Use with OpenClaw</a>
    <a class="btn" href="{esc(REPO_URL)}">GitHub</a>
  </div>
  <div class="install"><code>{esc(INSTALL_COMMAND)}</code><button type="button" class="copy-btn" data-copy="{esc(INSTALL_COMMAND)}">Copy</button></div>
  <ul class="chips">{chips}</ul>
  <p class="more"><a href="install.html">Other install options &rarr;</a></p>
</section>"""


def pager_html(page: Page, ordered: list[Page]) -> str:
    index = ordered.index(page)
    links = []
    if index > 0:
        prev = ordered[index - 1]
        links.append(f'<a class="prev" href="{esc(prev.out)}"><small>Previous</small>{esc(prev.title)}</a>')
    if index < len(ordered) - 1:
        nxt = ordered[index + 1]
        links.append(f'<a class="next" href="{esc(nxt.out)}"><small>Next</small>{esc(nxt.title)}</a>')
    return f'<nav class="pager" aria-label="Pages">{"".join(links)}</nav>' if links else ""


def layout(*, title: str, description: str, canonical: str, site_url: str, version: str, nav: str, main: str,
           toc: str = "", base: str = "", css_ref: str, js_ref: str) -> str:
    image = f"{site_url}assets/social-card.png"
    base_tag = f'<base href="{esc(base)}">' if base else ""
    return f"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
{base_tag}
<title>{esc(title)}</title>
<meta name="description" content="{esc(description)}">
<link rel="canonical" href="{esc(canonical)}">
<meta property="og:type" content="website">
<meta property="og:site_name" content="{esc(PRODUCT)}">
<meta property="og:title" content="{esc(title)}">
<meta property="og:description" content="{esc(description)}">
<meta property="og:url" content="{esc(canonical)}">
<meta property="og:image" content="{esc(image)}">
<meta name="twitter:card" content="summary_large_image">
<meta name="theme-color" content="#0f766e">
<link rel="icon" href="favicon.svg" type="image/svg+xml">
<script>{assets.THEME_BOOTSTRAP}</script>
<link rel="stylesheet" href="{esc(css_ref)}">
</head>
<body>
<a class="skip" href="#main">Skip to content</a>
<header class="topbar">
  <button type="button" class="icon-btn nav-toggle" aria-label="Menu" aria-expanded="false">&#9776;</button>
  <a class="brand" href="index.html"><img src="favicon.svg" alt="" width="24" height="24"><span>{esc(PRODUCT)}</span></a>
  <span class="version">v{esc(version)}</span>
  <nav class="top-links" aria-label="Main">
    <a href="quickstart.html">Quickstart</a>
    <a href="commands.html">Commands</a>
    <a href="openclaw.html">OpenClaw</a>
    <a href="{esc(REPO_URL)}">GitHub</a>
  </nav>
  <button type="button" class="icon-btn theme-toggle" aria-label="Switch light or dark theme">&#9680;</button>
</header>
<div class="layout">
<nav class="sidebar" aria-label="Documentation">{nav}</nav>
<main class="content" id="main">
{main}
</main>
{toc}
</div>
<footer class="footer">
  <p>{esc(PRODUCT)} v{esc(version)} &middot; <a href="{esc(REPO_URL)}/blob/main/LICENSE">MIT License</a> &middot; <a href="{esc(REPO_URL)}">GitHub</a> &middot; <a href="{esc(REPO_URL)}/blob/main/CHANGELOG.md">Changelog</a></p>
  <p>{esc(DISCLAIMER)}</p>
</footer>
<script src="{esc(js_ref)}"></script>
</body>
</html>
"""


def short_hash(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()[:10]


def resolve_site_url(override: str | None) -> tuple[str, str | None]:
    cname_file = DOCS / "CNAME"
    cname = cname_file.read_text(encoding="utf-8").strip() if cname_file.exists() else None
    url = override or (f"https://{cname}/" if cname else DEFAULT_SITE_URL)
    parts = urlsplit(url)
    if parts.scheme not in ("http", "https") or not parts.netloc:
        raise SystemExit(f"bad site URL: {url!r}")
    return url.rstrip("/") + "/", cname


def build(out: Path, site_url_override: str | None = None) -> int:
    site_url, cname = resolve_site_url(site_url_override)
    version = read_version()
    pages = load_pages()
    errors: list[str] = []

    in_nav = [rel for _, rels in SECTIONS for rel in rels]
    for rel in in_nav:
        if rel not in pages:
            errors.append(f"sidebar lists {rel}, but docs/{rel} does not exist")
    for rel in pages:
        if rel not in in_nav:
            errors.append(f"docs/{rel} is not in the sidebar (SECTIONS in scripts/build_site.py)")
    if errors:
        print("\n".join(f"error: {e}" for e in errors), file=sys.stderr)
        return 1

    for name, rels in SECTIONS:
        for rel in rels:
            pages[rel].section = name
    ordered = [pages[rel] for rel in in_nav]
    for page in ordered:
        render(page)
    for page in ordered:
        rewrite_links(page, pages, errors)
    if errors:
        print("\n".join(f"error: {e}" for e in errors), file=sys.stderr)
        return 1

    if out.exists():
        shutil.rmtree(out)
    (out / "assets").mkdir(parents=True)
    css_ref = f"assets/site.css?v={short_hash(assets.CSS)}"
    js_ref = f"assets/site.js?v={short_hash(assets.JS)}"
    (out / "assets" / "site.css").write_text(assets.CSS.lstrip(), encoding="utf-8")
    (out / "assets" / "site.js").write_text(assets.JS.lstrip(), encoding="utf-8")
    (out / "favicon.svg").write_text(assets.FAVICON_SVG, encoding="utf-8")
    (out / ".nojekyll").write_text("", encoding="utf-8")
    for item in (DOCS / "assets").glob("*") if (DOCS / "assets").exists() else []:
        if item.is_file():
            shutil.copy2(item, out / "assets" / item.name)

    for page in ordered:
        is_home = page.rel == "index.md"
        title = f"{PRODUCT}: {TAGLINE}" if is_home else f"{page.title} | {PRODUCT}"
        canonical = site_url if is_home else site_url + page.out
        parts = []
        if is_home:
            parts.append(hero_html())
        parts.append('<article class="doc">')
        if not is_home:
            parts.append(f'<p class="crumb">{esc(page.section)}</p>')
        parts.append(page.body)
        parts.append(f'<p class="page-meta"><a href="{esc(EDIT_URL)}/{esc(page.rel)}">Edit this page on GitHub</a></p>')
        parts.append(pager_html(page, ordered))
        parts.append("</article>")
        document = layout(
            title=title,
            description=page.description or SUMMARY,
            canonical=canonical,
            site_url=site_url,
            version=version,
            nav=nav_html(page, pages),
            main="\n".join(parts),
            toc=toc_html(page),
            css_ref=css_ref,
            js_ref=js_ref,
        )
        (out / page.out).write_text(document, encoding="utf-8")

    not_found = layout(
        title=f"Page not found | {PRODUCT}",
        description=SUMMARY,
        canonical=site_url,
        site_url=site_url,
        version=version,
        nav=nav_html(None, pages),
        main='<article class="doc"><h1>Page not found</h1><p>That page does not exist. '
        'Start from the <a href="index.html">overview</a> or the <a href="quickstart.html">quickstart</a>.</p></article>',
        base=site_url,  # GitHub Pages serves this file for any missing path, so resolve links from the site root
        css_ref=css_ref,
        js_ref=js_ref,
    )
    (out / "404.html").write_text(not_found, encoding="utf-8")

    urls = "".join(f"  <url><loc>{esc(site_url if p.rel == 'index.md' else site_url + p.out)}</loc></url>\n" for p in ordered)
    (out / "sitemap.xml").write_text(
        f'<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n{urls}</urlset>\n',
        encoding="utf-8",
    )
    if cname:
        (out / "CNAME").write_text(cname + "\n", encoding="utf-8")
        (out / "robots.txt").write_text(f"User-agent: *\nAllow: /\nSitemap: {site_url}sitemap.xml\n", encoding="utf-8")

    print(f"built {len(ordered)} pages into {out} for {site_url}")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description="Build the hikecli website from docs/.")
    parser.add_argument("--out", default=str(ROOT / "dist" / "site"), help="output folder (default: dist/site)")
    parser.add_argument("--site-url", help="public URL of the site (default: from docs/CNAME, else GitHub Pages)")
    args = parser.parse_args()
    return build(Path(args.out), args.site_url)


if __name__ == "__main__":
    sys.exit(main())
