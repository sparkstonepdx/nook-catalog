package main

// Container images (nook#64): the OCI distribution API under /v2/, next to the npm registry, behind the same token
// check. `docker login HOST` takes any username and the app's NOOK_TOKEN as the password.
//
//	data/oci/blobs/sha256/HEX   layers, configs and manifests, kept once however many repositories use them
//	data/oci/repos/NAME/repo.json  its owner, tags, manifests and which blobs it has
//	data/oci/uploads/UUID       uploads in progress (and UUID.json: which repository, which app)
//
// The same rules as npm: the app that first pushes to a repository owns it, and only its token can push, tag or
// delete there; any token can pull. Deleting a manifest drops what nothing refers to any more.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	repoRE   = regexp.MustCompile(`^[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*)*$`)
	digestRE = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	ociTagRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
	uuidRE   = regexp.MustCompile(`^[a-f0-9]{32}$`)
)

// repo is what's kept for one image repository.
type repo struct {
	Name      string            `json:"name"`
	Owner     string            `json:"owner"`
	Tags      map[string]string `json:"tags"`      // tag -> manifest digest
	Manifests map[string]string `json:"manifests"` // digest -> media type
	Blobs     map[string]bool   `json:"blobs"`     // layers and configs pushed or mounted here
}

type upload struct {
	Repo string `json:"repo"`
	App  string `json:"app"`
}

func ociFail(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"code": code, "message": msg}}})
}

func (s *server) ociDir(parts ...string) string {
	return filepath.Join(append([]string{s.dir, "oci"}, parts...)...)
}

func (s *server) blobPath(d string) string {
	return s.ociDir("blobs", "sha256", strings.TrimPrefix(d, "sha256:"))
}

