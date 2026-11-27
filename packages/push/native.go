package main

// Native push for mobile apps: APNs (iOS) and FCM (Android with Google Play services). Each app brings its own
// credentials from CI with NOOK_TOKEN; they stay here, like the VAPID keys. Android without Google uses
// UnifiedPush, whose endpoints take Web Push (webpush.go), so it needs nothing in this file.
//
// APNs: token-based auth, an ES256 JWT from the app's .p8 key (team id, key id), reused for 50 minutes as Apple
// asks, POST /3/device/TOKEN over HTTP/2.
// FCM: HTTP v1 with the app's service account: an RS256 JWT traded for an OAuth access token at Google's token
// endpoint, reused until 5 minutes before it expires, POST /v1/projects/PROJECT/messages:send.

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

type apnsCreds struct {
	TeamID   string `json:"teamId"`
	KeyID    string `json:"keyId"`
	Key      string `json:"key"`      // the .p8 file: a PKCS#8 P-256 key, PEM
	BundleID string `json:"bundleId"` // the apns-topic
}

type fcmCreds struct {
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"` // PKCS#8 RSA, PEM
	TokenURI    string `json:"token_uri"`
}

// native is an app's mobile push credentials; either may be missing.
type native struct {
	APNs *apnsCreds `json:"apns,omitempty"`
	FCM  *fcmCreds  `json:"fcm,omitempty"`
}

const googleTokenURI = "https://oauth2.googleapis.com/token"

var (
	apnsIDRE    = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	bundleRE    = regexp.MustCompile(`^[A-Za-z0-9.-]{1,155}$`)
	apnsTokenRE = regexp.MustCompile(`^[0-9a-fA-F]{64,200}$`)
	fcmTokenRE  = regexp.MustCompile(`^[A-Za-z0-9_:\-.]{20,1000}$`)
	projectRE   = regexp.MustCompile(`^[a-z][a-z0-9-]{4,62}$`)
)

func parseP8(pemText string) (*ecdsa.PrivateKey, error) {
	blk, _ := pem.Decode([]byte(pemText))
	if blk == nil {
		return nil, errors.New("key isn't PEM: send the .p8 file's contents")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("key: %v", err)
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok || ec.Curve.Params().Name != "P-256" {
		return nil, errors.New("key isn't a P-256 key, like the .p8 files Apple issues")
	}
	return ec, nil
}

func parseRSA(pemText string) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode([]byte(pemText))
	if blk == nil {
		return nil, errors.New("private_key isn't PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("private_key: %v", err)
	}
	r, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private_key isn't an RSA key, like a service account's")
	}
	return r, nil
}

func (c *apnsCreds) check() error {
	if !apnsIDRE.MatchString(c.TeamID) || !apnsIDRE.MatchString(c.KeyID) {
		return errors.New("teamId and keyId are the 10-character ids from your Apple developer account")
	}
	if !bundleRE.MatchString(c.BundleID) {
		return errors.New("bundleId is the app's bundle id, like com.example.app")
	}
	_, err := parseP8(c.Key)
	return err
}

func (c *fcmCreds) check() error {
	if !projectRE.MatchString(c.ProjectID) || !strings.Contains(c.ClientEmail, "@") {
		return errors.New("send the Firebase service account JSON as it was downloaded (project_id, client_email, private_key)")
	}
	if c.TokenURI == "" {
		c.TokenURI = googleTokenURI
	}
	if c.TokenURI != googleTokenURI {
		return errors.New("token_uri must be " + googleTokenURI)
	}
	_, err := parseRSA(c.PrivateKey)
	return err
}

// nativeSender holds the tokens it has made, and where it sends (tests point it elsewhere).
type nativeSender struct {
	mu        sync.Mutex
	apnsJWT   map[string]cachedToken // by team+key id
	fcmAccess map[string]cachedToken // by client email
	apnsURL   func(sandbox bool) string
	fcmURL    string
	tokenURL  string // where FCM access tokens come from (the service account's token_uri, checked)
}

type cachedToken struct {
	token string
	until time.Time
}

func newNativeSender() *nativeSender {
	return &nativeSender{apnsJWT: map[string]cachedToken{}, fcmAccess: map[string]cachedToken{},
		apnsURL: func(sandbox bool) string {
			if sandbox {
				return "https://api.sandbox.push.apple.com"
			}
			return "https://api.push.apple.com"
		},
		fcmURL: "https://fcm.googleapis.com", tokenURL: googleTokenURI}
}

func signJWT(header, claims any, sign func(sum []byte) ([]byte, error)) (string, error) {
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	in := b64.EncodeToString(h) + "." + b64.EncodeToString(c)
	sum := sha256.Sum256([]byte(in))
	sig, err := sign(sum[:])
	if err != nil {
		return "", err
	}
	return in + "." + b64.EncodeToString(sig), nil
}

func (n *nativeSender) apnsToken(c *apnsCreds, now time.Time) (string, error) {
	key := c.TeamID + "/" + c.KeyID
	n.mu.Lock()
	t, ok := n.apnsJWT[key]
	n.mu.Unlock()
	if ok && now.Before(t.until) {
		return t.token, nil
	}
	k, err := parseP8(c.Key)
	if err != nil {
		return "", err
	}
	jwt, err := signJWT(map[string]string{"alg": "ES256", "kid": c.KeyID}, map[string]any{"iss": c.TeamID, "iat": now.Unix()},
		func(sum []byte) ([]byte, error) {
			r, s, err := ecdsa.Sign(rand.Reader, k, sum)
			if err != nil {
				return nil, err
			}
			return append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...), nil
		})
	if err != nil {
		return "", err
	}
	n.mu.Lock()
	n.apnsJWT[key] = cachedToken{jwt, now.Add(50 * time.Minute)} // Apple: refresh between 20 and 60 minutes
	n.mu.Unlock()
	return jwt, nil
}

