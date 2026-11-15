// directory is a public page of the apps a nook server lists: each one's icon, name and description from its own
// web manifest, a page per app with its screenshots, and a link in. It reads the list from the dashboard's
// GET /api/v1/directory (sites are listed with "List in directory" on the dashboard), and is installable itself,
// so it works as a home-screen launcher too. No JavaScript.
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

//go:embed page.html style.css
var files embed.FS

type shot struct {
	Src        string `json:"src"`
	Label      string `json:"label"`
	FormFactor string `json:"formFactor"`
}

type site struct {
	Host        string `json:"host"`
	URL         string `json:"url"`
	Name        string `json:"name"`
	ShortName   string `json:"shortName"`
	Description string `json:"description"`
	Theme       string `json:"theme"`
	Background  string `json:"background"`
	Icon        string `json:"icon"`
	Screenshots []shot `json:"screenshots"`
	Installable bool   `json:"installable"`
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

var (
	colorRE = regexp.MustCompile(`^(#[0-9a-fA-F]{3,8}|[a-zA-Z]{3,20}|(rgb|rgba|hsl|hsla|oklch|oklab|lab|lch)\([0-9a-zA-Z.,%/ +-]{1,60}\))$`)
	iconRE  = regexp.MustCompile(`^[0-9a-f]{32}\.(png|jpg|webp|gif|ico|svg)$`)
)

type dir struct {
	source string // .../api/v1/directory
	client *http.Client

	mu    sync.Mutex
	sites []site
	ok    bool // has read the list at least once
	err   string
	icons map[string]icon
}

type icon struct {
	typ  string
	data []byte
}

func (d *dir) refresh() {
	resp, err := d.client.Get(d.source)
	if err == nil && resp.StatusCode != 200 {
		err = fmt.Errorf("%s answered %s", d.source, resp.Status)
		resp.Body.Close()
	}
	if err != nil {
		d.mu.Lock()
		d.err = err.Error()
		d.mu.Unlock()
		log.Print(err)
		return
	}
	defer resp.Body.Close()
	var body struct {
		Sites []site `json:"sites"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		log.Print(err)
		return
	}
	var clean []site
	for _, s := range body.Sites {
		u, err := url.Parse(s.URL)
		if err != nil || u.Scheme != "https" || u.Host != s.Host {
			continue
		}
		if !colorRE.MatchString(s.Theme) {
			s.Theme = ""
		}
		if !colorRE.MatchString(s.Background) {
			s.Background = ""
		}
		if !iconRE.MatchString(s.Icon) {
			s.Icon = ""
		}
		var shots []shot
		for _, sh := range s.Screenshots {
			if su, err := url.Parse(sh.Src); err == nil && su.Scheme == "https" && su.Host == s.Host {
				shots = append(shots, sh)
			}
		}
		s.Screenshots = shots
		clean = append(clean, s)
	}
	d.mu.Lock()
	d.sites, d.ok, d.err = clean, true, ""
	d.mu.Unlock()
}

// find returns the listed site for host and the one after it in the list (wrapping round), or nil next when it's the only one.
func (d *dir) find(host string) (site, *site, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, s := range d.sites {
		if s.Host == host {
			if len(d.sites) < 2 {
				return s, nil, true
			}
			n := d.sites[(i+1)%len(d.sites)]
			return s, &n, true
		}
	}
	return site{}, nil, false
}

// iconFor proxies a listed site's icon from the dashboard, kept in memory: this page's own origin serves it.
func (d *dir) iconFor(name string) (icon, bool) {
	d.mu.Lock()
	if ic, ok := d.icons[name]; ok {
		d.mu.Unlock()
		return ic, true
	}
	listed := false
	for _, s := range d.sites {
		listed = listed || s.Icon == name
	}
	d.mu.Unlock()
	if !listed {
		return icon{}, false
	}
	resp, err := d.client.Get(d.source + "/icon/" + name)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return icon{}, false
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return icon{}, false
	}
	typ := map[string]string{"png": "image/png", "jpg": "image/jpeg", "webp": "image/webp", "gif": "image/gif", "ico": "image/x-icon", "svg": "image/svg+xml"}[name[strings.LastIndex(name, ".")+1:]]
	ic := icon{typ, b}
	d.mu.Lock()
	if len(d.icons) > 500 {
		d.icons = map[string]icon{}
	}
	d.icons[name] = ic
	d.mu.Unlock()
	return ic, true
}

// onColor is the text color for a button filled with c: whichever of near-black and white has more contrast on it
// (WCAG), and white for colors that aren't hex, which can't be measured here.
func onColor(c string) template.CSS {
	h := strings.TrimPrefix(c, "#")
	if len(h) == 3 || len(h) == 4 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) < 6 || !strings.HasPrefix(c, "#") {
		return "#fff"
	}
	var rgb [3]float64
	for i := range rgb {
		var v uint8
		fmt.Sscanf(h[i*2:i*2+2], "%02x", &v)
		x := float64(v) / 255
		if x <= 0.04045 {
			rgb[i] = x / 12.92
		} else {
			rgb[i] = math.Pow((x+0.055)/1.055, 2.4)
		}
	}
	l := 0.2126*rgb[0] + 0.7152*rgb[1] + 0.0722*rgb[2]
	const dark = 0.0056 // #111
	if (l+0.05)/(dark+0.05) > 1.05/(l+0.05) {
		return "#111"
	}
	return "#fff"
}

type view struct {
	Title, Intro string
	Sites        []site
	Site         *site
	Next         *site // the app after Site, wrapping round; nil when it's the only one
	Error        string
}

type iconView struct {
	S   site
	Big bool
}

func main() {
	source := env("DIRECTORY_SOURCE", "")
	if source == "" {
		log.Fatal("DIRECTORY_SOURCE isn't set: the dashboard's list, like https://admin.example.com/api/v1/directory")
	}
	d := &dir{source: strings.TrimRight(source, "/"), client: &http.Client{Timeout: 15 * time.Second}, icons: map[string]icon{}}
	title, intro := env("DIRECTORY_TITLE", "Apps"), env("DIRECTORY_INTRO", "")
	go func() {
		for {
			d.refresh()
			time.Sleep(time.Minute)
		}
	}()
	mux := handler(d, title, intro)
	addr := "127.0.0.1:" + env("PORT", "8080")
	log.Printf("directory on %s, reading %s", addr, d.source)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func handler(d *dir, title, intro string) *http.ServeMux {
	tpl := template.Must(template.New("page.html").Funcs(template.FuncMap{
		"initial": func(s string) string {
			r := []rune(s)
			if len(r) == 0 {
				return "?"
			}
			return strings.ToUpper(string(r[0]))
		},
		"css":     func(s string) template.CSS { return template.CSS(s) }, // colors are checked against colorRE first
		"onColor": onColor,
		// the picture on a card and in an app's phone frame: its first narrow screenshot, else its first
		"hero": func(s site) *shot {
			for i := range s.Screenshots {
				if s.Screenshots[i].FormFactor == "narrow" {
					return &s.Screenshots[i]
				}
			}
			if len(s.Screenshots) > 0 {
				return &s.Screenshots[0]
			}
			return nil
		},
		"iconOf": func(s site, big bool) iconView { return iconView{S: s, Big: big} },
	}).ParseFS(files, "page.html"))
	css, _ := files.ReadFile("style.css")

	render := func(w http.ResponseWriter, status int, v view) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self' https:; style-src 'self' 'unsafe-inline'; manifest-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.WriteHeader(status)
		if err := tpl.Execute(w, v); err != nil {
			log.Print(err)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		v := view{Title: title, Intro: intro, Sites: d.sites}
		if !d.ok {
			v.Error = "The list isn't available yet."
		}
		d.mu.Unlock()
		render(w, 200, v)
	})
	mux.HandleFunc("GET /a/{host}", func(w http.ResponseWriter, r *http.Request) {
		s, next, ok := d.find(r.PathValue("host"))
		if !ok {
			render(w, 404, view{Title: title, Error: "That app isn't listed here."})
			return
		}
		render(w, 200, view{Title: title, Site: &s, Next: next})
	})
	mux.HandleFunc("GET /icon/{name}", func(w http.ResponseWriter, r *http.Request) {
		ic, ok := d.iconFor(r.PathValue("name"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ic.typ)
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
		w.Write(ic.data)
	})
	mux.HandleFunc("GET /style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Write(css)
	})
	mux.HandleFunc("GET /manifest.webmanifest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json")
		json.NewEncoder(w).Encode(map[string]any{
			"name": title, "short_name": title, "start_url": "/", "display": "standalone",
			"background_color": "#f4f1ec", "theme_color": "#f4f1ec",
			"icons": []map[string]string{{"src": "/app-icon.svg", "sizes": "any", "type": "image/svg+xml", "purpose": "any"}},
		})
	})
	mux.HandleFunc("GET /app-icon.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		// four rounded tiles: a grid of apps
		io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="#f4f1ec"/><g fill="#3b4f9a"><rect x="12" y="12" width="17" height="17" rx="5"/><rect x="35" y="12" width="17" height="17" rx="5"/><rect x="12" y="35" width="17" height="17" rx="5"/></g><rect x="35" y="35" width="17" height="17" rx="5" fill="#e0a43a"/></svg>`)
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		ok, err, n := d.ok, d.err, len(d.sites)
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(503)
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": ok, "sites": n, "error": err})
	})
	return mux
}
