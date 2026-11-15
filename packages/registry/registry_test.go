package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// nookAuth stands in for nook's token check: "Bearer token-APP" becomes X-Nook-App: APP, anything else is a 401.
func nookAuth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("X-Nook-App")
		app, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer token-")
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="nook"`)
			http.Error(w, "unauthorized", 401)
			return
		}
		r.Header.Set("X-Nook-App", app)
		r.Header.Del("Authorization")
		h.ServeHTTP(w, r)
	})
}

func newTestRegistry(t *testing.T, cfg config) (*httptest.Server, string) {
	dir := t.TempDir()
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = 50 << 20
	}
	srv := httptest.NewServer(nookAuth(newServer(dir, cfg)))
	t.Cleanup(srv.Close)
	return srv, dir
}

func tgzOf(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: "package/" + name, Mode: 0o644, Size: int64(len(body))})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func publishBodyFor(name, version string, tgz []byte, tags map[string]string) []byte {
	sum := sha512.Sum512(tgz)
	b, _ := json.Marshal(map[string]any{
		"_id": name, "name": name, "dist-tags": tags,
		"versions": map[string]any{version: map[string]any{"name": name, "version": version,
			"dist": map[string]any{"integrity": "sha512-" + base64.StdEncoding.EncodeToString(sum[:]), "tarball": "http://wherever/x.tgz"}}},
		"_attachments": map[string]any{name + "-" + version + ".tgz": map[string]any{"content_type": "application/octet-stream",
			"data": base64.StdEncoding.EncodeToString(tgz), "length": len(tgz)}},
	})
	return b
}

func call(t *testing.T, method, url, app string, body []byte) (int, []byte) {
	req, _ := http.NewRequest(method, url, bytes.NewReader(body))
	if app != "" {
		req.Header.Set("Authorization", "Bearer token-"+app)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

func TestPublishAndInstall(t *testing.T) {
	srv, _ := newTestRegistry(t, config{Scopes: []string{"@sparkstone"}})
	tgz := tgzOf(t, map[string]string{"package.json": `{"name":"@sparkstone/kit","version":"0.2.0"}`})

	if code, _ := call(t, "GET", srv.URL+"/@sparkstone%2fkit", "", nil); code != 401 {
		t.Fatalf("no token: got %d, want 401", code)
	}
	if code, b := call(t, "PUT", srv.URL+"/@sparkstone%2fkit", "build-docs", publishBodyFor("@sparkstone/kit", "0.2.0", tgz, map[string]string{"latest": "0.2.0"})); code != 201 {
		t.Fatalf("publish: %d %s", code, b)
	}
	code, b := call(t, "GET", srv.URL+"/@sparkstone%2fkit", "wordbag", nil)
	if code != 200 {
		t.Fatalf("packument: %d %s", code, b)
	}
	var p struct {
		DistTags map[string]string `json:"dist-tags"`
		Versions map[string]struct {
			Dist struct{ Tarball, Integrity, Shasum string }
		}
	}
	json.Unmarshal(b, &p)
	v := p.Versions["0.2.0"]
	if p.DistTags["latest"] != "0.2.0" || v.Dist.Tarball != "https://"+strings.TrimPrefix(srv.URL, "http://")+"/@sparkstone/kit/-/kit-0.2.0.tgz" || v.Dist.Shasum == "" {
		t.Fatalf("packument: %s", b)
	}
	code, got := call(t, "GET", srv.URL+"/@sparkstone/kit/-/kit-0.2.0.tgz", "wordbag", nil)
	if code != 200 || !bytes.Equal(got, tgz) {
		t.Fatalf("tarball: %d, %d bytes", code, len(got))
	}
	if code, b := call(t, "GET", srv.URL+"/-/whoami", "wordbag", nil); code != 200 || !strings.Contains(string(b), `"wordbag"`) {
		t.Fatalf("whoami: %d %s", code, b)
	}

	// only what's published here: nothing comes from npmjs.org
	if code, _ := call(t, "GET", srv.URL+"/@sparkstone%2fcss", "wordbag", nil); code != 404 {
		t.Fatalf("a package not published here: %d, want 404", code)
	}
}

func TestPublishRules(t *testing.T) {
	srv, _ := newTestRegistry(t, config{Scopes: []string{"@sparkstone"}})
	tgz := tgzOf(t, map[string]string{"package.json": "{}"})
	put := func(app, name, version string) (int, string) {
		code, b := call(t, "PUT", srv.URL+"/"+strings.Replace(name, "/", "%2f", 1), app, publishBodyFor(name, version, tgz, map[string]string{"latest": version}))
		return code, string(b)
	}
	if code, b := put("build-docs", "@sparkstone/kit", "1.0.0"); code != 201 {
		t.Fatalf("first: %d %s", code, b)
	}
	cases := []struct {
		app, name, version string
		want               int
	}{
		{"build-docs", "@sparkstone/kit", "1.0.0", 409}, // same version again
		{"wordbag", "@sparkstone/kit", "1.0.1", 403},    // someone else's package
		{"build-docs", "@other/kit", "1.0.0", 403},      // not one of the scopes
		{"build-docs", "leftpad", "1.0.0", 403},         // unscoped
	}
	for _, c := range cases {
		if code, b := put(c.app, c.name, c.version); code != c.want {
			t.Errorf("%s publishing %s@%s: %d %s, want %d", c.app, c.name, c.version, code, b, c.want)
		}
	}
	if code, b := put("build-docs", "@sparkstone/kit", "1.1.0-next.0"); code != 201 {
		t.Fatalf("prerelease: %d %s", code, b)
	}

	// a tampered tarball is refused
	body := publishBodyFor("@sparkstone/kit", "1.2.0", tgz, nil)
	body = bytes.Replace(body, []byte(`"integrity":"sha512-`), []byte(`"integrity":"sha512-AAAA`), 1)
	if code, _ := call(t, "PUT", srv.URL+"/@sparkstone%2fkit", "build-docs", body); code != 400 {
		t.Errorf("bad integrity: %d", code)
	}

	// dist-tags
	if code, b := call(t, "PUT", srv.URL+"/-/package/@sparkstone%2fkit/dist-tags/next", "build-docs", []byte(`"1.1.0-next.0"`)); code != 200 {
		t.Fatalf("dist-tag add: %d %s", code, b)
	}
	if code, _ := call(t, "PUT", srv.URL+"/-/package/@sparkstone%2fkit/dist-tags/next", "wordbag", []byte(`"1.0.0"`)); code != 403 {
		t.Errorf("dist-tag by another app: %d", code)
	}
	code, b := call(t, "GET", srv.URL+"/-/package/@sparkstone%2fkit/dist-tags", "wordbag", nil)
	if code != 200 || string(bytes.TrimSpace(b)) != `{"latest":"1.1.0-next.0","next":"1.1.0-next.0"}` {
		// publishing 1.1.0-next.0 with "latest" in its dist-tags moved latest, as npm does
		t.Errorf("dist-tags: %d %s", code, b)
	}
	if code, _ := call(t, "DELETE", srv.URL+"/@sparkstone%2fkit/-rev/1", "build-docs", nil); code != 405 {
		t.Errorf("unpublish: %d", code)
	}
}

// TestRealClients publishes with npm and installs with npm and pnpm, through the same token check nook does.
func TestRealClients(t *testing.T) {
	npm, err := exec.LookPath("npm")
	if err != nil || testing.Short() {
		t.Skip("npm isn't installed")
	}
	// the real npm clients talk HTTP to 127.0.0.1, so the tarball links must too
	inner := newServer(t.TempDir(), config{Scopes: []string{"@sparkstone"}, MaxBytes: 50 << 20})
	srv := httptest.NewServer(nookAuth(inner))
	defer srv.Close()
	inner.cfg.URL = srv.URL
	host := strings.TrimPrefix(srv.URL, "http://")
	npmrc := fmt.Sprintf("@sparkstone:registry=%s/\n//%s/:_authToken=token-%%s\n", srv.URL, host)

	pkg := t.TempDir()
	os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@sparkstone/kit","version":"0.2.0","main":"index.js"}`), 0o644)
	os.WriteFile(filepath.Join(pkg, "index.js"), []byte("export const hi = 'from nook'\n"), 0o644)
	os.WriteFile(filepath.Join(pkg, ".npmrc"), []byte(fmt.Sprintf(npmrc, "build-docs")), 0o644)
	run := func(dir string, name string, args ...string) string {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "npm_config_cache="+filepath.Join(dir, ".cache"), "npm_config_update_notifier=false")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		return string(out)
	}
	run(pkg, npm, "publish", "--provenance=false")
	if out := run(pkg, npm, "view", "@sparkstone/kit", "version"); strings.TrimSpace(out) != "0.2.0" {
		t.Fatalf("npm view: %q", out)
	}

	for _, client := range []string{"npm", "pnpm"} {
		bin, err := exec.LookPath(client)
		if err != nil {
			t.Logf("%s isn't installed, skipping", client)
			continue
		}
		app := t.TempDir()
		os.WriteFile(filepath.Join(app, "package.json"), []byte(`{"name":"app","private":true,"dependencies":{"@sparkstone/kit":"^0.2.0"}}`), 0o644)
		os.WriteFile(filepath.Join(app, ".npmrc"), []byte(fmt.Sprintf(npmrc, "wordbag")), 0o644)
		run(app, bin, "install")
		b, err := os.ReadFile(filepath.Join(app, "node_modules", "@sparkstone", "kit", "index.js"))
		if err != nil || !strings.Contains(string(b), "from nook") {
			t.Fatalf("%s install: %v %s", client, err, b)
		}
	}
}

