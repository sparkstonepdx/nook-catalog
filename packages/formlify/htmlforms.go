package main

import (
	"strings"
)

// Form is one Netlify-style form found in a page.
type Form struct {
	Name     string   `json:"name"`
	Page     string   `json:"page"`
	Action   string   `json:"action,omitempty"`   // where to send people after they submit (the form's own action)
	Honeypot string   `json:"honeypot,omitempty"` // a field only bots fill in (netlify-honeypot)
	Fields   []string `json:"fields"`
	Files    []string `json:"files,omitempty"` // file inputs
}

// Rewrite finds the marked forms in a page (netlify or data-netlify="true", or formlify or data-formlify="true") and returns the page with
// the marker removed and a hidden form-name input added to each, as Netlify does, plus what it found. Only those
// form start tags change; every other byte is copied as it was. Forms in comments, <script>, <style>, <template>,
// <textarea> and the like are skipped, since browsers don't treat them as forms either.
func Rewrite(page, html string) (string, []Form) {
	var out strings.Builder
	var forms []Form
	var cur *Form // the marked form we're inside, collecting its fields
	last, i := 0, 0
	lower := strings.ToLower(html)
	for i < len(html) {
		lt := strings.IndexByte(html[i:], '<')
		if lt < 0 {
			break
		}
		i += lt
		switch {
		case strings.HasPrefix(html[i:], "<!--"):
			end := strings.Index(html[i+4:], "-->")
			if end < 0 {
				i = len(html)
				continue
			}
			i += 4 + end + 3
			continue
		case strings.HasPrefix(html[i:], "<!") || strings.HasPrefix(html[i:], "<?"):
			end := strings.IndexByte(html[i:], '>')
			if end < 0 {
				i = len(html)
				continue
			}
			i += end + 1
			continue
		}
		t, ok := readTag(html, i)
		if !ok {
			i++
			continue
		}
		switch {
		case t.end && t.name == "form":
			if cur != nil {
				forms = append(forms, cur.done())
				cur = nil
			}
		case !t.end && t.name == "form" && cur == nil:
			if !marked(t) {
				break
			}
			f := Form{Page: page, Name: firstAttr(t, "name", "id"), Action: t.attr("action"), Honeypot: firstAttr(t, "netlify-honeypot", "data-netlify-honeypot", "formlify-honeypot", "data-formlify-honeypot")}
			if f.Name == "" {
				f.Name = "form"
			}
			out.WriteString(html[last:t.start])
			out.WriteString(t.without("netlify", "data-netlify", "netlify-honeypot", "data-netlify-honeypot", "data-netlify-recaptcha", "netlify-recaptcha", "formlify", "data-formlify", "formlify-honeypot", "data-formlify-honeypot"))
			if !hasFormNameInput(lower[t.stop:]) {
				out.WriteString(`<input type="hidden" name="form-name" value="` + escapeAttr(f.Name) + `">`)
			}
			last = t.stop
			cur = &f
		case !t.end && cur != nil && (t.name == "input" || t.name == "select" || t.name == "textarea" || t.name == "button"):
			n := t.attr("name")
			if n == "" || n == "form-name" || t.name == "button" || strings.EqualFold(t.attr("type"), "submit") {
				break
			}
			if strings.EqualFold(t.attr("type"), "file") {
				cur.Files = appendNew(cur.Files, n)
			} else {
				cur.Fields = appendNew(cur.Fields, n)
			}
			if t.name == "textarea" {
				if close := strings.Index(lower[t.stop:], "</textarea"); close >= 0 {
					i = t.stop + close
					continue
				}
			}
		case !t.end && skipContent[t.name]:
			// raw text: nothing inside is markup
			close := strings.Index(lower[t.stop:], "</"+t.name)
			if close < 0 {
				i = len(html)
				continue
			}
			i = t.stop + close
			continue
		}
		i = t.stop
	}
	if cur != nil { // no </form>: browsers close it at the end
		forms = append(forms, cur.done())
	}
	if last == 0 {
		return html, forms
	}
	out.WriteString(html[last:])
	return out.String(), forms
}

// done leaves the honeypot out of the fields: it's for bots, never kept.
func (f Form) done() Form {
	var keep []string
	for _, x := range f.Fields {
		if x != f.Honeypot {
			keep = append(keep, x)
		}
	}
	f.Fields = keep
	return f
}