func (n *nativeSender) fcmToken(client *http.Client, c *fcmCreds, now time.Time) (string, error) {
	n.mu.Lock()
	t, ok := n.fcmAccess[c.ClientEmail]
	n.mu.Unlock()
	if ok && now.Before(t.until) {
		return t.token, nil
	}
	k, err := parseRSA(c.PrivateKey)
	if err != nil {
		return "", err
	}
	assertion, err := signJWT(map[string]string{"alg": "RS256", "typ": "JWT"},
		map[string]any{"iss": c.ClientEmail, "scope": "https://www.googleapis.com/auth/firebase.messaging", "aud": googleTokenURI,
			"iat": now.Unix(), "exp": now.Add(time.Hour).Unix()},
		func(sum []byte) ([]byte, error) { return rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, sum) })
	if err != nil {
		return "", err
	}
	resp, err := client.PostForm(n.tokenURL, url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}})
	if err != nil {
		return "", fmt.Errorf("google token: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error_description"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&out)
	if resp.StatusCode != 200 || out.AccessToken == "" {
		return "", fmt.Errorf("google token: %d %s", resp.StatusCode, out.Error)
	}
	life := time.Duration(out.ExpiresIn)*time.Second - 5*time.Minute
	if life < time.Minute {
		life = time.Minute
	}
	n.mu.Lock()
	n.fcmAccess[c.ClientEmail] = cachedToken{out.AccessToken, now.Add(life)}
	n.mu.Unlock()
	return out.AccessToken, nil
}

// alertOf is the payload's title and body, when it has them: they become the notification the system shows.
func alertOf(payload json.RawMessage) (title, body string) {
	var p struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	json.Unmarshal(payload, &p)
	return p.Title, p.Body
}

// sendAPNs sends m to an iOS device. A payload with a title or body shows as an alert; the whole payload rides along
// as "payload" for the app. One without either is a background push.
func (n *nativeSender) sendAPNs(client *http.Client, c *apnsCreds, sub *subscription, m message, now time.Time) (gone bool, err error) {
	jwt, err := n.apnsToken(c, now)
	if err != nil {
		return false, err
	}
	title, text := alertOf(m.Payload)
	aps := map[string]any{}
	pushType, priority := "alert", "10"
	if title != "" || text != "" {
		aps["alert"] = map[string]string{"title": title, "body": text}
		aps["sound"] = "default"
		if m.Urgency == "low" || m.Urgency == "very-low" {
			priority = "5"
		}
	} else {
		aps["content-available"] = 1
		pushType, priority = "background", "5" // Apple requires 5 for background pushes
	}
	body, _ := json.Marshal(map[string]any{"aps": aps, "payload": m.Payload})
	req, _ := http.NewRequest("POST", n.apnsURL(sub.Sandbox)+"/3/device/"+sub.Token, bytes.NewReader(body))
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", c.BundleID)
	req.Header.Set("apns-push-type", pushType)
	req.Header.Set("apns-priority", priority)
	ttl := m.TTL
	if ttl == 0 {
		ttl = 24 * 3600
	}
	req.Header.Set("apns-expiration", fmt.Sprint(now.Add(time.Duration(ttl)*time.Second).Unix()))
	if m.Topic != "" {
		req.Header.Set("apns-collapse-id", m.Topic)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("apns: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Reason string `json:"reason"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 2048)).Decode(&out)
	switch {
	case resp.StatusCode == 200:
		return false, nil
	case resp.StatusCode == 410 || out.Reason == "BadDeviceToken" || out.Reason == "Unregistered" || out.Reason == "DeviceTokenNotForTopic":
		return true, nil
	case out.Reason == "ExpiredProviderToken" || out.Reason == "InvalidProviderToken":
		n.mu.Lock()
		delete(n.apnsJWT, c.TeamID+"/"+c.KeyID)
		n.mu.Unlock()
	}
	return false, fmt.Errorf("apns answered %d: %s", resp.StatusCode, out.Reason)
}

// sendFCM sends m to an Android device. A payload with a title or body also shows as a notification; the whole
// payload is in the data as "payload", a JSON string (FCM data values are strings).
func (n *nativeSender) sendFCM(client *http.Client, c *fcmCreds, sub *subscription, m message, now time.Time) (gone bool, err error) {
	access, err := n.fcmToken(client, c, now)
	if err != nil {
		return false, err
	}
	android := map[string]any{"priority": "HIGH"}
	if m.Urgency == "low" || m.Urgency == "very-low" {
		android["priority"] = "NORMAL"
	}
	ttl := m.TTL
	if ttl == 0 {
		ttl = 24 * 3600
	}
	android["ttl"] = fmt.Sprintf("%ds", ttl)
	if m.Topic != "" {
		android["collapse_key"] = m.Topic
	}
	msg := map[string]any{"token": sub.Token, "data": map[string]string{"payload": string(m.Payload)}, "android": android}
	if title, text := alertOf(m.Payload); title != "" || text != "" {
		msg["notification"] = map[string]string{"title": title, "body": text}
	}
	body, _ := json.Marshal(map[string]any{"message": msg})
	req, _ := http.NewRequest("POST", n.fcmURL+"/v1/projects/"+c.ProjectID+"/messages:send", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("fcm: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == 200:
		return false, nil
	case resp.StatusCode == 404 || bytes.Contains(raw, []byte("UNREGISTERED")):
		return true, nil
	case resp.StatusCode == 401:
		n.mu.Lock()
		delete(n.fcmAccess, c.ClientEmail)
		n.mu.Unlock()
	}
	return false, fmt.Errorf("fcm answered %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
}
