package main

// GitHub's API, the four calls runners need: a just-in-time config per runner (it registers the runner for one job;
// nothing to configure, nothing left to unregister if it never connects), listing and deleting our runners, and the
// latest runner release.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type scope struct {
	Org  string // set for an organization
	Repo string // owner/name for one repository
}

var (
	orgRE  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	repoRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$`)
)

// parseScope takes "sparkstonepdx" (an organization), "sparkstonepdx/wordbag" (a repository), or either as a
// github.com URL.
func parseScope(s string) (scope, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "github.com/")
	s = strings.Trim(s, "/")
	switch {
	case repoRE.MatchString(s):
		return scope{Repo: s}, nil
	case orgRE.MatchString(s):
		return scope{Org: s}, nil
	}
	return scope{}, fmt.Errorf("GITHUB_SCOPE should be an organization (sparkstonepdx) or a repository (sparkstonepdx/wordbag), not %q", s)
}

func (s scope) path() string {
	if s.Repo != "" {
		return "/repos/" + s.Repo + "/actions/runners"
	}
	return "/orgs/" + s.Org + "/actions/runners"
}

func (s scope) String() string { return s.Org + s.Repo }

type github struct {
	api, token string
	client     *http.Client
}

type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string { return fmt.Sprintf("GitHub answered %d: %s", e.Status, e.Msg) }

func (g *github) do(method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, g.api+path, rd)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "nook-runners")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		json.Unmarshal(b, &e)
		msg := e.Message
		if msg == "" {
			msg = strings.TrimSpace(string(b))
		}
		return &apiError{resp.StatusCode, msg}
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

type registered struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Busy   bool   `json:"busy"`
}

// jitConfig registers a runner for one job and returns its id and the config run.sh takes.
func (g *github) jitConfig(s scope, name string, labels []string, group int) (int64, string, error) {
	body := map[string]any{"name": name, "labels": labels, "work_folder": "_work"}
	if s.Org != "" {
		body["runner_group_id"] = group
	} else {
		body["runner_group_id"] = 1 // a repository's runners are in the default group
	}
	var out struct {
		Runner registered `json:"runner"`
		Config string     `json:"encoded_jit_config"`
	}
	if err := g.do("POST", s.path()+"/generate-jitconfig", body, &out); err != nil {
		return 0, "", err
	}
	return out.Runner.ID, out.Config, nil
}

func (g *github) remove(s scope, id int64) error {
	return g.do("DELETE", fmt.Sprintf("%s/%d", s.path(), id), nil, nil)
}

// ours lists registered runners whose names start with prefix.
func (g *github) ours(s scope, prefix string) ([]registered, error) {
	var out []registered
	for page := 1; page <= 10; page++ {
		var r struct {
			Runners []registered `json:"runners"`
		}
		if err := g.do("GET", fmt.Sprintf("%s?per_page=100&page=%d", s.path(), page), nil, &r); err != nil {
			return out, err
		}
		for _, x := range r.Runners {
			if strings.HasPrefix(x.Name, prefix) {
				out = append(out, x)
			}
		}
		if len(r.Runners) < 100 {
			break
		}
	}
	return out, nil
}

type runnerRelease struct {
	Tag    string
	URL    string
	SHA256 string // hex
}

var shaInNotesRE = func(platform string) *regexp.Regexp {
	return regexp.MustCompile(`BEGIN SHA ` + regexp.QuoteMeta(platform) + ` -->\s*([0-9a-f]{64})\s*<!--`)
}

// runnerRelease finds actions/runner's latest release (or tag) for platform (linux-x64, linux-arm64) and its
// checksum: the asset's digest when GitHub gives one, or the SHA the release notes list.
func (g *github) runnerRelease(tag, platform string) (runnerRelease, error) {
	path := "/repos/actions/runner/releases/latest"
	if tag != "" {
		path = "/repos/actions/runner/releases/tags/" + tag
	}
	var r struct {
		Tag    string `json:"tag_name"`
		Body   string `json:"body"`
		Assets []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := g.do("GET", path, nil, &r); err != nil {
		return runnerRelease{}, err
	}
	want := regexp.MustCompile(`^actions-runner-` + regexp.QuoteMeta(platform) + `-[0-9.]+\.tar\.gz$`)
	for _, a := range r.Assets {
		if !want.MatchString(a.Name) {
			continue
		}
		rel := runnerRelease{Tag: r.Tag, URL: a.URL}
		if d, ok := strings.CutPrefix(a.Digest, "sha256:"); ok {
			rel.SHA256 = d
		} else if m := shaInNotesRE(platform).FindStringSubmatch(r.Body); m != nil {
			rel.SHA256 = m[1]
		}
		return rel, nil
	}
	return runnerRelease{}, fmt.Errorf("actions/runner %s has no %s download", r.Tag, platform)
}

var httpClient = &http.Client{Timeout: 10 * time.Minute}
