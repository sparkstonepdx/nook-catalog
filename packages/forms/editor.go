package main

import "html/template"

// The editor: the form as it will look, with every part editable in place. It saves a moment after each change.
var editPage = template.Must(template.New("edit").Funcs(funcs).Parse(head + `<title>{{.Form.Title}}</title>` + style + `
<style>
.top{position:sticky;top:0;z-index:2;background:var(--linen);padding:.5rem 0;margin-bottom:.5rem}
.status{font-size:.9rem;color:var(--muted);min-width:5rem;text-align:right}
.big,.flat{background:transparent;color:inherit;font-family:inherit;width:100%}
.big{font-size:1.5rem;font-weight:700;border:0!important;border-bottom:1px solid var(--line)!important;border-radius:0!important;padding-inline:0!important}
.flat{border:0!important;border-bottom:1px solid var(--line)!important;border-radius:0!important;padding-inline:0!important}
.qhead{display:grid;grid-template-columns:minmax(0,1fr);gap:.5rem} @media(min-width:560px){.qhead{grid-template-columns:minmax(0,1fr) 12rem}}
.tools{display:flex;gap:.25rem;flex-wrap:wrap;justify-content:flex-end;align-items:center;border-top:1px solid var(--line);margin-top:.75rem;padding-top:.5rem}
.icon{min-width:2.75rem;min-height:2.75rem;border:0;background:none;color:var(--muted);font:inherit;border-radius:.5rem;cursor:pointer}
.icon:hover{background:var(--linen);color:var(--ink)} .optrow{display:flex;gap:.5rem;align-items:center;margin:.25rem 0}
.optrow .mark{width:1.25rem;height:1.25rem;border:2px solid var(--line);flex:none}.optrow .mark.round{border-radius:50%}.optrow .mark.square{border-radius:.25rem}
.switch{display:inline-flex;gap:.5rem;align-items:center;min-height:2.75rem;cursor:pointer}
.share{display:flex;gap:.5rem} .share input{flex:1;min-width:0}
</style></head><body><main>
<div class="top row spread"><a href="/">‹ Forms</a><span class="row"><a class="btn" href="/forms/{{.Form.ID}}/responses">Responses ({{.Count}})</a><span class="status" id="status" aria-live="polite"></span></span></div>
<div class="card head">
<input class="big" id="title" aria-label="Form title" placeholder="Form title" maxlength="200">
<textarea class="flat" id="desc" aria-label="Description" placeholder="Description (optional)" style="margin-top:.75rem;min-height:3rem"></textarea>
</div>
<div id="questions"></div>
<p><button class="btn" id="add" type="button">Add a question</button></p>
<div class="card">
<h2 style="font-size:1.05rem;margin:0 0 .5rem">Sharing</h2>
<label class="switch"><input type="checkbox" id="open"> Taking responses</label>
<div class="share" style="margin:.5rem 0"><input type="text" id="link" readonly value="{{.Share}}" aria-label="Link to the form"><button class="btn" type="button" id="copy">Copy</button></div>
<p class="small muted" style="margin:0">Anyone with the link can answer. <a href="/f/{{.Form.ID}}" target="_blank" rel="noopener">Open it</a></p>
<label class="small muted" for="thanks" style="display:block;margin-top:1rem">After sending, people see</label>
<textarea id="thanks" placeholder="Thanks, your response was sent." style="min-height:3rem"></textarea>
</div>
<form method="post" action="/forms/{{.Form.ID}}/delete" onsubmit="return confirm('Delete this form and all its responses? It can\'t be undone.')"><button class="btn danger">Delete form</button></form>
<script type="application/json" id="data">{{.JSON}}</script>
<script>
const TYPES = {{.Types}};
const OPTS = (t) => t === "choice" || t === "checkboxes" || t === "dropdown";
let form = JSON.parse(document.getElementById("data").textContent);
const $ = (id) => document.getElementById(id);
const el = (tag, attrs = {}, ...kids) => { const e = document.createElement(tag); for (const [k, v] of Object.entries(attrs)) { if (k.startsWith("on")) e.addEventListener(k.slice(2), v); else if (k in e && k !== "list") e[k] = v; else e.setAttribute(k, v); } e.append(...kids.filter((k) => k != null)); return e; };
const rid = () => Array.from(crypto.getRandomValues(new Uint8Array(4)), (b) => b.toString(16).padStart(2, "0")).join("");

// saving: a moment after the last change, one at a time
let timer = 0, saving = false, again = false;
const status = (t) => { $("status").textContent = t; };
function changed() { status("Saving…"); clearTimeout(timer); timer = setTimeout(save, 600); }
async function save() {
  if (saving) { again = true; return; }
  saving = true;
  try {
    const r = await fetch(location.pathname, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(form) });
    if (!r.ok) throw new Error(await r.text());
    const back = await r.json();
    form.questions.forEach((q, i) => { if (back.questions[i]) q.id = back.questions[i].id; });
    status("Saved");
  } catch (e) { status("Not saved: " + e.message.trim()); }
  saving = false;
  if (again) { again = false; save(); }
}
window.addEventListener("beforeunload", (e) => { if ($("status").textContent === "Saving…") e.preventDefault(); });

function bind(id, key) { const i = $(id); i.value = form[key] || ""; i.addEventListener("input", () => { form[key] = i.value; changed(); }); }
bind("title", "title"); bind("desc", "desc"); bind("thanks", "thanks");
$("open").checked = !!form.open; $("open").addEventListener("change", () => { form.open = $("open").checked; changed(); });
$("copy").addEventListener("click", () => navigator.clipboard?.writeText($("link").value).then(() => { $("copy").textContent = "Copied"; setTimeout(() => ($("copy").textContent = "Copy"), 1500); }));
$("add").addEventListener("click", () => { form.questions.push({ id: rid(), type: "choice", title: "", options: ["Option 1"] }); draw(); changed(); $("questions").lastChild.querySelector("input").focus(); });

function draw() {
  const list = $("questions");
  list.replaceChildren(...form.questions.map((q, i) => card(q, i)));
}

function card(q, i) {
  const set = (k, v) => { q[k] = v; changed(); };
  const type = el("select", { "aria-label": "Question type", onchange: (e) => { q.type = e.target.value; if (OPTS(q.type) && !(q.options || []).length) q.options = ["Option 1"]; draw(); changed(); } },
    ...Object.entries(TYPES).map(([k, v]) => el("option", { value: k, selected: k === q.type }, v)));
  const body = el("div", { style: "margin-top:.75rem" });
  if (OPTS(q.type)) {
    const mark = q.type === "checkboxes" ? "square" : q.type === "choice" ? "round" : null;
    q.options.forEach((o, j) => body.append(el("div", { class: "optrow" },
      mark ? el("span", { class: "mark " + mark }) : el("span", { class: "muted small", style: "width:1.25rem" }, (j + 1) + "."),
      el("input", { type: "text", class: "flat", value: o, "aria-label": "Option " + (j + 1), oninput: (e) => { q.options[j] = e.target.value; changed(); },
        onkeydown: (e) => { if (e.key === "Enter") { e.preventDefault(); q.options.splice(j + 1, 0, ""); draw(); changed(); focusOpt(i, j + 1); } } }),
      q.options.length > 1 ? el("button", { class: "icon", type: "button", "aria-label": "Remove option " + (j + 1), onclick: () => { q.options.splice(j, 1); draw(); changed(); } }, "×") : null)));
    body.append(el("button", { class: "icon", type: "button", style: "padding:0 .5rem;color:var(--accent)", onclick: () => { q.options.push("Option " + (q.options.length + 1)); draw(); changed(); focusOpt(i, q.options.length - 1); } }, "Add option"));
  } else {
    body.append(el("p", { class: "muted small", style: "margin:0" }, { short: "Short answer text", long: "Long answer text", email: "An email address", number: "A number", date: "A date" }[q.type]));
  }
  const move = (d) => { const j = i + d; if (j < 0 || j >= form.questions.length) return; [form.questions[i], form.questions[j]] = [form.questions[j], form.questions[i]]; draw(); changed(); };
  return el("div", { class: "card q" },
    el("div", { class: "qhead" },
      el("input", { type: "text", class: "flat", value: q.title || "", placeholder: "Question", "aria-label": "Question " + (i + 1), oninput: (e) => set("title", e.target.value) }),
      type),
    el("input", { type: "text", class: "flat small", value: q.help || "", placeholder: "Help text (optional)", "aria-label": "Help text", style: "margin-top:.5rem", oninput: (e) => set("help", e.target.value) }),
    body,
    el("div", { class: "tools" },
      el("label", { class: "switch small" }, el("input", { type: "checkbox", checked: !!q.required, onchange: (e) => set("required", e.target.checked) }), "Required"),
      el("button", { class: "icon", type: "button", "aria-label": "Move up", disabled: i === 0, onclick: () => move(-1) }, "↑"),
      el("button", { class: "icon", type: "button", "aria-label": "Move down", disabled: i === form.questions.length - 1, onclick: () => move(1) }, "↓"),
      el("button", { class: "icon", type: "button", "aria-label": "Duplicate", onclick: () => { form.questions.splice(i + 1, 0, { ...structuredClone(q), id: rid() }); draw(); changed(); } }, "⧉"),
      el("button", { class: "icon", type: "button", "aria-label": "Delete question", onclick: () => { form.questions.splice(i, 1); draw(); changed(); } }, "🗑")));
}
function focusOpt(i, j) { $("questions").children[i]?.querySelectorAll(".optrow input")[j]?.focus(); }
draw();
</script>
</main></body></html>`))
