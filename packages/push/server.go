package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Push services browsers use, and ntfy.sh, the public UnifiedPush distributor. An endpoint anywhere else is refused,
// so this can't be made to POST to any URL; a self-hosted distributor goes in PUSH_ENDPOINT_HOSTS.
var pushHosts = []string{"fcm.googleapis.com", "push.services.mozilla.com", "push.apple.com", "notify.windows.com", "ntfy.sh"}

const (
	maxPayload  = 3072 // push services take about 4 KB after encryption
	maxAhead    = 30 * 24 * time.Hour
	maxPending  = 100 // scheduled pushes per handle
	subsPerHour = 30  // new subscriptions per IP
)

var (
	appRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	handleRE  = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	topicRE   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	urgencies = map[string]bool{"": true, "very-low": true, "low": true, "normal": true, "high": true}
)

type server struct {
	st     *store
	cfg    config
	client *http.Client
	now    func() time.Time
	mu     sync.Mutex
	hits   map[string][]time.Time // rate limits: key -> recent times
	native *nativeSender
}

func newServer(st *store, cfg config) *server {
	return &server{st: st, cfg: cfg, client: &http.Client{Timeout: 15 * time.Second}, now: time.Now, hits: map[string][]time.Time{}, native: newNativeSender()}
}

// allow records a hit for key and says whether it's within max in the last hour.
func (s *server) allow(key string, max int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	recent := s.hits[key][:0]
	for _, t := range s.hits[key] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if len(recent) >= max {
		s.hits[key] = recent
		return false
	}
	s.hits[key] = append(recent, now)
	if len(s.hits) > 50000 { // forget the oldest-looking keys rather than grow forever
		for k, v := range s.hits {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > time.Hour {
				delete(s.hits, k)
			}
		}
	}
	return true
}

func clientIP(r *http.Request) string {
	// Caddy sets X-Forwarded-For, replacing whatever the client sent; the last entry is the client it saw.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	open := p == "/v1/health" || strings.HasPrefix(p, "/v1/subscribe/") || strings.HasPrefix(p, "/v1/subscriptions/") || strings.HasPrefix(p, "/v1/send/")
	if open {
		// browsers call these from the app's own origin
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Push-Secret")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	switch {
	case p == "/v1/health":
		writeJSON(w, 200, map[string]bool{"ok": true})
	case p == "/v1/config" && r.Method == "GET":
		s.appOnly(w, r, s.getConfig)
	case p == "/v1/keys" && r.Method == "PUT":
		s.appOnly(w, r, s.putKeys)
	case p == "/v1/stats" && r.Method == "GET":
		s.appOnly(w, r, s.stats)
	case len(parts) == 3 && parts[1] == "native" && (parts[2] == "apns" || parts[2] == "fcm") && (r.Method == "PUT" || r.Method == "DELETE"):
		s.appOnly(w, r, func(w http.ResponseWriter, r *http.Request, app string) { s.putNative(w, r, app, parts[2]) })
	case len(parts) == 3 && parts[1] == "subscribe" && r.Method == "POST":
		s.subscribe(w, r, parts[2])
	case len(parts) == 3 && parts[1] == "subscriptions" && r.Method == "DELETE":
		s.unsubscribe(w, r, parts[2])
	case len(parts) == 3 && parts[1] == "send" && r.Method == "POST":
		s.send(w, r, parts[2])
	case len(parts) == 4 && parts[1] == "send" && r.Method == "DELETE":
		s.cancel(w, r, parts[2], parts[3])
	default:
		fail(w, 404, "not found")
	}
}

// appOnly runs f for requests that came through nook's token check, which sets X-Nook-App.
func (s *server) appOnly(w http.ResponseWriter, r *http.Request, f func(http.ResponseWriter, *http.Request, string)) {
	app := r.Header.Get("X-Nook-App")
	if !appRE.MatchString(app) {
		fail(w, 401, "send your app's NOOK_TOKEN: Authorization: Bearer $NOOK_TOKEN")
		return
	}
	f(w, r, app)
}

