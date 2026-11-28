package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeNative answers as APNs, Google's token endpoint and FCM do, and keeps what it was sent.
type fakeNative struct {
	apnsStatus int
	apnsReason string
	fcmStatus  int
	fcmBody    string
	tokens     int // access tokens handed out
	reqs       []*http.Request
	bodies     []string
}

func (f *fakeNative) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	f.reqs, f.bodies = append(f.reqs, r), append(f.bodies, string(b))
	resp := func(code int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	}
	switch {
	case strings.HasSuffix(r.URL.Host, "push.apple.com"):
		if f.apnsStatus == 200 {
			return resp(200, "")
		}
		return resp(f.apnsStatus, `{"reason":"`+f.apnsReason+`"}`)
	case r.URL.Host == "oauth2.googleapis.com":
		f.tokens++
		return resp(200, `{"access_token":"ya29.test","expires_in":3599,"token_type":"Bearer"}`)
	case r.URL.Host == "fcm.googleapis.com":
		return resp(f.fcmStatus, f.fcmBody)
	}
	return resp(500, "unexpected host "+r.URL.Host)
}

func p8(t *testing.T) (*ecdsa.PrivateKey, string) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	return k, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func serviceAccount(t *testing.T) (*rsa.PrivateKey, string) {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	j, _ := json.Marshal(map[string]string{"type": "service_account", "project_id": "kinlog-app", "client_email": "push@kinlog-app.iam.gserviceaccount.com",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "token_uri": googleTokenURI})
	return k, string(j)
}

// jwtParts checks a JWT's signature with verify and returns its header and claims.
func jwtParts(t *testing.T, jwt string, verify func(sum, sig []byte) bool) (map[string]any, map[string]any) {
	t.Helper()
	p := strings.Split(jwt, ".")
	if len(p) != 3 {
		t.Fatalf("not a JWT: %q", jwt)
	}
	sig, _ := b64.DecodeString(p[2])
	sum := sha256.Sum256([]byte(p[0] + "." + p[1]))
	if !verify(sum[:], sig) {
		t.Fatal("JWT signature doesn't verify")
	}
	var h, c map[string]any
	hb, _ := b64.DecodeString(p[0])
	cb, _ := b64.DecodeString(p[1])
	json.Unmarshal(hb, &h)
	json.Unmarshal(cb, &c)
	return h, c
}

func nativeSetup(t *testing.T) (*server, *fakeNative) {
	s, _ := setup(t)
	f := &fakeNative{apnsStatus: 200, fcmStatus: 200, fcmBody: `{"name":"projects/kinlog-app/messages/1"}`}
	s.client = &http.Client{Transport: f}
	return s, f
}

func subscribeNative(t *testing.T, s *server, app, body string) (handle, secret string) {
	t.Helper()
	w := call(s, "POST", "/v1/subscribe/"+app, "", body)
	if w.Code != 200 {
		t.Fatalf("subscribe %s: %d %s", body, w.Code, w.Body)
	}
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	return out["handle"], out["secret"]
}

const iosToken = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

