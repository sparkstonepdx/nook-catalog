// forms makes forms and collects their answers, like Google Forms, on your own server. You build a form in the
// editor (behind nook's login), share its link, and read the responses as a table, a summary or a CSV. Anyone with
// the link can answer it; nothing else is public. Data stays in files under data/.
package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type config struct {
	Port        string
	URL         string // its own address, for share links and links in notices
	SMTPURL     string // from the bound email service
	NotifyEmail string
	NotifyFrom  string
	PushURL     string // from the bound notify service (ntfy)
	PushToken   string
}

type app struct {
	cfg   config
	store *store
	limit *ipLimit
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// Routes: /f/ID is the form people answer (open to anyone, nook.json "open"); everything else is the editor and
// the responses, behind nook's login.
func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		w.Write([]byte("ok\n"))
	case strings.HasPrefix(r.URL.Path, "/f/"):
		a.public(w, r)
	default:
		a.admin(w, r)
	}
}

func main() {
	st := &store{dir: env("FORMS_DATA", filepath.Join(env("HOME", "."), "data"))}
	cfg := config{Port: env("PORT", "8080"), SMTPURL: os.Getenv("SMTP_URL"), NotifyEmail: os.Getenv("NOTIFY_EMAIL"), NotifyFrom: os.Getenv("NOTIFY_FROM"),
		PushURL: os.Getenv("NOTIFY_URL"), PushToken: os.Getenv("NOTIFY_TOKEN")}
	if h := os.Getenv("SITE_HOST"); h != "" {
		cfg.URL = "https://" + h
	}
	if cfg.NotifyEmail != "" && (cfg.SMTPURL == "" || cfg.NotifyFrom == "") {
		log.Printf("NOTIFY_EMAIL is set but email can't go out: bind an email service (Mail relay) and set NOTIFY_FROM")
	}
	a := &app{cfg: cfg, store: st, limit: &ipLimit{max: 10, seen: map[string][]time.Time{}}}
	srv := &http.Server{Addr: "127.0.0.1:" + cfg.Port, Handler: a, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute}
	log.Printf("forms: serving on %s, data in %s", srv.Addr, st.dir)
	log.Fatal(srv.ListenAndServe())
}
