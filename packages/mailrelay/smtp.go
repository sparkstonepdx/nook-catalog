package main

import (
	"bufio"
	"bytes"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

type message struct {
	App  string
	From string
	To   []string
	Data []byte
}

type server struct {
	cfg  config
	lim  *limiter
	send func(c config, m message) error
}

func (s *server) serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.session(conn)
	}
}

// session speaks just enough SMTP for an app's mail library: EHLO, AUTH PLAIN or LOGIN, MAIL, RCPT, DATA, RSET,
// NOOP, QUIT. Loopback only, so no STARTTLS here; the hop to the provider is TLS.
func (s *server) session(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Minute))
	r := bufio.NewReader(io.LimitReader(conn, int64(s.cfg.MaxBytes)+1<<20))
	w := bufio.NewWriter(conn)
	say := func(line string) { w.WriteString(line + "\r\n"); w.Flush() }
	read := func() (string, error) {
		l, err := r.ReadString('\n')
		return strings.TrimRight(l, "\r\n"), err
	}
	app := ""
	var m message
	say("220 " + s.cfg.Hostname + " mailrelay ready")
	for {
		line, err := read()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO":
			say("250-" + s.cfg.Hostname)
			say(fmt.Sprintf("250-SIZE %d", s.cfg.MaxBytes))
			say("250-8BITMIME")
			say("250 AUTH PLAIN LOGIN")
		case "HELO":
			say("250 " + s.cfg.Hostname)
		case "AUTH":
			a, ok := s.auth(arg, say, read)
			if !ok {
				say("535 5.7.8 that token isn't bound to this mail service")
				continue
			}
			app = a
			say("235 2.7.0 authenticated as " + app)
		case "MAIL":
			if app == "" {
				say("530 5.7.0 authenticate first, with the token in SMTP_URL")
				continue
			}
			from := addrArg(arg, "FROM:")
			if !s.allowedFrom(from) {
				say("553 5.7.1 " + from + " isn't from " + strings.Join(s.cfg.FromDomains, ", "))
				continue
			}
			m = message{App: app, From: from}
			say("250 2.1.0 ok")
		case "RCPT":
			if m.From == "" {
				say("503 5.5.1 MAIL first")
				continue
			}
			to := addrArg(arg, "TO:")
			if to == "" || len(m.To) >= 50 {
				say("452 4.5.3 at most 50 recipients a message")
				continue
			}
			m.To = append(m.To, to)
			say("250 2.1.5 ok")
		case "DATA":
			if len(m.To) == 0 {
				say("503 5.5.1 RCPT first")
				continue
			}
			say("354 end with <CR><LF>.<CR><LF>")
			data, err := readData(r, s.cfg.MaxBytes)
			if err != nil {
				say("552 5.3.4 " + err.Error())
				m = message{}
				continue
			}
			m.Data = data
			say(s.deliver(m))
			m = message{}
		case "RSET":
			m = message{}
			say("250 2.0.0 ok")
		case "NOOP":
			say("250 2.0.0 ok")
		case "QUIT":
			say("221 2.0.0 bye")
			return
		default:
			say("502 5.5.2 not supported")
		}
	}
}

// auth reads AUTH PLAIN (inline or on the next line) or LOGIN and returns the app the password belongs to. The
// username isn't checked: the token alone says which app it is.
func (s *server) auth(arg string, say func(string), read func() (string, error)) (string, bool) {
	mech, initial, _ := strings.Cut(arg, " ")
	var pass string
	switch strings.ToUpper(mech) {
	case "PLAIN":
		if initial == "" {
			say("334 ")
			initial, _ = read()
		}
		b, err := base64.StdEncoding.DecodeString(initial)
		if err != nil {
			return "", false
		}
		parts := bytes.SplitN(b, []byte{0}, 3)
		if len(parts) != 3 {
			return "", false
		}
		pass = string(parts[2])
	case "LOGIN":
		say("334 VXNlcm5hbWU6")
		read()
		say("334 UGFzc3dvcmQ6")
		l, _ := read()
		b, err := base64.StdEncoding.DecodeString(l)
		if err != nil {
			return "", false
		}
		pass = string(b)
	default:
		return "", false
	}
	for tok, app := range s.cfg.Clients {
		if subtle.ConstantTimeCompare([]byte(tok), []byte(pass)) == 1 {
			return app, true
		}
	}
	return "", false
}

