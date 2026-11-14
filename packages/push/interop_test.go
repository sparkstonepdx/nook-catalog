package main

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// TestInterop prints a push for tests outside Go (scripts/interop.mjs checks it with the reference library).
// Skipped unless INTEROP_P256DH is set.
func TestInterop(t *testing.T) {
	p, a := os.Getenv("INTEROP_P256DH"), os.Getenv("INTEROP_AUTH")
	if p == "" {
		t.Skip("run by scripts/interop.mjs")
	}
	pb, _ := decodeKey(p)
	ab, _ := decodeKey(a)
	body, err := encrypt([]byte(`{"title":"hello from push"}`), pb, ab)
	if err != nil {
		t.Fatal(err)
	}
	k, err := parseVAPIDKey(os.Getenv("INTEROP_VAPID_PRIVATE"), os.Getenv("INTEROP_VAPID_PUBLIC"))
	if err != nil {
		t.Fatal(err)
	}
	h, _ := k.header("https://fcm.googleapis.com/fcm/send/abc", "mailto:a@example.com", time.Now())
	fmt.Printf("BODY=%s\nAUTH=%s\n", b64.EncodeToString(body), h)
}
