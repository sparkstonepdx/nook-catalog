package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func digestOf(b []byte) string { s := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(s[:]) }

type ociClient struct {
	t    *testing.T
	base string
	app  string
}

func (c ociClient) do(method, path string, body []byte, hdr ...string) *http.Response {
	c.t.Helper()
	url := path
	if !strings.HasPrefix(path, "http") {
		url = c.base + path
	}
	req, _ := http.NewRequest(method, url, bytes.NewReader(body))
	if body == nil {
		req.Body = nil
	}
	if c.app != "" {
		req.Header.Set("Authorization", "Bearer token-"+c.app)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return res
}

func (c ociClient) want(res *http.Response, status int, what string) []byte {
	c.t.Helper()
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != status {
		c.t.Fatalf("%s: %d, want %d: %s", what, res.StatusCode, status, b)
	}
	return b
}

// push sends a blob the way docker does: POST, PATCH in two chunks, PUT ?digest=.
func (c ociClient) pushBlob(repo string, blob []byte) string {
	c.t.Helper()
	d := digestOf(blob)
	res := c.do("POST", "/v2/"+repo+"/blobs/uploads/", nil)
	c.want(res, 202, "start upload")
	loc := res.Header.Get("Location")
	half := len(blob) / 2
	res = c.do("PATCH", loc, blob[:half], "Content-Range", fmt.Sprintf("0-%d", half-1), "Content-Type", "application/octet-stream")
	c.want(res, 202, "first chunk")
	if got := res.Header.Get("Range"); got != fmt.Sprintf("0-%d", half-1) {
		c.t.Fatalf("Range after first chunk: %q", got)
	}
	res = c.do("PATCH", res.Header.Get("Location"), blob[half:], "Content-Range", fmt.Sprintf("%d-%d", half, len(blob)-1))
	c.want(res, 202, "second chunk")
	res = c.do("PUT", res.Header.Get("Location")+"?digest="+d, nil)
	c.want(res, 201, "finish upload")
	if res.Header.Get("Docker-Content-Digest") != d {
		c.t.Fatal("finish: no digest header")
	}
	return d
}

func imageManifest(config string, configSize int, layers ...string) []byte {
	var ls []map[string]any
	for _, l := range layers {
		ls = append(ls, map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": l, "size": 10})
	}
	b, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": config, "size": configSize}, "layers": ls})
	return b
}

func TestOCIPushPull(t *testing.T) {
	srv, dir := newTestRegistry(t, config{MaxImageBytes: 1 << 20})
	anon := ociClient{t, srv.URL, ""}
	res := anon.do("GET", "/v2/", nil)
	anon.want(res, 401, "ping without a token")
	if !strings.HasPrefix(res.Header.Get("WWW-Authenticate"), "Basic") {
		t.Fatal("docker login needs a Basic challenge")
	}
	a := ociClient{t, srv.URL, "wordbag"}
	res = a.do("GET", "/v2/", nil)
	a.want(res, 200, "ping")
	if res.Header.Get("Docker-Distribution-API-Version") != "registry/2.0" {
		t.Fatal("no API version header")
	}

	layer := bytes.Repeat([]byte("layer-bytes "), 5000)
	a.want(a.do("HEAD", "/v2/sparkstone/wordbag/blobs/"+digestOf(layer), nil), 404, "head before push")
	ld := a.pushBlob("sparkstone/wordbag", layer)
	// the config in one go: POST ?digest= with the body
	cfg := []byte(`{"architecture":"amd64","os":"linux"}`)
	cd := digestOf(cfg)
	a.want(a.do("POST", "/v2/sparkstone/wordbag/blobs/uploads/?digest="+cd, cfg), 201, "monolithic upload")

	// a manifest naming a blob that isn't here
	missing := imageManifest(cd, len(cfg), ld, digestOf([]byte("nope")))
	a.want(a.do("PUT", "/v2/sparkstone/wordbag/manifests/v1", missing, "Content-Type", "application/vnd.oci.image.manifest.v1+json"), 400, "manifest with a missing layer")

	man := imageManifest(cd, len(cfg), ld)
	md := digestOf(man)
	res = a.do("PUT", "/v2/sparkstone/wordbag/manifests/v1", man, "Content-Type", "application/vnd.oci.image.manifest.v1+json")
	a.want(res, 201, "put manifest")
	if res.Header.Get("Docker-Content-Digest") != md {
		t.Fatal("manifest digest header")
	}

	// pulling, from another app's token too
	b := ociClient{t, srv.URL, "ci"}
	res = b.do("GET", "/v2/sparkstone/wordbag/manifests/v1", nil, "Accept", "application/vnd.oci.image.manifest.v1+json")
	if got := b.want(res, 200, "get manifest"); !bytes.Equal(got, man) || res.Header.Get("Content-Type") != "application/vnd.oci.image.manifest.v1+json" {
		t.Fatalf("manifest back: %s %s", res.Header.Get("Content-Type"), got)
	}
	b.want(b.do("HEAD", "/v2/sparkstone/wordbag/manifests/"+md, nil), 200, "head manifest by digest")
	if got := b.want(b.do("GET", "/v2/sparkstone/wordbag/blobs/"+ld, nil), 200, "get layer"); !bytes.Equal(got, layer) {
		t.Fatal("layer bytes")
	}
	if got := b.want(b.do("GET", "/v2/sparkstone/wordbag/blobs/"+ld, nil, "Range", "bytes=0-4"), 206, "range"); string(got) != "layer" {
		t.Fatalf("range: %q", got)
	}
	if got := b.want(b.do("GET", "/v2/sparkstone/wordbag/tags/list", nil), 200, "tags"); !strings.Contains(string(got), `"tags":["v1"]`) {
		t.Fatalf("tags: %s", got)
	}

	// only the owner pushes
	b.want(b.do("POST", "/v2/sparkstone/wordbag/blobs/uploads/", nil), 403, "another app's push")
	b.want(b.do("PUT", "/v2/sparkstone/wordbag/manifests/v2", man, "Content-Type", "application/vnd.oci.image.manifest.v1+json"), 403, "another app's tag")
	b.want(b.do("DELETE", "/v2/sparkstone/wordbag/manifests/"+md, nil), 403, "another app's delete")

	// a wrong digest, and a layer over the limit
	res = a.do("POST", "/v2/sparkstone/wordbag/blobs/uploads/", nil)
	a.want(res, 202, "start")
	loc := res.Header.Get("Location")
	a.want(a.do("PUT", loc+"?digest="+digestOf([]byte("other")), []byte("bytes")), 400, "wrong digest")
	res = a.do("POST", "/v2/sparkstone/wordbag/blobs/uploads/", nil)
	a.want(res, 202, "start")
	a.want(a.do("PATCH", res.Header.Get("Location"), bytes.Repeat([]byte("x"), 1<<20+1)), 416, "too big")

	// ci mounts the layer into its own repository instead of sending it again
	res = b.do("POST", "/v2/ci/runner/blobs/uploads/?mount="+ld+"&from=sparkstone/wordbag", nil)
	b.want(res, 201, "mount")
	b.want(b.do("GET", "/v2/ci/runner/blobs/"+ld, nil), 200, "mounted layer")
	b.want(b.do("GET", "/v2/ci/runner/blobs/"+cd, nil), 404, "a blob it didn't mount")

	// deleting: the manifest goes, then (an hour on) the blobs only it used
	a.want(a.do("DELETE", "/v2/sparkstone/wordbag/manifests/"+md, nil), 202, "delete manifest")
	a.want(a.do("GET", "/v2/sparkstone/wordbag/manifests/v1", nil), 404, "tag of a deleted manifest")
	old := time.Now().Add(-2 * time.Hour)
	for _, d := range []string{ld, cd, md} {
		os.Chtimes(filepath.Join(dir, "oci/blobs/sha256", strings.TrimPrefix(d, "sha256:")), old, old)
	}
	s := newServer(dir, config{})
	s.collect()
	if _, err := os.Stat(filepath.Join(dir, "oci/blobs/sha256", strings.TrimPrefix(cd, "sha256:"))); err == nil {
		t.Error("the config only the deleted manifest used is still there")
	}
	// ci/runner has no manifest yet, so its mounted layer isn't held by one: it goes too, as an unfinished push would
}

func TestOCINames(t *testing.T) {
	srv, _ := newTestRegistry(t, config{MaxImageBytes: 1 << 20})
	a := ociClient{t, srv.URL, "wordbag"}
	for _, bad := range []string{"/v2/UPPER/blobs/uploads/", "/v2/a//b/blobs/uploads/", "/v2/-a/blobs/uploads/"} {
		a.want(a.do("POST", bad, nil), 404, bad)
	}
	a.want(a.do("GET", "/v2/sparkstone/none/manifests/latest", nil), 404, "unknown repo")
	a.want(a.do("GET", "/v2/sparkstone/none/blobs/uploads/../../x", nil), 404, "upload path escape")
}
