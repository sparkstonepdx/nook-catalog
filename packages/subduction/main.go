// Subduction for nook: runs the Subduction sync server (embedded at release, bin/subduction_cli) on a local port
// and stands in front of it on the app's site. Sync traffic (WebSocket upgrades and /lp/ long polls) goes straight
// through; a plain visit to / gets a page with what apps need to sync through this server with Keyhive: its
// address, peer id and contact card, each ready to copy. /identity.json has the same, /health says it's up.
//
// Data in ~/data/subduction, the server's identity key in ~/data/subduction.key (made on first start; back it up).
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed bin
var bin embed.FS

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// install writes the embedded server to ~/bin/subduction_cli when it isn't there already, and returns its path.
func install(home string) (string, error) {
	b, err := fs.ReadFile(bin, "bin/subduction_cli")
	if err != nil {
		return "", errors.New("this build has no Subduction server in it (bin/subduction_cli); the release build adds it")
	}
	p := filepath.Join(home, "bin", "subduction_cli")
	if old, err := os.ReadFile(p); err == nil && sha256.Sum256(old) == sha256.Sum256(b) {
		return p, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	tmp := p + ".new"
	if err := os.WriteFile(tmp, b, 0o755); err != nil {
		return "", err
	}
	return p, os.Rename(tmp, p)
}

// freePort is a local port nothing is listening on, for the server behind this one. nook only lets an app listen
// on its own port unless its entry says "listen": "any" (apps/subduction.json does), which allows this one too.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// isSync: a request for the server behind, not the page.
func isSync(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || strings.HasPrefix(r.URL.Path, "/lp/")
}

func main() {
	home := env("HOME", ".")
	data := filepath.Join(home, "data")
	host := env("SITE_HOST", "localhost")
	keyFile := filepath.Join(data, "subduction.key")
	if err := os.MkdirAll(filepath.Join(data, "subduction"), 0o700); err != nil {
		log.Fatal(err)
	}
	if err := ensureKey(keyFile); err != nil {
		log.Fatal(err)
	}
	cli, err := install(home)
	if err != nil {
		log.Fatal(err)
	}
	inner, err := freePort()
	if err != nil {
		log.Fatalf("no local port for the server behind this one (does the catalog entry still say \"listen\": \"any\"?): %v", err)
	}
	cmd := exec.Command(cli, "server", "--socket", "127.0.0.1:"+strconv.Itoa(inner), "--data-dir", filepath.Join(data, "subduction"),
		"--key-file", keyFile, "--service-name", host)
	// glibc otherwise keeps freed memory (tested 277 MB -> 19 MB after storing 135 MB of files)
	cmd.Env = append(os.Environ(), "GLIBC_TUNABLES=glibc.malloc.arena_max=2:glibc.malloc.trim_threshold=131072:glibc.malloc.mmap_threshold=131072",
		"RUST_LOG="+env("RUST_LOG", "info"))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	// the server's end is this program's end (nook restarts it), and a stop stops the server too
	go func() {
		err := cmd.Wait()
		log.Printf("subduction_cli stopped: %v", err)
		os.Exit(1)
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-sig
		cmd.Process.Signal(s)
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}()

	target, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(inner))
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1 // long polls and streams pass through as they come
	page := &identityPage{keyFile: keyFile, cardFile: filepath.Join(data, "subduction-card.json"), host: host}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isSync(r):
			proxy.ServeHTTP(w, r)
		case r.URL.Path == "/health":
			if c, err := net.DialTimeout("tcp", target.Host, time.Second); err == nil {
				c.Close()
				w.Write([]byte("ok\n"))
				return
			}
			http.Error(w, "the server isn't answering yet", http.StatusServiceUnavailable)
		case r.URL.Path == "/identity.json":
			page.json(w)
		case r.URL.Path == "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
			page.html(w)
		case r.URL.Path == "/page.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Write([]byte(pageJS))
		default:
			proxy.ServeHTTP(w, r) // anything else is the server's to answer
		}
	})
	addr := "127.0.0.1:" + env("PORT", "8080")
	log.Printf("subduction on %s for wss://%s, server behind on %d", addr, host, inner)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// ensureKey makes the server's identity key (32 random bytes as hex) when there isn't one.
func ensureKey(p string) error {
	if b, err := os.ReadFile(p); err == nil && len(bytes.TrimSpace(b)) > 0 {
		return nil
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(hex.EncodeToString(seed)), 0o600)
}