// keyFor returns app's signing key, making one the first time; the caller holds st.mu.
func (s *server) keyFor(app string, create bool) (*vapidKey, error) {
	if k := s.st.Apps[app]; k != nil {
		return parseVAPIDKey(k.Private, k.Public)
	}
	if !create {
		return nil, nil
	}
	k, err := newVAPIDKey()
	if err != nil {
		return nil, err
	}
	s.st.Apps[app] = &appKeys{Public: b64.EncodeToString(k.Public()), Private: b64.EncodeToString(k.Private()), Created: s.now().UTC()}
	return k, s.st.save()
}

// GET /v1/config (token): what an app's build needs. Makes the app's key pair the first time.
func (s *server) getConfig(w http.ResponseWriter, r *http.Request, app string) {
	s.st.mu.Lock()
	k, err := s.keyFor(app, true)
	s.st.mu.Unlock()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	s.st.mu.Lock()
	nat := s.st.Native[app]
	s.st.mu.Unlock()
	writeJSON(w, 200, map[string]any{
		"app":       app,
		"publicKey": b64.EncodeToString(k.Public()),
		"subscribe": "https://" + r.Host + "/v1/subscribe/" + app,
		"send":      "https://" + r.Host + "/v1/send/",
		"subject":   s.cfg.Subject,
		"native":    map[string]bool{"apns": nat != nil && nat.APNs != nil, "fcm": nat != nil && nat.FCM != nil},
	})
}

// PUT /v1/keys (token): bring an existing key pair, so browsers already subscribed with it keep working.
func (s *server) putKeys(w http.ResponseWriter, r *http.Request, app string) {
	var in struct {
		PublicKey  string `json:"publicKey"`
		PrivateKey string `json:"privateKey"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		fail(w, 400, "send {\"publicKey\": \"...\", \"privateKey\": \"...\"} (base64url)")
		return
	}
	k, err := parseVAPIDKey(in.PrivateKey, in.PublicKey)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	if old := s.st.Apps[app]; old != nil && old.Public != b64.EncodeToString(k.Public()) {
		n := 0
		for _, sub := range s.st.Subs {
			if sub.App == app {
				n++
			}
		}
		if n > 0 && r.URL.Query().Get("replace") != "1" {
			fail(w, 409, fmt.Sprintf("%s already has a different key and %d subscriptions that only work with it; add ?replace=1 to drop them", app, n))
			return
		}
		for h, sub := range s.st.Subs {
			if sub.App == app {
				s.st.dropSub(h)
			}
		}
	}
	s.st.Apps[app] = &appKeys{Public: b64.EncodeToString(k.Public()), Private: b64.EncodeToString(k.Private()), Created: s.now().UTC()}
	if err := s.st.save(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"publicKey": b64.EncodeToString(k.Public())})
}

// GET /v1/stats (token)
func (s *server) stats(w http.ResponseWriter, r *http.Request, app string) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	subs, jobs := 0, 0
	kinds := map[string]int{"webpush": 0, "apns": 0, "fcm": 0}
	for _, sub := range s.st.Subs {
		if sub.App == app {
			subs++
			kinds[cmpOr(sub.Kind, "webpush")]++
		}
	}
	for _, j := range s.st.Jobs {
		if sub := s.st.Subs[j.Handle]; sub != nil && sub.App == app {
			jobs++
		}
	}
	nat := s.st.Native[app]
	writeJSON(w, 200, map[string]any{"app": app, "subscriptions": subs, "byKind": kinds, "scheduled": jobs, "hasKey": s.st.Apps[app] != nil,
		"native": map[string]bool{"apns": nat != nil && nat.APNs != nil, "fcm": nat != nil && nat.FCM != nil}})
}

func (s *server) endpointAllowed(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Port() != "" || u.User != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range append(append([]string{}, pushHosts...), s.cfg.Extra...) {
		h = strings.ToLower(h)
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// POST /v1/subscribe/APP (open): a browser's PushSubscription. Returns the handle to share and the secret to keep.
func (s *server) subscribe(w http.ResponseWriter, r *http.Request, app string) {
	if !s.allow("sub:"+clientIP(r), subsPerHour) {
		fail(w, 429, "too many subscriptions from here; try again in an hour")
		return
	}
	var in struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
		APNs *struct {
			Token   string `json:"token"`
			Sandbox bool   `json:"sandbox"`
		} `json:"apns"`
		FCM *struct {
			Token string `json:"token"`
		} `json:"fcm"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in); err != nil {
		fail(w, 400, "send the browser's PushSubscription as JSON (subscription.toJSON()), or {\"apns\": {\"token\"}} or {\"fcm\": {\"token\"}} from a native app")
		return
	}
	switch {
	case in.APNs != nil:
		s.subscribeNative(w, app, "apns", strings.ToLower(in.APNs.Token), in.APNs.Sandbox)
		return
	case in.FCM != nil:
		s.subscribeNative(w, app, "fcm", in.FCM.Token, false)
		return
	}
	if !s.endpointAllowed(in.Endpoint) {
		fail(w, 400, "that endpoint isn't a known push service")
		return
	}
	p, err1 := decodeKey(in.Keys.P256dh)
	a, err2 := decodeKey(in.Keys.Auth)
	if err1 != nil || err2 != nil || len(p) != 65 || len(a) != 16 {
		fail(w, 400, "keys.p256dh and keys.auth are missing or the wrong size")
		return
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	if !appRE.MatchString(app) || s.st.Apps[app] == nil {
		fail(w, 404, "no such app here: its build makes its key with GET /v1/config first")
		return
	}
	secret := randomID(32)
	handle := ""
	for h, sub := range s.st.Subs { // the same browser again: same handle, new secret
		if sub.App == app && sub.Kind == "" && sub.Endpoint == in.Endpoint {
			handle = h
		}
	}
	if handle == "" {
		handle = randomID(32)
	}
	s.st.Subs[handle] = &subscription{App: app, Endpoint: in.Endpoint, P256dh: b64.EncodeToString(p), Auth: b64.EncodeToString(a), Secret: hashSecret(secret), Created: s.now().UTC()}
	if err := s.st.save(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"handle": handle, "secret": secret})
}