func TestAPNs(t *testing.T) {
	s, f := nativeSetup(t)
	key, keyPEM := p8(t)
	if w := call(s, "POST", "/v1/subscribe/kinlog", "", `{"apns":{"token":"`+iosToken+`"}}`); w.Code != 404 {
		t.Fatalf("subscribe before credentials: %d", w.Code)
	}
	creds := func(team, pemText string) string {
		j, _ := json.Marshal(map[string]string{"teamId": team, "keyId": "ABC123DEFG", "key": pemText, "bundleId": "com.sparkstone.kinlog"})
		return string(j)
	}
	if w := call(s, "PUT", "/v1/native/apns", "kinlog", creds("TEAM123456", "not a key")); w.Code != 400 {
		t.Fatalf("a bad key was taken: %d", w.Code)
	}
	if w := call(s, "PUT", "/v1/native/apns", "", creds("TEAM123456", keyPEM)); w.Code != 401 {
		t.Fatalf("credentials without nook's token check: %d", w.Code)
	}
	if w := call(s, "PUT", "/v1/native/apns", "kinlog", creds("TEAM123456", keyPEM)); w.Code != 200 {
		t.Fatalf("credentials: %d %s", w.Code, w.Body)
	}
	if w := call(s, "GET", "/v1/config", "kinlog", ""); !strings.Contains(w.Body.String(), `"apns":true`) {
		t.Fatalf("config doesn't say apns: %s", w.Body)
	}
	h, secret := subscribeNative(t, s, "kinlog", `{"apns":{"token":"`+strings.ToUpper(iosToken)+`","sandbox":true}}`)
	if again, _ := subscribeNative(t, s, "kinlog", `{"apns":{"token":"`+iosToken+`","sandbox":true}}`); again != h {
		t.Fatal("the same device got a new handle")
	}

	if w := call(s, "POST", "/v1/send/"+h, "", `{"payload":{"title":"Feed time","body":"It's been 3 hours","url":"/log"},"topic":"feed","ttl":600}`); w.Code != 200 {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	r := f.reqs[len(f.reqs)-1]
	if r.URL.String() != "https://api.sandbox.push.apple.com/3/device/"+iosToken {
		t.Fatalf("sent to %s", r.URL)
	}
	if r.Header.Get("apns-topic") != "com.sparkstone.kinlog" || r.Header.Get("apns-push-type") != "alert" || r.Header.Get("apns-priority") != "10" || r.Header.Get("apns-collapse-id") != "feed" {
		t.Fatalf("headers: %v", r.Header)
	}
	head, claims := jwtParts(t, strings.TrimPrefix(r.Header.Get("authorization"), "bearer "), func(sum, sig []byte) bool {
		return len(sig) == 64 && ecdsa.Verify(&key.PublicKey, sum, new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
	})
	if head["alg"] != "ES256" || head["kid"] != "ABC123DEFG" || claims["iss"] != "TEAM123456" {
		t.Fatalf("jwt: %v %v", head, claims)
	}
	var body struct {
		APS     map[string]any  `json:"aps"`
		Payload json.RawMessage `json:"payload"`
	}
	json.Unmarshal([]byte(f.bodies[len(f.bodies)-1]), &body)
	if alert, _ := body.APS["alert"].(map[string]any); alert["title"] != "Feed time" || !strings.Contains(string(body.Payload), `"url":"/log"`) {
		t.Fatalf("body: %s", f.bodies[len(f.bodies)-1])
	}
	firstJWT := r.Header.Get("authorization")

	// no title or body: a background push; the provider token is reused
	call(s, "POST", "/v1/send/"+h, "", `{"payload":{"sync":true}}`)
	r = f.reqs[len(f.reqs)-1]
	if r.Header.Get("apns-push-type") != "background" || r.Header.Get("apns-priority") != "5" || !strings.Contains(f.bodies[len(f.bodies)-1], `"content-available":1`) {
		t.Fatalf("background: %v %s", r.Header, f.bodies[len(f.bodies)-1])
	}
	if r.Header.Get("authorization") != firstJWT {
		t.Fatal("made a new provider token for every push (Apple throttles that)")
	}

	// the app was deleted from the phone: Apple says so, the handle goes
	f.apnsStatus, f.apnsReason = 410, "Unregistered"
	if w := call(s, "POST", "/v1/send/"+h, "", `{"payload":{"title":"x"}}`); w.Code != 410 {
		t.Fatalf("unregistered: %d", w.Code)
	}
	if w := call(s, "DELETE", "/v1/subscriptions/"+h, "", "", "X-Push-Secret", secret); w.Code != 404 {
		t.Fatalf("the dropped handle is still there: %d", w.Code)
	}
}

func TestFCM(t *testing.T) {
	s, f := nativeSetup(t)
	key, account := serviceAccount(t)
	bad := strings.Replace(account, googleTokenURI, "https://evil.example.com/token", 1)
	if w := call(s, "PUT", "/v1/native/fcm", "kinlog", bad); w.Code != 400 {
		t.Fatalf("a token_uri elsewhere was taken: %d", w.Code)
	}
	if w := call(s, "PUT", "/v1/native/fcm", "kinlog", account); w.Code != 200 {
		t.Fatalf("credentials: %d %s", w.Code, w.Body)
	}
	token := "dGVzdC1kZXZpY2U:APA91bH-test_token.value-1234567890"
	h, _ := subscribeNative(t, s, "kinlog", `{"fcm":{"token":"`+token+`"}}`)

	for i := 0; i < 2; i++ {
		if w := call(s, "POST", "/v1/send/"+h, "", `{"payload":{"title":"Feed time","url":"/log"},"topic":"feed","urgency":"low"}`); w.Code != 200 {
			t.Fatalf("send: %d %s", w.Code, w.Body)
		}
	}
	if f.tokens != 1 {
		t.Fatalf("asked Google for %d access tokens for 2 pushes", f.tokens)
	}
	tokReq := f.reqs[0]
	form, _ := url.ParseQuery(f.bodies[0])
	if tokReq.URL.String() != googleTokenURI || form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		t.Fatalf("token request: %s %s", tokReq.URL, f.bodies[0])
	}
	_, claims := jwtParts(t, form.Get("assertion"), func(sum, sig []byte) bool {
		return rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum, sig) == nil
	})
	if claims["iss"] != "push@kinlog-app.iam.gserviceaccount.com" || claims["aud"] != googleTokenURI || claims["scope"] != "https://www.googleapis.com/auth/firebase.messaging" {
		t.Fatalf("assertion claims: %v", claims)
	}
	send := f.reqs[len(f.reqs)-1]
	if send.URL.String() != "https://fcm.googleapis.com/v1/projects/kinlog-app/messages:send" || send.Header.Get("Authorization") != "Bearer ya29.test" {
		t.Fatalf("send: %s %v", send.URL, send.Header)
	}
	var body struct {
		Message struct {
			Token        string            `json:"token"`
			Data         map[string]string `json:"data"`
			Notification map[string]string `json:"notification"`
			Android      map[string]any    `json:"android"`
		} `json:"message"`
	}
	json.Unmarshal([]byte(f.bodies[len(f.bodies)-1]), &body)
	m := body.Message
	if m.Token != token || !strings.Contains(m.Data["payload"], `"url":"/log"`) || m.Notification["title"] != "Feed time" ||
		m.Android["collapse_key"] != "feed" || m.Android["priority"] != "NORMAL" || m.Android["ttl"] != "86400s" {
		t.Fatalf("message: %s", f.bodies[len(f.bodies)-1])
	}

	// scheduled pushes go the same way
	at := s.now().Add(time.Minute).UTC().Format(time.RFC3339)
	if w := call(s, "POST", "/v1/send/"+h, "", `{"payload":{"title":"Later"},"at":"`+at+`"}`); w.Code != 202 {
		t.Fatalf("schedule: %d %s", w.Code, w.Body)
	}
	s.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	before := len(f.reqs)
	s.runDue()
	if len(f.reqs) != before+1 || !strings.Contains(f.bodies[len(f.bodies)-1], `Later`) {
		t.Fatal("the scheduled push wasn't sent")
	}

	f.fcmStatus, f.fcmBody = 404, `{"error":{"status":"NOT_FOUND","details":[{"errorCode":"UNREGISTERED"}]}}`
	if w := call(s, "POST", "/v1/send/"+h, "", `{"payload":{"title":"x"}}`); w.Code != 410 {
		t.Fatalf("unregistered: %d", w.Code)
	}
}