// TestNameCollisions: a name that's public on npmjs.org can't be published here, and when npmjs.org can't be asked,
// a new name waits rather than skip the check. New versions of a name already here don't ask.
func TestNameCollisions(t *testing.T) {
	asked := 0
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		if r.URL.EscapedPath() == "/@sparkstone%2Fcss" || r.URL.EscapedPath() == "/@sparkstone%2fcss" {
			io.WriteString(w, `{"name":"@sparkstone/css"}`)
			return
		}
		http.Error(w, `{"error":"Not found"}`, 404)
	}))
	defer public.Close()
	srv, _ := newTestRegistry(t, config{Public: public.URL, Scopes: []string{"@sparkstone"}})
	tgz := tgzOf(t, map[string]string{"package.json": "{}"})
	put := func(name, version string) int {
		code, _ := call(t, "PUT", srv.URL+"/"+strings.Replace(name, "/", "%2f", 1), "build-docs", publishBodyFor(name, version, tgz, nil))
		return code
	}
	if code := put("@sparkstone/css", "9.0.0"); code != 409 {
		t.Fatalf("a name public on npmjs.org: %d, want 409", code)
	}
	if code := put("@sparkstone/kit", "1.0.0"); code != 201 {
		t.Fatalf("a new private name: %d", code)
	}
	before := asked
	if code := put("@sparkstone/kit", "1.0.1"); code != 201 || asked != before {
		t.Fatalf("a new version of a name already here: %d, asked npmjs.org %d more times", code, asked-before)
	}
	public.Close()
	if code := put("@sparkstone/other", "1.0.0"); code != 502 {
		t.Fatalf("npmjs.org unreachable: %d, want 502", code)
	}
	if code, _ := call(t, "GET", srv.URL+"/@sparkstone%2fcss", "wordbag", nil); code != 502 {
		t.Fatalf("pass-through with the public registry down: %d, want 502", code)
	}
}