// DELETE /v1/subscriptions/HANDLE (open) with X-Push-Secret
func (s *server) unsubscribe(w http.ResponseWriter, r *http.Request, handle string) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	sub := s.st.Subs[handle]
	if sub == nil || !secretMatches(sub.Secret, r.Header.Get("X-Push-Secret")) {
		fail(w, 404, "no such subscription, or the wrong X-Push-Secret")
		return
	}
	s.st.dropSub(handle)
	s.st.save()
	w.WriteHeader(204)
}

func (m *message) check() string {
	if len(m.Payload) == 0 {
		return "payload is required: whatever your service worker expects, as JSON"
	}
	if len(m.Payload) > maxPayload {
		return fmt.Sprintf("payload is %d bytes; the limit is %d", len(m.Payload), maxPayload)
	}
	if !urgencies[m.Urgency] {
		return "urgency is very-low, low, normal or high"
	}
	if m.Topic != "" && !topicRE.MatchString(m.Topic) {
		return "topic is up to 32 letters, digits, - and _"
	}
	if m.TTL < 0 || m.TTL > 28*24*3600 {
		return "ttl is seconds, up to 28 days"
	}
	return ""
}

// POST /v1/send/HANDLE (open): {"payload": ..., "at": "2026-11-20T09:00:00Z"?, "topic"?, "ttl"?, "urgency"?}
func (s *server) send(w http.ResponseWriter, r *http.Request, handle string) {
	var in struct {
		message
		At string `json:"at"`
	}
	if !handleRE.MatchString(handle) {
		fail(w, 404, "no such handle")
		return
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxPayload+2048)).Decode(&in); err != nil {
		fail(w, 400, "send JSON: {\"payload\": ...}")
		return
	}
	if msg := in.message.check(); msg != "" {
		fail(w, 400, msg)
		return
	}
	s.st.mu.Lock()
	sub := s.st.Subs[handle]
	s.st.mu.Unlock()
	if sub == nil {
		fail(w, 410, "that handle is gone: the browser unsubscribed or its subscription expired")
		return
	}
	if !s.allow("send:"+handle, s.cfg.SendsPerHour) {
		fail(w, 429, "too many pushes to this handle; try again later")
		return
	}
	if in.At != "" {
		at, err := time.Parse(time.RFC3339, in.At)
		if err != nil {
			fail(w, 400, "at is an RFC 3339 time, like 2026-11-20T09:00:00Z")
			return
		}
		if at.After(s.now().Add(5 * time.Second)) {
			s.schedule(w, handle, at, in.message)
			return
		}
	}
	gone, err := s.deliver(handle, in.message)
	switch {
	case gone:
		fail(w, 410, "that handle is gone: the browser unsubscribed or its subscription expired")
	case err != nil:
		fail(w, 502, err.Error())
	default:
		writeJSON(w, 200, map[string]bool{"sent": true})
	}
}

