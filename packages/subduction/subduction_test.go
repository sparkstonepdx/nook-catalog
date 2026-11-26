package main

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func b(x []int) []byte {
	o := make([]byte, len(x))
	for i, n := range x {
		o[i] = byte(n)
	}
	return o
}

// The card is signed the way Keyhive checks it: Ed25519 over the share key's 32 bytes. (Checked against
// @keyhive/keyhive 0.4.0-alpha.1's receiveContactCard, which also rejects it with a byte of the share key changed.)
func TestMakeCard(t *testing.T) {
	seed := make([]byte, 32)
	seed[0] = 7
	c, err := makeCard(seed)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Add struct {
			Payload struct {
				ShareKey []int `json:"share_key"`
			} `json:"payload"`
			Issuer, Signature []int
		}
	}
	if err := json.Unmarshal([]byte(c), &v); err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(b(v.Add.Issuer), b(v.Add.Payload.ShareKey), b(v.Add.Signature)) || len(v.Add.Payload.ShareKey) != 32 {
		t.Fatalf("signature doesn't verify: %s", c)
	}
	if again, _ := makeCard(seed); again != c {
		t.Fatal("the card changes between makes")
	}
}

func TestIdentity(t *testing.T) {
	dir := t.TempDir()
	p := &identityPage{keyFile: filepath.Join(dir, "subduction.key"), cardFile: filepath.Join(dir, "subduction-card.json"), host: "sync.example.com"}
	if err := ensureKey(p.keyFile); err != nil {
		t.Fatal(err)
	}
	id, err := p.read()
	if err != nil || id.URL != "wss://sync.example.com" || len(id.PeerIDHex) != 64 || id.Card == "" {
		t.Fatalf("%+v %v", id, err)
	}
	made := id.Card

	// a saved card for this key is kept; one for another key isn't
	issuer, _ := cardIssuer([]byte(made))
	nums := make([]string, len(issuer))
	for i, x := range issuer {
		nums[i] = strconv.Itoa(int(x))
	}
	saved := `{"Add":{"payload":{"share_key":[1]},"issuer":[` + strings.Join(nums, ",") + `],"signature":[0]}}`
	os.WriteFile(p.cardFile, []byte(saved), 0o600)
	if id, _ := p.read(); id.Card != saved {
		t.Fatalf("saved card not kept: %s", id.Card)
	}
	os.WriteFile(p.cardFile, []byte(`{"Add":{"payload":{"share_key":[1]},"issuer":[`+strings.Repeat("1,", 31)+`1],"signature":[0]}}`), 0o600)
	if id, _ := p.read(); id.Card != made {
		t.Fatalf("another key's card used: %s", id.Card)
	}

	// an existing key is left alone
	before, _ := os.ReadFile(p.keyFile)
	ensureKey(p.keyFile)
	if after, _ := os.ReadFile(p.keyFile); string(after) != string(before) {
		t.Fatal("ensureKey replaced a key")
	}

	w := httptest.NewRecorder()
	p.html(w)
	if w.Code != 200 || !strings.Contains(w.Body.String(), id.PeerID) {
		t.Fatalf("page: %d", w.Code)
	}
}

func TestIsSync(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if isSync(r) {
		t.Fatal("a plain visit is the page")
	}
	r.Header.Set("Upgrade", "websocket")
	if !isSync(r) {
		t.Fatal("an upgrade is sync")
	}
	if !isSync(httptest.NewRequest(http.MethodPost, "/lp/recv", nil)) {
		t.Fatal("long poll is sync")
	}
}