// addrArg reads the address out of "FROM:<a@b> SIZE=12".
func addrArg(arg, prefix string) string {
	if len(arg) < len(prefix) || !strings.EqualFold(arg[:len(prefix)], prefix) {
		return ""
	}
	a := strings.TrimSpace(arg[len(prefix):])
	if i := strings.IndexByte(a, '>'); strings.HasPrefix(a, "<") && i > 0 {
		a = a[1:i]
	}
	a, _, _ = strings.Cut(a, " ")
	return strings.TrimSpace(a)
}

func (s *server) allowedFrom(addr string) bool {
	_, domain, ok := strings.Cut(strings.ToLower(addr), "@")
	if !ok {
		return false
	}
	for _, d := range s.cfg.FromDomains {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	return false
}

// readData reads the message up to the lone dot, undoing dot-stuffing.
func readData(r *bufio.Reader, max int) ([]byte, error) {
	var b bytes.Buffer
	for {
		l, err := r.ReadString('\n')
		if err != nil {
			return nil, errors.New("the message ended early")
		}
		if l == ".\r\n" || l == ".\n" {
			break
		}
		if strings.HasPrefix(l, "..") {
			l = l[1:]
		}
		if b.Len()+len(l) > max {
			io.Copy(io.Discard, r) // the connection is useless now
			return nil, fmt.Errorf("messages are at most %d MB", max>>20)
		}
		b.WriteString(l)
	}
	return b.Bytes(), nil
}

// deliver checks the message's own From header and the app's hourly limit, then hands it to the provider.
func (s *server) deliver(m message) string {
	if msg, err := mail.ReadMessage(bytes.NewReader(m.Data)); err == nil {
		if list, err := msg.Header.AddressList("From"); err == nil {
			for _, a := range list {
				if !s.allowedFrom(a.Address) {
					log.Printf("%s: refused, From: %s isn't an allowed domain", m.App, a.Address)
					return "550 5.7.1 From: " + a.Address + " isn't from " + strings.Join(s.cfg.FromDomains, ", ")
				}
			}
		}
	}
	if !s.lim.take(m.App, len(m.To), time.Now()) {
		log.Printf("%s: refused, over %d recipients an hour", m.App, s.cfg.PerHour)
		return fmt.Sprintf("452 4.3.1 over this app's %d recipients an hour; try later", s.cfg.PerHour)
	}
	if err := s.send(s.cfg, m); err != nil {
		log.Printf("%s: %s to %d: provider refused: %v", m.App, m.From, len(m.To), err)
		return "451 4.3.0 the mail provider didn't take it: " + oneLine(err.Error())
	}
	log.Printf("%s: %s to %d recipients: sent", m.App, m.From, len(m.To))
	return "250 2.0.0 sent"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// sendUpstream passes the message to the provider: STARTTLS (required unless it's on this machine), then AUTH.
func sendUpstream(c config, m message) error {
	host, _, _ := net.SplitHostPort(c.Upstream)
	cl, err := smtp.Dial(c.Upstream)
	if err != nil {
		return err
	}
	defer cl.Close()
	if err := cl.Hello(c.Hostname); err != nil {
		return err
	}
	if ok, _ := cl.Extension("STARTTLS"); ok {
		if err := cl.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	} else if host != "127.0.0.1" && host != "localhost" {
		return errors.New("the provider doesn't offer STARTTLS; not sending the login in the clear")
	}
	if err := cl.Auth(smtp.PlainAuth("", c.User, c.Pass, host)); err != nil {
		return err
	}
	if err := cl.Mail(m.From); err != nil {
		return err
	}
	for _, to := range m.To {
		if err := cl.Rcpt(to); err != nil {
			return err
		}
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(m.Data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}
