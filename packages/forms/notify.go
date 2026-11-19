package main

// Telling people about a new response, each way optional: an email through the bound email service (SMTP_URL) to
// NOTIFY_EMAIL, and a push through the bound notify service (NOTIFY_URL, ntfy). Failures are logged; the response
// is kept either way.

import (
	"bytes"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"time"
)

func (a *app) notify(f Form, r Response) {
	if a.cfg.SMTPURL != "" && a.cfg.NotifyEmail != "" && a.cfg.NotifyFrom != "" {
		if err := sendMail(a.cfg, f, r); err != nil {
			log.Printf("%s: email: %v", f.ID, err)
		}
	}
	if a.cfg.PushURL != "" {
		if err := push(a.cfg, f, r); err != nil {
			log.Printf("%s: push: %v", f.ID, err)
		}
	}
}

// summary is the answers as "question: answer" lines; limit cuts each answer and the number of lines (0: all).
func summary(f Form, r Response, limit int) string {
	var b strings.Builder
	n := 0
	for _, q := range f.Questions {
		v, ok := r.Answers[q.ID]
		if !ok {
			continue
		}
		if limit > 0 && n == limit {
			b.WriteString("…\n")
			break
		}
		a := strings.Join(v, ", ")
		if limit > 0 && len(a) > 80 {
			a = a[:80] + "…"
		}
		fmt.Fprintf(&b, "%s: %s\n", cmpOr(q.Title, "Untitled question"), a)
		n++
	}
	return b.String()
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (c config) responsesURL(f Form) string {
	if c.URL == "" {
		return ""
	}
	return c.URL + "/forms/" + f.ID + "/responses"
}

func sendMail(cfg config, f Form, r Response) error {
	u, err := url.Parse(cfg.SMTPURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("SMTP_URL isn't a URL")
	}
	host, _, _ := net.SplitHostPort(u.Host)
	pass, _ := u.User.Password()
	text := "A new response to " + f.Title + ":\n\n" + summary(f, r, 0)
	if l := cfg.responsesURL(f); l != "" {
		text += "\nAll of them: " + l + "\n"
	}
	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n",
		cfg.NotifyFrom, cfg.NotifyEmail, mime.QEncoding.Encode("utf-8", "New response: "+f.Title), time.Now().Format(time.RFC1123Z))
	for _, q := range f.Questions {
		if v := r.Answers[q.ID]; q.Type == "email" && len(v) == 1 {
			fmt.Fprintf(&msg, "Reply-To: %s\r\n", v[0]) // replying answers the person who wrote
			break
		}
	}
	msg.WriteString("\r\n" + strings.ReplaceAll(text, "\n", "\r\n"))
	var auth smtp.Auth
	if u.User != nil {
		auth = smtp.PlainAuth("", u.User.Username(), pass, host)
	}
	return smtp.SendMail(u.Host, auth, cfg.NotifyFrom, strings.Split(cfg.NotifyEmail, ","), msg.Bytes())
}

// push publishes a short notice ntfy's way: the message as the body, with a title, a tag and a link.
func push(cfg config, f Form, r Response) error {
	req, err := http.NewRequest("POST", cfg.PushURL, strings.NewReader(strings.TrimSpace(summary(f, r, 4))))
	if err != nil {
		return err
	}
	req.Header.Set("Title", mime.QEncoding.Encode("utf-8", "New response: "+f.Title)) // headers are ASCII; ntfy reads the encoded form
	req.Header.Set("Tags", "clipboard")
	if l := cfg.responsesURL(f); l != "" {
		req.Header.Set("Click", l)
	}
	if cfg.PushToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.PushToken)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("the notify service answered %s", resp.Status)
	}
	return nil
}
