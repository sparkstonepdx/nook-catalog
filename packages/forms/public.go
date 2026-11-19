package main

// The form people answer: plain HTML that works without JavaScript. A hidden field only bots fill in drops a
// response quietly, and each address may send 10 a minute.

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ipLimit struct {
	mu   sync.Mutex
	max  int
	seen map[string][]time.Time
}

func (l *ipLimit) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	var keep []time.Time
	for _, t := range l.seen[key] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep) >= l.max {
		l.seen[key] = keep
		return false
	}
	l.seen[key] = append(keep, now)
	return true
}

// clientIP is the visitor's address from Caddy's X-Forwarded-For (Caddy adds the last one).
func clientIP(r *http.Request) string {
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return r.RemoteAddr
	}
	parts := strings.Split(xff, ",")
	return strings.TrimSpace(parts[len(parts)-1])
}

const honeypot = "website" // a field people never see

func (a *app) public(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/f/"), "/")
	f, err := a.store.get(id)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		render(w, closedPage, map[string]any{"Title": "No such form", "Text": "Check the link you were sent."})
		return
	}
	if !f.Open {
		render(w, closedPage, map[string]any{"Title": f.Title, "Text": "This form isn't taking responses right now."})
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.page(w, f, 0, nil, url.Values{}, nil)
	case http.MethodPost:
		if !a.limit.allow(id+" "+clientIP(r), time.Now()) {
			w.WriteHeader(http.StatusTooManyRequests)
			render(w, closedPage, map[string]any{"Title": f.Title, "Text": "That's a lot of sending. Wait a minute and try again."})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			render(w, closedPage, map[string]any{"Title": f.Title, "Text": "That was too much to send in one go."})
			return
		}
		a.step(w, f, r.PostForm)
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

// step takes one page of a form. The pages people went through so far are in "path" (section numbers), and the
// answers from them ride along as hidden fields, so it all works without JavaScript or a session. Back goes to the
// last page in the path; Next checks this page's answers and goes where its section leads; the last page checks
// every page they went through again and keeps the response. Answers on pages they skipped are left out.
func (a *app) step(w http.ResponseWriter, f Form, v url.Values) {
	secs := f.sections()
	var path []int
	for _, p := range strings.Split(v.Get("path"), ",") {
		if n, err := strconv.Atoi(p); err == nil && n >= 0 && n < len(secs) && len(path) < len(secs) {
			path = append(path, n)
		}
	}
	cur := 0
	if len(path) > 0 {
		cur, path = path[len(path)-1], path[:len(path)-1]
	}
	if v.Get("back") != "" && len(path) > 0 {
		a.page(w, f, path[len(path)-1], path[:len(path)-1], v, nil)
		return
	}
	got, errs := answers(secs[cur].Questions, func(k string) []string { return v[k] })
	if len(errs) > 0 {
		w.WriteHeader(http.StatusUnprocessableEntity)
		a.page(w, f, cur, path, v, errs)
		return
	}
	if next := f.next(secs, cur, got); next >= 0 && !contains(intStrings(path), strconv.Itoa(next)) && next != cur {
		a.page(w, f, next, append(path, cur), v, nil)
		return
	}
	if v.Get(honeypot) != "" { // looks sent; nothing kept
		render(w, closedPage, map[string]any{"Title": f.Title, "Text": cmpOr(f.Thanks, "Thanks, your response was sent."), "Again": "/f/" + f.ID})
		return
	}
	all := map[string][]string{}
	for _, i := range append(path, cur) {
		got, errs := answers(secs[i].Questions, func(k string) []string { return v[k] })
		if len(errs) > 0 { // a changed hidden field: back to that page
			w.WriteHeader(http.StatusUnprocessableEntity)
			a.page(w, f, i, nil, v, errs)
			return
		}
		for k, x := range got {
			all[k] = x
		}
	}
	resp := Response{ID: newID(8), At: time.Now().UTC().Format(time.RFC3339), Answers: all}
	if err := a.store.add(f.ID, resp); err != nil {
		http.Error(w, "Couldn't save that. Try again.", http.StatusInternalServerError)
		return
	}
	go a.notify(f, resp)
	render(w, closedPage, map[string]any{"Title": f.Title, "Text": cmpOr(f.Thanks, "Thanks, your response was sent."), "Again": "/f/" + f.ID})
}

func intStrings(l []int) []string {
	out := make([]string, len(l))
	for i, n := range l {
		out[i] = strconv.Itoa(n)
	}
	return out
}

type hidden struct{ Name, Value string }

// page shows section i, with what's been answered elsewhere carried along hidden.
func (a *app) page(w http.ResponseWriter, f Form, i int, path []int, v url.Values, errs map[string]string) {
	secs := f.sections()
	here := map[string]bool{}
	for _, q := range secs[i].Questions {
		here["q_"+q.ID] = true
	}
	var keep []hidden
	for _, q := range f.Asked() {
		if k := "q_" + q.ID; !here[k] {
			for _, x := range v[k] {
				keep = append(keep, hidden{k, x})
			}
		}
	}
	// the button says Submit when no answer here can lead on
	last := f.next(secs, i, nil) < 0
	for _, q := range secs[i].Questions {
		for _, t := range q.GoTo {
			if t != "submit" {
				last = false
			}
		}
	}
	if errs == nil {
		errs = map[string]string{}
	}
	required := false
	for _, q := range secs[i].Questions {
		required = required || q.Required
	}
	render(w, answerPage, map[string]any{"Form": f, "Section": secs[i], "First": i == 0, "Values": v, "Errors": errs,
		"Path": strings.Join(intStrings(append(append([]int{}, path...), i)), ","), "Back": len(path) > 0, "Keep": keep, "Last": last, "Required": required})
}

func render(w http.ResponseWriter, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "same-origin") // "no-referrer" would send Origin: null on our own posts
	t.Execute(w, data)
}

