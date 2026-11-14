package main

// Web Push: encrypting a message for one browser (RFC 8291, aes128gcm from RFC 8188) and signing the request
// with the app's VAPID key (RFC 8292). Only the standard library.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"
)

var b64 = base64.RawURLEncoding

// decodeKey accepts base64url with or without padding, and standard base64 (what some clients send).
func decodeKey(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	s = strings.NewReplacer("+", "-", "/", "_").Replace(s)
	return b64.DecodeString(s)
}

// encrypt makes the aes128gcm body for a browser with public key p256dh and auth secret auth.
func encrypt(payload, p256dh, auth []byte) ([]byte, error) {
	if len(auth) != 16 {
		return nil, errors.New("auth secret must be 16 bytes")
	}
	curve := ecdh.P256()
	uaPub, err := curve.NewPublicKey(p256dh)
	if err != nil {
		return nil, fmt.Errorf("p256dh: %v", err)
	}
	asPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	asPub := asPriv.PublicKey().Bytes()
	shared, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	ikm, err := hkdf.Key(sha256.New, shared, auth, "WebPush: info\x00"+string(p256dh)+string(asPub), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain := append(append([]byte{}, payload...), 0x02) // one record, so it's the last: delimiter 2, no padding
	out := make([]byte, 0, 16+4+1+len(asPub)+len(plain)+gcm.Overhead())
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, 4096)
	out = append(out, byte(len(asPub)))
	out = append(out, asPub...)
	return gcm.Seal(out, nonce, plain, nil), nil
}

// vapidKey is an app's signing key. Public is the 65-byte uncompressed point browsers get as applicationServerKey.
type vapidKey struct {
	priv *ecdsa.PrivateKey
}

func newVAPIDKey() (*vapidKey, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &vapidKey{k}, nil
}

func (k *vapidKey) Public() []byte {
	e, _ := k.priv.PublicKey.ECDH()
	return e.Bytes()
}

func (k *vapidKey) Private() []byte { return k.priv.D.FillBytes(make([]byte, 32)) }

// parseVAPIDKey takes the raw 32-byte private scalar (base64url, as web-push libraries export it) and, if given,
// checks it matches the public key.
func parseVAPIDKey(private, public string) (*vapidKey, error) {
	d, err := decodeKey(private)
	if err != nil || len(d) != 32 {
		return nil, errors.New("private key should be 32 bytes, base64url")
	}
	ek, err := ecdh.P256().NewPrivateKey(d)
	if err != nil {
		return nil, fmt.Errorf("private key: %v", err)
	}
	pub := ek.PublicKey().Bytes()
	if public != "" {
		p, err := decodeKey(public)
		if err != nil || string(p) != string(pub) {
			return nil, errors.New("the public key doesn't belong to that private key")
		}
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), pub) // the point was checked by ecdh above
	priv := &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, D: new(big.Int).SetBytes(d)}
	return &vapidKey{priv}, nil
}

// header is the Authorization header for a push to endpoint: a JWT for the push service's origin, valid 12 hours.
func (k *vapidKey) header(endpoint, subject string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	head := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": u.Scheme + "://" + u.Host, "exp": now.Add(12 * time.Hour).Unix(), "sub": subject})
	signing := head + "." + b64.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, k.priv, sum[:])
	if err != nil {
		return "", err
	}
	sig := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	return "vapid t=" + signing + "." + b64.EncodeToString(sig) + ", k=" + b64.EncodeToString(k.Public()), nil
}
