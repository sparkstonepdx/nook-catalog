package main

import (
	"encoding/csv"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// The pages at Formlify's own address, behind nook's login: every site's forms, each form's submissions, a CSV
// download, uploaded files, and deleting one submission.

func (a *app) admin(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Path == "/" && r.Method == http.MethodGet:
		a.home(w)
	case len(parts) == 3 && parts[0] == "s" && r.Method == http.MethodGet:
		host, form := parts[1], parts[2]
		if strings.HasSuffix(form, ".csv") {
			a.csv(w, host, strings.TrimSuffix(form, ".csv"))
			return
		}
		a.form(w, host, form)
	case len(parts) == 4 && parts[0] == "s" && parts[3] == "delete" && r.Method == http.MethodPost:
		host, form, id := parts[1], parts[2], r.PostFormValue("id")
		if !safeHost(host) || id == "" || r.Header.Get("Origin") != "" && !strings.HasSuffix(r.Header.Get("Origin"), "://"+r.Host) {
			http.Error(w, "no", http.StatusBadRequest)
			return
		}
		a.store.remove(host, form, id)
		http.Redirect(w, r, "/s/"+host+"/"+form, http.StatusSeeOther)
	case len(parts) == 6 && parts[0] == "f" && r.Method == http.MethodGet:
		// /f/HOST/FORM/ID/FIELD/NAME, where FIELD is only for the URL to read well
		host, form, id, name := parts[1], parts[2], parts[3], parts[5]
		if !safeHost(host) {
			http.NotFound(w, r)
			return
		}
		p := filepath.Join(a.store.formDir(host, form), "files", safeName(id), safeName(name))
		if _, err := os.Stat(p); err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(safeName(name), `"`, "")+`"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, p)
	default:
		http.NotFound(w, r)
	}
}

type formRow struct {
	Host, Name, Page string
	Count            int
}

func (a *app) home(w http.ResponseWriter) {
	var rows []formRow
	for _, sf := range a.store.sites() {
		for _, f := range sf.Forms {
			rows = append(rows, formRow{Host: sf.Host, Name: f.Name, Page: f.Page, Count: a.store.count(sf.Host, f.Name)})
		}
	}
	render(w, homePage, map[string]any{"Rows": rows})
}

func (a *app) form(w http.ResponseWriter, host, form string) {
	sf, ok := a.store.site(host)
	var f *Form
	for i := range sf.Forms {
		if sf.Forms[i].Name == form {
			f = &sf.Forms[i]
		}
	}
	if !ok || f == nil {
		http.Error(w, "No such form", http.StatusNotFound)
		return
	}
	render(w, formPage, map[string]any{"Host": host, "Form": f, "Subs": a.store.list(host, form), "Columns": f.Fields})
}

func (a *app) csv(w http.ResponseWriter, host, form string) {
	sf, ok := a.store.site(host)
	var f *Form
	for i := range sf.Forms {
		if sf.Forms[i].Name == form {
			f = &sf.Forms[i]
		}
	}
	if !ok || f == nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeName(host+"-"+form)+`.csv"`)
	c := csv.NewWriter(w)
	head := append([]string{"id", "at", "page"}, f.Fields...)
	if len(f.Files) > 0 {
		head = append(head, "files")
	}
	c.Write(head)
	for _, s := range a.store.list(host, form) {
		row := []string{s.ID, s.At, s.Page}
		for _, k := range f.Fields {
			row = append(row, noFormula(strings.Join(s.Fields[k], ", ")))
		}
		if len(f.Files) > 0 {
			var names []string
			for _, u := range s.Files {
				names = append(names, u.Name)
			}
			row = append(row, noFormula(strings.Join(names, ", ")))
		}
		c.Write(row)
	}
	c.Flush()
}

// noFormula keeps a spreadsheet from running what a visitor typed (=HYPERLINK(...)).
func noFormula(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func render(w http.ResponseWriter, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'")
	t.Execute(w, data)
}

const pageHead = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Formlify</title><style>
body{font:1rem/1.5 system-ui,sans-serif;margin:0;color:#222;background:#fbfaf7}
main{max-width:72rem;margin:0 auto;padding:1.5rem}
h1{font-size:1.5rem;margin:0 0 1rem} a{color:#3a4f9a}
table{border-collapse:collapse;width:100%;background:#fff;border:1px solid #e3ded4;border-radius:.5rem;overflow:hidden}
th,td{text-align:left;padding:.5rem .75rem;border-bottom:1px solid #eee;vertical-align:top}
th{background:#f4f1ea;font-weight:600} td.v{white-space:pre-wrap;max-width:28rem;overflow-wrap:anywhere}
.muted{color:#777} .wrap{overflow-x:auto} .bar{display:flex;gap:1rem;align-items:baseline;flex-wrap:wrap;margin-bottom:1rem}
button{font:inherit;background:none;border:0;color:#9a3a3a;cursor:pointer;padding:0}
</style></head><body><main>`

var homePage = template.Must(template.New("home").Parse(pageHead + `<h1>Formlify</h1>
{{if .Rows}}<div class="wrap"><table><tr><th>Site</th><th>Form</th><th>Page</th><th>Submissions</th></tr>
{{range .Rows}}<tr><td>{{.Host}}</td><td><a href="/s/{{.Host}}/{{.Name}}">{{.Name}}</a></td><td class="muted">{{.Page}}</td><td>{{.Count}}</td></tr>{{end}}
</table></div>{{else}}<p>No forms yet. Bind a static site to Formlify on nook's Services page, mark its forms with <code>netlify</code> or <code>data-netlify="true"</code>, and deploy it.</p>{{end}}
</main></body></html>`))

var formPage = template.Must(template.New("form").Parse(pageHead + `<div class="bar"><a href="/">All forms</a><h1>{{.Form.Name}} <span class="muted">on {{.Host}}</span></h1>
<a href="/s/{{.Host}}/{{.Form.Name}}.csv">Download CSV</a></div>
{{if .Subs}}<div class="wrap"><table><tr><th>When</th>{{range .Columns}}<th>{{.}}</th>{{end}}{{if .Form.Files}}<th>Files</th>{{end}}<th></th></tr>
{{$host := .Host}}{{$form := .Form.Name}}{{$cols := .Columns}}{{$files := .Form.Files}}
{{range .Subs}}{{$s := .}}<tr><td class="muted">{{.At}}</td>{{range $cols}}<td class="v">{{range $i, $v := index $s.Fields .}}{{if $i}}, {{end}}{{$v}}{{end}}</td>{{end}}
{{if $files}}<td>{{range .Files}}<a href="/f/{{$host}}/{{$form}}/{{$s.ID}}/{{.Field}}/{{.Name}}">{{.Name}}</a> {{end}}</td>{{end}}
<td><form method="post" action="/s/{{$host}}/{{$form}}/delete"><input type="hidden" name="id" value="{{.ID}}"><button>Delete</button></form></td></tr>{{end}}
</table></div>{{else}}<p class="muted">Nothing yet.</p>{{end}}
</main></body></html>`))
