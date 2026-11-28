# push

Push for every app on a [nook](https://github.com/sparkstonepdx/nook) server: one install serves them all. Web Push for browsers, and native push for mobile apps: APNs on iOS, FCM on Android with Google Play services, and UnifiedPush on Android without Google. A handle works the same whichever kind of device is behind it.

- **Keys:** each app gets its own VAPID key pair, made the first time its CI asks with `NOOK_TOKEN`. The private keys stay on the server; apps never hold one.
- **Send handles:** a browser that subscribes gets a handle. Anyone holding a handle can push to that one browser, now or at a set time, and nothing else. A local-first app can put handles in its shared document, so players notify each other with no backend of their own.
- **No new secrets:** nook checks the token before a request gets here (`{"auth": "nook-token"}`), and this server only sees which app is asking. Browser paths are open and rate-limited instead.

Install it from the nook store (it's in the official catalog) and set `PUSH_SUBJECT`, a `mailto:` address push services can reach you at. The source lives here in `packages/push`; a change to `VERSION` on main releases it as `push-vX.Y.Z`, and the catalog picks that up.

## In your app's build

```yaml
- name: Push config
  run: |
    curl -fsS -H "Authorization: Bearer $NOOK_TOKEN" https://push.example.com/v1/config > public/push.json
  env:
    NOOK_TOKEN: ${{ secrets.NOOK_TOKEN }}
```

`push.json` is `{"app", "publicKey", "subscribe", "send", "subject"}`. The first call makes the app's key pair; later calls return the same one. Already have keys? `PUT /v1/keys` with `{"publicKey", "privateKey"}` (base64url, as `web-push generate-vapid-keys` prints them) before browsers subscribe, so existing subscriptions keep working.

## In the browser

```js
const cfg = await (await fetch("/push.json")).json();
const reg = await navigator.serviceWorker.ready;
const sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: cfg.publicKey });
const { handle, secret } = await (await fetch(cfg.subscribe, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(sub) })).json();
// keep `secret` on this device; share `handle` with whoever may notify you (another player, your backend)

// notify someone, now or later
await fetch(cfg.send + theirHandle, { method: "POST", headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ payload: { title: "Your turn", url: "/game/42" }, topic: "turn" }) });
```

```js
// service worker: the payload arrives exactly as sent
self.addEventListener("push", (e) => {
  const p = e.data.json();
  e.waitUntil(self.registration.showNotification(p.title, { body: p.body, data: p }));
});
```

## Native apps

Each app brings its own credentials from its build, with `NOOK_TOKEN`. They stay on this server; the app never ships them.

```yaml
- name: Native push credentials
  run: |
    # iOS: the .p8 key from Apple Developer > Keys (with APNs enabled), its key id and your team id
    jq -n --arg key "$APNS_KEY" '{teamId: "TEAM123456", keyId: "ABC123DEFG", key: $key, bundleId: "com.example.app"}' |
      curl -fsS -X PUT -H "Authorization: Bearer $NOOK_TOKEN" --data-binary @- https://push.example.com/v1/native/apns
    # Android: the Firebase service account JSON (Project settings > Service accounts > Generate new private key)
    curl -fsS -X PUT -H "Authorization: Bearer $NOOK_TOKEN" --data-binary "$FCM_SERVICE_ACCOUNT" https://push.example.com/v1/native/fcm
  env:
    NOOK_TOKEN: ${{ secrets.NOOK_TOKEN }}
    APNS_KEY: ${{ secrets.APNS_KEY }}
    FCM_SERVICE_ACCOUNT: ${{ secrets.FCM_SERVICE_ACCOUNT }}
```

`push.json` then says which the app has: `"native": {"apns": true, "fcm": true}`. In the app, subscribe with the device token instead of a `PushSubscription`, and keep the handle and secret the same way:

```js
// Capacitor, with @sparkstone/capacitor-push (capacitor/ here): registers, subscribes with the device's keys
import { subscribe } from "@sparkstone/capacitor-push";
const { handle, secret } = await subscribe(cfg);
```

Or post `{"apns": {"token", "sandbox"?}}` / `{"fcm": {"token"}}` yourself.

- **Encrypted:** a device that subscribes with `"keys": {"p256dh", "auth"}` beside its token (the plugin always does) gets every push encrypted to those keys, the Web Push way, so Apple and Google carry only ciphertext. iOS gets a "New notification" placeholder with `mutable-content` and the ciphertext as `enc`; the app's Notification Service Extension decrypts it and shows the real title and body. Android gets a data-only message with `enc`, which the plugin decrypts and shows. Encrypted payloads are up to 2800 bytes (Apple's 4 KB, after encryption and base64). Without keys, pushes go readable as below.

- **Payload (unencrypted):** a `title` or `body` in it shows as the system notification. The whole payload reaches the app: on iOS as `payload` beside `aps`, on Android as the data field `payload`, a JSON string. Without a title or body, iOS gets a background push.
- **`sandbox`:** iOS builds run from Xcode get development tokens, which only Apple's sandbox takes; send `sandbox: true` from those.
- **`topic`, `ttl`, `urgency`:** become `apns-collapse-id`, `apns-expiration` and `apns-priority` (low and very-low are 5, anything else 10) on iOS, and `collapse_key`, `ttl` and `priority` (NORMAL or HIGH) on Android.
- **Gone:** a token Apple or Google says is no longer registered is dropped, and the sender gets 410, as with browsers.
- **Removing:** `DELETE /v1/native/apns` or `/v1/native/fcm` removes the credentials and every device subscribed through them.

**UnifiedPush** (Android without Google) is Web Push: the distributor app (ntfy, Sunup, NextPush) gives your app an endpoint and keys. Send them as a `PushSubscription` (`{"endpoint", "keys": {"p256dh", "auth"}}`), and pass `cfg.publicKey` as the VAPID key when registering. Endpoints on ntfy.sh and Mozilla's push service are allowed already; for your own ntfy (it's in the nook catalog), add its host to `PUSH_ENDPOINT_HOSTS`.

