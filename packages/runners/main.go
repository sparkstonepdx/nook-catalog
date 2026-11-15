// runners keeps a set of GitHub Actions runners registered for an organization or a repository, each for one job:
// a runner registers with a just-in-time config, runs one job in a fresh folder, and is replaced. Runs as a nook
// service, so jobs run as the app's own user in its sandbox: no Docker, no root. Fine for Node, Go, npm, Playwright
// and the setup-* actions; not for container jobs or apt.
//
// A self-hosted runner runs whatever a workflow gives it, so use it for your own repositories, not public ones that
// take pull requests from forks.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type config struct {
	Scope   scope
	Token   string
	Count   int
	Labels  []string
	Group   int
	Version string // pin actions/runner to a tag, like v2.329.0
	SHA256  string // the download's checksum, when GitHub's release doesn't list one
	Data    string
	Prefix  string // registered names start with this, so leftovers can be found and removed
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func loadConfig() (config, error) {
	c := config{Token: env("GITHUB_TOKEN", ""), Version: env("RUNNER_VERSION", ""), SHA256: strings.ToLower(env("RUNNER_SHA256", ""))}
	var err error
	if c.Scope, err = parseScope(env("GITHUB_SCOPE", "")); err != nil {
		return c, err
	}
	if c.Token == "" {
		return c, errors.New("GITHUB_TOKEN isn't set: a fine-grained token with Self-hosted runners (organization) or Administration (repository), read and write")
	}
	if c.Count, err = strconv.Atoi(env("RUNNERS", "2")); err != nil || c.Count < 1 || c.Count > 16 {
		return c, errors.New("RUNNERS is how many run at once, 1 to 16")
	}
	if c.Group, err = strconv.Atoi(env("RUNNER_GROUP", "1")); err != nil {
		return c, errors.New("RUNNER_GROUP is a runner group id (1 is Default)")
	}
	arch := map[string]string{"amd64": "X64", "arm64": "ARM64"}[runtime.GOARCH]
	c.Labels = []string{"self-hosted", "Linux", arch}
	for _, l := range strings.Split(env("RUNNER_LABELS", "nook"), ",") {
		if l = strings.TrimSpace(l); l != "" {
			c.Labels = append(c.Labels, l)
		}
	}
	host, _ := os.Hostname()
	c.Prefix = "nook-" + strings.Split(host, ".")[0] + "-" + env("SITE", "runners") + "-"
	c.Data = env("RUNNERS_DATA", filepath.Join(env("HOME", "."), "data", "runners"))
	return c, nil
}

func platform() string {
	return "linux-" + map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
}

// slot is one runner place: what it's doing, for /health.
type slot struct {
	N     int       `json:"slot"`
	State string    `json:"state"` // starting, waiting, running, cleaning, error
	Name  string    `json:"name,omitempty"`
	Job   string    `json:"job,omitempty"`
	Since time.Time `json:"since"`
	Jobs  int       `json:"jobs"`
	Error string    `json:"error,omitempty"`
}

type supervisor struct {
	cfg  config
	gh   *github
	mu   sync.Mutex
	base string // the unpacked runner
	ver  string
	err  string
	slot []*slot
	ids  map[int]int64 // slot -> registered runner id, removed on shutdown
	pgid map[int]int
}

func (s *supervisor) set(n int, f func(*slot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s.slot[n])
}

// prepare makes sure the right runner version is unpacked; it checks for a newer one at most daily.
func (s *supervisor) prepare() error {
	rel, err := s.gh.runnerRelease(s.cfg.Version, platform())
	if err != nil {
		return fmt.Errorf("finding the runner: %v", err)
	}
	if s.cfg.SHA256 != "" {
		rel.SHA256 = s.cfg.SHA256
	}
	dir := filepath.Join(s.cfg.Data, "runner", rel.Tag)
	if _, err := os.Stat(filepath.Join(dir, "run.sh")); err != nil {
		archive := filepath.Join(s.cfg.Data, "dist", rel.Tag+".tar.gz")
		if err := download(httpClient, rel.URL, rel.SHA256, archive); err != nil {
			return err
		}
		if err := unpack(archive, dir); err != nil {
			return err
		}
		log.Printf("runner %s ready", rel.Tag)
	}
	s.mu.Lock()
	old := s.base
	s.base, s.ver = dir, rel.Tag
	s.mu.Unlock()
	if old != "" && old != dir { // jobs already running keep their own copies
		os.RemoveAll(old)
	}
	return nil
}

// removeLeftovers deletes our runners that are registered but offline: from a crash or a restart.
func (s *supervisor) removeLeftovers() {
	rs, err := s.gh.ours(s.cfg.Scope, s.cfg.Prefix)
	if err != nil {
		log.Printf("listing runners: %v", err)
		return
	}
	for _, r := range rs {
		if r.Status == "offline" && !r.Busy {
			if err := s.gh.remove(s.cfg.Scope, r.ID); err == nil {
				log.Printf("removed leftover runner %s", r.Name)
			}
		}
	}
}

func randomSuffix() string {
	b := make([]byte, 3)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// jobEnv is all a job sees: not GITHUB_TOKEN or anything else the supervisor was given.
func (s *supervisor) jobEnv() []string {
	keep := []string{"HOME", "USER", "LANG", "TZ", "SITE", "SITE_HOST", "HOST"}
	out := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"RUNNER_TOOL_CACHE=" + filepath.Join(s.cfg.Data, "toolcache"), // shared, so setup-node and friends download once
		"DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1",                     // the runner without libicu
	}
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// runSlot keeps slot n busy: fresh folder, register, run one job, clean up, again.
func (s *supervisor) runSlot(ctx context.Context, n int) {
	backoff := 30 * time.Second
	for ctx.Err() == nil {
		err := s.oneJob(ctx, n)
		if err == nil {
			backoff = 30 * time.Second
			continue
		}
		if ctx.Err() != nil {
			return
		}
		log.Printf("slot %d: %v (trying again in %s)", n, err, backoff)
		s.set(n, func(x *slot) { x.State, x.Error, x.Since = "error", err.Error(), time.Now() })
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 10*time.Minute)
	}
}

