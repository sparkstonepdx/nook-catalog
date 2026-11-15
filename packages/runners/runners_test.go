package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseScope(t *testing.T) {
	for in, want := range map[string]scope{
		"sparkstonepdx":                             {Org: "sparkstonepdx"},
		"sparkstonepdx/wordbag":                     {Repo: "sparkstonepdx/wordbag"},
		"https://github.com/sparkstonepdx/wordbag/": {Repo: "sparkstonepdx/wordbag"},
	} {
		if got, err := parseScope(in); err != nil || got != want {
			t.Errorf("%q: %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "a/b/c", "../x", "has space"} {
		if _, err := parseScope(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

type entry struct {
	name, body, link string
	typ              byte
	mode             int64
}

func tgz(t *testing.T, es []entry) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range es {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Size: int64(len(e.body)), Linkname: e.link}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		tw.WriteHeader(h)
		if e.typ == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func TestUnpackRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	for _, es := range [][]entry{
		{{name: "../evil", body: "x", typ: tar.TypeReg, mode: 0o644}},
		{{name: "link", link: "/etc/passwd", typ: tar.TypeSymlink}},
		{{name: "a/link", link: "../../out", typ: tar.TypeSymlink}},
	} {
		p := filepath.Join(dir, "x.tar.gz")
		os.WriteFile(p, tgz(t, es), 0o644)
		if err := unpack(p, filepath.Join(dir, "out")); err == nil {
			t.Errorf("%+v should be refused", es[0])
		}
	}
	ok := []entry{{name: "bin/", typ: tar.TypeDir, mode: 0o755}, {name: "bin/node", body: "ELF", typ: tar.TypeReg, mode: 0o755}, {name: "node", link: "bin/node", typ: tar.TypeSymlink}}
	p := filepath.Join(dir, "ok.tar.gz")
	os.WriteFile(p, tgz(t, ok), 0o644)
	if err := unpack(p, filepath.Join(dir, "good")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "good", "node")); string(b) != "ELF" {
		t.Fatal("the in-folder link should work")
	}
}

func TestDownloadChecksSHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("tarball")) }))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "r.tar.gz")
	if err := download(srv.Client(), srv.URL, strings.Repeat("0", 64), dst); err == nil {
		t.Fatal("a wrong checksum was accepted")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("a bad download was kept")
	}
	if err := download(srv.Client(), srv.URL, "", dst); err == nil {
		t.Fatal("no checksum should refuse")
	}
	sum := sha256.Sum256([]byte("tarball"))
	if err := download(srv.Client(), srv.URL, hex.EncodeToString(sum[:]), dst); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseChecksumFromNotes(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"tag_name": "v2.400.0",
			"body":   "notes\n<!-- BEGIN SHA linux-x64 -->" + sum + "<!-- END SHA linux-x64 -->\n",
			"assets": []map[string]string{{"name": "actions-runner-linux-x64-2.400.0.tar.gz", "browser_download_url": "https://x/r.tgz"}}})
	}))
	defer srv.Close()
	g := &github{api: srv.URL, client: srv.Client()}
	rel, err := g.runnerRelease("", "linux-x64")
	if err != nil || rel.SHA256 != sum || rel.Tag != "v2.400.0" {
		t.Fatalf("%+v %v", rel, err)
	}
}

// fakeGitHub serves the runner release (with a digest), JIT configs, and runner listing and deletes.
type fakeGitHub struct {
	mu      sync.Mutex
	jit     []map[string]any
	deleted []string
	tarball []byte
	srv     *httptest.Server
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" && r.URL.Path != "/dl" {
		w.WriteHeader(401)
		return
	}
	sum := sha256.Sum256(f.tarball)
	switch {
	case r.URL.Path == "/dl":
		w.Write(f.tarball)
	case r.URL.Path == "/repos/actions/runner/releases/latest":
		json.NewEncoder(w).Encode(map[string]any{"tag_name": "v2.400.0", "assets": []map[string]string{
			{"name": "actions-runner-" + platform() + "-2.400.0.tar.gz", "browser_download_url": f.srv.URL + "/dl", "digest": "sha256:" + hex.EncodeToString(sum[:])}}})
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/generate-jitconfig"):
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.jit = append(f.jit, body)
		json.NewEncoder(w).Encode(map[string]any{"runner": map[string]any{"id": len(f.jit), "name": body["name"]}, "encoded_jit_config": fmt.Sprintf("cfg%d", len(f.jit))})
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/actions/runners"):
		json.NewEncoder(w).Encode(map[string]any{"runners": []map[string]any{
			{"id": 900, "name": "nook-host-runners-0-dead", "status": "offline"},
			{"id": 901, "name": "someone-else", "status": "offline"}}})
	case r.Method == "DELETE":
		f.deleted = append(f.deleted, r.URL.Path)
		w.WriteHeader(204)
	default:
		w.WriteHeader(404)
	}
}