## API

| | | |
|---|---|---|
| `GET /v1/config` | token | the app's public key and URLs; makes the key pair the first time |
| `PUT /v1/keys` | token | bring an existing key pair; `?replace=1` if browsers are subscribed with another |
| `GET /v1/stats` | token | subscriptions (by kind) and scheduled pushes for the app |
| `PUT /v1/native/apns` | token | `{teamId, keyId, key, bundleId}`; `DELETE` removes them |
| `PUT /v1/native/fcm` | token | the Firebase service account JSON; `DELETE` removes it |
| `POST /v1/subscribe/APP` | open | a `PushSubscription`, `{"apns": {token, sandbox?}}` or `{"fcm": {token}}` → `{handle, secret}`; the same device again gets the same handle |
| `DELETE /v1/subscriptions/HANDLE` | open | with `X-Push-Secret` |
| `POST /v1/send/HANDLE` | open | `{payload, at?, topic?, ttl?, urgency?}` → 200 sent, 202 scheduled, 410 gone |
| `DELETE /v1/send/HANDLE/ID` | open | cancel a scheduled push by id or topic |
| `GET /v1/health` | open | |

- **Payload:** any JSON, up to 3 KB, delivered as is.
- **`at`:** RFC 3339, up to 30 days ahead. A scheduled push with the same `topic` replaces the pending one, which suits reminders that move.
- **`topic`:** also tells the push service to replace an undelivered push with the same topic.
- **Gone:** when a push service says a subscription has ended, it's dropped and the sender gets 410, so it can forget the handle.
- **Retries:** a scheduled push that fails is retried after 1, 5 and 30 minutes.

## Limits

| Setting | Default | |
|---|---|---|
| `PUSH_SENDS_PER_HOUR` | 60 | per handle, sends and schedules together |
| subscriptions | 30 an hour per IP | |
| scheduled | 100 per handle | |
| `PUSH_ENDPOINT_HOSTS` | | Web Push services beyond FCM, Mozilla, Apple, Windows and ntfy.sh, comma-separated (your own ntfy, for UnifiedPush) |

Endpoints must be HTTPS on a known push service, so the server can't be pointed at other URLs.

## Data

`$HOME/data/push/store.json` (0600): every app's key pair and native credentials, the subscriptions, and the scheduled pushes. Back it up: losing an app's private key means every browser has to subscribe again.

## Development

`go test ./...` covers encryption (checked against a separately written browser side), VAPID signatures, the HTTP flow and scheduling, and native push against fakes of APNs, Google's token endpoint and FCM (the JWTs checked with the keys' public halves). `scripts/interop.mjs` checks the output with `http_ece` and `web-push`, the reference libraries.
