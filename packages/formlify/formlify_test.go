package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const contactPage = `<html><body><form name="contact" method="POST" netlify netlify-honeypot="bot-field" action="/thanks/">
<input name="bot-field"><input type="email" name="email"><textarea name="message"></textarea><input type="file" name="cv">
<button>Send</button></form></body></html>`

// publishSite runs the publish hook on one page and returns the rewritten files it handed back.
func publishSite(t *testing.T, st *store, host string, pages map[string]string) map[string]string {
	t.Setenv("SITE_HOST", host)
	t.Setenv("SITE", "www")
	t.Setenv("NOOK_CONSUMER", "blog")
	var in bytes.Buffer
	tw := tar.NewWriter(&in)
	for name, body := range pages {
		tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(body))})
		tw.Write([]byte(body))
	}
	tw.Close()
	var out, log bytes.Buffer
	if err := publish(st, &in, &out, &log); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	tr := tar.NewReader(&out)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(tr)
		got[h.Name] = string(b)
	}
	if !strings.Contains(log.String(), "contact (/contact/)") && len(got) > 0 {
		t.Fatalf("deploy log: %q", log.String())
	}
	return got
}

func newApp(t *testing.T, webhook string) *app {
	return &app{cfg: config{Webhook: webhook, URL: "https://formlify.example.com"}, store: &store{dir: t.TempDir()}, limit: &ipLimit{max: 3, seen: map[string][]time.Time{}}}
}

func post(a *app, host, ctype string, body io.Reader, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/contact/", body)
	r.Header.Set("Content-Type", ctype)
	r.Header.Set("X-Nook-Site", "blog/"+host)
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

func TestPublishAndSubmit(t *testing.T) {
	hooks := make(chan map[string]any, 4)
	wh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		hooks <- m
	}))
	defer wh.Close()
	a := newApp(t, wh.URL)
	got := publishSite(t, a.store, "example.com", map[string]string{"contact/index.html": contactPage, "about.html": "<p>no forms</p>", "app.js": "x"})
	if len(got) != 1 || !strings.Contains(got["contact/index.html"], `<input type="hidden" name="form-name" value="contact">`) {
		t.Fatalf("handed back %v", got)
	}

	// a multipart submission with a file and a field the form doesn't declare
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("form-name", "contact")
	mw.WriteField("email", "ana@example.org")
	mw.WriteField("message", "=HYPERLINK(\"x\")\nhello")
	mw.WriteField("injected", "nope")
	fw, _ := mw.CreateFormFile("cv", "../../etc/passwd")
	fw.Write([]byte("my cv"))
	mw.Close()
	w := post(a, "example.com", mw.FormDataContentType(), &body, map[string]string{"Referer": "https://example.com/contact/"})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/thanks/" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	subs := a.store.list("example.com", "contact")
	if len(subs) != 1 || subs[0].Fields["email"][0] != "ana@example.org" || subs[0].Fields["injected"] != nil || subs[0].Page != "/contact/" {
		t.Fatalf("%+v", subs)
	}
	if len(subs[0].Files) != 1 || subs[0].Files[0].Name != "passwd" {
		t.Fatalf("file: %+v", subs[0].Files)
	}
	if b, _ := os.ReadFile(filepath.Join(a.store.formDir("example.com", "contact"), "files", subs[0].ID, "passwd")); string(b) != "my cv" {
		t.Fatal("the file wasn't kept in the form's own folder")
	}
	select {
	case m := <-hooks:
		if m["form"] != "contact" || m["site"] != "example.com" {
			t.Fatalf("webhook: %v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no webhook")
	}

	// a bot fills the honeypot: thanked, not kept
	form := url.Values{"form-name": {"contact"}, "email": {"bot@x.y"}, "bot-field": {"buy"}}
	w = post(a, "example.com", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), map[string]string{"Accept": "application/json"})
	if w.Code != 200 || len(a.store.list("example.com", "contact")) != 1 {
		t.Fatalf("honeypot: %d", w.Code)
	}

	// another site, an unknown form, too many tries
	if w := post(a, "other.com", "application/x-www-form-urlencoded", strings.NewReader("form-name=contact&email=a"), nil); w.Code != 404 {
		t.Fatalf("unbound site: %d", w.Code)
	}
	if w := post(a, "example.com", "application/x-www-form-urlencoded", strings.NewReader("form-name=nope&email=a"), nil); w.Code != 404 {
		t.Fatalf("unknown form: %d", w.Code)
	}
	if w := post(a, "example.com", "application/x-www-form-urlencoded", strings.NewReader("form-name=contact&email=a"), nil); w.Code != 429 {
		t.Fatalf("rate limit: %d", w.Code)
	}

	// the pages and the CSV
	r := httptest.NewRequest("GET", "/s/example.com/contact.csv", nil)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, r)
	if csv := rec.Body.String(); !strings.Contains(csv, "id,at,page,email,message,files") || !strings.Contains(csv, "'=HYPERLINK") {
		t.Fatalf("csv: %s", csv)
	}
	rec = httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest("GET", "/s/example.com/contact", nil))
	if !strings.Contains(rec.Body.String(), "ana@example.org") || !strings.Contains(rec.Body.String(), "=HYPERLINK(&#34;x&#34;)") {
		t.Fatalf("form page: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), `/s/example.com/contact`) {
		t.Fatalf("home: %s", rec.Body.String())
	}

	// delete one
	del := httptest.NewRequest("POST", "/s/example.com/contact/delete", strings.NewReader("id="+subs[0].ID))
	del.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.ServeHTTP(httptest.NewRecorder(), del)
	if n := len(a.store.list("example.com", "contact")); n != 0 {
		t.Fatalf("%d left", n)
	}
}

func TestMailText(t *testing.T) {
	sub := submission{Fields: map[string][]string{"email": {"ana@example.org"}, "message": {"hi"}}}
	txt := mailText(config{URL: "https://f.example.com"}, siteForms{Host: "example.com"}, Form{Name: "contact"}, sub)
	if !strings.Contains(txt, "message: hi") || !strings.Contains(txt, "https://f.example.com/s/example.com/contact") {
		t.Fatal(txt)
	}
	if firstEmail(sub) != "ana@example.org" || firstEmail(submission{Fields: map[string][]string{"email": {"a@b\r\nBcc: x@y"}}}) != "" {
		t.Fatal("reply-to")
	}
}

func TestPush(t *testing.T) {
	got := make(chan *http.Request, 1)
	bodies := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- r
		bodies <- string(b)
	}))
	defer srv.Close()
	cfg := config{URL: "https://forms.example.com", PushURL: srv.URL + "/formlify", PushToken: "tk_abc"}
	sub := submission{Fields: map[string][]string{"email": {"ana@example.org"}, "message": {strings.Repeat("x", 200)}}}
	if err := push(cfg, siteForms{Host: "example.com"}, Form{Name: "contact"}, sub); err != nil {
		t.Fatal(err)
	}
	r, body := <-got, <-bodies
	if r.URL.Path != "/formlify" || r.Header.Get("Authorization") != "Bearer tk_abc" || r.Header.Get("Title") != "contact: new submission on example.com" ||
		r.Header.Get("Click") != "https://forms.example.com/s/example.com/contact" || !strings.HasPrefix(body, "email: ana@example.org\nmessage: xxx") || len(body) > 150 {
		t.Fatalf("%v %q", r.Header, body)
	}
}
