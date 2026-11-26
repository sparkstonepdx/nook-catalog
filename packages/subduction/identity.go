package main

// What an app needs to sync through this server with Keyhive: its address, its peer id (the public half of its
// key) and its Keyhive contact card. Shown at / and as /identity.json; all of it is public.

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"strings"
)

type identity struct {
	URL       string `json:"url"`
	PeerID    string `json:"peerId"`    // base64, the form automerge-repo-keyhive takes
	PeerIDHex string `json:"peerIdHex"` // how the server logs it
	Card      string `json:"card"`      // the contact card JSON
}

// readSeed reads a key file: 32 bytes, raw or as 64 hex digits (subduction_cli takes both).
func readSeed(b []byte) ([]byte, error) {
	if s := strings.TrimSpace(string(b)); len(s) == 64 {
		if seed, err := hex.DecodeString(s); err == nil {
			return seed, nil
		}
	}
	if len(b) == ed25519.SeedSize {
		return b, nil
	}
	return nil, fmt.Errorf("the key file must hold 32 bytes, raw or as 64 hex digits")
}

// makeCard makes the server's contact card from its key, the way @keyhive/keyhive does: an Add op whose payload is
// an X25519 share key, signed by the server's Ed25519 key over the share key's 32 bytes (bincode of Keyhive's
// AddKeyOp). The share key is derived from the seed, so it's the same card every time. Only public halves.
func makeCard(seed []byte) (string, error) {
	h := sha256.Sum256(append([]byte("nook subduction share key\x00"), seed...))
	share, err := ecdh.X25519().NewPrivateKey(h[:])
	if err != nil {
		return "", err
	}
	pub := share.PublicKey().Bytes()
	priv := ed25519.NewKeyFromSeed(seed)
	ints := func(b []byte) []int {
		out := make([]int, len(b))
		for i, x := range b {
			out[i] = int(x)
		}
		return out
	}
	type payload struct {
		ShareKey []int `json:"share_key"`
	}
	type add struct {
		Payload   payload `json:"payload"`
		Issuer    []int   `json:"issuer"`
		Signature []int   `json:"signature"`
	}
	b, err := json.Marshal(map[string]add{"Add": {payload{ints(pub)}, ints(priv.Public().(ed25519.PublicKey)), ints(ed25519.Sign(priv, pub))}})
	return string(b), err
}

// cardIssuer is the public key a contact card says it's from.
func cardIssuer(card []byte) ([]byte, error) {
	var c struct {
		Add struct {
			Raw []int `json:"issuer"`
		} `json:"Add"`
	}
	if err := json.Unmarshal(card, &c); err != nil {
		return nil, err
	}
	out := make([]byte, len(c.Add.Raw))
	for i, v := range c.Add.Raw {
		if v < 0 || v > 255 {
			return nil, fmt.Errorf("issuer isn't bytes")
		}
		out[i] = byte(v)
	}
	if len(out) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("issuer is %d bytes, want 32", len(out))
	}
	return out, nil
}

type identityPage struct{ keyFile, cardFile, host string }

// read works the identity out from the key. A saved card (data/subduction-card.json, the one apps were given before
// this made them) is kept while it's for this key, so apps holding it don't need a new one.
func (p *identityPage) read() (identity, error) {
	b, err := os.ReadFile(p.keyFile)
	if err != nil {
		return identity{}, err
	}
	seed, err := readSeed(b)
	if err != nil {
		return identity{}, err
	}
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	id := identity{URL: "wss://" + p.host, PeerID: base64.StdEncoding.EncodeToString(pub), PeerIDHex: hex.EncodeToString(pub)}
	if card, err := os.ReadFile(p.cardFile); err == nil {
		if issuer, err := cardIssuer(card); err == nil && ed25519.PublicKey(issuer).Equal(pub) {
			id.Card = strings.TrimSpace(string(card))
			return id, nil
		}
	}
	id.Card, err = makeCard(seed)
	return id, err
}

func (p *identityPage) json(w http.ResponseWriter) {
	id, err := p.read()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(id)
}

const pageCSP = "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

func (p *identityPage) html(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", pageCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	id, err := p.read()
	var body string
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		body = `<p class="bad">Couldn't read the server's key: ` + html.EscapeString(err.Error()) + `</p>`
	} else {
		row := func(label, value, hint string) string {
			return `<div class="row"><label>` + label + `</label><div class="field"><input readonly value="` + html.EscapeString(value) +
				`"><button type="button" data-copy="` + html.EscapeString(value) + `">Copy</button></div>` + hint + `</div>`
		}
		all := "SUBDUCTION_URL=" + id.URL + "\nSYNC_SERVER_PEER_ID=" + id.PeerID + "\nSYNC_SERVER_CONTACT_CARD=" + id.Card
		body = `<p>What an app needs to sync through this server with Keyhive.</p>` +
			`<p><button type="button" data-copy="` + html.EscapeString(all) + `">Copy all as env</button></p>` +
			row("Address", id.URL, "") +
			row("Peer id", id.PeerID, `<small>The server's public key, base64. Apps set it as SYNC_SERVER_PEER_ID.</small>`) +
			row("Keyhive contact card", id.Card, `<small>Apps set it as SYNC_SERVER_CONTACT_CARD, to add the server to a doc as a relay.</small>`)
	}
	w.Write([]byte(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Subduction</title>
<script src="/page.js" defer></script>
<style>
body { font: 16px/1.5 system-ui, sans-serif; margin: 0 auto; max-width: 40rem; padding: 1.5rem; color: #222; background: #fbfaf6; }
h1 { font-size: 1.6rem; margin: 0 0 .5rem; }
.row { display: grid; gap: .25rem; margin: 1.25rem 0; }
label { font-weight: 600; }
.field { display: flex; gap: .5rem; }
input { flex: 1; min-width: 0; font: 14px ui-monospace, monospace; padding: .5rem .6rem; border: 1px solid #bbb; border-radius: 6px; background: #fff; }
button { font: inherit; padding: .45rem .9rem; border: 1px solid #bbb; border-radius: 6px; background: #fff; cursor: pointer; }
small { color: #666; }
.bad { color: #a11; }
@media (prefers-color-scheme: dark) { body { color: #eee; background: #1b1a18; } input, button { background: #262522; color: #eee; border-color: #555; } small { color: #aaa; } }
</style>
</head>
<body>
<h1>Subduction</h1>
` + body + `
</body>
</html>
`))
}

const pageJS = `"use strict";
document.addEventListener("click", (e) => {
  const b = e.target.closest("button[data-copy]");
  if (!b) return;
  navigator.clipboard.writeText(b.dataset.copy).then(() => { const t = b.textContent; b.textContent = "Copied"; setTimeout(() => (b.textContent = t), 1200); });
});
`