// TestRealPassThrough installs a private package from here and a public one in the same scope from npmjs.org, with
// real npm. It needs the network, so it runs only with NPM_LIVE=1.
func TestRealPassThrough(t *testing.T) {
	npm, err := exec.LookPath("npm")
	if err != nil || os.Getenv("NPM_LIVE") != "1" {
		t.Skip("set NPM_LIVE=1 (needs npm and npmjs.org)")
	}
	inner := newServer(t.TempDir(), config{Scopes: []string{"@sparkstone"}, Public: "https://registry.npmjs.org", MaxBytes: 50 << 20})
	srv := httptest.NewServer(nookAuth(inner))
	defer srv.Close()
	inner.cfg.URL = srv.URL
	host := strings.TrimPrefix(srv.URL, "http://")
	npmrc := fmt.Sprintf("@sparkstone:registry=%s/\n//%s/:_authToken=token-%%s\n", srv.URL, host)
	run := func(dir string, args ...string) {
		cmd := exec.Command(npm, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "npm_config_cache="+filepath.Join(dir, ".cache"), "npm_config_update_notifier=false")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("npm %v: %v\n%s", args, err, out)
		}
	}
	pkg := t.TempDir()
	os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@sparkstone/nook-test-private","version":"0.1.0","main":"index.js"}`), 0o644)
	os.WriteFile(filepath.Join(pkg, "index.js"), []byte("module.exports = 'private'\n"), 0o644)
	os.WriteFile(filepath.Join(pkg, ".npmrc"), []byte(fmt.Sprintf(npmrc, "build-docs")), 0o644)
	run(pkg, "publish", "--provenance=false")

	app := t.TempDir()
	os.WriteFile(filepath.Join(app, "package.json"), []byte(`{"name":"app","private":true,"dependencies":{"@sparkstone/nook-test-private":"^0.1.0","@sparkstone/css":"^4.0.0"}}`), 0o644)
	os.WriteFile(filepath.Join(app, ".npmrc"), []byte(fmt.Sprintf(npmrc, "wordbag")), 0o644)
	run(app, "install")
	for _, p := range []string{"@sparkstone/nook-test-private/index.js", "@sparkstone/css/package.json"} {
		if _, err := os.Stat(filepath.Join(app, "node_modules", p)); err != nil {
			t.Fatalf("%s wasn't installed: %v", p, err)
		}
	}
}