func (s *server) schedule(w http.ResponseWriter, handle string, at time.Time, m message) {
	if at.After(s.now().Add(maxAhead)) {
		fail(w, 400, "at is more than 30 days away")
		return
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	keep, pending := s.st.Jobs[:0], 0
	for _, j := range s.st.Jobs {
		if j.Handle == handle && m.Topic != "" && j.Msg.Topic == m.Topic {
			continue // a newer one with the same topic replaces it
		}
		if j.Handle == handle {
			pending++
		}
		keep = append(keep, j)
	}
	s.st.Jobs = keep
	if pending >= maxPending {
		fail(w, 429, fmt.Sprintf("this handle already has %d scheduled pushes", pending))
		return
	}
	j := &job{ID: randomID(12), Handle: handle, At: at.UTC(), Msg: m}
	s.st.Jobs = append(s.st.Jobs, j)
	sort.Slice(s.st.Jobs, func(a, b int) bool { return s.st.Jobs[a].At.Before(s.st.Jobs[b].At) })
	if err := s.st.save(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 202, map[string]string{"scheduled": j.ID, "at": j.At.Format(time.RFC3339)})
}

// DELETE /v1/send/HANDLE/ID-or-topic (open): cancel a scheduled push.
func (s *server) cancel(w http.ResponseWriter, r *http.Request, handle, id string) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	keep, n := s.st.Jobs[:0], 0
	for _, j := range s.st.Jobs {
		if j.Handle == handle && (j.ID == id || j.Msg.Topic == id) {
			n++
			continue
		}
		keep = append(keep, j)
	}
	s.st.Jobs = keep
	if n == 0 {
		fail(w, 404, "nothing scheduled with that id or topic")
		return
	}
	s.st.save()
	writeJSON(w, 200, map[string]int{"cancelled": n})
}

