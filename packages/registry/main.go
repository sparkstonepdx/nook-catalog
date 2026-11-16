// registry is a small private npm registry for the apps on a nook server. Packages are published and installed with the
// app's NOOK_TOKEN. Apps point their scope at it (@sparkstone:registry=...): private packages in that scope are served
// from here, and the scope's public ones (@sparkstone/css) are passed through from npmjs.org, so one scope can hold
// both. Nothing outside NPM_SCOPES is passed through, so it's never a general npm mirror, and a name that's public on
// npmjs.org can't be published here, so the two never collide.
//
// Behind nook's token check ({"auth": "nook-token"}): every request arrives with X-Nook-App and never the token.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type config struct {
	URL      string   // NPM_URL: this registry's public address; defaults to https:// and the request's host
	Scopes   []string // NPM_SCOPES: scopes that may be published here, like @sparkstone; empty for any scope
	Public   string   // NPM_PUBLIC: the public registry: public packages in NPM_SCOPES come from it, and new names are checked against it; "off" for neither
	MaxBytes int64    // NPM_MAX_MB: the largest publish
	// OCI_MAX_MB: the largest image layer
	MaxImageBytes int64
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func loadConfig() config {
	mb, _ := strconv.Atoi(env("NPM_MAX_MB", "50"))
	omb, _ := strconv.Atoi(env("OCI_MAX_MB", "2048"))
	cfg := config{URL: strings.TrimRight(env("NPM_URL", ""), "/"), Public: strings.TrimRight(env("NPM_PUBLIC", "https://registry.npmjs.org"), "/"), MaxBytes: int64(mb) << 20, MaxImageBytes: int64(omb) << 20}
	if cfg.Public == "off" {
		cfg.Public = ""
	}
	for _, s := range strings.Split(os.Getenv("NPM_SCOPES"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			if !strings.HasPrefix(s, "@") {
				s = "@" + s
			}
			cfg.Scopes = append(cfg.Scopes, s)
		}
	}
	return cfg
}

func main() {
	dir := env("NPM_DATA", filepath.Join(env("HOME", "."), "data", "npm"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Fatalf("making %s: %v", dir, err)
	}
	s := newServer(dir, loadConfig())
	addr := "127.0.0.1:" + env("PORT", "8080")
	log.Printf("registry listening on %s, packages in %s", addr, dir)
	srv := &http.Server{Addr: addr, Handler: s, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
