package main

import (
	"encoding/json"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	maxBody  = 10 << 20 // a whole submission, files included
	maxFiles = 5
	maxField = 64 << 10 // one field's text
)

// ipLimit allows each address a few submissions a minute per site.
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

// clientIP is the visitor's address from Caddy's X-Forwarded-For (Caddy adds it; the client's own copies come
// first, so it's the last one).
func clientIP(r *http.Request) string {
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return r.RemoteAddr
	}
	parts := strings.Split(xff, ",")
	return strings.TrimSpace(parts[len(parts)-1])
}

// submit takes a form post from a bound static site. nook routes the site's POSTs here with X-Nook-Site: APP/HOST.
func (a *app) submit(w http.ResponseWriter, r *http.Request) {
	_, host, _ := strings.Cut(r.Header.Get("X-Nook-Site"), "/")
	host = strings.ToLower(host)
	sf, ok := a.store.site(host)
	if !safeHost(host) || !ok {
		a.answer(w, r, http.StatusNotFound, "There's no form here.", "")
		return
	}
	if !a.limit.allow(host+" "+clientIP(r), time.Now()) {
		a.answer(w, r, http.StatusTooManyRequests, "That's a lot of sending. Wait a minute and try again.", "")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var err error
	if ct == "multipart/form-data" {
		err = r.ParseMultipartForm(1 << 20)
	} else {
		err = r.ParseForm()
	}
	if err != nil {
		a.answer(w, r, http.StatusBadRequest, "That submission was too big or couldn't be read.", "")
		return
	}
	var f *Form
	name := r.PostFormValue("form-name")
	for i := range sf.Forms {
		if name != "" && sf.Forms[i].Name == name || name == "" && sf.Forms[i].Page == r.URL.Path {
			f = &sf.Forms[i]
			break
		}
	}
	if f == nil {
		a.answer(w, r, http.StatusNotFound, "There's no form here.", "")
		return
	}
	if f.Honeypot != "" && r.PostFormValue(f.Honeypot) != "" {
		a.answer(w, r, http.StatusOK, "", f.Action) // a bot: thank it and keep nothing
		return
	}
	sub := submission{ID: newID(), At: time.Now().UTC().Format(time.RFC3339), Page: refererPath(r, host), Fields: map[string][]string{}}
	for _, k := range f.Fields { // only the fields the form declares
		if k == f.Honeypot {
			continue
		}
		for _, v := range r.PostForm[k] {
			if len(v) > maxField {
				v = v[:maxField]
			}
			sub.Fields[k] = append(sub.Fields[k], v)
		}
	}
	if r.MultipartForm != nil {
		for _, field := range f.Files {
			for _, fh := range r.MultipartForm.File[field] {
				if len(sub.Files) >= maxFiles || fh.Filename == "" {
					continue
				}
				src, err := fh.Open()
				if err != nil {
					continue
				}
				dir := filepath.Join(a.store.formDir(host, f.Name), "files", sub.ID)
				os.MkdirAll(dir, 0o700)
				fn := safeName(filepath.Base(fh.Filename))
				dst, err := os.OpenFile(filepath.Join(dir, fn), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
				if err == nil {
					n, _ := io.Copy(dst, src)
					dst.Close()
					sub.Files = append(sub.Files, upload{Field: field, Name: fn, Size: n})
				}
				src.Close()
			}
		}
	}
	if len(sub.Fields) == 0 && len(sub.Files) == 0 {
		a.answer(w, r, http.StatusBadRequest, "That submission was empty.", "")
		return
	}
	if err := a.store.add(host, f.Name, sub); err != nil {
		a.answer(w, r, http.StatusInternalServerError, "The form couldn't be saved. Try again in a moment.", "")
		return
	}
	go a.notify(sf, *f, sub)
	a.answer(w, r, http.StatusOK, "", f.Action)
}

// answer replies to a visitor: JSON for a script, a redirect to the form's own thank-you page if it has one,
// otherwise a small page of our own.
func (a *app) answer(w http.ResponseWriter, r *http.Request, status int, problem, action string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") || r.Header.Get("X-Requested-With") != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"ok": problem == "", "error": problem})
		return
	}
	if problem == "" && strings.HasPrefix(action, "/") && !strings.HasPrefix(action, "//") {
		http.Redirect(w, r, action, http.StatusSeeOther)
		return
	}
	host := r.Header.Get("X-Nook-Site")
	_, host, _ = strings.Cut(host, "/")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	thanksPage.Execute(w, map[string]string{"Problem": problem, "Back": backTo(r, host)})
}

// backTo is the page the form was on, if the browser said, else the site's front page.
func backTo(r *http.Request, host string) string {
	if p := refererPath(r, host); p != "" {
		return p
	}
	return "/"
}

func refererPath(r *http.Request, host string) string {
	u, err := url.Parse(r.Referer())
	if err != nil || !strings.EqualFold(u.Host, host) {
		return ""
	}
	return u.Path
}

var thanksPage = template.Must(template.New("thanks").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>{{if .Problem}}Not sent{{else}}Thank you{{end}}</title>
<style>body{font:1.125rem/1.5 system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 1.5rem;color:#222;background:#fbfaf7}
a{color:#3a4f9a}h1{font-size:1.75rem;margin:0 0 .5rem}</style></head><body>
{{if .Problem}}<h1>Not sent</h1><p>{{.Problem}}</p>{{else}}<h1>Thank you</h1><p>Your message was sent.</p>{{end}}
<p><a href="{{.Back}}">Back to the page</a></p></body></html>`))