// deliver encrypts m for handle's browser and posts it to its push service. gone means the subscription is dead
// (and has been dropped).
func (s *server) deliver(handle string, m message) (gone bool, err error) {
	s.st.mu.Lock()
	sub := s.st.Subs[handle]
	var k *vapidKey
	if sub != nil {
		k, err = s.keyFor(sub.App, false)
	}
	s.st.mu.Unlock()
	if sub == nil {
		return true, nil
	}
	if sub.Kind != "" {
		return s.deliverNative(handle, sub, m)
	}
	if err != nil || k == nil {
		return false, fmt.Errorf("%s has no key", sub.App)
	}
	if s.cfg.Subject == "" {
		return false, fmt.Errorf("the push server has no PUSH_SUBJECT set")
	}
	p, _ := decodeKey(sub.P256dh)
	a, _ := decodeKey(sub.Auth)
	body, err := encrypt(m.Payload, p, a)
	if err != nil {
		return false, err
	}
	auth, err := k.header(sub.Endpoint, s.cfg.Subject, s.now())
	if err != nil {
		return false, err
	}
	req, _ := http.NewRequest("POST", sub.Endpoint, bytes.NewReader(body))
	ttl := m.TTL
	if ttl == 0 {
		ttl = 24 * 3600
	}
	req.Header.Set("TTL", fmt.Sprint(ttl))
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", auth)
	if m.Urgency != "" {
		req.Header.Set("Urgency", m.Urgency)
	}
	if m.Topic != "" {
		req.Header.Set("Topic", m.Topic)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("push service: %v", err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode == 404 || resp.StatusCode == 410:
		s.st.mu.Lock()
		s.st.dropSub(handle)
		s.st.save()
		s.st.mu.Unlock()
		return true, nil
	case resp.StatusCode >= 300:
		return false, fmt.Errorf("push service answered %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return false, nil
}

// runScheduler sends scheduled pushes when they're due. A failure is tried again 1, 5 and 30 minutes later.
func (s *server) runScheduler(every time.Duration) {
	for {
		s.runDue()
		time.Sleep(every)
	}
}

func (s *server) runDue() {
	now := s.now()
	s.st.mu.Lock()
	var due []*job
	keep := s.st.Jobs[:0]
	for _, j := range s.st.Jobs {
		if !j.At.After(now) {
			due = append(due, j)
		} else {
			keep = append(keep, j)
		}
	}
	s.st.Jobs = keep
	if len(due) > 0 {
		s.st.save()
	}
	s.st.mu.Unlock()
	retry := []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}
	for _, j := range due {
		gone, err := s.deliver(j.Handle, j.Msg)
		if gone || err == nil || j.Tries >= len(retry) {
			continue
		}
		j.At, j.Tries = now.Add(retry[j.Tries]), j.Tries+1
		s.st.mu.Lock()
		if s.st.Subs[j.Handle] != nil {
			s.st.Jobs = append(s.st.Jobs, j)
			s.st.save()
		}
		s.st.mu.Unlock()
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// PUT /v1/native/apns or /v1/native/fcm (token): the app's credentials for native push. APNs takes {"teamId",
// "keyId", "key" (the .p8 file's text), "bundleId"}; FCM takes the Firebase service account JSON as downloaded.
// DELETE removes them, and with them the devices subscribed through that service.
func (s *server) putNative(w http.ResponseWriter, r *http.Request, app, kind string) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	n := s.st.Native[app]
	if n == nil {
		n = &native{}
	}
	if r.Method == "DELETE" {
		if kind == "apns" {
			n.APNs = nil
		} else {
			n.FCM = nil
		}
		for h, sub := range s.st.Subs {
			if sub.App == app && sub.Kind == kind {
				s.st.dropSub(h)
			}
		}
	} else {
		body := io.LimitReader(r.Body, 16384)
		var err error
		if kind == "apns" {
			var c apnsCreds
			if err = json.NewDecoder(body).Decode(&c); err == nil {
				if err = c.check(); err == nil {
					n.APNs = &c
				}
			}
		} else {
			var c fcmCreds
			if err = json.NewDecoder(body).Decode(&c); err == nil {
				if err = c.check(); err == nil {
					n.FCM = &c
				}
			}
		}
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
	}
	s.st.Native[app] = n
	if n.APNs == nil && n.FCM == nil {
		delete(s.st.Native, app)
	}
	if err := s.st.save(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"apns": n.APNs != nil, "fcm": n.FCM != nil})
}

// subscribeNative: a native app's device token. Same handle for the same device again, like a browser.
func (s *server) subscribeNative(w http.ResponseWriter, app, kind, token string, sandbox bool) {
	if (kind == "apns" && !apnsTokenRE.MatchString(token)) || (kind == "fcm" && !fcmTokenRE.MatchString(token)) {
		fail(w, 400, "that isn't a "+kind+" device token")
		return
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	n := s.st.Native[app]
	if !appRE.MatchString(app) || n == nil || (kind == "apns" && n.APNs == nil) || (kind == "fcm" && n.FCM == nil) {
		fail(w, 404, app+" has no "+kind+" credentials here: its build sends them with PUT /v1/native/"+kind+" first")
		return
	}
	secret := randomID(32)
	handle := ""
	for h, sub := range s.st.Subs {
		if sub.App == app && sub.Kind == kind && sub.Token == token {
			handle = h
		}
	}
	if handle == "" {
		handle = randomID(32)
	}
	s.st.Subs[handle] = &subscription{App: app, Kind: kind, Token: token, Sandbox: sandbox, Secret: hashSecret(secret), Created: s.now().UTC()}
	if err := s.st.save(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"handle": handle, "secret": secret})
}

func (s *server) deliverNative(handle string, sub *subscription, m message) (gone bool, err error) {
	s.st.mu.Lock()
	n := s.st.Native[sub.App]
	s.st.mu.Unlock()
	switch {
	case sub.Kind == "apns" && n != nil && n.APNs != nil:
		gone, err = s.native.sendAPNs(s.client, n.APNs, sub, m, s.now())
	case sub.Kind == "fcm" && n != nil && n.FCM != nil:
		gone, err = s.native.sendFCM(s.client, n.FCM, sub, m, s.now())
	default:
		return false, fmt.Errorf("%s has no %s credentials", sub.App, sub.Kind)
	}
	if gone {
		s.st.mu.Lock()
		s.st.dropSub(handle)
		s.st.save()
		s.st.mu.Unlock()
	}
	return gone, err
}
