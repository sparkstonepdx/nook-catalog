package main

// The page a logged-in user sees at /: every app's files, a Share button on each, and the live links.
// Plain HTML with one script and one stylesheet from this server; data only ever goes in as text.

import (
	"html"
	"net/http"
)

const pageCSP = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

func (s *server) home(w http.ResponseWriter, r *http.Request) {
	c, ok := callerOf(r)
	if !ok || c.user == "" {
		fail(w, 401, "this page is for nook logins; with a token, use /v1/files")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", pageCSP)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.Write([]byte(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Files</title>
<link rel="stylesheet" href="/ui.css">
<script src="/ui.js" defer></script>
</head>
<body>
<header><h1>Files</h1><span class="who">` + html.EscapeString(c.user) + ` · <a href="/.nook/logout">Log out</a></span></header>
<main>
<section id="links" hidden><h2>Share links</h2><ul class="list" id="link-list"></ul></section>
<section><h2>All files</h2><p class="hint" id="empty" hidden>Nothing uploaded yet. CI uploads with the app's NOOK_TOKEN: <code>PUT /v1/files/PATH</code>.</p><div id="apps"></div></section>
</main>
</body>
</html>
`))
}

func ui(w http.ResponseWriter, p string) {
	h := w.Header()
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	if p == "/ui.js" {
		h.Set("Content-Type", "text/javascript; charset=utf-8")
		w.Write([]byte(uiJS))
		return
	}
	h.Set("Content-Type", "text/css; charset=utf-8")
	w.Write([]byte(uiCSS))
}

const uiJS = `"use strict";
const el = (tag, props = {}, ...kids) => { const e = Object.assign(document.createElement(tag), props); e.append(...kids); return e; };
const size = (n) => n < 1024 ? n + " B" : n < 1 << 20 ? (n / 1024).toFixed(1) + " KB" : n < 1 << 30 ? (n / (1 << 20)).toFixed(1) + " MB" : (n / (1 << 30)).toFixed(2) + " GB";
const when = (t) => new Date(t).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
const durations = [["1 hour", 1], ["1 day", 24], ["7 days", 168], ["30 days", 720]];

async function api(method, path, body) {
  const res = await fetch(path, { method, headers: body ? { "Content-Type": "application/json" } : {}, body: body && JSON.stringify(body), credentials: "same-origin" });
  if (res.status === 204) return null;
  const out = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(out.error || res.statusText);
  return out;
}

function copyRow(url) {
  const input = el("input", { className: "input", value: url, readOnly: true });
  input.addEventListener("focus", () => input.select());
  const btn = el("button", { className: "btn", textContent: "Copy" });
  btn.addEventListener("click", async () => { await navigator.clipboard.writeText(url); btn.textContent = "Copied"; setTimeout(() => (btn.textContent = "Copy"), 1500); });
  return el("div", { className: "copy" }, input, btn);
}

async function loadLinks() {
  const { shares } = await api("GET", "/v1/shares");
  const list = document.getElementById("link-list");
  list.replaceChildren(...shares.map((s) => {
    const remove = el("button", { className: "btn subtle", textContent: "Stop sharing" });
    remove.addEventListener("click", async () => { remove.disabled = true; await api("DELETE", "/v1/shares/" + s.id); loadLinks(); });
    return el("li", { className: "row" },
      el("div", { className: "row-main" }, el("span", { textContent: s.app + "/" + s.path }), el("small", { textContent: "Until " + when(s.expires) + " · by " + s.by })),
      copyRow(s.url), remove);
  }));
  document.getElementById("links").hidden = shares.length === 0;
}

function shareControls(f) {
  const pick = el("select", { className: "input", ariaLabel: "How long the link lasts" }, ...durations.map(([label, h]) => el("option", { value: h, textContent: label, selected: h === 168 })));
  const go = el("button", { className: "btn", textContent: "Share" });
  const out = el("div", { className: "out" });
  go.addEventListener("click", async () => {
    go.disabled = true;
    try {
      const s = await api("POST", "/v1/shares", { app: f.app, path: f.path, hours: Number(pick.value) });
      out.replaceChildren(copyRow(s.url), el("small", { textContent: "Anyone with this link can download it until " + when(s.expires) + "." }));
      loadLinks();
    } catch (e) {
      out.replaceChildren(el("small", { className: "error", textContent: e.message }));
    }
    go.disabled = false;
  });
  return [el("div", { className: "share" }, pick, go), out];
}

async function loadFiles() {
  const { files } = await api("GET", "/v1/files");
  const byApp = new Map();
  for (const f of files) (byApp.get(f.app) || byApp.set(f.app, []).get(f.app)).push(f);
  document.getElementById("empty").hidden = files.length > 0;
  document.getElementById("apps").replaceChildren(...[...byApp].map(([app, fs]) => el("div", { className: "app" },
    el("h3", { textContent: app }),
    el("ul", { className: "list" }, ...fs.map((f) => el("li", { className: "row" },
      el("div", { className: "row-main" }, el("a", { href: "/f/" + f.app + "/" + f.path, textContent: f.path }), el("small", { textContent: size(f.size) + " · " + when(f.modified) })),
      ...shareControls(f)))))));
}

loadFiles().catch((e) => document.getElementById("apps").replaceChildren(el("p", { className: "error", textContent: e.message })));
loadLinks().catch(() => {});
`

const uiCSS = `:root { color-scheme: light dark; --bg: #fbfaf8; --fg: #1d1b18; --muted: #6b665e; --line: #e3dfd8; --card: #fff; --accent: #2f5d8a; --error: #a3312a; }
@media (prefers-color-scheme: dark) { :root { --bg: #161513; --fg: #edeae4; --muted: #a29c92; --line: #34312c; --card: #1e1c19; --accent: #8db7e0; --error: #f08a80; } }
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--fg); font: 16px/1.45 system-ui, sans-serif; }
header { display: flex; flex-wrap: wrap; gap: 8px; align-items: baseline; justify-content: space-between; padding: 16px; border-bottom: 1px solid var(--line); }
h1 { margin: 0; font-size: 22px; } h2 { font-size: 15px; color: var(--muted); font-weight: 600; margin: 24px 0 8px; } h3 { font-size: 15px; margin: 16px 0 8px; }
main { max-width: 760px; margin: 0 auto; padding: 0 16px 48px; }
a { color: var(--accent); } .who { color: var(--muted); font-size: 14px; }
.list { list-style: none; margin: 0; padding: 0; border: 1px solid var(--line); border-radius: 12px; background: var(--card); }
.row { display: grid; gap: 8px; padding: 12px; border-top: 1px solid var(--line); }
.row:first-child { border-top: 0; }
.row-main { display: flex; flex-direction: column; min-width: 0; } .row-main > * { overflow-wrap: anywhere; }
small, .hint { color: var(--muted); font-size: 13px; }
.share, .copy { display: flex; gap: 8px; } .copy .input { flex: 1; min-width: 0; }
.out:empty { display: none; } .out { display: grid; gap: 4px; }
.input, .btn { font: inherit; font-size: 14px; min-height: 36px; padding: 0 10px; border-radius: 8px; border: 1px solid var(--line); background: var(--bg); color: var(--fg); }
.btn { cursor: pointer; font-weight: 600; } .btn:disabled { opacity: .5; } .btn.subtle { justify-self: start; font-weight: 500; color: var(--muted); }
.error { color: var(--error); }
@media (min-width: 640px) { .row { grid-template-columns: 1fr auto; align-items: center; } .out, .copy, .row .btn.subtle { grid-column: 1 / -1; } }
`
