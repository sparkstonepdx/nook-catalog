// files is file storage for the apps on a nook server: CI (or you) uploads with the app's NOOK_TOKEN, people with
// a nook login download, and anyone can download from a share link until it expires. Downloads take ranges, so an
// interrupted APK download resumes. Each app has its own folder: wordbag's files are at /f/wordbag/..., and only
// wordbag's token can change them.
//
// Behind nook's login ({"auth": "nook-login", "open": ["/s/*", "/health"]}): a deploy token arrives as
// X-Nook-App (never the token itself), a logged-in dashboard user as X-Nook-User. Either may come empty (treat
// empty as absent). Share links, /s/ID, are open.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	appRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	segmentRE = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,127}$`) // no leading dot: no hidden files, no ..
	shareRE   = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)
)

const (
	shareDefault = 7 * 24 * time.Hour
	shareMax     = 30 * 24 * time.Hour
)

type server struct {
	root    string // data/files: <app>/<path>
	meta    string // data/meta: <app>/<path>.json
	shares  string // data/shares: <id>.json
	maxSize int64
	public  string // https://files.example.com, for the URLs uploads return
}

type info struct {
	App       string    `json:"app,omitempty"`
	Path      string    `json:"path"`
	URL       string    `json:"url"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Type      string    `json:"type"`
	Modified  time.Time `json:"modified"`
	Public    bool      `json:"public,omitempty"`
	PublicURL string    `json:"publicUrl,omitempty"` // /p/APP/PATH, for anyone, while Public

}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// clean checks a relative path segment by segment: letters, digits, . _ -, no hidden or empty parts, 8 deep at most.
func clean(p string) (string, bool) {
	p = strings.Trim(p, "/")
	parts := strings.Split(p, "/")
	if p == "" || len(parts) > 8 {
		return "", false
	}
	for _, s := range parts {
		if !segmentRE.MatchString(s) {
			return "", false
		}
	}
	return strings.Join(parts, "/"), true
}

func contentType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if t, ok := map[string]string{
		".apk": "application/vnd.android.package-archive", ".aab": "application/octet-stream", ".ipa": "application/octet-stream",
		".dmg": "application/x-apple-diskimage", ".msi": "application/x-msi", ".appimage": "application/octet-stream",
	}[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
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

func (s *server) url(app, rel string) string { return s.public + "/f/" + app + "/" + rel }

func (s *server) publicURL(app, rel string) string { return s.public + "/p/" + app + "/" + rel }

// urls fills in a file's links: its own, and its public one while it's public.
func (s *server) urls(app, rel string, in info) info {
	in.URL, in.PublicURL = s.url(app, rel), ""
	if in.Public {
		in.PublicURL = s.publicURL(app, rel)
	}
	return in
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch p := r.URL.Path; {
	case p == "/health":
		writeJSON(w, 200, map[string]bool{"ok": true})
	case p == "/" && r.Method == "GET":
		s.home(w, r)
	case (p == "/ui.js" || p == "/ui.css") && r.Method == "GET":
		ui(w, p)
	case p == "/favicon.ico":
		w.WriteHeader(204)
	case strings.HasPrefix(p, "/f/") && (r.Method == "GET" || r.Method == "HEAD"):
		s.download(w, r, strings.TrimPrefix(p, "/f/"))
	case strings.HasPrefix(p, "/p/") && (r.Method == "GET" || r.Method == "HEAD"):
		s.publicFile(w, r, strings.TrimPrefix(p, "/p/"))
	case p == "/v1/public" && r.Method == "POST":
		s.withCaller(w, r, s.setPublic)
	case strings.HasPrefix(p, "/s/") && (r.Method == "GET" || r.Method == "HEAD"):
		s.shared(w, r, strings.TrimPrefix(p, "/s/"))
	case p == "/v1/shares" && r.Method == "POST":
		s.withCaller(w, r, s.share)
	case p == "/v1/shares" && r.Method == "GET":
		s.withCaller(w, r, s.listShares)
	case strings.HasPrefix(p, "/v1/shares/") && r.Method == "DELETE":
		s.withCaller(w, r, func(w http.ResponseWriter, r *http.Request, c caller) {
			s.unshare(w, c, strings.TrimPrefix(p, "/v1/shares/"))
		})
	case p == "/v1/files" && r.Method == "GET":
		s.withCaller(w, r, s.list)
	case strings.HasPrefix(p, "/v1/files/"):
		rel, ok := clean(strings.TrimPrefix(p, "/v1/files/"))
		if !ok {
			fail(w, 400, "paths are up to 8 parts of letters, digits, . _ and -, none starting with a dot")
			return
		}
		switch r.Method {
		case "PUT":
			s.withApp(w, r, func(w http.ResponseWriter, r *http.Request, app string) { s.upload(w, r, app, rel) })
		case "DELETE":
			// a token deletes its own app's files; a login any app's, named with ?app=
			s.withCaller(w, r, func(w http.ResponseWriter, r *http.Request, c caller) {
				app := c.app
				if c.user != "" {
					app = r.URL.Query().Get("app")
				} else if a := r.URL.Query().Get("app"); a != "" && a != app {
					fail(w, 403, "a token deletes only its own app's files")
					return
				}
				if !appRE.MatchString(app) {
					fail(w, 400, "say which app's file: ?app=NAME")
					return
				}
				s.remove(w, app, rel, c)
			})
		case "GET", "HEAD":
			s.withApp(w, r, func(w http.ResponseWriter, r *http.Request, app string) { s.stat(w, app, rel) })
		default:
			fail(w, 405, "PUT to upload, DELETE to remove, GET for its details")
		}
	default:
		fail(w, 404, "not found")
	}
}

// caller is who nook's login check let in: an app's deploy token (app) or a dashboard user (user).
type caller struct{ app, user string }

// can says whether c may read app's files and share them: a user allowed on Files reads every app's, a token
// only its own app's.
func (c caller) can(app string) bool { return c.user != "" || c.app == app }

func callerOf(r *http.Request) (caller, bool) {
	if u := r.Header.Get("X-Nook-User"); u != "" && len(u) <= 128 && !strings.ContainsAny(u, "\r\n") {
		return caller{user: u}, true
	}
	if a := r.Header.Get("X-Nook-App"); appRE.MatchString(a) {
		return caller{app: a}, true
	}
	return caller{}, false
}

func (s *server) withCaller(w http.ResponseWriter, r *http.Request, f func(http.ResponseWriter, *http.Request, caller)) {
	c, ok := callerOf(r)
	if !ok {
		fail(w, 401, "log in with nook, or send your app's NOOK_TOKEN: Authorization: Bearer $NOOK_TOKEN")
		return
	}
	f(w, r, c)
}

// withApp runs f for requests with a deploy token, which sets X-Nook-App: changing files is CI's job.
func (s *server) withApp(w http.ResponseWriter, r *http.Request, f func(http.ResponseWriter, *http.Request, string)) {
	app := r.Header.Get("X-Nook-App")
	if !appRE.MatchString(app) {
		fail(w, 401, "send your app's NOOK_TOKEN: Authorization: Bearer $NOOK_TOKEN")
		return
	}
	f(w, r, app)
}

// PUT /v1/files/PATH: the body is the file. ?alias=NAME also puts it at NAME in the same folder (a stable
// "latest" link, like kinlog.apk beside kinlog-1.4.0.apk). ?public=1 makes both public, ?public=0 private;
// without it a path keeps whatever it was, so re-uploading the latest keeps its public link.
func (s *server) upload(w http.ResponseWriter, r *http.Request, app, rel string) {
	pub := r.URL.Query().Get("public")
	if pub != "" && pub != "1" && pub != "0" {
		fail(w, 400, "public is 1 or 0")
		return
	}
	alias := r.URL.Query().Get("alias")
	if alias != "" {
		a, ok := clean(path.Join(path.Dir(rel), alias))
		if !ok || strings.Contains(alias, "/") {
			fail(w, 400, "alias is a file name in the same folder")
			return
		}
		alias = a
	}
	if r.ContentLength > s.maxSize {
		fail(w, 413, fmt.Sprintf("files are up to %d MB here (FILES_MAX_MB)", s.maxSize>>20))
		return
	}
	dst := filepath.Join(s.root, app, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		fail(w, 409, "a file is in the way of that folder")
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".upload-*")
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), http.MaxBytesReader(w, r.Body, s.maxSize))
	tmp.Close()
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			fail(w, 413, fmt.Sprintf("files are up to %d MB here (FILES_MAX_MB)", s.maxSize>>20))
			return
		}
		fail(w, 400, "the upload broke off: "+err.Error())
		return
	}
	if fi, err := os.Stat(dst); err == nil && fi.IsDir() {
		fail(w, 409, "that's a folder")
		return
	}
	os.Chmod(tmp.Name(), 0o644)
	if err := os.Rename(tmp.Name(), dst); err != nil {
		fail(w, 500, err.Error())
		return
	}
	isPublic := func(at string) bool {
		if pub != "" {
			return pub == "1"
		}
		old, _ := s.loadMeta(app, at)
		return old.Public
	}
	in := info{Path: rel, URL: s.url(app, rel), Size: n, SHA256: hex.EncodeToString(h.Sum(nil)), Type: contentType(rel), Modified: time.Now().UTC(), Public: isPublic(rel)}
	s.saveMeta(app, rel, in)
	out := map[string]any{"path": in.Path, "url": in.URL, "size": in.Size, "sha256": in.SHA256, "type": in.Type}
	if in.Public {
		out["publicUrl"] = s.publicURL(app, rel)
	}
	if alias != "" && alias != rel {
		adst := filepath.Join(s.root, app, filepath.FromSlash(alias))
		atmp := adst + ".link"
		os.Remove(atmp)
		if err := os.Link(dst, atmp); err == nil {
			os.Rename(atmp, adst)
			a := in
			a.Path, a.URL, a.Public = alias, s.url(app, alias), isPublic(alias)
			s.saveMeta(app, alias, a)
			out["alias"] = a.URL
			if a.Public {
				out["aliasPublicUrl"] = s.publicURL(app, alias)
			}
		}
	}
	log.Printf("%s uploaded %s (%d bytes)", app, rel, n)
	writeJSON(w, 201, out)
}

func (s *server) metaPath(app, rel string) string {
	return filepath.Join(s.meta, app, filepath.FromSlash(rel)+".json")
}

func (s *server) saveMeta(app, rel string, in info) {
	p := s.metaPath(app, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	b, _ := json.Marshal(in)
	os.WriteFile(p, b, 0o644)
}

func (s *server) loadMeta(app, rel string) (info, bool) {
	var in info
	b, err := os.ReadFile(s.metaPath(app, rel))
	if err != nil || json.Unmarshal(b, &in) != nil {
		return in, false
	}
	return in, true
}

func (s *server) stat(w http.ResponseWriter, app, rel string) {
	in, ok := s.loadMeta(app, rel)
	if !ok {
		fail(w, 404, "no such file")
		return
	}
	writeJSON(w, 200, s.urls(app, rel, in))
}

func (s *server) remove(w http.ResponseWriter, app, rel string, c caller) {
	p := filepath.Join(s.root, app, filepath.FromSlash(rel))
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		fail(w, 404, "no such file")
		return
	}
	os.Remove(p)
	os.Remove(s.metaPath(app, rel))
	s.dropShares(app, rel)
	log.Printf("%s deleted %s/%s", cmpOr(c.user, c.app+" (token)"), app, rel)
	// empty folders go too, up to the app's own
	for d := filepath.Dir(p); d != filepath.Join(s.root, app); d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			break
		}
	}
	w.WriteHeader(204)
}

// GET /v1/files: every file of the token's app, or for a logged-in user, of every app (?app=A for one).
func (s *server) list(w http.ResponseWriter, r *http.Request, c caller) {
	if c.app != "" {
		writeJSON(w, 200, map[string]any{"app": c.app, "files": s.files(c.app)})
		return
	}
	apps := []string{}
	if a := r.URL.Query().Get("app"); a != "" {
		if !appRE.MatchString(a) {
			fail(w, 400, "no such app")
			return
		}
		apps = append(apps, a)
	} else if ds, err := os.ReadDir(s.root); err == nil {
		for _, d := range ds {
			if d.IsDir() && appRE.MatchString(d.Name()) {
				apps = append(apps, d.Name())
			}
		}
	}
	out := []info{}
	for _, a := range apps {
		for _, in := range s.files(a) {
			in.App = a
			out = append(out, in)
		}
	}
	writeJSON(w, 200, map[string]any{"files": out})
}

func (s *server) files(app string) []info {
	base := filepath.Join(s.root, app)
	out := []info{}
	filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		rel = filepath.ToSlash(rel)
		in, ok := s.loadMeta(app, rel)
		if !ok {
			fi, _ := d.Info()
			in = info{Path: rel, Size: fi.Size(), Type: contentType(rel), Modified: fi.ModTime().UTC()}
		}
		out = append(out, s.urls(app, rel, in))
		return nil
	})
	return out
}

// GET /f/APP/PATH (a nook login, or the app's own token): the file.
func (s *server) download(w http.ResponseWriter, r *http.Request, p string) {
	app, rest, _ := strings.Cut(p, "/")
	rel, ok := clean(rest)
	if !appRE.MatchString(app) || !ok {
		http.NotFound(w, r)
		return
	}
	c, ok := callerOf(r)
	if !ok {
		fail(w, 401, "log in with nook, or ask for a share link")
		return
	}
	if !c.can(app) {
		http.NotFound(w, r)
		return
	}
	s.serveFile(w, r, app, rel, "private, max-age=300")
}

// serveFile sends a file with ranges and its SHA-256. Nothing it serves can run script on this origin.
func (s *server) serveFile(w http.ResponseWriter, r *http.Request, app, rel, cache string) {
	f, err := os.Open(filepath.Join(s.root, app, filepath.FromSlash(rel)))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	typ := contentType(rel)
	h := w.Header()
	h.Set("Content-Type", typ)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; media-src 'self'; style-src 'unsafe-inline'; sandbox")
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Cache-Control", cache)
	if in, ok := s.loadMeta(app, rel); ok {
		h.Set("X-Checksum-SHA256", in.SHA256)
	}
	if !inline(typ) {
		h.Set("Content-Disposition", "attachment; filename="+strconv.Quote(path.Base(rel)))
	}
	http.ServeContent(w, r, rel, fi.ModTime(), f)
}

// inline says whether a browser may show a type in place; everything else (APKs, HTML, archives) downloads.
func inline(typ string) bool {
	base, _, _ := strings.Cut(typ, ";")
	switch {
	case strings.HasPrefix(base, "image/"), strings.HasPrefix(base, "video/"), strings.HasPrefix(base, "audio/"):
		return true
	}
	return base == "application/pdf" || base == "text/plain" || base == "application/json"
}

func main() {
	// nook caps any single file an app writes at 2 GB (LimitFSIZE=2G), so a bigger upload would fail partway
	maxMB, _ := strconv.Atoi(env("FILES_MAX_MB", "2047"))
	if maxMB < 1 || maxMB > 2047 {
		maxMB = 2047
	}
	data := env("FILES_DATA", filepath.Join(env("HOME", "."), "data"))
	s := &server{root: filepath.Join(data, "files"), meta: filepath.Join(data, "meta"), shares: filepath.Join(data, "shares"), maxSize: int64(maxMB) << 20,
		public: strings.TrimRight(env("FILES_URL", "https://"+env("SITE_HOST", "localhost")), "/")}
	os.MkdirAll(s.root, 0o755)
	addr := "127.0.0.1:" + env("PORT", "8080")
	log.Printf("files on %s, %s, up to %d MB each", addr, s.public, maxMB)
	srv := &http.Server{Addr: addr, Handler: s, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// GET /p/APP/PATH (open): a file marked public, to anyone. Anything else is a 404, the same as a missing file.
func (s *server) publicFile(w http.ResponseWriter, r *http.Request, p string) {
	app, rest, _ := strings.Cut(p, "/")
	rel, ok := clean(rest)
	if !appRE.MatchString(app) || !ok {
		http.NotFound(w, r)
		return
	}
	if in, ok := s.loadMeta(app, rel); !ok || !in.Public {
		http.NotFound(w, r)
		return
	}
	s.serveFile(w, r, app, rel, "public, max-age=300")
}

// POST /v1/public {"app", "path", "public": true|false}: a login for any app's file, a token for its own (no app).
func (s *server) setPublic(w http.ResponseWriter, r *http.Request, c caller) {
	if t, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); t != "application/json" {
		fail(w, 415, `send JSON: {"path": "...", "public": true}`)
		return
	}
	var in struct {
		App    string `json:"app"`
		Path   string `json:"path"`
		Public *bool  `json:"public"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil || in.Public == nil {
		fail(w, 400, `send JSON: {"path": "...", "public": true}`)
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
		fail(w, 403, "a token changes only its own app's files")
		return
	}
	if fi, err := os.Stat(filepath.Join(s.root, app, filepath.FromSlash(rel))); err != nil || fi.IsDir() {
		fail(w, 404, "no such file")
		return
	}
	meta, ok := s.loadMeta(app, rel)
	if !ok {
		fi, _ := os.Stat(filepath.Join(s.root, app, filepath.FromSlash(rel)))
		meta = info{Path: rel, Size: fi.Size(), Type: contentType(rel), Modified: fi.ModTime().UTC()}
	}
	meta.Public = *in.Public
	s.saveMeta(app, rel, meta)
	log.Printf("%s made %s/%s %s", map[bool]string{true: c.user, false: c.app + " (token)"}[c.user != ""], app, rel, map[bool]string{true: "public", false: "private"}[meta.Public])
	writeJSON(w, 200, s.urls(app, rel, meta))
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
