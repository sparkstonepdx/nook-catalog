package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Everything lives in files under data/: what each site's last publish declared, and one JSON line per submission,
// so backups have it and any tool can read it.
//
//	data/sites/HOST.json                 the site's forms, from its last publish
//	data/forms/HOST/FORM.jsonl           its submissions, oldest first
//	data/forms/HOST/FORM/files/ID/NAME   uploaded files

type siteForms struct {
	App       string `json:"app"`
	Site      string `json:"site"`
	Host      string `json:"host"`
	Forms     []Form `json:"forms"`
	Published string `json:"published"`
}

type upload struct {
	Field string `json:"field"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
}

type submission struct {
	ID     string              `json:"id"`
	At     string              `json:"at"`
	Page   string              `json:"page,omitempty"`
	Fields map[string][]string `json:"fields"`
	Files  []upload            `json:"files,omitempty"`
}

type store struct {
	dir string
	mu  sync.Mutex
}

var hostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

func safeHost(h string) bool { return hostRE.MatchString(h) && !strings.Contains(h, "..") }

// safeName turns a form or file name into one path segment.
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 || r < 32 {
			return '_'
		}
		return r
	}, strings.TrimSpace(s))
	s = strings.Trim(s, ".")
	if s == "" {
		return "_"
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

func (s *store) sitePath(host string) string { return filepath.Join(s.dir, "sites", host+".json") }

func (s *store) site(host string) (siteForms, bool) {
	var sf siteForms
	b, err := os.ReadFile(s.sitePath(host))
	if err != nil || json.Unmarshal(b, &sf) != nil {
		return sf, false
	}
	return sf, true
}

func (s *store) saveSite(sf siteForms) error {
	os.MkdirAll(filepath.Join(s.dir, "sites"), 0o700)
	b, _ := json.MarshalIndent(sf, "", "  ")
	tmp := s.sitePath(sf.Host) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.sitePath(sf.Host))
}

func (s *store) sites() []siteForms {
	m, _ := filepath.Glob(filepath.Join(s.dir, "sites", "*.json"))
	var out []siteForms
	for _, f := range m {
		if sf, ok := s.site(strings.TrimSuffix(filepath.Base(f), ".json")); ok {
			out = append(out, sf)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

func (s *store) formDir(host, form string) string {
	return filepath.Join(s.dir, "forms", host, safeName(form))
}
func (s *store) logPath(host, form string) string {
	return filepath.Join(s.dir, "forms", host, safeName(form)+".jsonl")
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

func (s *store) add(host, form string, sub submission) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	os.MkdirAll(filepath.Join(s.dir, "forms", host), 0o700)
	f, err := os.OpenFile(s.logPath(host, form), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(sub)
	_, err = f.Write(append(b, '\n'))
	return err
}

// list is a form's submissions, newest first.
func (s *store) list(host, form string) []submission {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.logPath(host, form))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []submission
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var sub submission
		if json.Unmarshal(sc.Bytes(), &sub) == nil {
			out = append(out, sub)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (s *store) count(host, form string) int { return len(s.list(host, form)) }

// remove drops one submission (and its files), rewriting the file without it.
func (s *store) remove(host, form, id string) error {
	subs := s.list(host, form)
	s.mu.Lock()
	defer s.mu.Unlock()
	var keep []string
	for i := len(subs) - 1; i >= 0; i-- {
		if subs[i].ID == id {
			continue
		}
		b, _ := json.Marshal(subs[i])
		keep = append(keep, string(b))
	}
	p := s.logPath(host, form)
	text := strings.Join(keep, "\n")
	if text != "" {
		text += "\n"
	}
	if err := os.WriteFile(p+".tmp", []byte(text), 0o600); err != nil {
		return err
	}
	os.RemoveAll(filepath.Join(s.formDir(host, form), "files", safeName(id)))
	return os.Rename(p+".tmp", p)
}
