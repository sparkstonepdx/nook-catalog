package main

// Getting the runner: download GitHub's tarball once per version, check its SHA-256, unpack it into
// data/runner/<version>/, and give each job a fresh folder hard-linked from that (a few MB, not ~200).

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// download saves url to dst if it isn't there already with the right checksum.
func download(client *http.Client, url, sum, dst string) error {
	if sum == "" {
		return errors.New("no checksum for the runner download: set RUNNER_SHA256 to its SHA-256 (from the actions/runner release notes)")
	}
	if got, err := fileSHA(dst); err == nil && got == sum {
		return nil
	}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("downloading %s: %s", url, resp.Status)
	}
	os.MkdirAll(filepath.Dir(dst), 0o755)
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		os.Remove(tmp)
		return fmt.Errorf("the runner download's SHA-256 is %s, not %s: refusing it", got, sum)
	}
	return os.Rename(tmp, dst)
}

func fileSHA(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// inside says whether name, joined to root, stays in root.
func inside(root, name string) (string, bool) {
	p := filepath.Join(root, name)
	return p, p == root || strings.HasPrefix(p, root+string(os.PathSeparator))
}

// unpack extracts a .tar.gz into dir (made fresh). Entries that would land outside it, or links pointing out of
// it, are refused.
func unpack(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tmp := dir + ".part"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		p, ok := inside(tmp, h.Name)
		if !ok {
			return fmt.Errorf("%s: %q points outside the folder", archive, h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(p), 0o755)
			out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(h.Mode)&0o755|0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			out.Close()
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			target := h.Linkname
			if filepath.IsAbs(target) {
				return fmt.Errorf("%s: link %q is absolute", archive, h.Name)
			}
			if _, ok := inside(tmp, filepath.Join(filepath.Dir(h.Name), target)); !ok {
				return fmt.Errorf("%s: link %q points outside the folder", archive, h.Name)
			}
			os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.Symlink(target, p); err != nil {
				return err
			}
		case tar.TypeLink:
			src, ok := inside(tmp, h.Linkname)
			if !ok {
				return fmt.Errorf("%s: link %q points outside the folder", archive, h.Name)
			}
			if err := os.Link(src, p); err != nil {
				return err
			}
		}
	}
	os.RemoveAll(dir)
	return os.Rename(tmp, dir)
}

// freshCopy makes dst a hard-linked copy of src: a new folder for one job, sharing the runner's files.
func freshCopy(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(to, 0o755)
		case d.Type()&fs.ModeSymlink != 0:
			t, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(t, to)
		default:
			return os.Link(p, to)
		}
	})
}
