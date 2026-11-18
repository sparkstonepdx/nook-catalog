package main

import (
	"net"
	"net/smtp"
	"strings"
	"sync"
	"testing"
	"time"
)

// start runs a server on a free loopback port and returns its address.
func start(t *testing.T, s *server) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go s.serve(ln)
	return ln.Addr().String()
}

type capture struct {
	mu   sync.Mutex
	msgs []message
}

func (c *capture) send(_ config, m message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, m)
	return nil
}

const body = "From: Shop <orders@example.com>\r\nTo: you@else.org\r\nSubject: Hi\r\n\r\nHello\r\n.leading dot\r\n"

func TestRelaysThroughTheProvider(t *testing.T) {
	// the provider: a server that takes the account's own login and keeps what it gets
	got := &capture{}
	provider := start(t, &server{cfg: config{Hostname: "provider", MaxBytes: 1 << 20, FromDomains: []string{"example.com"}, Clients: map[string]string{"account-secret": "account"}},
		lim: &limiter{max: 100, sent: map[string][]time.Time{}}, send: got.send})

	relay := start(t, &server{cfg: config{Hostname: "relay", Upstream: provider, User: "account", Pass: "account-secret", MaxBytes: 1 << 20,
		FromDomains: []string{"example.com"}, PerHour: 100, Clients: clients(`{"email": {"shop": "shop-token", "blog": "blog-token"}}`)},
		lim: &limiter{max: 100, sent: map[string][]time.Time{}}, send: sendUpstream})

	auth := smtp.PlainAuth("", "nook", "shop-token", "127.0.0.1")
	if err := smtp.SendMail(relay, auth, "orders@example.com", []string{"you@else.org"}, []byte(body)); err != nil {
		t.Fatal(err)
	}
	if len(got.msgs) != 1 || got.msgs[0].App != "account" || got.msgs[0].From != "orders@example.com" || got.msgs[0].To[0] != "you@else.org" {
		t.Fatalf("%+v", got.msgs)
	}
	if !strings.Contains(string(got.msgs[0].Data), "\r\n.leading dot\r\n") {
		t.Fatalf("dot-stuffing lost a line: %q", got.msgs[0].Data)
	}
}

func TestRefuses(t *testing.T) {
	got := &capture{}
	relay := start(t, &server{cfg: config{Hostname: "relay", MaxBytes: 1 << 20, FromDomains: []string{"example.com"}, PerHour: 2,
		Clients: clients(`{"email": {"shop": "shop-token"}}`)}, lim: &limiter{max: 2, sent: map[string][]time.Time{}}, send: got.send})
	send := func(tok, from string, to []string, data string) error {
		return smtp.SendMail(relay, smtp.PlainAuth("", "nook", tok, "127.0.0.1"), from, to, []byte(data))
	}
	cases := []struct {
		name, tok, from, data, want string
		to                          []string
	}{
		{"a token that isn't bound", "nope", "a@example.com", body, "535", []string{"x@y.z"}},
		{"another domain in MAIL FROM", "shop-token", "a@evil.com", body, "553", []string{"x@y.z"}},
		{"another domain in the From header", "shop-token", "a@example.com", strings.Replace(body, "orders@example.com", "boss@bank.com", 1), "550", []string{"x@y.z"}},
		{"a lookalike domain", "shop-token", "a@notexample.com", body, "553", []string{"x@y.z"}},
	}
	for _, c := range cases {
		if err := send(c.tok, c.from, c.to, c.data); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if len(got.msgs) != 0 {
		t.Fatalf("sent %d it should have refused", len(got.msgs))
	}
	// a subdomain is fine; the hourly limit counts recipients
	if err := send("shop-token", "a@mail.example.com", []string{"x@y.z", "w@y.z"}, body); err != nil {
		t.Fatal(err)
	}
	if err := send("shop-token", "a@example.com", []string{"v@y.z"}, body); err == nil || !strings.Contains(err.Error(), "452") {
		t.Fatalf("over the limit: %v", err)
	}
}

func TestAuthLogin(t *testing.T) {
	got := &capture{}
	relay := start(t, &server{cfg: config{Hostname: "relay", MaxBytes: 1 << 20, FromDomains: []string{"example.com"}, PerHour: 10,
		Clients: clients(`{"email": {"shop": "shop-token"}}`)}, lim: &limiter{max: 10, sent: map[string][]time.Time{}}, send: got.send})
	c, err := net.Dial("tcp", relay)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	buf := make([]byte, 512)
	talk := func(line string) string {
		if line != "" {
			c.Write([]byte(line + "\r\n"))
		}
		time.Sleep(30 * time.Millisecond)
		n, _ := c.Read(buf)
		return string(buf[:n])
	}
	talk("")
	talk("EHLO me")
	talk("AUTH LOGIN")
	talk("bm9vaw==")                                                 // nook
	if r := talk("c2hvcC10b2tlbg=="); !strings.HasPrefix(r, "235") { // shop-token
		t.Fatalf("AUTH LOGIN: %q", r)
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("MAIL_PROVIDER", "ses")
	t.Setenv("SES_REGION", "us-west-2")
	t.Setenv("SES_SMTP_USERNAME", "AKIA")
	t.Setenv("SES_SMTP_PASSWORD", "secret")
	t.Setenv("FROM_DOMAINS", "example.com, @shop.example.org")
	c, err := loadConfig()
	if err != nil || c.Upstream != "email-smtp.us-west-2.amazonaws.com:587" || len(c.FromDomains) != 2 || c.FromDomains[1] != "shop.example.org" {
		t.Fatalf("%+v %v", c, err)
	}
	t.Setenv("MAIL_PROVIDER", "sendgrid")
	t.Setenv("SENDGRID_API_KEY", "SG.x")
	if c, err := loadConfig(); err != nil || c.Upstream != "smtp.sendgrid.net:587" || c.User != "apikey" {
		t.Fatalf("%+v %v", c, err)
	}
	t.Setenv("FROM_DOMAINS", "")
	if _, err := loadConfig(); err == nil {
		t.Fatal("FROM_DOMAINS is required")
	}
}