func TestNativeRemoveDropsDevices(t *testing.T) {
	s, _ := nativeSetup(t)
	_, keyPEM := p8(t)
	j, _ := json.Marshal(map[string]string{"teamId": "TEAM123456", "keyId": "ABC123DEFG", "key": keyPEM, "bundleId": "com.sparkstone.kinlog"})
	call(s, "PUT", "/v1/native/apns", "kinlog", string(j))
	h, _ := subscribeNative(t, s, "kinlog", `{"apns":{"token":"`+iosToken+`"}}`)
	if w := call(s, "DELETE", "/v1/native/apns", "kinlog", ""); w.Code != 200 {
		t.Fatalf("remove: %d", w.Code)
	}
	if w := call(s, "POST", "/v1/send/"+h, "", `{"payload":{"title":"x"}}`); w.Code != 410 {
		t.Fatalf("a device of removed credentials still takes pushes: %d", w.Code)
	}
	if w := call(s, "POST", "/v1/subscribe/kinlog", "", `{"apns":{"token":"nothex"}}`); w.Code != 400 {
		t.Fatalf("a bad token was taken: %d", w.Code)
	}
}

// A device that subscribes with keys gets only ciphertext through Apple and Google, which it can decrypt.
func TestNativeEncrypted(t *testing.T) {
	s, f := nativeSetup(t)
	_, keyPEM := p8(t)
	j, _ := json.Marshal(map[string]string{"teamId": "TEAM123456", "keyId": "ABC123DEFG", "key": keyPEM, "bundleId": "com.sparkstone.kinlog"})
	call(s, "PUT", "/v1/native/apns", "kinlog", string(j))
	_, account := serviceAccount(t)
	call(s, "PUT", "/v1/native/fcm", "kinlog", account)
	keys := func(b *browser) string {
		return `"keys":{"p256dh":"` + b64.EncodeToString(b.priv.PublicKey().Bytes()) + `","auth":"` + b64.EncodeToString(b.auth) + `"}`
	}
	if w := call(s, "POST", "/v1/subscribe/kinlog", "", `{"apns":{"token":"`+iosToken+`"},"keys":{"p256dh":"short","auth":"x"}}`); w.Code != 400 {
		t.Fatalf("bad keys taken: %d", w.Code)
	}
	phone, droid := newBrowser(t), newBrowser(t)
	hi, _ := subscribeNative(t, s, "kinlog", `{"apns":{"token":"`+iosToken+`"},`+keys(phone)+`}`)
	ha, _ := subscribeNative(t, s, "kinlog", `{"fcm":{"token":"dGVzdC1kZXZpY2U:APA91bH-test_token.value-1234567890"},`+keys(droid)+`}`)
	payload := `{"title":"Feed time","body":"It's been 3 hours","url":"/log"}`

	if w := call(s, "POST", "/v1/send/"+hi, "", `{"payload":`+payload+`}`); w.Code != 200 {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	body := f.bodies[len(f.bodies)-1]
	if strings.Contains(body, "Feed time") || strings.Contains(body, "/log") {
		t.Fatalf("Apple could read it: %s", body)
	}
	var apns struct {
		APS map[string]any `json:"aps"`
		Enc string         `json:"enc"`
	}
	json.Unmarshal([]byte(body), &apns)
	if apns.APS["mutable-content"] != float64(1) || apns.APS["alert"] == nil {
		t.Fatalf("the extension wouldn't run: %s", body)
	}
	ct, _ := b64.DecodeString(apns.Enc)
	if got := phone.decrypt(t, ct); string(got) != payload {
		t.Fatalf("iOS decrypts %q", got)
	}

	if w := call(s, "POST", "/v1/send/"+ha, "", `{"payload":`+payload+`,"urgency":"low"}`); w.Code != 200 {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	body = f.bodies[len(f.bodies)-1]
	var fcm struct {
		Message struct {
			Data         map[string]string `json:"data"`
			Notification map[string]string `json:"notification"`
			Android      map[string]any    `json:"android"`
		} `json:"message"`
	}
	json.Unmarshal([]byte(body), &fcm)
	if strings.Contains(body, "Feed time") || fcm.Message.Notification != nil || fcm.Message.Android["priority"] != "HIGH" {
		t.Fatalf("Google could read or show it, or it won't wake the app: %s", body)
	}
	ct, _ = b64.DecodeString(fcm.Message.Data["enc"])
	if got := droid.decrypt(t, ct); string(got) != payload {
		t.Fatalf("Android decrypts %q", got)
	}

	big := `{"title":"` + strings.Repeat("x", maxEncryptedNative) + `"}`
	if w := call(s, "POST", "/v1/send/"+hi, "", `{"payload":`+big+`}`); w.Code != 400 {
		t.Fatalf("an encrypted push too big for Apple was taken: %d", w.Code)
	}
}
