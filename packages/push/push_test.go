package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// browser is the receiving side of RFC 8291, written separately from encrypt.
type browser struct {
	priv *ecdh.PrivateKey
	auth []byte
}

func newBrowser(t *testing.T) *browser {
	k, _ := ecdh.P256().GenerateKey(rand.Reader)
	a := make([]byte, 16)
	rand.Read(a)
	return &browser{k, a}
}

func (b *browser) decrypt(t *testing.T, body []byte) []byte {
	t.Helper()
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if rs != 4096 || idlen != 65 {
		t.Fatalf("header: rs %d idlen %d", rs, idlen)
	}
	asPub, ct := body[21:21+idlen], body[21+idlen:]
	pub, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := b.priv.ECDH(pub)
	ua := b.priv.PublicKey().Bytes()
	prkKey, _ := hkdf.Extract(sha256.New, shared, b.auth)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(ua)+string(asPub), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	blk, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(blk)
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain[len(plain)-1] != 2 {
		t.Fatalf("last record delimiter is %d", plain[len(plain)-1])
	}
	return plain[:len(plain)-1]
}

func TestEncryptRoundTrip(t *testing.T) {
	b := newBrowser(t)
	msg := []byte(`{"title":"Your turn","url":"/game/42"}`)
	body, err := encrypt(msg, b.priv.PublicKey().Bytes(), b.auth)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.decrypt(t, body); !bytes.Equal(got, msg) {
		t.Fatalf("got %q", got)
	}
}

func verifyVAPID(t *testing.T, header, endpoint, subject string) {
	t.Helper()
	parts := strings.SplitN(strings.TrimPrefix(header, "vapid "), ", ", 2)
	jwt, k := strings.TrimPrefix(parts[0], "t="), strings.TrimPrefix(parts[1], "k=")
	seg := strings.Split(jwt, ".")
	pubb, _ := b64.DecodeString(k)
	x, y := elliptic.Unmarshal(elliptic.P256(), pubb)
	sig, _ := b64.DecodeString(seg[2])
	sum := sha256.Sum256([]byte(seg[0] + "." + seg[1]))
	if !ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("VAPID signature doesn't verify")
	}
	var c struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	cb, _ := b64.DecodeString(seg[1])
	json.Unmarshal(cb, &c)
	if !strings.HasPrefix(endpoint, c.Aud+"/") || c.Sub != subject || c.Exp < time.Now().Unix() || c.Exp > time.Now().Add(24*time.Hour).Unix() {
		t.Fatalf("claims %+v", c)
	}
}

func TestVAPIDImportExport(t *testing.T) {
	k, _ := newVAPIDKey()
	again, err := parseVAPIDKey(b64.EncodeToString(k.Private()), b64.EncodeToString(k.Public()))
	if err != nil || !bytes.Equal(again.Public(), k.Public()) {
		t.Fatalf("round trip: %v", err)
	}
	other, _ := newVAPIDKey()
	if _, err := parseVAPIDKey(b64.EncodeToString(k.Private()), b64.EncodeToString(other.Public())); err == nil {
		t.Fatal("a mismatched pair was accepted")
	}
	h, _ := again.header("https://fcm.googleapis.com/fcm/send/abc", "mailto:a@example.com", time.Now())
	verifyVAPID(t, h, "https://fcm.googleapis.com/fcm/send/abc", "mailto:a@example.com")
}

// a fake push service: records what it got, answers with status
type fakePush struct {
	status int
	got    []*http.Request
	bodies [][]byte
}

func (f *fakePush) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	f.got, f.bodies = append(f.got, r), append(f.bodies, b)
	return &http.Response{StatusCode: f.status, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
}

func setup(t *testing.T) (*server, *fakePush) {
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(st, config{Subject: "mailto:a@example.com", SendsPerHour: 5})
	fp := &fakePush{status: 201}
	s.client = &http.Client{Transport: fp}
	return s, fp
}