var funcs = template.FuncMap{
	"has":  func(v url.Values, k, opt string) bool { return contains(v[k], opt) },
	"val":  func(v url.Values, k string) string { return v.Get(k) },
	"opts": hasOptions,
	"inc":  func(i int) int { return i + 1 },
	"when": func(s string) string {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return s
		}
		return t.Local().Format("Jan 2, 3:04 PM")
	},
}

// The look: warm paper, one column, big targets. Shared by every page.
const style = `<style>
:root{--ink:#2a2420;--muted:#6f655b;--paper:#fffdf8;--linen:#f5efe6;--line:#e4dccf;--accent:#3a55a0;--bad:#a33a2e;color-scheme:light dark}
@media (prefers-color-scheme:dark){:root{--ink:#efe8dd;--muted:#b3a899;--paper:#221d19;--linen:#171310;--line:#3a332c;--accent:#9fb2ea;--bad:#ef9a8c}}
*{box-sizing:border-box} body{margin:0;background:var(--linen);color:var(--ink);font:1.0625rem/1.5 system-ui,-apple-system,sans-serif}
main{max-width:44rem;margin:0 auto;padding:1.5rem 1rem 4rem} h1{font-size:1.75rem;line-height:1.2;margin:.25rem 0 .5rem}
a{color:var(--accent)} .muted{color:var(--muted)} .small{font-size:.9rem}
.card{background:var(--paper);border:1px solid var(--line);border-radius:.75rem;padding:1.25rem;margin:0 0 1rem}
.card.head{border-top:6px solid var(--accent)} .card.sec{background:var(--accent);color:var(--paper);border-color:var(--accent)} .q h2{font-size:1.05rem;margin:0 0 .25rem;font-weight:650} .req{color:var(--bad)}
label.opt{display:flex;gap:.6rem;align-items:center;min-height:2.75rem;cursor:pointer}
input[type=radio],input[type=checkbox]{width:1.25rem;height:1.25rem;accent-color:var(--accent);flex:none}
input[type=text],input[type=email],input[type=number],input[type=date],select,textarea{width:100%;font:inherit;color:inherit;background:transparent;border:1px solid var(--line);border-radius:.5rem;padding:.6rem .75rem;min-height:2.75rem}
textarea{min-height:6rem;resize:vertical} input:focus,select:focus,textarea:focus{outline:2px solid var(--accent);outline-offset:1px;border-color:transparent}
.btn{display:inline-flex;align-items:center;justify-content:center;min-height:2.75rem;padding:0 1.25rem;border-radius:.5rem;border:1px solid var(--line);background:var(--paper);color:var(--ink);font:inherit;font-weight:600;text-decoration:none;cursor:pointer}
.btn.primary{background:var(--accent);border-color:var(--accent);color:var(--paper)} .btn.danger{color:var(--bad)}
.err{color:var(--bad);font-size:.95rem;margin:.4rem 0 0} .q.bad{border-color:var(--bad)}
.hp{position:absolute;left:-9999px;width:1px;height:1px;overflow:hidden}
.row{display:flex;gap:.75rem;align-items:center;flex-wrap:wrap} .spread{justify-content:space-between}
</style>`

