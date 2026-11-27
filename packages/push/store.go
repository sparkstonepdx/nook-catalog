package main

// Everything is one JSON file, data/push/store.json (0600: it holds each app's private key). Small: a
// subscription is a few hundred bytes, and every change rewrites the file atomically.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type appKeys struct {
	Public  string    `json:"public"`  // base64url, 65 bytes
	Private string    `json:"private"` // base64url, 32 bytes
	Created time.Time `json:"created"`
}

type subscription struct {
	App string `json:"app"`
	// Kind is "" for Web Push (browsers, and UnifiedPush on Android), or "apns" or "fcm" for a native app's device
	// token (native.go).
	Kind     string    `json:"kind,omitempty"`
	Token    string    `json:"token,omitempty"`   // the device token, for apns and fcm
	Sandbox  bool      `json:"sandbox,omitempty"` // apns: a development build, sent through Apple's sandbox
	Endpoint string    `json:"endpoint,omitempty"`
	P256dh   string    `json:"p256dh,omitempty"`
	Auth     string    `json:"auth,omitempty"`
	Secret   string    `json:"secret"` // sha256 of the secret that unsubscribes it
	Created  time.Time `json:"created"`
}

// message is what a sender asks for. Payload goes to the app's service worker as is.
type message struct {
	Payload json.RawMessage `json:"payload"`
	TTL     int             `json:"ttl,omitempty"`     // seconds the push service keeps it if the device is offline
	Urgency string          `json:"urgency,omitempty"` // very-low, low, normal, high
	Topic   string          `json:"topic,omitempty"`   // a newer push with the same topic replaces an undelivered one
}

type job struct {
	ID     string    `json:"id"`
	Handle string    `json:"handle"`
	At     time.Time `json:"at"`
	Tries  int       `json:"tries,omitempty"`
	Msg    message   `json:"msg"`
}

type store struct {
	mu     sync.Mutex
	path   string
	Apps   map[string]*appKeys      `json:"apps"`
	Native map[string]*native       `json:"native,omitempty"` // apps' APNs and FCM credentials
	Subs   map[string]*subscription `json:"subs"`             // by handle
	Jobs   []*job                   `json:"jobs"`
}

func openStore(dir string) (*store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &store{path: filepath.Join(dir, "store.json"), Apps: map[string]*appKeys{}, Subs: map[string]*subscription{}, Native: map[string]*native{}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	if s.Apps == nil {
		s.Apps = map[string]*appKeys{}
	}
	if s.Subs == nil {
		s.Subs = map[string]*subscription{}
	}
	if s.Native == nil {
		s.Native = map[string]*native{}
	}
	return s, nil
}

// save writes the store; the caller holds mu.
func (s *store) save() error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func randomID(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return b64.EncodeToString(b)
}

func hashSecret(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func secretMatches(stored, given string) bool {
	return given != "" && subtle.ConstantTimeCompare([]byte(stored), []byte(hashSecret(given))) == 1
}

// dropSub removes a subscription and anything scheduled for it; the caller holds mu.
func (s *store) dropSub(handle string) {
	delete(s.Subs, handle)
	keep := s.Jobs[:0]
	for _, j := range s.Jobs {
		if j.Handle != handle {
			keep = append(keep, j)
		}
	}
	s.Jobs = keep
}