var skipContent = map[string]bool{"script": true, "style": true, "template": true, "textarea": true, "title": true, "xmp": true, "noscript": true, "iframe": true, "noembed": true, "noframes": true}

// marked: Netlify's markup (netlify, data-netlify="true") or Formlify's own (formlify, data-formlify="true").
func marked(t tag) bool {
	for _, n := range []string{"netlify", "formlify"} {
		if t.has(n) {
			return true
		}
		if v, ok := t.get("data-" + n); ok && strings.EqualFold(v, "true") {
			return true
		}
	}
	return false
}

// hasFormNameInput: the form already sends form-name itself (before its </form>).
func hasFormNameInput(rest string) bool {
	if end := strings.Index(rest, "</form"); end >= 0 {
		rest = rest[:end]
	}
	return strings.Contains(rest, `name="form-name"`) || strings.Contains(rest, `name='form-name'`) || strings.Contains(rest, "name=form-name")
}

func appendNew(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

func firstAttr(t tag, names ...string) string {
	for _, n := range names {
		if v := t.attr(n); v != "" {
			return v
		}
	}
	return ""
}

func escapeAttr(s string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(s)
}

type attr struct {
	name, value string
	start, stop int // the attribute's bytes in the page, leading space included
	hasValue    bool
}

type tag struct {
	name        string
	end         bool
	start, stop int // stop is just past '>'
	attrs       []attr
	src         string
}

func (t tag) get(name string) (string, bool) {
	for _, a := range t.attrs {
		if a.name == name {
			return a.value, true
		}
	}
	return "", false
}

func (t tag) has(name string) bool { _, ok := t.get(name); return ok }

func (t tag) attr(name string) string { v, _ := t.get(name); return v }

// without is the tag's own text with these attributes cut out.
func (t tag) without(names ...string) string {
	drop := map[string]bool{}
	for _, n := range names {
		drop[n] = true
	}
	var b strings.Builder
	pos := t.start
	for _, a := range t.attrs {
		if drop[a.name] {
			b.WriteString(t.src[pos:a.start])
			pos = a.stop
		}
	}
	b.WriteString(t.src[pos:t.stop])
	return b.String()
}

// readTag reads the tag starting at html[i] == '<', the way browsers do for the parts that matter here: a name,
// attributes with double-, single- or unquoted values, and a '>' that isn't inside quotes.
func readTag(html string, i int) (tag, bool) {
	t := tag{start: i, src: html}
	j := i + 1
	if j < len(html) && html[j] == '/' {
		t.end = true
		j++
	}
	n := j
	for n < len(html) && isNameByte(html[n]) {
		n++
	}
	if n == j || !isLetter(html[j]) {
		return t, false
	}
	t.name = strings.ToLower(html[j:n])
	j = n
	for j < len(html) {
		ws := j
		for j < len(html) && isSpace(html[j]) {
			j++
		}
		if j >= len(html) {
			return t, false
		}
		if html[j] == '>' {
			t.stop = j + 1
			return t, true
		}
		if html[j] == '/' {
			j++
			continue
		}
		a := attr{start: ws}
		k := j
		for k < len(html) && !isSpace(html[k]) && html[k] != '=' && html[k] != '>' && !(html[k] == '/' && k+1 < len(html) && html[k+1] == '>') {
			k++
		}
		a.name = strings.ToLower(html[j:k])
		j = k
		for j < len(html) && isSpace(html[j]) {
			j++
		}
		if j < len(html) && html[j] == '=' {
			j++
			for j < len(html) && isSpace(html[j]) {
				j++
			}
			if j < len(html) && (html[j] == '"' || html[j] == '\'') {
				q := html[j]
				e := strings.IndexByte(html[j+1:], q)
				if e < 0 {
					return t, false
				}
				a.value = html[j+1 : j+1+e]
				j += e + 2
			} else {
				v := j
				for j < len(html) && !isSpace(html[j]) && html[j] != '>' {
					j++
				}
				a.value = html[v:j]
			}
			a.hasValue = true
		} else {
			j = k
		}
		a.stop = j
		if a.name != "" {
			t.attrs = append(t.attrs, a)
		} else {
			j++ // a stray character: step over it
		}
	}
	return t, false
}

func isSpace(b byte) bool  { return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' }
func isLetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }
func isNameByte(b byte) bool {
	return isLetter(b) || b >= '0' && b <= '9' || b == '-' || b == ':' || b == '_'
}
