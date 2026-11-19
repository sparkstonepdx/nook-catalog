package main

// A form is questions, made in the editor; a response is the answers to one, kept as a JSON line.
//
// Sections work as in Google Forms: a "section" item in the list starts a new page, with its own title and
// description. After each section people continue to the next, go to a chosen one, or submit (the first section's
// choice is Form.Next, the others' their section item's Next). A multiple choice or dropdown question can instead
// send people to a section by their answer (GoTo); the last such answer in a section decides.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Question struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"` // short, long, choice, checkboxes, dropdown, email, number, date, or section
	Title    string            `json:"title"`
	Help     string            `json:"help,omitempty"` // a section's description
	Required bool              `json:"required,omitempty"`
	Options  []string          `json:"options,omitempty"` // choice, checkboxes, dropdown
	GoTo     map[string]string `json:"goto,omitempty"`    // choice, dropdown: option -> section id or "submit"
	Next     string            `json:"next,omitempty"`    // section: after it, "" (the next one), a section id, or "submit"
}

type Form struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Desc      string     `json:"desc,omitempty"`
	Questions []Question `json:"questions"`
	Next      string     `json:"next,omitempty"`   // after the first section (see Question.Next)
	Open      bool       `json:"open"`             // taking responses
	Thanks    string     `json:"thanks,omitempty"` // what people see after sending
	Created   string     `json:"created"`
	Updated   string     `json:"updated"`
}

type Response struct {
	ID      string              `json:"id"`
	At      string              `json:"at"`
	Answers map[string][]string `json:"answers"` // by question id
}

var types = map[string]string{
	"short": "Short answer", "long": "Paragraph", "choice": "Multiple choice", "checkboxes": "Checkboxes",
	"dropdown": "Dropdown", "email": "Email", "number": "Number", "date": "Date",
}

// Section is one page of the form: the questions up to the next section item.
type Section struct {
	ID, Title, Help, Next string // ID "" for the first
	Questions             []Question
}

func (f *Form) sections() []Section {
	out := []Section{{Next: f.Next}}
	for _, q := range f.Questions {
		if q.Type == "section" {
			out = append(out, Section{ID: q.ID, Title: q.Title, Help: q.Help, Next: q.Next})
			continue
		}
		out[len(out)-1].Questions = append(out[len(out)-1].Questions, q)
	}
	return out
}

// next is the section after section i, given its answers: -1 to submit.
func (f *Form) next(secs []Section, i int, answers map[string][]string) int {
	target := secs[i].Next
	for _, q := range secs[i].Questions {
		if v := answers[q.ID]; len(v) == 1 && q.GoTo[v[0]] != "" {
			target = q.GoTo[v[0]]
		}
	}
	switch target {
	case "":
		if i+1 < len(secs) {
			return i + 1
		}
		return -1
	case "submit":
		return -1
	}
	for j, s := range secs {
		if s.ID == target {
			return j
		}
	}
	return -1
}

// Asked is every question that isn't a section break.
func (f Form) Asked() []Question {
	var out []Question
	for _, q := range f.Questions {
		if q.Type != "section" {
			out = append(out, q)
		}
	}
	return out
}

func hasOptions(t string) bool { return t == "choice" || t == "checkboxes" || t == "dropdown" }

var idRE = regexp.MustCompile(`^[a-z0-9]{6,32}$`)

func newID(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

const (
	maxQuestions = 100
	maxOptions   = 50
	maxText      = 2000  // a title, a description, an option
	maxAnswer    = 10000 // one answer
)

// clean checks a form from the editor and trims what's too long. It keeps the form's id and dates as they were.
func (f *Form) clean() error {
	f.Title = trim(f.Title, 200)
	if f.Title == "" {
		f.Title = "Untitled form"
	}
	f.Desc, f.Thanks = trim(f.Desc, maxText), trim(f.Thanks, maxText)
	if len(f.Questions) > maxQuestions {
		return fmt.Errorf("at most %d questions", maxQuestions)
	}
	seen := map[string]bool{}
	for i := range f.Questions {
		q := &f.Questions[i]
		if _, ok := types[q.Type]; !ok && q.Type != "section" {
			return fmt.Errorf("question %d: unknown type %q", i+1, q.Type)
		}
		if !idRE.MatchString(q.ID) || seen[q.ID] {
			q.ID = newID(4)
		}
		seen[q.ID] = true
		q.Title, q.Help = trim(q.Title, maxText), trim(q.Help, maxText)
		if q.Type != "section" {
			q.Next = ""
		}
		if q.Type != "choice" && q.Type != "dropdown" {
			q.GoTo = nil
		}
		if !hasOptions(q.Type) {
			q.Options = nil
			continue
		}
		var opts []string
		dup := map[string]bool{}
		for _, o := range q.Options {
			if o = trim(o, 500); o != "" && !dup[o] {
				dup[o] = true
				opts = append(opts, o)
			}
		}
		if len(opts) > maxOptions {
			return fmt.Errorf("question %d: at most %d options", i+1, maxOptions)
		}
		if len(opts) == 0 {
			opts = []string{"Option 1"}
		}
		q.Options = opts
	}
	// where sections lead: a section that still exists, or submit; anything else is the next section
	ids := map[string]bool{"submit": true}
	for _, q := range f.Questions {
		if q.Type == "section" {
			ids[q.ID] = true
		}
	}
	if !ids[f.Next] {
		f.Next = ""
	}
	for i := range f.Questions {
		q := &f.Questions[i]
		if !ids[q.Next] {
			q.Next = ""
		}
		for o, t := range q.GoTo {
			if !ids[t] || !contains(q.Options, o) {
				delete(q.GoTo, o)
			}
		}
		if len(q.GoTo) == 0 {
			q.GoTo = nil
		}
	}
	return nil
}

func trim(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r", ""))
	if len(s) > n {
		s = s[:n]
	}
	return s
}

// answers reads a filled-in form: one value per question (several for checkboxes), checked against its type.
// The errors are by question id, for showing beside each.
func answers(qs []Question, get func(string) []string) (map[string][]string, map[string]string) {
	out, errs := map[string][]string{}, map[string]string{}
	for _, q := range qs {
		if q.Type == "section" {
			continue
		}
		var vals []string
		for _, v := range get("q_" + q.ID) {
			if v = strings.TrimSpace(strings.ReplaceAll(v, "\r", "")); v != "" {
				vals = append(vals, v)
			}
		}
		if q.Type != "checkboxes" && len(vals) > 1 {
			vals = vals[:1]
		}
		if len(vals) == 0 {
			if q.Required {
				errs[q.ID] = "This question needs an answer."
			}
			continue
		}
		bad := ""
		for _, v := range vals {
			switch {
			case len(v) > maxAnswer:
				bad = "That's too long."
			case q.Type == "short" && strings.Contains(v, "\n"):
				bad = "One line, please."
			case hasOptions(q.Type) && !contains(q.Options, v):
				bad = "Pick one of the options."
			case q.Type == "email" && !isEmail(v):
				bad = "That doesn't look like an email address."
			case q.Type == "number" && !isNumber(v):
				bad = "A number, please."
			case q.Type == "date" && !isDate(v):
				bad = "A date, please."
			}
		}
		if bad != "" {
			errs[q.ID] = bad
			continue
		}
		out[q.ID] = vals
	}
	return out, errs
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func isEmail(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && strings.Contains(s[strings.Index(s, "@"):], ".")
}

func isNumber(s string) bool { _, err := strconv.ParseFloat(s, 64); return err == nil }

func isDate(s string) bool { _, err := time.Parse("2006-01-02", s); return err == nil }