const head = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="robots" content="noindex">`

var answerPage = template.Must(template.New("answer").Funcs(funcs).Parse(head + `<title>{{.Form.Title}}</title>` + style + `</head><body><main>
<div class="card head"><h1>{{.Form.Title}}</h1>{{if .Form.Desc}}<p style="white-space:pre-wrap;margin:0">{{.Form.Desc}}</p>{{end}}
{{if .Required}}<p class="small muted" style="margin:.75rem 0 0"><span class="req">*</span> needs an answer</p>{{end}}</div>
{{if not .First}}<div class="card sec"><h2 style="font-size:1.25rem;margin:0">{{.Section.Title}}</h2>{{if .Section.Help}}<p style="white-space:pre-wrap;margin:.5rem 0 0">{{.Section.Help}}</p>{{end}}</div>{{end}}
{{$v := .Values}}{{$e := .Errors}}
<form method="post" novalidate>
<div class="hp" aria-hidden="true"><label>Leave this empty <input type="text" name="website" tabindex="-1" autocomplete="off" value="{{val $v "website"}}"></label></div>
<input type="hidden" name="path" value="{{.Path}}">{{range .Keep}}<input type="hidden" name="{{.Name}}" value="{{.Value}}">{{end}}
{{range .Section.Questions}}{{$name := print "q_" .ID}}{{$err := index $e .ID}}
<fieldset class="card q{{if $err}} bad{{end}}" style="margin-inline:0;min-width:0"><legend class="hp">{{.Title}}</legend>
<h2 id="t_{{.ID}}">{{.Title}}{{if .Required}} <span class="req" aria-label="needs an answer">*</span>{{end}}</h2>
{{if .Help}}<p class="muted small" style="margin:0 0 .5rem;white-space:pre-wrap">{{.Help}}</p>{{end}}
{{if eq .Type "short"}}<input type="text" name="{{$name}}" value="{{val $v $name}}" aria-labelledby="t_{{.ID}}"{{if .Required}} required{{end}}>
{{else if eq .Type "long"}}<textarea name="{{$name}}" aria-labelledby="t_{{.ID}}"{{if .Required}} required{{end}}>{{val $v $name}}</textarea>
{{else if eq .Type "email"}}<input type="email" name="{{$name}}" value="{{val $v $name}}" autocomplete="email" aria-labelledby="t_{{.ID}}"{{if .Required}} required{{end}}>
{{else if eq .Type "number"}}<input type="number" step="any" inputmode="decimal" name="{{$name}}" value="{{val $v $name}}" aria-labelledby="t_{{.ID}}"{{if .Required}} required{{end}}>
{{else if eq .Type "date"}}<input type="date" name="{{$name}}" value="{{val $v $name}}" aria-labelledby="t_{{.ID}}"{{if .Required}} required{{end}}>
{{else if eq .Type "dropdown"}}<select name="{{$name}}" aria-labelledby="t_{{.ID}}"{{if .Required}} required{{end}}><option value="">Choose</option>{{range .Options}}<option{{if has $v $name .}} selected{{end}}>{{.}}</option>{{end}}</select>
{{else}}{{$t := .Type}}{{range .Options}}<label class="opt"><input type="{{if eq $t "checkboxes"}}checkbox{{else}}radio{{end}}" name="{{$name}}" value="{{.}}"{{if has $v $name .}} checked{{end}}> {{.}}</label>{{end}}
{{end}}{{if $err}}<p class="err" role="alert">{{$err}}</p>{{end}}
</fieldset>{{end}}
<div class="row spread"><span class="row">{{if .Back}}<button class="btn" name="back" value="1" formnovalidate>Back</button>{{end}}<button class="btn primary">{{if .Last}}Submit{{else}}Next{{end}}</button></span><span class="small muted">Never send passwords through a form.</span></div>
</form></main></body></html>`))

var closedPage = template.Must(template.New("closed").Parse(head + `<title>{{.Title}}</title>` + style + `</head><body><main>
<div class="card head"><h1>{{.Title}}</h1><p style="white-space:pre-wrap">{{.Text}}</p>{{if .Again}}<a href="{{.Again}}">Send another response</a>{{end}}</div>
</main></body></html>`))
