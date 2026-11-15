package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func newTest(t *testing.T) *server {
	d := t.TempDir()
	return &server{root: d + "/files", meta: d + "/meta", shares: d + "/shares", maxSize: 1 << 20, public: "https://files.example.com"}
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

func doUser(s *server, method, path, user, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-Nook-User", user)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
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
	if w := do(s, "GET", "/f/kinlog/android/kinlog.apk", "", ""); w.Code != 401 {
		t.Fatalf("download without a login or token: %d", w.Code)
	}
	if w := do(s, "GET", "/f/kinlog/android/kinlog.apk", "wordbag", ""); w.Code != 404 {
		t.Fatalf("another app's token downloading: %d", w.Code)
	}
	for _, p := range []string{"/f/kinlog/android/kinlog-1.4.0.apk", "/f/kinlog/android/kinlog.apk"} {
		w := doUser(s, "GET", p, "adam", "")
		if w.Code != 200 || w.Body.String() != apk || w.Header().Get("Content-Type") != "application/vnd.android.package-archive" ||
			!strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") || w.Header().Get("X-Checksum-SHA256") != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s: %d %v", p, w.Code, w.Header())
		}
	}
	// a resumed download
	r := httptest.NewRequest("GET", "/f/kinlog/android/kinlog.apk", nil)
	r.Header.Set("Range", "bytes=100-199")
	r.Header.Set("X-Nook-App", "kinlog")
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
	if w := do(s, "GET", "/f/kinlog/android/kinlog-1.4.0.apk", "kinlog", ""); w.Code != 404 {
		t.Fatal("deleted file still served")
	}
	if w := do(s, "GET", "/f/kinlog/android/kinlog.apk", "kinlog", ""); w.Code != 200 {
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
	if w := do(s, "GET", "/f/kinlog/../../etc/passwd", "kinlog", ""); w.Code != 404 {
		t.Errorf("escape on download: %d", w.Code)
	}
}

func TestHTMLCantRun(t *testing.T) {
	s := newTest(t)
	do(s, "PUT", "/v1/files/page.html", "kinlog", "<script>alert(1)</script>")
	do(s, "PUT", "/v1/files/shot.png", "kinlog", "\x89PNG")
	w := do(s, "GET", "/f/kinlog/page.html", "kinlog", "")
	if !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("html should download, sandboxed: %v", w.Header())
	}
	if w := do(s, "GET", "/f/kinlog/shot.png", "kinlog", ""); w.Header().Get("Content-Disposition") != "" {
		t.Fatal("an image can show inline")
	}
}

