package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTest(t *testing.T) *server {
	d := t.TempDir()
	return &server{root: d + "/files", meta: d + "/meta", maxSize: 1 << 20, public: "https://files.example.com"}
}

func do(s *server, method, path, app, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if app != "" {
		r.Header.Set("X-Nook-App", app)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestUploadDownload(t *testing.T) {
	s := newTest(t)
	apk := strings.Repeat("PK\x03\x04apk", 1000)
	if w := do(s, "PUT", "/v1/files/android/kinlog-1.4.0.apk", "", apk); w.Code != 401 {
		t.Fatalf("upload without nook's X-Nook-App: %d", w.Code)
	}
	w := do(s, "PUT", "/v1/files/android/kinlog-1.4.0.apk?alias=kinlog.apk", "kinlog", apk)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	sum := sha256.Sum256([]byte(apk))
	if w.Code != 201 || out["url"] != "https://files.example.com/f/kinlog/android/kinlog-1.4.0.apk" || out["sha256"] != hex.EncodeToString(sum[:]) || out["alias"] != "https://files.example.com/f/kinlog/android/kinlog.apk" {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	for _, p := range []string{"/f/kinlog/android/kinlog-1.4.0.apk", "/f/kinlog/android/kinlog.apk"} {
		w := do(s, "GET", p, "", "")
		if w.Code != 200 || w.Body.String() != apk || w.Header().Get("Content-Type") != "application/vnd.android.package-archive" ||
			!strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") || w.Header().Get("X-Checksum-SHA256") != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s: %d %v", p, w.Code, w.Header())
		}
	}
	// a resumed download
	r := httptest.NewRequest("GET", "/f/kinlog/android/kinlog.apk", nil)
	r.Header.Set("Range", "bytes=100-199")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != apk[100:200] {
		t.Fatalf("range: %d", w.Code)
	}
	// list and delete are the app's own
	w = do(s, "GET", "/v1/files", "kinlog", "")
	if !strings.Contains(w.Body.String(), "kinlog-1.4.0.apk") {
		t.Fatalf("list: %s", w.Body)
	}
	if w := do(s, "GET", "/v1/files", "wordbag", ""); strings.Contains(w.Body.String(), "kinlog") {
		t.Fatal("another app can see kinlog's files in its list")
	}
	if w := do(s, "DELETE", "/v1/files/android/kinlog-1.4.0.apk", "wordbag", ""); w.Code != 404 {
		t.Fatalf("another app deleting: %d", w.Code)
	}
	if w := do(s, "DELETE", "/v1/files/android/kinlog-1.4.0.apk", "kinlog", ""); w.Code != 204 {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := do(s, "GET", "/f/kinlog/android/kinlog-1.4.0.apk", "", ""); w.Code != 404 {
		t.Fatal("deleted file still served")
	}
	if w := do(s, "GET", "/f/kinlog/android/kinlog.apk", "", ""); w.Code != 200 {
		t.Fatal("the alias is its own link and should stay")
	}
}

func TestPathsAndLimits(t *testing.T) {
	s := newTest(t)
	for _, p := range []string{"/v1/files/../x", "/v1/files/.env", "/v1/files/a/.hidden/x", "/v1/files/a//b", "/v1/files/a%20b"} {
		if w := do(s, "PUT", p, "kinlog", "x"); w.Code != 400 && w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	if w := do(s, "PUT", "/v1/files/big.bin", "kinlog", strings.Repeat("x", 2<<20)); w.Code != 413 {
		t.Errorf("over the limit: %d", w.Code)
	}
	if w := do(s, "GET", "/f/kinlog/../../etc/passwd", "", ""); w.Code != 404 {
		t.Errorf("escape on download: %d", w.Code)
	}
}

func TestHTMLCantRun(t *testing.T) {
	s := newTest(t)
	do(s, "PUT", "/v1/files/page.html", "kinlog", "<script>alert(1)</script>")
	do(s, "PUT", "/v1/files/shot.png", "kinlog", "\x89PNG")
	w := do(s, "GET", "/f/kinlog/page.html", "", "")
	if !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("html should download, sandboxed: %v", w.Header())
	}
	if w := do(s, "GET", "/f/kinlog/shot.png", "", ""); w.Header().Get("Content-Disposition") != "" {
		t.Fatal("an image can show inline")
	}
}
