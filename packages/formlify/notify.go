package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"sort"
	"strings"
	"time"
)

// notify tells people about a new submission: an email through the bound email service (SMTP_URL) to
// NOTIFY_EMAIL, and a JSON POST to NOTIFY_WEBHOOK. Either is optional; failures are logged, the submission is kept.
func (a *app) notify(sf siteForms, f Form, sub submission) {
	if a.cfg.SMTPURL != "" && a.cfg.NotifyEmail != "" && a.cfg.NotifyFrom != "" {
		if err := sendMail(a.cfg, sf, f, sub); err != nil {
			log.Printf("%s %s: email: %v", sf.Host, f.Name, err)
		}
	}
	if a.cfg.Webhook != "" {
		if err := postWebhook(a.cfg.Webhook, sf, f, sub); err != nil {
			log.Printf("%s %s: webhook: %v", sf.Host, f.Name, err)
		}
	}
}

func sortedKeys(m map[string][]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func mailText(cfg config, sf siteForms, f Form, sub submission) string {
	var b strings.Builder
	fmt.Fprintf(&b, "A new submission to %s on %s:\n\n", f.Name, sf.Host)
	for _, k := range sortedKeys(sub.Fields) {
		fmt.Fprintf(&b, "%s: %s\n", k, strings.Join(sub.Fields[k], ", "))
	}
	for _, u := range sub.Files {
		fmt.Fprintf(&b, "%s: %s (%d KB)\n", u.Field, u.Name, (u.Size+1023)/1024)
	}
	if cfg.URL != "" {
		fmt.Fprintf(&b, "\nAll of them: %s/s/%s/%s\n", cfg.URL, url.PathEscape(sf.Host), url.PathEscape(f.Name))
	}
	return b.String()
}

func sendMail(cfg config, sf siteForms, f Form, sub submission) error {
	u, err := url.Parse(cfg.SMTPURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("SMTP_URL isn't a URL")
	}
	host, _, _ := net.SplitHostPort(u.Host)
	pass, _ := u.User.Password()
	var msg bytes.Buffer
	subject := mime.QEncoding.Encode("utf-8", fmt.Sprintf("%s: new submission on %s", f.Name, sf.Host))
	fmt.Fprintf(&msg, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n",
		cfg.NotifyFrom, cfg.NotifyEmail, subject, time.Now().Format(time.RFC1123Z))
	if email := firstEmail(sub); email != "" {
		fmt.Fprintf(&msg, "Reply-To: %s\r\n", email) // replying answers the person who wrote
	}
	msg.WriteString("\r\n" + strings.ReplaceAll(mailText(cfg, sf, f, sub), "\n", "\r\n"))
	var auth smtp.Auth
	if u.User != nil {
		auth = smtp.PlainAuth("", u.User.Username(), pass, host)
	}
	return smtp.SendMail(u.Host, auth, cfg.NotifyFrom, strings.Split(cfg.NotifyEmail, ","), msg.Bytes())
}

// firstEmail is the submitter's address, from a field named like email, if it looks like one.
func firstEmail(sub submission) string {
	for _, k := range sortedKeys(sub.Fields) {
		if strings.Contains(strings.ToLower(k), "email") {
			for _, v := range sub.Fields[k] {
				v = strings.TrimSpace(v)
				if strings.Count(v, "@") == 1 && !strings.ContainsAny(v, " \r\n<>,") {
					return v
				}
			}
		}
	}
	return ""
}

func postWebhook(to string, sf siteForms, f Form, sub submission) error {
	body, _ := json.Marshal(map[string]any{"site": sf.Host, "form": f.Name, "id": sub.ID, "at": sub.At, "page": sub.Page, "fields": sub.Fields, "files": sub.Files})
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Post(to, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s answered %s", to, resp.Status)
	}
	return nil
}