func TestShares(t *testing.T) {
	s := newTest(t)
	do(s, "PUT", "/v1/files/android/kinlog.apk", "kinlog", "APK")
	do(s, "PUT", "/v1/files/words.txt", "wordbag", "words")

	// a logged-in user shares any app's file; JSON only
	r := httptest.NewRequest("POST", "/v1/shares", strings.NewReader(`{"app":"kinlog","path":"android/kinlog.apk"}`))
	r.Header.Set("X-Nook-User", "adam")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatalf("a form post: %d", w.Code)
	}
	w = doUser(s, "POST", "/v1/shares", "adam", `{"app":"kinlog","path":"android/kinlog.apk","hours":24}`)
	var sh share
	json.Unmarshal(w.Body.Bytes(), &sh)
	if w.Code != 201 || !strings.HasPrefix(sh.URL, "https://files.example.com/s/") || sh.By != "adam" || time.Until(sh.Expires) > 24*time.Hour {
		t.Fatalf("share: %d %s", w.Code, w.Body)
	}
	id := strings.TrimPrefix(sh.URL, "https://files.example.com/s/")
	if w := do(s, "GET", "/s/"+id, "", ""); w.Code != 200 || w.Body.String() != "APK" || w.Header().Get("Content-Type") != "application/vnd.android.package-archive" {
		t.Fatalf("open share: %d %s", w.Code, w.Body)
	}
	if w := do(s, "GET", "/s/AAAAAAAAAAAAAAAAAAAAAA", "", ""); w.Code != 404 {
		t.Fatalf("a made-up link: %d", w.Code)
	}

	// limits and who
	for _, bad := range []string{`{"app":"kinlog","path":"android/kinlog.apk","hours":0.5}`, `{"app":"kinlog","path":"android/kinlog.apk","hours":721}`, `{"app":"kinlog","path":"nope.apk"}`, `{"path":"android/kinlog.apk"}`} {
		if w := doUser(s, "POST", "/v1/shares", "adam", bad); w.Code == 201 {
			t.Errorf("%s: shared", bad)
		}
	}
	r = httptest.NewRequest("POST", "/v1/shares", strings.NewReader(`{"app":"kinlog","path":"android/kinlog.apk"}`))
	r.Header.Set("X-Nook-App", "wordbag")
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("a token sharing another app's file: %d", w.Code)
	}
	r = httptest.NewRequest("POST", "/v1/shares", strings.NewReader(`{"path":"words.txt","hours":1}`))
	r.Header.Set("X-Nook-App", "wordbag")
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatalf("a token sharing its own file: %d %s", w.Code, w.Body)
	}

	// lists: a token sees its own app's links, a user every link
	if w := do(s, "GET", "/v1/shares", "wordbag", ""); strings.Contains(w.Body.String(), "kinlog") || !strings.Contains(w.Body.String(), "words.txt") {
		t.Fatalf("token list: %s", w.Body)
	}
	if w := doUser(s, "GET", "/v1/shares", "adam", ""); !strings.Contains(w.Body.String(), "kinlog") || !strings.Contains(w.Body.String(), "words.txt") {
		t.Fatalf("user list: %s", w.Body)
	}
	if w := do(s, "DELETE", "/v1/shares/"+id, "wordbag", ""); w.Code != 404 {
		t.Fatalf("a token removing another app's link: %d", w.Code)
	}

	// expired: gone with a 410, and off the list
	sh.Expires = time.Now().Add(-time.Minute)
	b, _ := json.Marshal(sh)
	os.WriteFile(s.sharePath(id), b, 0o600)
	if w := do(s, "GET", "/s/"+id, "", ""); w.Code != 410 {
		t.Fatalf("expired: %d", w.Code)
	}
	if w := doUser(s, "GET", "/v1/shares", "adam", ""); strings.Contains(w.Body.String(), "kinlog") {
		t.Fatal("expired link still listed")
	}

	// stop sharing
	w = doUser(s, "POST", "/v1/shares", "adam", `{"app":"kinlog","path":"android/kinlog.apk"}`)
	json.Unmarshal(w.Body.Bytes(), &sh)
	if time.Until(sh.Expires) < 6*24*time.Hour {
		t.Fatalf("default should be 7 days: %v", sh.Expires)
	}
	if w := doUser(s, "DELETE", "/v1/shares/"+sh.ID, "adam", ""); w.Code != 204 {
		t.Fatalf("stop sharing: %d", w.Code)
	}
	if w := do(s, "GET", "/s/"+sh.ID, "", ""); w.Code != 404 {
		t.Fatalf("after stop sharing: %d", w.Code)
	}
}

func TestUserListAndPage(t *testing.T) {
	s := newTest(t)
	do(s, "PUT", "/v1/files/a.apk", "kinlog", "A")
	do(s, "PUT", "/v1/files/b.txt", "wordbag", "B")
	var out struct{ Files []info }
	json.Unmarshal(doUser(s, "GET", "/v1/files", "adam", "").Body.Bytes(), &out)
	if len(out.Files) != 2 || out.Files[0].App == "" {
		t.Fatalf("user list: %+v", out)
	}
	if w := doUser(s, "GET", "/", "<b>adam</b>", ""); w.Code != 200 || strings.Contains(w.Body.String(), "<b>adam") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("page: %d %v", w.Code, w.Header())
	}
	if w := do(s, "GET", "/", "kinlog", ""); w.Code != 401 {
		t.Fatalf("page with a token: %d", w.Code)
	}
}