var jobLineRE = strings.NewReplacer("\r", "")

func (s *supervisor) oneJob(ctx context.Context, n int) error {
	s.mu.Lock()
	base := s.base
	s.mu.Unlock()
	dir := filepath.Join(s.cfg.Data, "slots", strconv.Itoa(n))
	s.set(n, func(x *slot) { x.State, x.Since, x.Error, x.Job = "starting", time.Now(), "", "" })
	if err := freshCopy(base, dir); err != nil {
		return fmt.Errorf("making its folder: %v", err)
	}
	name := s.cfg.Prefix + strconv.Itoa(n) + "-" + randomSuffix()
	id, jit, err := s.gh.jitConfig(s.cfg.Scope, name, s.cfg.Labels, s.cfg.Group)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) && (ae.Status == 401 || ae.Status == 403 || ae.Status == 404) {
			return fmt.Errorf("registering: %v (check GITHUB_SCOPE and that GITHUB_TOKEN can manage its self-hosted runners)", err)
		}
		return fmt.Errorf("registering: %v", err)
	}
	s.mu.Lock()
	s.ids[n] = id
	s.mu.Unlock()
	s.set(n, func(x *slot) { x.State, x.Name, x.Since = "waiting", name, time.Now() })

	cmd := exec.Command(filepath.Join(dir, "run.sh"), "--jitconfig", jit)
	cmd.Dir, cmd.Env = dir, s.jobEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		s.gh.remove(s.cfg.Scope, id)
		return fmt.Errorf("starting the runner: %v", err)
	}
	s.mu.Lock()
	s.pgid[n] = cmd.Process.Pid
	s.mu.Unlock()
	s.follow(n, out)
	werr := cmd.Wait()
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // anything a job left running
	s.mu.Lock()
	delete(s.pgid, n)
	delete(s.ids, n)
	s.mu.Unlock()

	s.set(n, func(x *slot) { x.State, x.Since = "cleaning", time.Now() })
	if err := s.gh.remove(s.cfg.Scope, id); err != nil { // usually gone already: a one-job runner removes itself
		var ae *apiError
		if !errors.As(err, &ae) || ae.Status != 404 {
			log.Printf("slot %d: removing %s: %v", n, name, err)
		}
	}
	os.RemoveAll(dir)
	if ctx.Err() != nil {
		return nil
	}
	if werr != nil {
		return fmt.Errorf("the runner stopped: %v", werr)
	}
	return nil
}

// follow copies the runner's output to the log and notes when a job starts and ends.
func (s *supervisor) follow(n int, r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := jobLineRE.Replace(sc.Text())
		log.Printf("slot %d: %s", n, line)
		if i := strings.Index(line, "Running job: "); i >= 0 {
			job := strings.TrimSpace(line[i+len("Running job: "):])
			s.set(n, func(x *slot) { x.State, x.Job, x.Since = "running", job, time.Now() })
		}
		if strings.Contains(line, "completed with result") {
			s.set(n, func(x *slot) { x.Jobs++ })
		}
	}
}

func (s *supervisor) health(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := 200
	if s.err != "" {
		status = 503
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"scope": s.cfg.Scope.String(), "version": s.ver, "error": s.err, "runners": s.slot})
}

func main() {
	log.SetFlags(0)
	cfg, cfgErr := loadConfig()
	s := &supervisor{cfg: cfg, gh: &github{api: env("GITHUB_API", "https://api.github.com"), token: cfg.Token, client: &http.Client{Timeout: time.Minute}}, ids: map[int]int64{}, pgid: map[int]int{}}
	for i := 0; i < cfg.Count; i++ {
		s.slot = append(s.slot, &slot{N: i, State: "starting", Since: time.Now()})
	}
	http.HandleFunc("/health", s.health)
	go func() { log.Fatal(http.ListenAndServe("127.0.0.1:"+env("PORT", "8080"), nil)) }()
	if cfgErr != nil {
		s.err = cfgErr.Error()
		log.Print(cfgErr)
		select {} // keep answering /health with the reason; a settings change restarts it
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	for {
		err := s.prepare()
		s.mu.Lock()
		s.err = ""
		if err != nil {
			s.err = err.Error()
		}
		s.mu.Unlock()
		if err == nil {
			break
		}
		log.Print(err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Minute):
		}
	}
	s.removeLeftovers()
	log.Printf("%d runners for %s, labels %s", cfg.Count, cfg.Scope, strings.Join(cfg.Labels, ","))
	var wg sync.WaitGroup
	for i := 0; i < cfg.Count; i++ {
		wg.Add(1)
		go func(n int) { defer wg.Done(); s.runSlot(ctx, n) }(i)
	}
	go func() { // a newer runner each day: GitHub stops sending jobs to old ones
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(24 * time.Hour):
				if err := s.prepare(); err != nil {
					log.Printf("updating the runner: %v", err)
				}
			}
		}
	}()
	<-ctx.Done()
	log.Print("stopping: ending runners and removing their registrations")
	s.mu.Lock()
	for _, p := range s.pgid {
		syscall.Kill(-p, syscall.SIGTERM)
	}
	ids := s.ids
	s.mu.Unlock()
	for _, id := range ids {
		s.gh.remove(cfg.Scope, id)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
	}
}
