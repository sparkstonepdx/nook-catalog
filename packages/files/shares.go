package main

// Share links: anyone with /s/ID gets one file until the link expires (an hour to 30 days, 7 days unless asked).
// A link follows its path, so a share of an alias like kinlog.apk serves whatever is the latest until it expires.
//
//	POST   /v1/shares  {"app": "kinlog", "path": "android/kinlog.apk", "hours": 24}   app only for a logged-in user
//	GET    /v1/shares  the live links you can see
//	DELETE /v1/shares/ID
//	GET    /s/ID       (open) the file

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type share struct {
	ID      string    `json:"id"`
	URL     string    `json:"url"`
	App     string    `json:"app"`
	Path    string    `json:"path"`
	By      string    `json:"by"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *server) sharePath(id string) string { return filepath.Join(s.shares, id+".json") }

// loadShare reads a live share; an expired one is removed and reported as gone.
func (s *server) loadShare(id string) (share, bool, bool) {
	var sh share
	if !shareRE.MatchString(id) {
		return sh, false, false
	}
	b, err := os.ReadFile(s.sharePath(id))
	if err != nil || json.Unmarshal(b, &sh) != nil {
		return sh, false, false
	}
	if time.Now().After(sh.Expires) {
		os.Remove(s.sharePath(id))
		return sh, false, true
	}
	sh.URL = s.public + "/s/" + id
	return sh, true, false
}

func (s *server) share(w http.ResponseWriter, r *http.Request, c caller) {
	// JSON only: a form on another site can't send it, and with the cookie being SameSite=Lax it couldn't log in anyway
	if t, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); t != "application/json" {
		fail(w, 415, "send JSON: {\"path\": \"...\", \"hours\": 24}")
		return
	}
	var in struct {
		App   string  `json:"app"`
		Path  string  `json:"path"`
		Hours float64 `json:"hours"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		fail(w, 400, "send JSON: {\"path\": \"...\", \"hours\": 24}")
		return
	}
	app := in.App
	if app == "" {
		app = c.app
	}
	rel, ok := clean(in.Path)
	if !appRE.MatchString(app) || !ok {
		fail(w, 400, "say which file: path, and app when you're logged in")
		return
	}
	if !c.can(app) {
		fail(w, 403, "a token shares only its own app's files")
		return
	}
	if fi, err := os.Stat(filepath.Join(s.root, app, filepath.FromSlash(rel))); err != nil || fi.IsDir() {
		fail(w, 404, "no such file")
		return
	}
	ttl := shareDefault
	if in.Hours != 0 {
		ttl = time.Duration(in.Hours * float64(time.Hour))
	}
	if ttl < time.Hour || ttl > shareMax {
		fail(w, 400, fmt.Sprintf("a link lasts from 1 hour to %d days", int(shareMax.Hours()/24)))
		return
	}
	now := time.Now().UTC()
	by := c.user
	if by == "" {
		by = c.app + " (token)"
	}
	sh := share{ID: newID(), App: app, Path: rel, By: by, Created: now, Expires: now.Add(ttl).Truncate(time.Second)}
	os.MkdirAll(s.shares, 0o700)
	b, _ := json.Marshal(sh)
	if err := os.WriteFile(s.sharePath(sh.ID), b, 0o600); err != nil {
		fail(w, 500, err.Error())
		return
	}
	sh.URL = s.public + "/s/" + sh.ID
	log.Printf("%s shared %s/%s until %s", by, app, rel, sh.Expires.Format(time.RFC3339))
	writeJSON(w, 201, sh)
}

func (s *server) listShares(w http.ResponseWriter, r *http.Request, c caller) {
	out := []share{}
	ds, _ := os.ReadDir(s.shares)
	for _, d := range ds {
		id, ok := strings.CutSuffix(d.Name(), ".json")
		if !ok {
			continue
		}
		if sh, live, _ := s.loadShare(id); live && c.can(sh.App) {
			out = append(out, sh)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Expires.Before(out[j].Expires) })
	writeJSON(w, 200, map[string]any{"shares": out})
}

func (s *server) unshare(w http.ResponseWriter, c caller, id string) {
	sh, live, _ := s.loadShare(id)
	if !live || !c.can(sh.App) {
		fail(w, 404, "no such link")
		return
	}
	os.Remove(s.sharePath(id))
	w.WriteHeader(204)
}

// GET /s/ID (open): the shared file, while the link lasts.
func (s *server) shared(w http.ResponseWriter, r *http.Request, id string) {
	sh, live, expired := s.loadShare(id)
	if expired {
		http.Error(w, "This link has expired. Ask whoever sent it for a new one.", 410)
		return
	}
	if !live {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Robots-Tag", "noindex")
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.serveFile(w, r, sh.App, sh.Path, "private, no-cache")
}

// dropShares ends every link to app/rel: the file it pointed at is gone, and a new file at the same path
// shouldn't be served on an old link.
func (s *server) dropShares(app, rel string) {
	ds, _ := os.ReadDir(s.shares)
	for _, d := range ds {
		id, ok := strings.CutSuffix(d.Name(), ".json")
		if !ok {
			continue
		}
		if sh, live, _ := s.loadShare(id); live && sh.App == app && sh.Path == rel {
			os.Remove(s.sharePath(id))
		}
	}
}
