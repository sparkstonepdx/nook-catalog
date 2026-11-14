// push is a Web Push server for every app on a nook server. Each app gets its own VAPID key pair, made the first
// time its CI asks for it with NOOK_TOKEN. Browsers subscribe and get a send handle: anyone holding the handle (another
// player, the app's own backend) can push to that one browser, now or at a set time, and nobody else can.
//
// Behind nook's token check ({"auth": "nook-token"}): token requests arrive with X-Nook-App and never the token.
// The browser-facing paths are open (nook.json "open"), so they're rate-limited here instead.
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
	Subject      string   // PUSH_SUBJECT: mailto: or https: contact the push services can reach
	SendsPerHour int      // PUSH_SENDS_PER_HOUR: per handle, sends and schedules together
	Extra        []string // PUSH_ENDPOINT_HOSTS: push services beyond the usual ones
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func main() {
	dir := env("PUSH_DATA", filepath.Join(env("HOME", "."), "data", "push"))
	st, err := openStore(dir)
	if err != nil {
		log.Fatalf("opening %s: %v", dir, err)
	}
	n, _ := strconv.Atoi(env("PUSH_SENDS_PER_HOUR", "60"))
	cfg := config{Subject: env("PUSH_SUBJECT", ""), SendsPerHour: n}
	for _, h := range strings.Split(os.Getenv("PUSH_ENDPOINT_HOSTS"), ",") {
		if h = strings.TrimSpace(h); h != "" {
			cfg.Extra = append(cfg.Extra, h)
		}
	}
	if cfg.Subject == "" {
		log.Print("PUSH_SUBJECT isn't set: pushes will fail until it is (a mailto: address, like mailto:you@example.com)")
	}
	s := newServer(st, cfg)
	go s.runScheduler(10 * time.Second)
	addr := "127.0.0.1:" + env("PORT", "8080")
	log.Printf("push listening on %s, data in %s", addr, dir)
	log.Fatal(http.ListenAndServe(addr, s))
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
