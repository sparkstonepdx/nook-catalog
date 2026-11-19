package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newApp(t *testing.T) *app {
	return &app{cfg: config{URL: "https://forms.example.com"}, store: &store{dir: t.TempDir()}, limit: &ipLimit{max: 10, seen: map[string][]time.Time{}}}
}

func do(a *app, method, path, body, ctype string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

func TestClean(t *testing.T) {
	f := Form{Title: "  ", Questions: []Question{
		{ID: "bad id", Type: "choice", Title: "Pick", Options: []string{" a ", "a", "", "b"}},
		{ID: "aaaaaa", Type: "short", Options: []string{"x"}},
		{ID: "aaaaaa", Type: "dropdown"},
	}}
	if err := f.clean(); err != nil {
		t.Fatal(err)
	}
	if f.Title != "Untitled form" || f.Questions[0].ID == "bad id" || len(f.Questions[0].Options) != 2 || f.Questions[1].Options != nil ||
		f.Questions[2].ID == "aaaaaa" || len(f.Questions[2].Options) != 1 {
		t.Fatalf("%+v", f)
	}
	if (&Form{Questions: []Question{{Type: "script"}}}).clean() == nil {
		t.Fatal("an unknown question type got through")
	}
}

func TestAnswers(t *testing.T) {
	f := Form{Questions: []Question{
		{ID: "name00", Type: "short", Required: true}, {ID: "mail00", Type: "email"}, {ID: "pick00", Type: "choice", Options: []string{"Yes", "No"}},
		{ID: "many00", Type: "checkboxes", Options: []string{"A", "B", "C"}}, {ID: "num000", Type: "number"}, {ID: "day000", Type: "date"},
	}}
	v := url.Values{"q_name00": {"Ana"}, "q_mail00": {"ana@example.org"}, "q_pick00": {"Yes"}, "q_many00": {"A", "C"}, "q_num000": {"4.5"}, "q_day000": {"2026-11-18"}}
	got, errs := answers(f.Questions, func(k string) []string { return v[k] })
	if len(errs) != 0 || len(got["many00"]) != 2 || got["pick00"][0] != "Yes" {
		t.Fatalf("%v %v", got, errs)
	}
	bad := url.Values{"q_name00": {"  "}, "q_mail00": {"ana"}, "q_pick00": {"Maybe"}, "q_many00": {"Z"}, "q_num000": {"four"}, "q_day000": {"18/11/2026"}}
	_, errs = answers(f.Questions, func(k string) []string { return bad[k] })
	if len(errs) != 6 {
		t.Fatalf("want 6 errors, got %v", errs)
	}
}

func TestFlow(t *testing.T) {
	a := newApp(t)
	// make a form, then the editor saves it whole
	w := do(a, "POST", "/new", "", "")
	if w.Code != http.StatusSeeOther {
		t.Fatal(w.Code)
	}
	id := strings.TrimPrefix(w.Header().Get("Location"), "/forms/")
	edit := `{"title":"Picnic","open":true,"thanks":"See you there","questions":[{"id":"name00","type":"short","title":"Name","required":true},{"id":"food00","type":"checkboxes","title":"Bringing","options":["Salad","=cmd"]}]}`
	if w := do(a, "PUT", "/forms/"+id, edit, "application/json"); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if w := do(a, "GET", "/forms/"+id, "", ""); !strings.Contains(w.Body.String(), "Picnic") || !strings.Contains(w.Body.String(), "https://forms.example.com/f/"+id) {
		t.Fatal("editor page")
	}
	// someone answers: a missing required answer comes back with what they typed
	form := "application/x-www-form-urlencoded"
	w = do(a, "POST", "/f/"+id, "q_food00=Salad", form)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "needs an answer") || !strings.Contains(w.Body.String(), `value="Salad" checked`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := do(a, "POST", "/f/"+id, "q_name00=Ana&q_food00=Salad&q_food00=%3Dcmd", form); w.Code != 200 || !strings.Contains(w.Body.String(), "See you there") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// a bot fills the hidden field: it looks sent, nothing is kept
	do(a, "POST", "/f/"+id, "q_name00=Bot&website=spam", form)
	rs := a.store.responses(id)
	if len(rs) != 1 || rs[0].Answers["name00"][0] != "Ana" {
		t.Fatalf("%+v", rs)
	}
	// responses: summary, table, CSV (formula quoted)
	if w := do(a, "GET", "/forms/"+id+"/responses", "", ""); !strings.Contains(w.Body.String(), "1 response") || !strings.Contains(w.Body.String(), "Salad") {
		t.Fatal("responses page")
	}
	if w := do(a, "GET", "/forms/"+id+"/responses.csv", "", ""); w.Body.String() != "Sent,Name,Bringing\n"+rs[0].At+",Ana,\"Salad, =cmd\"\n" {
		t.Fatalf("csv %q", w.Body)
	}
	// closed: no more responses
	var f Form
	json.Unmarshal([]byte(edit), &f)
	f.Open = false
	b, _ := json.Marshal(f)
	do(a, "PUT", "/forms/"+id, string(b), "application/json")
	if w := do(a, "POST", "/f/"+id, "q_name00=Late", form); !strings.Contains(w.Body.String(), "taking responses right now") || a.store.count(id) != 1 {
		t.Fatal("a closed form took a response")
	}
	// another site can't post to the editor with your login
	r := httptest.NewRequest("POST", "/forms/"+id+"/delete", nil)
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("cross-site delete")
	}
	a.store.removeResponse(id, rs[0].ID)
	if a.store.count(id) != 0 {
		t.Fatal("removeResponse")
	}
}