func TestSupervisorRunsJobs(t *testing.T) {
	// a runner that "runs one job": prints what the real one prints, records its args and env, and exits
	data := t.TempDir()
	out := filepath.Join(data, "calls")
	run := `#!/bin/sh
echo "$(date) Listening for Jobs"
echo "$(date) Running job: build"
echo "args=$*" >> "` + out + `"
env >> "` + out + `.env"
echo "Job build completed with result: Succeeded"
`
	fg := &fakeGitHub{}
	fg.tarball = tgz(t, []entry{{name: "run.sh", body: run, typ: tar.TypeReg, mode: 0o755}})
	fg.srv = httptest.NewServer(fg)
	defer fg.srv.Close()

	t.Setenv("GITHUB_TOKEN", "tok")
	s := &supervisor{
		cfg: config{Scope: scope{Org: "sparkstonepdx"}, Token: "tok", Count: 2, Labels: []string{"self-hosted", "Linux", "X64", "nook"}, Group: 1, Data: data, Prefix: "nook-host-runners-"},
		gh:  &github{api: fg.srv.URL, token: "tok", client: fg.srv.Client()}, ids: map[int]int64{}, pgid: map[int]int{},
	}
	httpClient = fg.srv.Client()
	for i := 0; i < 2; i++ {
		s.slot = append(s.slot, &slot{N: i})
	}
	if err := s.prepare(); err != nil {
		t.Fatal(err)
	}
	s.removeLeftovers()
	fg.mu.Lock()
	if len(fg.deleted) != 1 || !strings.HasSuffix(fg.deleted[0], "/900") {
		t.Fatalf("leftovers: only our offline runner goes, got %v", fg.deleted)
	}
	fg.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(n int) { defer wg.Done(); s.runSlot(ctx, n) }(i)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		fg.mu.Lock()
		n := len(fg.jit)
		fg.mu.Unlock()
		if n >= 4 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	wg.Wait()

	fg.mu.Lock()
	defer fg.mu.Unlock()
	if len(fg.jit) < 4 {
		t.Fatalf("each slot should register again after its job: %d registrations", len(fg.jit))
	}
	first := fg.jit[0]
	if first["runner_group_id"].(float64) != 1 || !strings.HasPrefix(first["name"].(string), "nook-host-runners-") {
		t.Fatalf("jit body: %v", first)
	}
	if labels := fmt.Sprint(first["labels"]); !strings.Contains(labels, "nook") || !strings.Contains(labels, "self-hosted") {
		t.Fatalf("labels: %s", labels)
	}
	calls, _ := os.ReadFile(out)
	if !strings.Contains(string(calls), "args=--jitconfig cfg") {
		t.Fatalf("run.sh args: %s", calls)
	}
	envs, _ := os.ReadFile(out + ".env")
	if strings.Contains(string(envs), "GITHUB_TOKEN") || strings.Contains(string(envs), "tok\n") {
		t.Fatal("a job could see the supervisor's GitHub token")
	}
	if !strings.Contains(string(envs), "RUNNER_TOOL_CACHE="+filepath.Join(data, "toolcache")) {
		t.Fatal("jobs should share one tool cache")
	}
	s.mu.Lock()
	jobs := s.slot[0].Jobs + s.slot[1].Jobs
	s.mu.Unlock()
	if jobs < 2 {
		t.Fatalf("job count: %d", jobs)
	}
	if entries, _ := os.ReadDir(filepath.Join(data, "slots")); len(entries) != 0 {
		t.Fatalf("job folders should be removed after each job: %d left", len(entries))
	}
}