func (s *server) loadRepo(name string) (*repo, error) {
	b, err := os.ReadFile(s.ociDir("repos", filepath.FromSlash(name), "repo.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r repo
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *server) saveRepo(r *repo) error {
	dir := s.ociDir("repos", filepath.FromSlash(r.Name))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	tmp := filepath.Join(dir, ".repo.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "repo.json"))
}

// writable loads (or starts) name for app to push to: someone else's repository is refused.
func (s *server) writable(name, app string) (*repo, error) {
	r, err := s.loadRepo(name)
	if err != nil {
		return nil, err
	}
	if r == nil {
		r = &repo{Name: name, Owner: app}
	}
	if r.Owner != app {
		return nil, fmt.Errorf("%s belongs to the app %s; push with its token", name, r.Owner)
	}
	if r.Tags == nil {
		r.Tags = map[string]string{}
	}
	if r.Manifests == nil {
		r.Manifests = map[string]string{}
	}
	if r.Blobs == nil {
		r.Blobs = map[string]bool{}
	}
	return r, nil
}

// serveOCI handles everything under /v2/.
func (s *server) serveOCI(w http.ResponseWriter, r *http.Request, app string) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	p := strings.TrimPrefix(r.URL.Path, "/v2/")
	if p == "" {
		writeJSON(w, 200, map[string]any{})
		return
	}
	var name, kind, ref string
	for _, k := range []string{"/blobs/uploads/", "/blobs/", "/manifests/", "/tags/list"} {
		if i := strings.LastIndex(p, k); i > 0 {
			name, kind, ref = p[:i], strings.Trim(k, "/"), p[i+len(k):]
			break
		}
	}
	if strings.HasSuffix(p, "/blobs/uploads") { // POST without the trailing slash
		name, kind, ref = strings.TrimSuffix(p, "/blobs/uploads"), "blobs/uploads", ""
	}
	if name == "" || len(name) > 255 || !repoRE.MatchString(name) {
		ociFail(w, 404, "NAME_INVALID", "not a repository name")
		return
	}
	get := r.Method == "GET" || r.Method == "HEAD"
	switch {
	case kind == "tags/list" && get:
		s.ociTags(w, r, name)
	case kind == "manifests" && get:
		s.ociGetManifest(w, r, name, ref)
	case kind == "manifests" && r.Method == "PUT":
		s.ociPutManifest(w, r, app, name, ref)
	case kind == "manifests" && r.Method == "DELETE":
		s.ociDeleteManifest(w, app, name, ref)
	case kind == "blobs" && get:
		s.ociGetBlob(w, r, name, ref)
	case kind == "blobs" && r.Method == "DELETE":
		ociFail(w, 405, "UNSUPPORTED", "blobs go when nothing refers to them: delete the manifest")
	case kind == "blobs/uploads" && ref == "" && r.Method == "POST":
		s.ociStartUpload(w, r, app, name)
	case kind == "blobs/uploads" && ref != "":
		s.ociUpload(w, r, app, name, ref)
	default:
		ociFail(w, 405, "UNSUPPORTED", "not supported by this registry")
	}
}

func (s *server) ociTags(w http.ResponseWriter, r *http.Request, name string) {
	rp, err := s.loadRepo(name)
	if err != nil || rp == nil {
		ociFail(w, 404, "NAME_UNKNOWN", "no such repository")
		return
	}
	tags := []string{}
	for t := range rp.Tags {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	if last := r.URL.Query().Get("last"); last != "" {
		i := sort.SearchStrings(tags, last)
		if i < len(tags) && tags[i] == last {
			i++
		}
		tags = tags[i:]
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && n >= 0 && n < len(tags) {
		tags = tags[:n]
		w.Header().Set("Link", fmt.Sprintf(`</v2/%s/tags/list?n=%d&last=%s>; rel="next"`, name, n, tags[len(tags)-1]))
	}
	writeJSON(w, 200, map[string]any{"name": name, "tags": tags})
}

func (s *server) ociGetBlob(w http.ResponseWriter, r *http.Request, name, d string) {
	rp, err := s.loadRepo(name)
	if err != nil || rp == nil || !digestRE.MatchString(d) || !(rp.Blobs[d] || rp.Manifests[d] != "") {
		ociFail(w, 404, "BLOB_UNKNOWN", "no such blob in "+name)
		return
	}
	f, err := os.Open(s.blobPath(d))
	if err != nil {
		ociFail(w, 404, "BLOB_UNKNOWN", "no such blob")
		return
	}
	defer f.Close()
	w.Header().Set("Docker-Content-Digest", d)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "max-age=31536000, immutable")
	http.ServeContent(w, r, "", time.Time{}, f) // Range requests too
}

func (s *server) ociGetManifest(w http.ResponseWriter, r *http.Request, name, ref string) {
	rp, err := s.loadRepo(name)
	if err != nil || rp == nil {
		ociFail(w, 404, "NAME_UNKNOWN", "no such repository")
		return
	}
	d := ref
	if !digestRE.MatchString(ref) {
		d = rp.Tags[ref]
	}
	mt, ok := rp.Manifests[d]
	if !ok {
		ociFail(w, 404, "MANIFEST_UNKNOWN", "no such manifest")
		return
	}
	b, err := os.ReadFile(s.blobPath(d))
	if err != nil {
		ociFail(w, 404, "MANIFEST_UNKNOWN", "no such manifest")
		return
	}
	w.Header().Set("Content-Type", mt)
	w.Header().Set("Docker-Content-Digest", d)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(200)
	if r.Method != "HEAD" {
		w.Write(b)
	}
}

// manifestRefs is what a manifest needs present: an image's config and layers, or an index's manifests.
type manifestBody struct {
	MediaType string `json:"mediaType"`
	Config    *struct {
		Digest string `json:"digest"`
	} `json:"config"`
	Layers []struct {
		Digest string   `json:"digest"`
		URLs   []string `json:"urls"`
	} `json:"layers"`
	Manifests []struct {
		Digest string `json:"digest"`
	} `json:"manifests"`
	Subject *struct {
		Digest string `json:"digest"`
	} `json:"subject"`
}

func (s *server) ociPutManifest(w http.ResponseWriter, r *http.Request, app, name, ref string) {
	if !digestRE.MatchString(ref) && !ociTagRE.MatchString(ref) {
		ociFail(w, 400, "TAG_INVALID", "a tag is up to 128 letters, digits, . _ -")
		return
	}
	mt := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
	b, err := io.ReadAll(io.LimitReader(r.Body, 4<<20+1))
	if err != nil || len(b) > 4<<20 {
		ociFail(w, 400, "MANIFEST_INVALID", "a manifest is at most 4 MB of JSON")
		return
	}
	var m manifestBody
	if err := json.Unmarshal(b, &m); err != nil {
		ociFail(w, 400, "MANIFEST_INVALID", "not JSON: "+err.Error())
		return
	}
	if mt == "" {
		mt = m.MediaType
	}
	if mt == "" {
		ociFail(w, 400, "MANIFEST_INVALID", "no media type")
		return
	}
	sum := sha256.Sum256(b)
	d := "sha256:" + hex.EncodeToString(sum[:])
	if digestRE.MatchString(ref) && ref != d {
		ociFail(w, 400, "DIGEST_INVALID", "the manifest's digest is "+d)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, err := s.writable(name, app)
	if err != nil {
		ociFail(w, 403, "DENIED", err.Error())
		return
	}
	var missing []string
	need := func(x string) {
		if !digestRE.MatchString(x) {
			missing = append(missing, x)
		} else if _, err := os.Stat(s.blobPath(x)); err != nil {
			missing = append(missing, x)
		}
	}
	if m.Config != nil {
		need(m.Config.Digest)
	}
	for _, l := range m.Layers {
		if len(l.URLs) == 0 { // foreign layers live elsewhere
			need(l.Digest)
		}
	}
	for _, c := range m.Manifests {
		if rp.Manifests[c.Digest] == "" {
			missing = append(missing, c.Digest)
		}
	}
	if len(missing) > 0 {
		ociFail(w, 400, "MANIFEST_BLOB_UNKNOWN", "push these first: "+strings.Join(missing, ", "))
		return
	}
	if err := writeBlob(s.blobPath(d), b); err != nil {
		ociFail(w, 500, "UNKNOWN", err.Error())
		return
	}
	rp.Manifests[d] = mt
	if m.Config != nil {
		rp.Blobs[m.Config.Digest] = true
	}
	for _, l := range m.Layers {
		if len(l.URLs) == 0 {
			rp.Blobs[l.Digest] = true
		}
	}
	if !digestRE.MatchString(ref) {
		rp.Tags[ref] = d
	}
	if err := s.saveRepo(rp); err != nil {
		ociFail(w, 500, "UNKNOWN", err.Error())
		return
	}
	w.Header().Set("Location", "/v2/"+name+"/manifests/"+d)
	w.Header().Set("Docker-Content-Digest", d)
	if m.Subject != nil {
		w.Header().Set("OCI-Subject", m.Subject.Digest)
	}
	w.WriteHeader(201)
}

func (s *server) ociDeleteManifest(w http.ResponseWriter, app, name, ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, err := s.loadRepo(name)
	if err != nil || rp == nil {
		ociFail(w, 404, "NAME_UNKNOWN", "no such repository")
		return
	}
	if rp.Owner != app {
		ociFail(w, 403, "DENIED", fmt.Sprintf("%s belongs to the app %s", name, rp.Owner))
		return
	}
	if digestRE.MatchString(ref) {
		if rp.Manifests[ref] == "" {
			ociFail(w, 404, "MANIFEST_UNKNOWN", "no such manifest")
			return
		}
		delete(rp.Manifests, ref)
		for t, d := range rp.Tags {
			if d == ref {
				delete(rp.Tags, t)
			}
		}
	} else { // a tag: only the tag goes; its manifest stays until deleted by digest
		if _, ok := rp.Tags[ref]; !ok {
			ociFail(w, 404, "MANIFEST_UNKNOWN", "no such tag")
			return
		}
		delete(rp.Tags, ref)
	}
	if err := s.saveRepo(rp); err != nil {
		ociFail(w, 500, "UNKNOWN", err.Error())
		return
	}
	s.collect()
	w.WriteHeader(202)
}

// collect drops each repository's links to blobs none of its manifests use, then blobs no repository uses. A blob
// written in the last hour stays: it may belong to a push that hasn't sent its manifest yet.
func (s *server) collect() {
	used := map[string]bool{}
	filepath.WalkDir(s.ociDir("repos"), func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || e.Name() != "repo.json" {
			return nil
		}
		name, _ := filepath.Rel(s.ociDir("repos"), filepath.Dir(p))
		rp, err := s.loadRepo(filepath.ToSlash(name))
		if err != nil || rp == nil {
			return nil
		}
		keep := map[string]bool{}
		for d := range rp.Manifests {
			used[d] = true
			b, err := os.ReadFile(s.blobPath(d))
			if err != nil {
				continue
			}
			var m manifestBody
			json.Unmarshal(b, &m)
			if m.Config != nil {
				keep[m.Config.Digest] = true
			}
			for _, l := range m.Layers {
				keep[l.Digest] = true
			}
		}
		changed := false
		for d := range rp.Blobs {
			if !keep[d] {
				delete(rp.Blobs, d)
				changed = true
			}
		}
		if changed {
			s.saveRepo(rp)
		}
		for d := range keep {
			used[d] = true
		}
		return nil
	})
	if ups, _ := os.ReadDir(s.ociDir("uploads")); ups != nil { // pushes given up on
		for _, e := range ups {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 24*time.Hour {
				os.Remove(s.ociDir("uploads", e.Name()))
			}
		}
	}
	entries, _ := os.ReadDir(s.ociDir("blobs", "sha256"))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || used["sha256:"+e.Name()] || time.Since(info.ModTime()) < time.Hour {
			continue
		}
		os.Remove(s.ociDir("blobs", "sha256", e.Name()))
	}
}

func writeBlob(path string, b []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil // same digest, same bytes
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---- uploads ----

func (s *server) ociStartUpload(w http.ResponseWriter, r *http.Request, app, name string) {
	q := r.URL.Query()
	s.mu.Lock()
	rp, err := s.writable(name, app)
	if err == nil {
		err = s.saveRepo(rp) // the first push claims the name
	}
	s.mu.Unlock()
	if err != nil {
		ociFail(w, 403, "DENIED", err.Error())
		return
	}
	// a blob another repository has: link it here without sending it again
	if mount, from := q.Get("mount"), q.Get("from"); digestRE.MatchString(mount) && repoRE.MatchString(from) {
		if src, _ := s.loadRepo(from); src != nil && src.Blobs[mount] {
			if _, err := os.Stat(s.blobPath(mount)); err == nil {
				s.linkBlob(w, app, name, mount)
				return
			}
		}
	}
	id := make([]byte, 16)
	rand.Read(id)
	uuid := hex.EncodeToString(id)
	os.MkdirAll(s.ociDir("uploads"), 0o700)
	meta, _ := json.Marshal(upload{Repo: name, App: app})
	if err := os.WriteFile(s.ociDir("uploads", uuid+".json"), meta, 0o600); err != nil {
		ociFail(w, 500, "UNKNOWN", err.Error())
		return
	}
	if err := os.WriteFile(s.ociDir("uploads", uuid), nil, 0o600); err != nil {
		ociFail(w, 500, "UNKNOWN", err.Error())
		return
	}
	if d := q.Get("digest"); d != "" { // the whole blob in the POST
		s.finishUpload(w, r, app, name, uuid, d)
		return
	}
	s.uploadStatus(w, name, uuid, 202)
}

func (s *server) uploadStatus(w http.ResponseWriter, name, uuid string, status int) {
	size := int64(0)
	if fi, err := os.Stat(s.ociDir("uploads", uuid)); err == nil {
		size = fi.Size()
	}
	w.Header().Set("Location", "/v2/"+name+"/blobs/uploads/"+uuid)
	w.Header().Set("Docker-Upload-UUID", uuid)
	w.Header().Set("Range", fmt.Sprintf("0-%d", max(0, size-1)))
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(status)
}

func (s *server) ociUpload(w http.ResponseWriter, r *http.Request, app, name, uuid string) {
	if !uuidRE.MatchString(uuid) {
		ociFail(w, 404, "BLOB_UPLOAD_UNKNOWN", "no such upload")
		return
	}
	b, err := os.ReadFile(s.ociDir("uploads", uuid+".json"))
	var u upload
	if err != nil || json.Unmarshal(b, &u) != nil || u.Repo != name {
		ociFail(w, 404, "BLOB_UPLOAD_UNKNOWN", "no such upload")
		return
	}
	if u.App != app {
		ociFail(w, 403, "DENIED", "another app's upload")
		return
	}
	switch r.Method {
	case "GET":
		s.uploadStatus(w, name, uuid, 204)
	case "DELETE":
		os.Remove(s.ociDir("uploads", uuid))
		os.Remove(s.ociDir("uploads", uuid+".json"))
		w.WriteHeader(204)
	case "PATCH":
		if err := s.appendChunk(r, uuid); err != nil {
			ociFail(w, 416, "BLOB_UPLOAD_INVALID", err.Error())
			return
		}
		s.uploadStatus(w, name, uuid, 202)
	case "PUT":
		s.finishUpload(w, r, app, name, uuid, r.URL.Query().Get("digest"))
	default:
		ociFail(w, 405, "UNSUPPORTED", "not supported by this registry")
	}
}

// appendChunk adds the request body to the upload, checking Content-Range starts where it left off.
func (s *server) appendChunk(r *http.Request, uuid string) error {
	path := s.ociDir("uploads", uuid)
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if cr := r.Header.Get("Content-Range"); cr != "" {
		var from, to int64
		if _, err := fmt.Sscanf(strings.TrimPrefix(cr, "bytes "), "%d-%d", &from, &to); err != nil || from != fi.Size() {
			return fmt.Errorf("this chunk starts at %d; the upload has %d bytes", from, fi.Size())
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	limit := s.cfg.MaxImageBytes - fi.Size()
	n, err := io.Copy(f, io.LimitReader(r.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		f.Truncate(fi.Size())
		return fmt.Errorf("a layer is at most %d MB here (OCI_MAX_MB)", s.cfg.MaxImageBytes>>20)
	}
	return nil
}

func (s *server) finishUpload(w http.ResponseWriter, r *http.Request, app, name, uuid, d string) {
	path := s.ociDir("uploads", uuid)
	if !digestRE.MatchString(d) {
		ociFail(w, 400, "DIGEST_INVALID", "?digest=sha256:... is needed to finish an upload")
		return
	}
	if r.ContentLength != 0 {
		if err := s.appendChunk(r, uuid); err != nil {
			ociFail(w, 416, "BLOB_UPLOAD_INVALID", err.Error())
			return
		}
	}
	f, err := os.Open(path)
	if err != nil {
		ociFail(w, 404, "BLOB_UPLOAD_UNKNOWN", "no such upload")
		return
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	f.Close()
	if err != nil || "sha256:"+hex.EncodeToString(h.Sum(nil)) != d {
		ociFail(w, 400, "DIGEST_INVALID", "the upload's content doesn't match "+d)
		return
	}
	dst := s.blobPath(d)
	os.MkdirAll(filepath.Dir(dst), 0o700)
	if _, err := os.Stat(dst); err == nil {
		os.Remove(path)
	} else if err := os.Rename(path, dst); err != nil {
		ociFail(w, 500, "UNKNOWN", err.Error())
		return
	}
	os.Remove(path + ".json")
	s.linkBlob(w, app, name, d)
}

func (s *server) linkBlob(w http.ResponseWriter, app, name, d string) {
	s.mu.Lock()
	rp, err := s.writable(name, app)
	if err == nil {
		rp.Blobs[d] = true
		err = s.saveRepo(rp)
	}
	s.mu.Unlock()
	if err != nil {
		ociFail(w, 403, "DENIED", err.Error())
		return
	}
	w.Header().Set("Location", "/v2/"+name+"/blobs/"+d)
	w.Header().Set("Docker-Content-Digest", d)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(201)
}
