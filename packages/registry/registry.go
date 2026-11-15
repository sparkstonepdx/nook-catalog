package main

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	appRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	nameRE    = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)
	versionRE = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	tagRE     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,63}$`)
)

// doc is what's kept for each package: the version manifests exactly as published, plus who owns it.
type doc struct {
	Name     string                     `json:"name"`
	Owner    string                     `json:"owner"` // the nook app that first published it; only it may publish more
	DistTags map[string]string          `json:"dist-tags"`
	Versions map[string]json.RawMessage `json:"versions"`
	Time     map[string]string          `json:"time"`
}

type server struct {
	dir    string
	cfg    config
	client *http.Client
	now    func() time.Time
	mu     sync.Mutex // publishes and tag changes, one at a time
}

func newServer(dir string, cfg config) *server {
	return &server{dir: dir, cfg: cfg, client: &http.Client{Timeout: 30 * time.Second}, now: time.Now}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path // already unescaped, so @scope%2fname arrives as @scope/name
	if p == "/-/ping" {
		writeJSON(w, 200, map[string]any{})
		return
	}
	app := r.Header.Get("X-Nook-App")
	if !appRE.MatchString(app) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="nook"`)
		fail(w, 401, "send your app's NOOK_TOKEN: //HOST/:_authToken=${NOOK_TOKEN} in .npmrc")
		return
	}
	get := r.Method == "GET" || r.Method == "HEAD"
	switch {
	case p == "/-/whoami" && get:
		writeJSON(w, 200, map[string]string{"username": app})
	case strings.HasPrefix(p, "/-/package/"):
		s.distTags(w, r, app, strings.TrimPrefix(p, "/-/package/"))
	case strings.HasPrefix(p, "/-/"):
		fail(w, 404, "not supported by this registry")
	default:
		name, rest := splitName(strings.TrimPrefix(p, "/"))
		if !validName(name) {
			fail(w, 404, "not found")
			return
		}
		switch {
		case rest == "" && get:
			s.packument(w, r, name)
		case rest == "" && r.Method == "PUT":
			s.publish(w, r, app, name)
		case strings.HasPrefix(rest, "-/") && get:
			s.tarball(w, r, name, strings.TrimPrefix(rest, "-/"))
		case rest == "" || strings.HasPrefix(rest, "-rev/") || strings.HasPrefix(rest, "-/"):
			fail(w, 405, "published versions can't be changed or removed")
		default:
			fail(w, 404, "not found")
		}
	}
}

// splitName takes "@scope/name/rest" or "name/rest" apart.
func splitName(p string) (name, rest string) {
	n := 1
	if strings.HasPrefix(p, "@") {
		n = 2
	}
	parts := strings.SplitN(p, "/", n+1)
	if len(parts) < n {
		return "", ""
	}
	name = strings.Join(parts[:n], "/")
	if len(parts) > n {
		rest = parts[n]
	}
	return name, rest
}

func validName(name string) bool { return len(name) <= 214 && nameRE.MatchString(name) }

func scopeOf(name string) string {
	if i := strings.Index(name, "/"); strings.HasPrefix(name, "@") && i > 0 {
		return name[:i]
	}
	return ""
}

func (s *server) pkgDir(name string) string {
	return filepath.Join(s.dir, "packages", filepath.FromSlash(name))
}

func (s *server) load(name string) (*doc, error) {
	b, err := os.ReadFile(filepath.Join(s.pkgDir(name), "package.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var d doc
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &d, nil
}

func (s *server) save(d *doc) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.pkgDir(d.Name), "package.json"), b, false)
}

