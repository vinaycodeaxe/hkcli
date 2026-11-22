"""Static assets for the hkcli website (CSS, JavaScript, favicon). Used by build_site.py."""

FAVICON_SVG = """<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">
  <rect width="64" height="64" rx="14" fill="#0f766e"/>
  <rect x="9" y="13" width="46" height="31" rx="8" fill="#ffffff"/>
  <path d="M17 42v11l11-10z" fill="#ffffff"/>
  <path d="M19 23l7 5.5-7 5.5" fill="none" stroke="#0f766e" stroke-width="4" stroke-linecap="round" stroke-linejoin="round"/>
  <path d="M30 35h13" stroke="#0f766e" stroke-width="4" stroke-linecap="round"/>
</svg>
"""

# Runs in <head> before CSS paints, so a saved theme never flashes.
THEME_BOOTSTRAP = (
    "(function(){try{var t=localStorage.getItem('hkcli-theme');"
    "if(t==='light'||t==='dark'){document.documentElement.dataset.theme=t;}}catch(e){}})();"
)

CSS = """
:root {
  --bg: #ffffff; --bg-soft: #f6f8fa; --fg: #1f2328; --muted: #59636e; --border: #d1d9e0;
  --accent: #0f766e; --accent-fg: #ffffff; --accent-soft: #d5f5ef; --code-bg: #f6f8fa;
  --font: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
  --mono: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace;
  color-scheme: light;
}
:root[data-theme="dark"] {
  --bg: #0d1117; --bg-soft: #151b23; --fg: #e6edf3; --muted: #9198a1; --border: #30363d;
  --accent: #2dd4bf; --accent-fg: #04201d; --accent-soft: #0b3b36; --code-bg: #151b23;
  color-scheme: dark;
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    --bg: #0d1117; --bg-soft: #151b23; --fg: #e6edf3; --muted: #9198a1; --border: #30363d;
    --accent: #2dd4bf; --accent-fg: #04201d; --accent-soft: #0b3b36; --code-bg: #151b23;
    color-scheme: dark;
  }
}
* { box-sizing: border-box; }
html { scroll-padding-top: 72px; }
body { margin: 0; background: var(--bg); color: var(--fg); font: 16px/1.6 var(--font); -webkit-font-smoothing: antialiased; }
a { color: var(--accent); text-decoration: none; }
a:hover { text-decoration: underline; }
.skip { position: absolute; left: -9999px; }
.skip:focus { left: 8px; top: 8px; z-index: 100; padding: 8px 12px; background: var(--bg); border: 1px solid var(--border); }

.topbar { position: sticky; top: 0; z-index: 50; display: flex; align-items: center; gap: 12px; height: 56px; padding: 0 20px;
  background: color-mix(in srgb, var(--bg) 90%, transparent); backdrop-filter: blur(8px); border-bottom: 1px solid var(--border); }
.brand { display: flex; align-items: center; gap: 8px; font-weight: 700; font-size: 17px; color: var(--fg); }
.brand:hover { text-decoration: none; }
.version { font: 12px var(--mono); color: var(--muted); border: 1px solid var(--border); border-radius: 999px; padding: 1px 8px; }
.top-links { margin-left: auto; display: flex; gap: 18px; font-size: 14px; }
.top-links a { color: var(--muted); }
.top-links a:hover { color: var(--fg); text-decoration: none; }
.icon-btn { display: inline-flex; align-items: center; justify-content: center; width: 34px; height: 34px; padding: 0;
  border: 1px solid var(--border); border-radius: 8px; background: none; color: var(--fg); font-size: 16px; cursor: pointer; }
.icon-btn:hover { border-color: var(--accent); }
.nav-toggle { display: none; }

.layout { display: grid; grid-template-columns: 250px minmax(0, 1fr) 220px; max-width: 1320px; margin: 0 auto; }
.sidebar { position: sticky; top: 56px; height: calc(100vh - 56px); overflow-y: auto; padding: 24px 16px 40px 20px; border-right: 1px solid var(--border); }
.nav-title { margin: 20px 0 6px; font-size: 12px; font-weight: 700; letter-spacing: .06em; text-transform: uppercase; color: var(--muted); }
.nav-section:first-child .nav-title { margin-top: 0; }
.sidebar ul { list-style: none; margin: 0; padding: 0; }
.sidebar a { display: block; padding: 5px 10px; border-radius: 6px; color: var(--fg); font-size: 14.5px; }
.sidebar a:hover { background: var(--bg-soft); text-decoration: none; }
.sidebar a.active { background: var(--accent-soft); color: var(--accent); font-weight: 600; }

.content { min-width: 0; padding: 32px 48px 64px; }
.doc { max-width: 780px; }
.crumb { margin: 0 0 4px; font-size: 13px; font-weight: 600; letter-spacing: .05em; text-transform: uppercase; color: var(--accent); }
.doc h1 { margin: 0 0 16px; font-size: 34px; line-height: 1.2; letter-spacing: -.01em; }
.doc h2 { margin: 40px 0 12px; padding-bottom: 6px; font-size: 23px; border-bottom: 1px solid var(--border); }
.doc h3 { margin: 28px 0 8px; font-size: 18px; }
.doc p, .doc ul, .doc ol { margin: 0 0 16px; }
.doc li { margin: 4px 0; }
.doc code { font: .88em var(--mono); background: var(--code-bg); border: 1px solid var(--border); border-radius: 5px; padding: .1em .35em; }
.doc pre { position: relative; margin: 0 0 18px; padding: 14px 16px; overflow-x: auto; background: var(--code-bg); border: 1px solid var(--border); border-radius: 10px; font: 13.5px/1.55 var(--mono); }
.doc pre code { padding: 0; border: 0; background: none; font: inherit; }
.copy-btn { position: absolute; top: 8px; right: 8px; padding: 3px 9px; border: 1px solid var(--border); border-radius: 6px;
  background: var(--bg); color: var(--muted); font: 12px var(--font); cursor: pointer; opacity: 0; transition: opacity .15s; }
pre:hover .copy-btn, .copy-btn:focus { opacity: 1; }
.table-wrap { margin: 0 0 18px; overflow-x: auto; }
.doc table { width: 100%; border-collapse: collapse; font-size: 14.5px; }
.doc th, .doc td { padding: 7px 10px; text-align: left; vertical-align: top; border: 1px solid var(--border); }
.doc th { background: var(--bg-soft); }
.doc blockquote { margin: 0 0 16px; padding: 8px 16px; border-left: 4px solid var(--accent); border-radius: 0 8px 8px 0; background: var(--bg-soft); }
.anchor { margin-left: 6px; color: var(--muted); font-weight: 400; opacity: 0; }
.anchor:hover { text-decoration: none; }
h2:hover .anchor, h3:hover .anchor, .anchor:focus { opacity: 1; }
.page-meta { margin-top: 40px; font-size: 14px; }
.pager { display: flex; justify-content: space-between; gap: 16px; margin-top: 20px; padding-top: 20px; border-top: 1px solid var(--border); }
.pager a { display: flex; flex-direction: column; min-width: 180px; padding: 10px 14px; border: 1px solid var(--border); border-radius: 10px; color: var(--fg); }
.pager a:hover { border-color: var(--accent); text-decoration: none; }
.pager small { color: var(--muted); font-size: 12px; }
.pager .next { margin-left: auto; text-align: right; }

.toc { position: sticky; top: 56px; height: calc(100vh - 56px); overflow-y: auto; padding: 32px 20px; font-size: 13.5px; }
.toc-title { margin: 0 0 8px; font-size: 12px; font-weight: 700; letter-spacing: .06em; text-transform: uppercase; color: var(--muted); }
.toc ul { list-style: none; margin: 0; padding: 0; }
.toc li { margin: 4px 0; }
.toc li.l3 { padding-left: 12px; }
.toc a { color: var(--muted); }
.toc a:hover, .toc a.active { color: var(--accent); text-decoration: none; }

.hero { max-width: 780px; margin: 8px 0 36px; padding: 36px 32px; border: 1px solid var(--border); border-radius: 16px;
  background: radial-gradient(1200px 300px at 0% 0%, var(--accent-soft), transparent 60%), var(--bg-soft); }
.eyebrow { margin: 0 0 8px; font-size: 13px; font-weight: 700; letter-spacing: .08em; text-transform: uppercase; color: var(--accent); }
.hero h1 { margin: 0 0 14px; font-size: 44px; line-height: 1.1; letter-spacing: -.02em; }
.lede { max-width: 640px; margin: 0 0 22px; font-size: 18px; color: var(--muted); }
.hero-actions { display: flex; flex-wrap: wrap; gap: 10px; margin-bottom: 20px; }
.btn { display: inline-block; padding: 9px 16px; border: 1px solid var(--border); border-radius: 9px; background: var(--bg); color: var(--fg); font-weight: 600; font-size: 15px; }
.btn:hover { border-color: var(--accent); text-decoration: none; }
.btn.primary { background: var(--accent); border-color: var(--accent); color: var(--accent-fg); }
.install { display: flex; align-items: center; gap: 10px; max-width: 520px; padding: 10px 10px 10px 16px; border: 1px solid var(--border); border-radius: 10px; background: var(--bg); font: 14.5px var(--mono); }
.install code { flex: 1; overflow-x: auto; white-space: nowrap; }
.install .copy-btn { position: static; opacity: 1; }
.chips { display: flex; flex-wrap: wrap; gap: 8px; margin: 20px 0 10px; padding: 0; list-style: none; }
.chips li { padding: 3px 10px; border: 1px solid var(--border); border-radius: 999px; background: var(--bg); color: var(--muted); font-size: 13px; }
.more { margin: 0; font-size: 14px; }

.footer { padding: 24px 20px 40px; border-top: 1px solid var(--border); text-align: center; font-size: 13.5px; color: var(--muted); }
.footer p { margin: 4px 0; }

@media (max-width: 1200px) {
  .layout { grid-template-columns: 240px minmax(0, 1fr); }
  .toc { display: none; }
}
@media (max-width: 900px) {
  .layout { grid-template-columns: minmax(0, 1fr); }
  .nav-toggle { display: inline-flex; }
  .top-links { display: none; }
  .theme-toggle { margin-left: auto; }
  .sidebar { position: fixed; top: 56px; left: 0; z-index: 40; width: 280px; background: var(--bg); transform: translateX(-100%); transition: transform .2s; }
  body.nav-open .sidebar { transform: none; box-shadow: 0 10px 30px rgba(0, 0, 0, .2); }
  .content { padding: 24px 20px 48px; }
  .hero { padding: 26px 20px; }
  .hero h1 { font-size: 34px; }
}
"""