func TestSections(t *testing.T) {
	a := newApp(t)
	f := Form{ID: "abcdef123456", Title: "Trip", Open: true, Questions: []Question{
		{ID: "name00", Type: "short", Title: "Name", Required: true},
		{ID: "come00", Type: "choice", Title: "Coming?", Options: []string{"Yes", "No"}, GoTo: map[string]string{"No": "secnot"}},
		{ID: "secyes", Type: "section", Title: "Details", Next: "submit"},
		{ID: "diet00", Type: "short", Title: "Diet", Required: true},
		{ID: "secnot", Type: "section", Title: "Sorry to miss you", GoTo: map[string]string{"x": "y"}, Next: "nowhere"},
		{ID: "why000", Type: "long", Title: "Why not?"},
	}}
	if err := f.clean(); err != nil || f.Questions[4].Next != "" || f.Questions[4].GoTo != nil {
		t.Fatalf("clean: %v %+v", err, f.Questions[4])
	}
	a.store.save(f)
	form := "application/x-www-form-urlencoded"
	post := func(body string) *httptest.ResponseRecorder { return do(a, "POST", "/f/"+f.ID, body, form) }

	// page 1 says Next, and a Yes goes on to Details (whose Next is submit)
	if w := do(a, "GET", "/f/"+f.ID, "", ""); !strings.Contains(w.Body.String(), ">Next<") || strings.Contains(w.Body.String(), "Diet") {
		t.Fatal("page 1")
	}
	w := post("path=0&q_name00=Ana&q_come00=Yes")
	if !strings.Contains(w.Body.String(), "Details") || !strings.Contains(w.Body.String(), `name="path" value="0,1"`) || !strings.Contains(w.Body.String(), `name="q_name00" value="Ana"`) || !strings.Contains(w.Body.String(), ">Submit<") {
		t.Fatalf("page 2: %s", w.Body)
	}
	// Back keeps what was typed on page 2
	if w := post("path=0,1&q_name00=Ana&q_come00=Yes&q_diet00=Vegan&back=1"); !strings.Contains(w.Body.String(), `name="path" value="0"`) || !strings.Contains(w.Body.String(), `name="q_diet00" value="Vegan"`) {
		t.Fatalf("back: %s", w.Body)
	}
	// Details needs its answer, then submits; the skipped page's answer isn't kept
	if w := post("path=0,1&q_name00=Ana&q_come00=Yes&q_why000=sneaky"); w.Code != http.StatusUnprocessableEntity {
		t.Fatal("required on page 2")
	}
	if w := post("path=0,1&q_name00=Ana&q_come00=Yes&q_diet00=Vegan&q_why000=sneaky"); !strings.Contains(w.Body.String(), "Thanks") {
		t.Fatalf("submit: %s", w.Body)
	}
	// a No jumps past Details to the last section
	if w := post("path=0&q_name00=Ben&q_come00=No"); !strings.Contains(w.Body.String(), "Sorry to miss you") || !strings.Contains(w.Body.String(), `value="0,2"`) {
		t.Fatalf("jump: %s", w.Body)
	}
	post("path=0,2&q_name00=Ben&q_come00=No&q_why000=Away")
	// a hidden answer changed to something invalid sends them back to that page
	if w := post("path=0,2&q_name00=&q_come00=No&q_why000=Away"); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `value="0"`) {
		t.Fatalf("tampered: %d", w.Code)
	}
	rs := a.store.responses(f.ID)
	if len(rs) != 2 || rs[1].Answers["why000"] != nil || rs[1].Answers["diet00"][0] != "Vegan" || rs[0].Answers["why000"][0] != "Away" {
		t.Fatalf("%+v", rs)
	}
	if w := do(a, "GET", "/forms/"+f.ID+"/responses.csv", "", ""); strings.Contains(w.Body.String(), "Details") {
		t.Fatal("a section break became a CSV column")
	}
}
