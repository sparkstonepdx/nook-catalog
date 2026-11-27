package main

import (
	"crypto/ecdh"
	"encoding/json"
	"os"
	"testing"
)

// PUSH_WRITE_VECTORS=1 go test -run TestWriteVectors writes capacitor/testdata/vectors.json: pushes this server
// encrypted for a fixed device key, which the Android and iOS decryption is tested against.
func TestWriteVectors(t *testing.T) {
	if os.Getenv("PUSH_WRITE_VECTORS") == "" {
		t.Skip("set PUSH_WRITE_VECTORS=1 to rewrite them")
	}
	d := make([]byte, 32)
	for i := range d {
		d[i] = byte(i*7 + 1)
	}
	k, err := ecdh.P256().NewPrivateKey(d)
	if err != nil {
		t.Fatal(err)
	}
	auth := []byte("0123456789abcdef")
	out := map[string]any{"privateKey": b64.EncodeToString(d), "p256dh": b64.EncodeToString(k.PublicKey().Bytes()), "auth": b64.EncodeToString(auth)}
	var cases []map[string]string
	for _, p := range []string{`{"title":"Feed time","body":"It's been 3 hours","url":"/log"}`, `{"sync":true}`, `{"title":"ü ✓ 日本"}`} {
		ct, err := encrypt([]byte(p), k.PublicKey().Bytes(), auth)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, map[string]string{"payload": p, "enc": b64.EncodeToString(ct)})
	}
	out["cases"] = cases
	b, _ := json.MarshalIndent(out, "", "  ")
	os.MkdirAll("capacitor/testdata", 0o755)
	if err := os.WriteFile("capacitor/testdata/vectors.json", append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
