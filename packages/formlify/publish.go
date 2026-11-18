package main

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"
)

// publish is nook's publish hook for a bound static site: the site's HTML arrives as a tar on stdin, the pages
// with marked forms go back rewritten as a tar on stdout, what was found goes to stderr (the deploy log), and the
// site's forms are recorded for the submissions to come.
func publish(st *store, in io.Reader, out, log io.Writer) error {
	host, site, app := os.Getenv("SITE_HOST"), os.Getenv("SITE"), os.Getenv("NOOK_CONSUMER")
	if !safeHost(host) {
		return fmt.Errorf("no site address (SITE_HOST) from nook; update nook")
	}
	tr := tar.NewReader(in)
	tw := tar.NewWriter(out)
	byName := map[string]*Form{}
	var order []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, 32<<20))
		if err != nil {
			return err
		}
		page := "/" + strings.TrimPrefix(h.Name, "./")
		if path.Base(page) == "index.html" || path.Base(page) == "index.htm" {
			page = path.Dir(page)
			if page != "/" {
				page += "/"
			}
		}
		rewritten, forms := Rewrite(page, string(b))
		for _, f := range forms {
			if prev, ok := byName[f.Name]; ok { // one form on several pages: one form, its fields together
				for _, x := range f.Fields {
					prev.Fields = appendNew(prev.Fields, x)
				}
				for _, x := range f.Files {
					prev.Files = appendNew(prev.Files, x)
				}
				continue
			}
			f := f
			byName[f.Name] = &f
			order = append(order, f.Name)
		}
		if rewritten != string(b) {
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: h.Name, Mode: 0o644, Size: int64(len(rewritten)), ModTime: h.ModTime}); err != nil {
				return err
			}
			if _, err := io.WriteString(tw, rewritten); err != nil {
				return err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	sf := siteForms{App: app, Site: site, Host: host, Published: time.Now().UTC().Format(time.RFC3339)}
	for _, n := range order {
		sf.Forms = append(sf.Forms, *byName[n])
	}
	if err := st.saveSite(sf); err != nil {
		return err
	}
	if len(sf.Forms) == 0 {
		fmt.Fprintln(log, "formlify: no forms marked netlify or formlify")
		return nil
	}
	var names []string
	for _, f := range sf.Forms {
		names = append(names, fmt.Sprintf("%s (%s)", f.Name, f.Page))
	}
	fmt.Fprintf(log, "formlify: %d %s: %s\n", len(sf.Forms), plural(len(sf.Forms), "form", "forms"), strings.Join(names, ", "))
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
