# push

Web Push for every app on a [nook](https://github.com/sparkstonepdx/nook) server: one install serves them all.

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

## API

| | | |
|---|---|---|
| `GET /v1/config` | token | the app's public key and URLs; makes the key pair the first time |
| `PUT /v1/keys` | token | bring an existing key pair; `?replace=1` if browsers are subscribed with another |
| `GET /v1/stats` | token | subscriptions and scheduled pushes for the app |
| `POST /v1/subscribe/APP` | open | a `PushSubscription` → `{handle, secret}`; the same browser again gets the same handle |
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
| `PUSH_ENDPOINT_HOSTS` | | push services beyond FCM, Mozilla, Apple and Windows, comma-separated |

Endpoints must be HTTPS on a known push service, so the server can't be pointed at other URLs.

## Data

`$HOME/data/push/store.json` (0600): every app's key pair, the subscriptions, and the scheduled pushes. Back it up: losing an app's private key means every browser has to subscribe again.

## Development

`go test ./...` covers encryption (checked against a separately written browser side), VAPID signatures, the HTTP flow and scheduling. `scripts/interop.mjs` checks the output with `http_ece` and `web-push`, the reference libraries.
