package main

// Everything is files in data/forms/ID/: form.json, and responses.jsonl with one response a line. Backups get
// them as they are, readable with anything.

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type store struct {
	dir string
	mu  sync.Mutex
}

var errNoForm = errors.New("no such form")

func (s *store) formDir(id string) string { return filepath.Join(s.dir, "forms", id) }

func (s *store) get(id string) (Form, error) {
	var f Form
	if !idRE.MatchString(id) {
		return f, errNoForm
	}
	b, err := os.ReadFile(filepath.Join(s.formDir(id), "form.json"))
	if err != nil {
		return f, errNoForm
	}
	return f, json.Unmarshal(b, &f)
}

func (s *store) list() []Form {
	es, _ := os.ReadDir(filepath.Join(s.dir, "forms"))
	var out []Form
	for _, e := range es {
		if f, err := s.get(e.Name()); err == nil {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated > out[j].Updated })
	return out
}

// save writes the form whole, atomically.
func (s *store) save(f Form) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	if f.Created == "" {
		f.Created = now
	}
	f.Updated = now
	if err := os.MkdirAll(s.formDir(f.ID), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	tmp := filepath.Join(s.formDir(f.ID), ".form.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.formDir(f.ID), "form.json"))
}

func (s *store) remove(id string) error {
	if !idRE.MatchString(id) {
		return errNoForm
	}
	return os.RemoveAll(s.formDir(id))
}

func (s *store) add(id string, r Response) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(s.formDir(id), "responses.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(r)
	_, err = f.Write(append(b, '\n'))
	return err
}

// responses is every response, newest first.
func (s *store) responses(id string) []Response {
	f, err := os.Open(filepath.Join(s.formDir(id), "responses.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Response
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var r Response
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			out = append(out, r)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (s *store) count(id string) int { return len(s.responses(id)) }

// removeResponse rewrites the file without one response.
func (s *store) removeResponse(id, rid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.responses(id)
	tmp := filepath.Join(s.formDir(id), ".responses.tmp")
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i].ID != rid {
			b, _ := json.Marshal(rs[i])
			f.Write(append(b, '\n'))
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.formDir(id), "responses.jsonl"))
}
