// formlify catches the forms on static sites hosted on nook, with Netlify's own markup: a <form netlify> (or
// data-netlify="true") in a site bound to Formlify works unchanged, on Netlify or here.
//
//   - publish: nook's publish hook. Each deploy of a bound site, it rewrites the marked forms the way Netlify does
//     (drops the attribute, adds a hidden form-name) and records them.
//   - serve: the site's POSTs, which nook routes here, are checked (a declared form, declared fields, honeypot,
//     a per-visitor limit, size) and kept as one JSON line each under data/. People are told by email (through
//     the bound email service) or a webhook. Its own address shows the submissions, behind nook's login.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type config struct {
	Port        string
	Data        string
	URL         string // its own address, for links in emails
	SMTPURL     string // from the bound email service
	NotifyEmail string
	NotifyFrom  string
	Webhook     string
	PushURL     string // from the bound notify service (ntfy): a topic to publish each submission to
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

func dataDir() string { return env("FORMLIFY_DATA", filepath.Join(env("HOME", "."), "data")) }

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		w.Write([]byte("ok\n"))
	case r.Header.Get("X-Nook-Site") != "" && r.Method == http.MethodPost:
		a.submit(w, r)
	default:
		a.admin(w, r)
	}
}

func main() {
	st := &store{dir: dataDir()}
	if len(os.Args) > 1 && os.Args[1] == "publish" {
		if err := publish(st, os.Stdin, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "formlify:", err)
			os.Exit(1)
		}
		return
	}
	cfg := config{Port: env("PORT", "8080"), Data: st.dir, SMTPURL: os.Getenv("SMTP_URL"), NotifyEmail: os.Getenv("NOTIFY_EMAIL"),
		NotifyFrom: os.Getenv("NOTIFY_FROM"), Webhook: os.Getenv("NOTIFY_WEBHOOK"), PushURL: os.Getenv("NOTIFY_URL"), PushToken: os.Getenv("NOTIFY_TOKEN")}
	if h := os.Getenv("SITE_HOST"); h != "" {
		cfg.URL = "https://" + h
	}
	if cfg.NotifyEmail != "" && (cfg.SMTPURL == "" || cfg.NotifyFrom == "") {
		log.Printf("NOTIFY_EMAIL is set but email can't go out: bind an email service (Mail relay) and set NOTIFY_FROM")
	}
	a := &app{cfg: cfg, store: st, limit: &ipLimit{max: 10, seen: map[string][]time.Time{}}}
	srv := &http.Server{Addr: "127.0.0.1:" + cfg.Port, Handler: a, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute}
	log.Printf("formlify: serving on %s, data in %s", srv.Addr, st.dir)
	log.Fatal(srv.ListenAndServe())
}
