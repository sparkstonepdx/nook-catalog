package main

// The editor and the responses, behind nook's login: every form, making and changing one (it saves as you go),
// its share link, and its responses as a summary, a table and a CSV.

import (
	"encoding/csv"
	"encoding/json"
	"html/template"
	"net/http"
	"sort"
	"strings"
)

func (a *app) admin(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if r.Method != http.MethodGet && !sameOrigin(r) {
		http.Error(w, "That came from another site.", http.StatusForbidden)
		return
	}
	switch {
	case r.URL.Path == "/" && r.Method == http.MethodGet:
		a.home(w)
	case r.URL.Path == "/new" && r.Method == http.MethodPost:
		f := Form{ID: newID(6), Title: "Untitled form", Questions: []Question{{ID: newID(4), Type: "short", Title: "Your name"}}}
		if err := a.store.save(f); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/forms/"+f.ID, http.StatusSeeOther)
	case len(parts) >= 2 && parts[0] == "forms":
		f, err := a.store.get(parts[1])
		if err != nil {
			http.Error(w, "No such form", http.StatusNotFound)
			return
		}
		a.form(w, r, f, parts[2:])
	default:
		http.NotFound(w, r)
	}
}

// sameOrigin keeps another site from posting here with a logged-in visitor's cookie.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	return o == "" || strings.HasSuffix(o, "://"+r.Host)
}

func (a *app) form(w http.ResponseWriter, r *http.Request, f Form, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		b, _ := json.Marshal(f)
		render(w, editPage, map[string]any{"Form": f, "JSON": template.JS(b), "Types": types, "Share": a.cfg.URL + "/f/" + f.ID, "Count": a.store.count(f.ID)})
	case len(rest) == 0 && r.Method == http.MethodPut:
		var in Form
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			http.Error(w, "That couldn't be read.", http.StatusBadRequest)
			return
		}
		in.ID, in.Created = f.ID, f.Created
		if err := in.clean(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := a.store.save(in); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, _ := a.store.get(f.ID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	case len(rest) == 1 && rest[0] == "delete" && r.Method == http.MethodPost:
		a.store.remove(f.ID)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	case len(rest) == 1 && rest[0] == "responses" && r.Method == http.MethodGet:
		rs := a.store.responses(f.ID)
		render(w, responsesPage, map[string]any{"Form": f, "Responses": rs, "Summary": summarize(f, rs)})
	case len(rest) == 1 && rest[0] == "responses.csv" && r.Method == http.MethodGet:
		writeCSV(w, f, a.store.responses(f.ID))
	case len(rest) == 2 && rest[0] == "responses" && rest[1] == "delete" && r.Method == http.MethodPost:
		a.store.removeResponse(f.ID, r.PostFormValue("id"))
		http.Redirect(w, r, "/forms/"+f.ID+"/responses", http.StatusSeeOther)
	default:
		http.NotFound(w, r)
	}
}

type listRow struct {
	Form  Form
	Count int
}

func (a *app) home(w http.ResponseWriter) {
	var rows []listRow
	for _, f := range a.store.list() {
		rows = append(rows, listRow{f, a.store.count(f.ID)})
	}
	render(w, homePage, map[string]any{"Rows": rows})
}

// A question's answers at a glance: counts for the ones with options, the latest few for the rest.
type qSummary struct {
	Q       Question
	Answers int
	Counts  []optCount
	Latest  []string
}

type optCount struct {
	Option string
	N, Pct int
}

func summarize(f Form, rs []Response) []qSummary {
	var out []qSummary
	for _, q := range f.Asked() {
		s := qSummary{Q: q}
		counts := map[string]int{}
		for _, r := range rs {
			v, ok := r.Answers[q.ID]
			if !ok {
				continue
			}
			s.Answers++
			for _, x := range v {
				counts[x]++
			}
			if !hasOptions(q.Type) && len(s.Latest) < 5 {
				s.Latest = append(s.Latest, strings.Join(v, ", "))
			}
		}
		if hasOptions(q.Type) {
			for _, o := range q.Options {
				p := 0
				if s.Answers > 0 {
					p = counts[o] * 100 / s.Answers
				}
				s.Counts = append(s.Counts, optCount{o, counts[o], p})
			}
			sort.SliceStable(s.Counts, func(i, j int) bool { return s.Counts[i].N > s.Counts[j].N })
		}
		out = append(out, s)
	}
	return out
}