// TestPassThrough: a public package in one of the scopes comes from the public registry; anything else doesn't.
func TestPassThrough(t *testing.T) {
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/@sparkstone%2fcss", "/@sparkstone%2Fcss":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"name":"@sparkstone/css","dist-tags":{"latest":"4.0.0"},"versions":{"4.0.0":{"dist":{"tarball":"https://registry.npmjs.org/@sparkstone/css/-/css-4.0.0.tgz"}}}}`)
		case "/react", "/@other%2fthing":
			io.WriteString(w, `{"name":"x"}`)
		default:
			http.Error(w, `{"error":"Not found"}`, 404)
		}
	}))
	defer public.Close()
	srv, _ := newTestRegistry(t, config{Public: public.URL, Scopes: []string{"@sparkstone"}})
	code, b := call(t, "GET", srv.URL+"/@sparkstone%2fcss", "wordbag", nil)
	if code != 200 || !strings.Contains(string(b), "registry.npmjs.org/@sparkstone/css/-/css-4.0.0.tgz") {
		t.Fatalf("public package in the scope: %d %s (its tarball should still come from the public registry)", code, b)
	}
	if code, _ := call(t, "GET", srv.URL+"/@sparkstone%2fnothing", "wordbag", nil); code != 404 {
		t.Fatalf("missing in both: %d", code)
	}
	for _, p := range []string{"/react", "/@other%2fthing"} {
		if code, _ := call(t, "GET", srv.URL+p, "wordbag", nil); code != 404 {
			t.Fatalf("%s is outside the scopes and must not pass through: %d", p, code)
		}
	}
	public.Close()
	if code, _ := call(t, "GET", srv.URL+"/@sparkstone%2fcss", "wordbag", nil); code != 502 {
		t.Fatalf("public registry down: %d, want 502", code)
	}
}