JS = """
(function () {
  var root = document.documentElement;
  function currentTheme() {
    var t = root.dataset.theme;
    if (t === "light" || t === "dark") return t;
    return window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }
  var themeBtn = document.querySelector(".theme-toggle");
  if (themeBtn) {
    themeBtn.addEventListener("click", function () {
      var next = currentTheme() === "dark" ? "light" : "dark";
      root.dataset.theme = next;
      try { localStorage.setItem("hkcli-theme", next); } catch (e) {}
    });
  }

  var navBtn = document.querySelector(".nav-toggle");
  if (navBtn) {
    navBtn.addEventListener("click", function () {
      var open = document.body.classList.toggle("nav-open");
      navBtn.setAttribute("aria-expanded", open ? "true" : "false");
    });
  }

  function copyText(text, btn) {
    function done() {
      var old = btn.textContent;
      btn.textContent = "Copied";
      setTimeout(function () { btn.textContent = old; }, 1500);
    }
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(done, function () {});
      return;
    }
    var ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand("copy"); done(); } catch (e) {}
    document.body.removeChild(ta);
  }
  document.querySelectorAll(".doc pre").forEach(function (pre) {
    var btn = document.createElement("button");
    btn.type = "button";
    btn.className = "copy-btn";
    btn.textContent = "Copy";
    btn.addEventListener("click", function () {
      var code = pre.querySelector("code") || pre;
      copyText(code.innerText.replace(/\\n$/, ""), btn);
    });
    pre.appendChild(btn);
  });
  document.querySelectorAll("[data-copy]").forEach(function (btn) {
    btn.addEventListener("click", function () { copyText(btn.getAttribute("data-copy"), btn); });
  });

  var links = Array.prototype.slice.call(document.querySelectorAll(".toc a"));
  if (links.length && "IntersectionObserver" in window) {
    var byId = {};
    links.forEach(function (a) { byId[decodeURIComponent(a.hash.slice(1))] = a; });
    var observer = new IntersectionObserver(function (entries) {
      entries.forEach(function (entry) {
        if (!entry.isIntersecting) return;
        links.forEach(function (a) { a.classList.remove("active"); });
        var link = byId[entry.target.id];
        if (link) link.classList.add("active");
      });
    }, { rootMargin: "-60px 0px -70% 0px" });
    Object.keys(byId).forEach(function (id) {
      var el = document.getElementById(id);
      if (el) observer.observe(el);
    });
  }
})();
"""