// writeCSV is one row a response, a column a question, oldest first. A cell that would run as a formula in a
// spreadsheet is quoted.
func writeCSV(w http.ResponseWriter, f Form, rs []Response) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeFile(f.Title)+`.csv"`)
	c := csv.NewWriter(w)
	head := []string{"Sent"}
	for _, q := range f.Asked() {
		head = append(head, noFormula(cmpOr(q.Title, "Untitled question")))
	}
	c.Write(head)
	for i := len(rs) - 1; i >= 0; i-- {
		row := []string{rs[i].At}
		for _, q := range f.Asked() {
			row = append(row, noFormula(strings.Join(rs[i].Answers[q.ID], ", ")))
		}
		c.Write(row)
	}
	c.Flush()
}

func noFormula(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func safeFile(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`"\/:*?<>|`, r) {
			return -1
		}
		return r
	}, s)
	return cmpOr(strings.TrimSpace(s), "responses")
}

var homePage = template.Must(template.New("home").Funcs(funcs).Parse(head + `<title>Forms</title>` + style + `</head><body><main>
<div class="row spread"><h1>Forms</h1><form method="post" action="/new"><button class="btn primary">New form</button></form></div>
{{range .Rows}}<a class="card row spread" href="/forms/{{.Form.ID}}" style="text-decoration:none;color:inherit">
<span><strong>{{.Form.Title}}</strong><br><span class="small muted">{{if .Form.Open}}Taking responses{{else}}Closed{{end}} · {{len .Form.Asked}} questions</span></span>
<span class="muted">{{.Count}} {{if eq .Count 1}}response{{else}}responses{{end}} ›</span></a>
{{else}}<div class="card"><p>No forms yet. Make one, share its link, and the answers come back here.</p></div>{{end}}
</main></body></html>`))

var responsesPage = template.Must(template.New("responses").Funcs(funcs).Parse(head + `<title>Responses: {{.Form.Title}}</title>` + style + `
<style>.bar{height:.5rem;border-radius:.25rem;background:var(--accent);min-width:2px}.cnt{display:grid;grid-template-columns:minmax(0,1fr) 3.5rem;gap:.25rem .75rem;align-items:center}
.cnt .l{overflow-wrap:anywhere}.wrap{overflow-x:auto}table{border-collapse:collapse;width:100%}th,td{text-align:left;padding:.5rem;border-bottom:1px solid var(--line);vertical-align:top;white-space:pre-wrap;overflow-wrap:anywhere;min-width:8rem}
td form{display:inline}button.link{background:none;border:0;color:var(--bad);font:inherit;cursor:pointer;min-height:2.75rem}</style></head><body><main>
<p><a href="/forms/{{.Form.ID}}">‹ {{.Form.Title}}</a></p>
<div class="row spread"><h1>{{len .Responses}} {{if eq (len .Responses) 1}}response{{else}}responses{{end}}</h1>{{if .Responses}}<a class="btn" href="/forms/{{.Form.ID}}/responses.csv">Download CSV</a>{{end}}</div>
{{if .Responses}}
{{range .Summary}}<div class="card"><h2 style="font-size:1.05rem;margin:0 0 .25rem">{{.Q.Title}}</h2><p class="small muted" style="margin:0 0 .75rem">{{.Answers}} answered</p>
{{if .Counts}}<div class="cnt">{{range .Counts}}<span class="l">{{.Option}}<div class="bar" style="width:{{.Pct}}%"></div></span><span class="muted small">{{.N}} · {{.Pct}}%</span>{{end}}</div>
{{else}}{{range .Latest}}<p style="margin:.25rem 0;white-space:pre-wrap">{{.}}</p>{{end}}{{end}}</div>{{end}}
<h2>Each response</h2>
<div class="card wrap" style="padding:0"><table><tr><th>Sent</th>{{range .Form.Asked}}<th>{{.Title}}</th>{{end}}<th></th></tr>
{{$f := .Form}}{{range .Responses}}{{$r := .}}<tr><td class="muted small" style="white-space:nowrap">{{when .At}}</td>{{range $f.Asked}}<td>{{range $i, $x := index $r.Answers .ID}}{{if $i}}, {{end}}{{$x}}{{end}}</td>{{end}}
<td><form method="post" action="/forms/{{$f.ID}}/responses/delete" onsubmit="return confirm('Delete this response? It can\'t be undone.')"><input type="hidden" name="id" value="{{.ID}}"><button class="link">Delete</button></form></td></tr>{{end}}
</table></div>
{{else}}<div class="card"><p>Nothing yet. Share the form's link from its page.</p></div>{{end}}
</main></body></html>`))