func call(s *server, method, path, app, body string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if app != "" {
		r.Header.Set("X-Nook-App", app)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func subJSON(b *browser, endpoint string) string {
	j, _ := json.Marshal(map[string]any{"endpoint": endpoint, "keys": map[string]string{"p256dh": b64.EncodeToString(b.priv.PublicKey().Bytes()), "auth": b64.EncodeToString(b.auth)}})
	return string(j)
}

func TestFlow(t *testing.T) {
	s, fp := setup(t)
	b := newBrowser(t)
	ep := "https://fcm.googleapis.com/fcm/send/xyz"

	if w := call(s, "GET", "/v1/config", "", ""); w.Code != 401 {
		t.Fatalf("config without nook's X-Nook-App: %d", w.Code)
	}
	if w := call(s, "POST", "/v1/subscribe/wordbag", "", subJSON(b, ep)); w.Code != 404 {
		t.Fatalf("subscribe before the app has a key: %d", w.Code)
	}
	w := call(s, "GET", "/v1/config", "wordbag", "")
	var cfg map[string]string
	json.Unmarshal(w.Body.Bytes(), &cfg)
	if w.Code != 200 || len(cfg["publicKey"]) != 87 {
		t.Fatalf("config: %d %s", w.Code, w.Body)
	}
	if w := call(s, "GET", "/v1/config", "wordbag", ""); !strings.Contains(w.Body.String(), cfg["publicKey"]) {
		t.Fatal("the key changed between calls")
	}
	if w := call(s, "POST", "/v1/subscribe/wordbag", "", subJSON(b, "https://evil.example.com/x")); w.Code != 400 {
		t.Fatalf("an endpoint that isn't a push service: %d", w.Code)
	}
	w = call(s, "POST", "/v1/subscribe/wordbag", "", subJSON(b, ep))
	var sub map[string]string
	json.Unmarshal(w.Body.Bytes(), &sub)
	if w.Code != 200 || !handleRE.MatchString(sub["handle"]) || sub["secret"] == "" {
		t.Fatalf("subscribe: %d %s", w.Code, w.Body)
	}

	w = call(s, "POST", "/v1/send/"+sub["handle"], "", `{"payload":{"title":"Your turn"},"urgency":"high","topic":"turn"}`)
	if w.Code != 200 || len(fp.got) != 1 {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	r := fp.got[0]
	if r.URL.String() != ep || r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("Urgency") != "high" || r.Header.Get("Topic") != "turn" || r.Header.Get("TTL") != "86400" {
		t.Fatalf("request to the push service: %s %v", r.URL, r.Header)
	}
	verifyVAPID(t, r.Header.Get("Authorization"), ep, "mailto:a@example.com")
	if !strings.Contains(r.Header.Get("Authorization"), "k="+cfg["publicKey"]) {
		t.Fatal("signed with a key other than the app's")
	}
	if got := b.decrypt(t, fp.bodies[0]); string(got) != `{"title":"Your turn"}` {
		t.Fatalf("the browser got %q", got)
	}

	// scheduled, replaced by topic, then sent when due
	at := s.now().Add(time.Hour).UTC().Format(time.RFC3339)
	call(s, "POST", "/v1/send/"+sub["handle"], "", `{"payload":1,"topic":"remind","at":"`+at+`"}`)
	w = call(s, "POST", "/v1/send/"+sub["handle"], "", `{"payload":2,"topic":"remind","at":"`+at+`"}`)
	if w.Code != 202 || len(s.st.Jobs) != 1 {
		t.Fatalf("schedule: %d %s, %d jobs", w.Code, w.Body, len(s.st.Jobs))
	}
	s.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	s.runDue()
	if len(fp.got) != 2 || string(b.decrypt(t, fp.bodies[1])) != "2" || len(s.st.Jobs) != 0 {
		t.Fatalf("scheduled push: %d sent, %d jobs left", len(fp.got), len(s.st.Jobs))
	}
	s.now = time.Now

	// rate limit (5 an hour in this test, 3 used)
	call(s, "POST", "/v1/send/"+sub["handle"], "", `{"payload":3}`)
	call(s, "POST", "/v1/send/"+sub["handle"], "", `{"payload":4}`)
	if w := call(s, "POST", "/v1/send/"+sub["handle"], "", `{"payload":5}`); w.Code != 429 {
		t.Fatalf("over the limit: %d", w.Code)
	}

	// unsubscribe needs the secret
	if w := call(s, "DELETE", "/v1/subscriptions/"+sub["handle"], "", "", "X-Push-Secret", "wrong"); w.Code != 404 {
		t.Fatalf("wrong secret: %d", w.Code)
	}
	if w := call(s, "DELETE", "/v1/subscriptions/"+sub["handle"], "", "", "X-Push-Secret", sub["secret"]); w.Code != 204 {
		t.Fatalf("unsubscribe: %d", w.Code)
	}
	if w := call(s, "POST", "/v1/send/"+sub["handle"], "", `{"payload":6}`); w.Code != 410 {
		t.Fatalf("send after unsubscribe: %d", w.Code)
	}
}

func TestGoneSubscriptionIsDropped(t *testing.T) {
	s, fp := setup(t)
	call(s, "GET", "/v1/config", "kinlog", "")
	w := call(s, "POST", "/v1/subscribe/kinlog", "", subJSON(newBrowser(t), "https://updates.push.services.mozilla.com/wpush/v2/abc"))
	var sub map[string]string
	json.Unmarshal(w.Body.Bytes(), &sub)
	fp.status = 410
	if w := call(s, "POST", "/v1/send/"+sub["handle"], "", `{"payload":1}`); w.Code != 410 {
		t.Fatalf("expired subscription: %d", w.Code)
	}
	if len(s.st.Subs) != 0 {
		t.Fatal("the dead subscription was kept")
	}
}

func TestImportKeys(t *testing.T) {
	s, _ := setup(t)
	k, _ := newVAPIDKey()
	body := `{"publicKey":"` + b64.EncodeToString(k.Public()) + `","privateKey":"` + b64.EncodeToString(k.Private()) + `"}`
	if w := call(s, "PUT", "/v1/keys", "wordbag", body); w.Code != 200 {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	if w := call(s, "GET", "/v1/config", "wordbag", ""); !strings.Contains(w.Body.String(), b64.EncodeToString(k.Public())) {
		t.Fatal("config doesn't return the imported key")
	}
	call(s, "POST", "/v1/subscribe/wordbag", "", subJSON(newBrowser(t), "https://fcm.googleapis.com/fcm/send/1"))
	k2, _ := newVAPIDKey()
	body2 := `{"publicKey":"` + b64.EncodeToString(k2.Public()) + `","privateKey":"` + b64.EncodeToString(k2.Private()) + `"}`
	if w := call(s, "PUT", "/v1/keys", "wordbag", body2); w.Code != 409 {
		t.Fatalf("replacing a key with subscribers needs ?replace=1: %d", w.Code)
	}
}

func TestCORSPreflight(t *testing.T) {
	s, _ := setup(t)
	w := call(s, "OPTIONS", "/v1/send/x", "", "")
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("preflight: %d %v", w.Code, w.Header())
	}
	if w := call(s, "OPTIONS", "/v1/config", "", ""); w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("token paths shouldn't allow cross-origin reads")
	}
}
