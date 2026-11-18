// mailrelay lets every app on a nook server send email through one provider account (Amazon SES, SendGrid, or any
// SMTP relay), without any app holding that account's credentials. It provides nook's "email" service: an app
// bound to it gets SMTP_URL, smtp://nook:<its own token>@127.0.0.1:<port>, and sends as it would to any mail
// server. mailrelay checks the token against NOOK_CLIENTS (which nook fills from the bindings), checks the sender is
// one of FROM_DOMAINS, counts the app's messages against its hourly limit, and passes the message on over TLS.
//
// Only bound apps can reach its port (nook's firewall), and it listens on loopback only.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type config struct {
	Port        string
	Upstream    string // host:port of the provider's SMTP server
	User, Pass  string // the provider account's SMTP login
	FromDomains []string
	PerHour     int
	MaxBytes    int
	Clients     map[string]string // token -> app
	Hostname    string
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// loadConfig reads the provider (MAIL_PROVIDER: ses, sendgrid or smtp) and its settings from the environment.
func loadConfig() (config, error) {
	c := config{Port: env("PORT", "2525"), Hostname: env("SITE", "mailrelay")}
	switch p := env("MAIL_PROVIDER", "smtp"); p {
	case "ses":
		region := env("SES_REGION", "")
		if region == "" {
			return c, fmt.Errorf("SES_REGION is required, like us-west-2")
		}
		c.Upstream = "email-smtp." + region + ".amazonaws.com:587"
		c.User, c.Pass = env("SES_SMTP_USERNAME", ""), env("SES_SMTP_PASSWORD", "")
	case "sendgrid":
		c.Upstream, c.User, c.Pass = "smtp.sendgrid.net:587", "apikey", env("SENDGRID_API_KEY", "")
	case "smtp":
		c.Upstream, c.User, c.Pass = env("SMTP_HOST", ""), env("SMTP_USERNAME", ""), env("SMTP_PASSWORD", "")
		if c.Upstream != "" && !strings.Contains(c.Upstream, ":") {
			c.Upstream += ":587"
		}
	default:
		return c, fmt.Errorf("MAIL_PROVIDER is ses, sendgrid or smtp, not %q", p)
	}
	if c.Upstream == "" || c.User == "" || c.Pass == "" {
		return c, fmt.Errorf("the provider's login is missing (see the app's settings)")
	}
	for _, d := range strings.FieldsFunc(env("FROM_DOMAINS", ""), func(r rune) bool { return r == ',' || r == ' ' }) {
		c.FromDomains = append(c.FromDomains, strings.ToLower(strings.TrimPrefix(d, "@")))
	}
	if len(c.FromDomains) == 0 {
		return c, fmt.Errorf("FROM_DOMAINS is required: the domains apps may send from, like example.com")
	}
	c.PerHour, _ = strconv.Atoi(env("SENDS_PER_HOUR", "200"))
	c.MaxBytes = 10 << 20
	c.Clients = clients(os.Getenv("NOOK_CLIENTS"))
	return c, nil
}

// clients reads NOOK_CLIENTS ({"email": {"app": "token"}}) into token -> app.
func clients(s string) map[string]string {
	var by map[string]map[string]string
	json.Unmarshal([]byte(s), &by)
	out := map[string]string{}
	for app, tok := range by["email"] {
		if tok != "" {
			out[tok] = app
		}
	}
	return out
}

// limiter counts each app's recipients over the last hour.
type limiter struct {
	mu   sync.Mutex
	max  int
	sent map[string][]time.Time
}

func (l *limiter) take(app string, n int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	var keep []time.Time
	for _, t := range l.sent[app] {
		if now.Sub(t) < time.Hour {
			keep = append(keep, t)
		}
	}
	if len(keep)+n > l.max {
		l.sent[app] = keep
		return false
	}
	for i := 0; i < n; i++ {
		keep = append(keep, now)
	}
	l.sent[app] = keep
	return true
}

func main() {
	c, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+c.Port)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("mailrelay: relaying for %d apps through %s, from %s", len(c.Clients), c.Upstream, strings.Join(c.FromDomains, ", "))
	s := &server{cfg: c, lim: &limiter{max: c.PerHour, sent: map[string][]time.Time{}}, send: sendUpstream}
	log.Fatal(s.serve(ln))
}
