package main

import (
	"strings"
	"testing"
)

func TestRewrite(t *testing.T) {
	page := `<!doctype html><html><head><title><form netlify></title>
<script>const s = '<form netlify name="fake">';</script></head><body>
<!-- <form netlify name="commented"> -->
<template><form netlify name="tpl"></form></template>
<form name="contact" method="POST" netlify netlify-honeypot="bot-field" action="/thanks/">
  <p hidden><input name="bot-field"></p>
  <input type="email" name="email" required>
  <textarea name="message"><form netlify name="inside"></textarea>
  <select name="topic"><option>a</option></select>
  <input type="file" name="cv">
  <button type="submit">Send</button>
</form>
<form data-netlify="true" name='news'><input name=email><input type="submit" name="go"></form>
<form data-netlify="false" name="nope"></form>
<form name="plain"><input name="q"></form>
</body></html>`
	out, forms := Rewrite("/contact/", page)
	if len(forms) != 2 {
		t.Fatalf("found %+v", forms)
	}
	c := forms[0]
	if c.Name != "contact" || c.Action != "/thanks/" || c.Honeypot != "bot-field" || c.Page != "/contact/" ||
		strings.Join(c.Fields, ",") != "email,message,topic" || strings.Join(c.Files, ",") != "cv" {
		t.Fatalf("contact: %+v", c)
	}
	if forms[1].Name != "news" || strings.Join(forms[1].Fields, ",") != "email" {
		t.Fatalf("news: %+v", forms[1])
	}
	for _, want := range []string{
		`<form name="contact" method="POST" action="/thanks/"><input type="hidden" name="form-name" value="contact">`,
		`<form name='news'><input type="hidden" name="form-name" value="news">`,
		`<form data-netlify="false" name="nope">`,
		`const s = '<form netlify name="fake">';`,
		`<!-- <form netlify name="commented"> -->`,
		`<template><form netlify name="tpl"></form></template>`,
		`<textarea name="message"><form netlify name="inside"></textarea>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// nothing else changed: put the markers back and it's the same page
	back := strings.Replace(out, `<input type="hidden" name="form-name" value="contact">`, "", 1)
	back = strings.Replace(back, `<input type="hidden" name="form-name" value="news">`, "", 1)
	back = strings.Replace(back, `method="POST" action`, `method="POST" netlify netlify-honeypot="bot-field" action`, 1)
	back = strings.Replace(back, `<form name='news'>`, `<form data-netlify="true" name='news'>`, 1)
	if back != page {
		t.Fatal("bytes outside the marked form tags changed")
	}
}

func TestRewriteLeavesUnmarkedPagesAlone(t *testing.T) {
	for _, page := range []string{"", "<p>hi</p>", "<form><input name=a></form>", "<a href='x>y'>", "<form netlify", "< form", "<3 forms"} {
		if out, forms := Rewrite("/", page); out != page || len(forms) != 0 {
			t.Errorf("%q -> %q %+v", page, out, forms)
		}
	}
}

func TestRewriteKeepsItsOwnFormName(t *testing.T) {
	page := `<form netlify name="a"><input type="hidden" name="form-name" value="a"><input name="x"></form>`
	out, forms := Rewrite("/", page)
	if strings.Count(out, "form-name") != 1 || len(forms) != 1 || strings.Join(forms[0].Fields, ",") != "x" {
		t.Fatalf("%s %+v", out, forms)
	}
	// no name: the id, then "form"
	if _, f := Rewrite("/", `<form netlify id="signup"></form><form netlify></form>`); f[0].Name != "signup" || f[1].Name != "form" {
		t.Fatalf("%+v", f)
	}
}

func TestFormlifyMarkup(t *testing.T) {
	out, f := Rewrite("/", `<form formlify name="a" formlify-honeypot="trap"></form><form data-formlify="true" name="b"></form><form data-formlify="false" name="c"></form>`)
	if len(f) != 2 || f[0].Name != "a" || f[0].Honeypot != "trap" || f[1].Name != "b" || strings.Contains(out, "formlify-honeypot") || strings.Contains(out, `data-formlify="true"`) || !strings.Contains(out, `data-formlify="false"`) {
		t.Fatalf("%v\n%s", f, out)
	}
}
