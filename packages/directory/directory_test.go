package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeNook(t *testing.T, sites []map[string]any) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/directory":
			json.NewEncoder(w).Encode(map[string]any{"sites": sites})
		case "/api/v1/directory/icon/0123456789abcdef0123456789abcdef.png":
			w.Write([]byte("\x89PNG"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRefreshCleans(t *testing.T) {
	srv := fakeNook(t, []map[string]any{
		{"host": "a.example.com", "url": "https://a.example.com/", "name": "A", "theme": "red;}body{display:none", "background": "#fff",
			"icon": "../../etc/passwd", "screenshots": []map[string]string{{"src": "https://a.example.com/s.png"}, {"src": "https://evil.example/x.png"}}},
		{"host": "b.example.com", "url": "javascript:alert(1)", "name": "B"},
		{"host": "c.example.com", "url": "https://other.example.com/", "name": "C"},
	})
	d := &dir{source: srv.URL + "/api/v1/directory", client: srv.Client(), icons: map[string]icon{}}
	d.refresh()
	if len(d.sites) != 1 {
		t.Fatalf("only a site whose URL is its own https host stays: %+v", d.sites)
	}
	a := d.sites[0]
	if a.Theme != "" || a.Background != "#fff" || a.Icon != "" || len(a.Screenshots) != 1 {
		t.Fatalf("cleaning: %+v", a)
	}
}

func TestIconOnlyForListed(t *testing.T) {
	srv := fakeNook(t, []map[string]any{{"host": "a.example.com", "url": "https://a.example.com/", "name": "A", "icon": "0123456789abcdef0123456789abcdef.png"}})
	d := &dir{source: srv.URL + "/api/v1/directory", client: srv.Client(), icons: map[string]icon{}}
	d.refresh()
	if ic, ok := d.iconFor("0123456789abcdef0123456789abcdef.png"); !ok || ic.typ != "image/png" {
		t.Fatal("a listed icon should come through")
	}
	if _, ok := d.iconFor("ffffffffffffffffffffffffffffffff.png"); ok {
		t.Fatal("an icon no listed site has shouldn't be fetched")
	}
}

func TestOnColor(t *testing.T) {
	for c, want := range map[string]string{"#ffd166": "#111", "#fff": "#111", "#3d6b4f": "#fff", "#8a3ffc": "#fff", "oklch(0.9 0.1 90)": "#fff"} {
		if got := string(onColor(c)); got != want {
			t.Errorf("%s: %s, want %s", c, got, want)
		}
	}
}

func TestPageEscapes(t *testing.T) {
	srv := fakeNook(t, []map[string]any{{"host": "a.example.com", "url": "https://a.example.com/", "name": `<script>alert(1)</script>`, "description": `"onmouseover="x`, "theme": "#3d6b4f"}})
	t.Setenv("DIRECTORY_SOURCE", srv.URL+"/api/v1/directory")
	d := &dir{source: srv.URL + "/api/v1/directory", client: srv.Client(), icons: map[string]icon{}}
	d.refresh()
	h := handler(d, "Apps", "")
	for _, path := range []string{"/", "/a/a.example.com"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		body := w.Body.String()
		if w.Code != 200 || strings.Contains(body, "<script>alert") || strings.Contains(body, `"onmouseover="`) {
			t.Fatalf("%s: %d\n%s", path, w.Code, body)
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") {
			t.Fatalf("%s: no CSP", path)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/a/not-listed.example.com", nil))
	if w.Code != 404 {
		t.Fatalf("unlisted app page: %d", w.Code)
	}
}