// writeAtomic writes through a temp file and a rename; with excl it refuses to replace an existing file.
func writeAtomic(path string, b []byte, excl bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if excl {
		if _, err := os.Stat(path); err == nil {
			return fs.ErrExist
		}
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (s *server) base(r *http.Request) string {
	if s.cfg.URL != "" {
		return s.cfg.URL
	}
	return "https://" + r.Host
}

// tarballName is the file a version's tarball is kept as: kit-1.2.0.tgz for @sparkstone/kit.
func tarballName(name, version string) string {
	return name[strings.LastIndex(name, "/")+1:] + "-" + version + ".tgz"
}

func (s *server) packument(w http.ResponseWriter, r *http.Request, name string) {
	d, err := s.load(name)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if d == nil {
		s.upstream(w, r, name)
		return
	}
	versions := map[string]any{}
	for v, raw := range d.Versions {
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		if dist, ok := m["dist"].(map[string]any); ok {
			dist["tarball"] = s.base(r) + "/" + name + "/-/" + tarballName(name, v)
		}
		versions[v] = m
	}
	writeJSON(w, 200, map[string]any{"_id": name, "name": name, "dist-tags": d.DistTags, "versions": versions, "time": d.Time})
}

// upstream passes a package that isn't published here through from the public registry.
func (s *server) upstream(w http.ResponseWriter, r *http.Request, name string) {
	if s.cfg.Upstream == "" {
		fail(w, 404, "not found")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), "GET", s.cfg.Upstream+"/"+url.PathEscape(name), nil)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if a := r.Header.Get("Accept"); a != "" {
		req.Header.Set("Accept", a)
	}
	res, err := s.client.Do(req)
	if err != nil {
		fail(w, 502, "upstream: "+err.Error())
		return
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(res.StatusCode)
	if r.Method != "HEAD" {
		io.Copy(w, res.Body)
	}
}

// onUpstream says whether the public registry already has a package by this name.
func (s *server) onUpstream(r *http.Request, name string) (bool, error) {
	if s.cfg.Upstream == "" {
		return false, nil
	}
	req, err := http.NewRequestWithContext(r.Context(), "GET", s.cfg.Upstream+"/"+url.PathEscape(name), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/vnd.npm.install-v1+json")
	res, err := s.client.Do(req)
	if err != nil {
		return false, err
	}
	res.Body.Close()
	switch {
	case res.StatusCode == 404:
		return false, nil
	case res.StatusCode < 300:
		return true, nil
	}
	return false, fmt.Errorf("%s answered %d", s.cfg.Upstream, res.StatusCode)
}

func (s *server) tarball(w http.ResponseWriter, r *http.Request, name, file string) {
	if strings.ContainsAny(file, `/\`) || !strings.HasSuffix(file, ".tgz") {
		fail(w, 404, "not found")
		return
	}
	f, err := os.Open(filepath.Join(s.pkgDir(name), file))
	if err != nil {
		fail(w, 404, "not found")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, r, file, st.ModTime(), f)
}

type publishBody struct {
	Name        string                     `json:"name"`
	Versions    map[string]json.RawMessage `json:"versions"`
	DistTags    map[string]string          `json:"dist-tags"`
	Attachments map[string]struct {
		Data   string `json:"data"`
		Length int64  `json:"length"`
	} `json:"_attachments"`
}

func (s *server) publish(w http.ResponseWriter, r *http.Request, app, name string) {
	if scope := scopeOf(name); scope == "" {
		fail(w, 403, "only scoped packages (@scope/name) can be published here")
		return
	} else if len(s.cfg.Scopes) > 0 && !contains(s.cfg.Scopes, scope) {
		fail(w, 403, scope+" isn't one of this registry's scopes ("+strings.Join(s.cfg.Scopes, ", ")+")")
		return
	}
	// base64 makes the body about 4/3 of the tarball
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxBytes*4/3+1<<20))
	if err != nil {
		fail(w, 413, "too big: the limit is NPM_MAX_MB")
		return
	}
	var pb publishBody
	if err := json.Unmarshal(body, &pb); err != nil {
		fail(w, 400, "not a publish: "+err.Error())
		return
	}
	if pb.Name != name || len(pb.Versions) != 1 || len(pb.Attachments) != 1 {
		fail(w, 400, "a publish carries one version and its tarball, for the package in the URL")
		return
	}
	var version string
	var raw json.RawMessage
	for version, raw = range pb.Versions {
	}
	var manifest map[string]any
	if !versionRE.MatchString(version) || json.Unmarshal(raw, &manifest) != nil || manifest["name"] != name || manifest["version"] != version {
		fail(w, 400, "the version's manifest doesn't match its name and version")
		return
	}
	var tgz []byte
	for _, a := range pb.Attachments {
		if tgz, err = base64.StdEncoding.DecodeString(a.Data); err != nil || (a.Length != 0 && a.Length != int64(len(tgz))) {
			fail(w, 400, "the tarball doesn't decode to its stated length")
			return
		}
	}
	if int64(len(tgz)) > s.cfg.MaxBytes || len(tgz) < 2 || tgz[0] != 0x1f || tgz[1] != 0x8b {
		fail(w, 400, "the tarball isn't a .tgz under NPM_MAX_MB")
		return
	}
	sum1 := sha1.Sum(tgz)
	sum512 := sha512.Sum512(tgz)
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(sum512[:])
	dist, _ := manifest["dist"].(map[string]any)
	if dist == nil {
		dist = map[string]any{}
	}
	if sent, _ := dist["integrity"].(string); sent != "" && sent != integrity {
		fail(w, 400, "the tarball doesn't match the manifest's integrity")
		return
	}
	dist["shasum"] = hex.EncodeToString(sum1[:])
	dist["integrity"] = integrity
	dist["tarball"] = tarballName(name, version) // the address is filled in when it's served
	manifest["dist"] = dist
	manifest["_npmUser"] = map[string]string{"name": app}
	delete(manifest, "_attachments")
	clean, err := json.Marshal(manifest)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load(name)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if d == nil {
		// a name the public registry has would shadow it for every app here, so it stays there
		on, err := s.onUpstream(r, name)
		if err != nil {
			fail(w, 502, "couldn't check the public registry for "+name+": "+err.Error())
			return
		}
		if on {
			fail(w, 409, name+" is on "+s.cfg.Upstream+"; publishing it here would hide that one from every app")
			return
		}
		d = &doc{Name: name, Owner: app, DistTags: map[string]string{}, Versions: map[string]json.RawMessage{}, Time: map[string]string{}}
	}
	if d.Owner != app {
		fail(w, 403, name+" is published by "+d.Owner+"'s token")
		return
	}
	if _, ok := d.Versions[version]; ok {
		fail(w, 409, "cannot publish over the previously published version "+version)
		return
	}
	if err := writeAtomic(filepath.Join(s.pkgDir(name), tarballName(name, version)), tgz, true); err != nil {
		fail(w, 500, err.Error())
		return
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	d.Versions[version] = clean
	d.Time[version] = now
	if d.Time["created"] == "" {
		d.Time["created"] = now
	}
	d.Time["modified"] = now
	for tag, v := range pb.DistTags {
		if tagRE.MatchString(tag) && v == version {
			d.DistTags[tag] = v
		}
	}
	if d.DistTags["latest"] == "" {
		d.DistTags["latest"] = version
	}
	if err := s.save(d); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]bool{"ok": true})
}

// distTags answers npm dist-tag ls/add/rm: /-/package/NAME/dist-tags[/TAG].
func (s *server) distTags(w http.ResponseWriter, r *http.Request, app, p string) {
	name, rest := splitName(p)
	tag, isTags := strings.CutPrefix(rest, "dist-tags")
	tag = strings.TrimPrefix(tag, "/")
	if !validName(name) || !isTags || (tag != "" && !tagRE.MatchString(tag)) {
		fail(w, 404, "not found")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load(name)
	if err != nil || d == nil {
		fail(w, 404, "not found")
		return
	}
	if r.Method == "GET" {
		writeJSON(w, 200, d.DistTags)
		return
	}
	if d.Owner != app {
		fail(w, 403, name+" is published by "+d.Owner+"'s token")
		return
	}
	switch {
	case tag != "" && (r.Method == "PUT" || r.Method == "POST"):
		var v string
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1024))
		if json.Unmarshal(bytes.TrimSpace(b), &v) != nil || d.Versions[v] == nil {
			fail(w, 400, "send a published version as a JSON string")
			return
		}
		d.DistTags[tag] = v
	case tag != "" && r.Method == "DELETE":
		if tag == "latest" {
			fail(w, 400, "latest can be moved but not removed")
			return
		}
		delete(d.DistTags, tag)
	default:
		fail(w, 405, "method not allowed")
		return
	}
	if err := s.save(d); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
