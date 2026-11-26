# Subduction for nook

The [Subduction](https://github.com/inkandswitch/subduction) sync server for Automerge apps, as a nook catalog app.

- **Server:** `subduction_cli`, pinned in `fetch.sh` (tag and sha256 per architecture), embedded at release and
  written to `~/bin` on start. It runs on a local port; this program stands in front of it on the app's site.
- **Sync:** WebSocket upgrades and `/lp/` long polls go straight through to the server.
- **Page:** a plain visit to `/` shows what apps need to sync through it with Keyhive: the address, the peer id and
  the contact card, each with Copy, and Copy all as env (`SUBDUCTION_URL`, `SYNC_SERVER_PEER_ID`,
  `SYNC_SERVER_CONTACT_CARD`). `/identity.json` has the same. All of it is public.
- **Contact card:** made from the key here, the way `@keyhive/keyhive` makes one (an X25519 share key, derived
  from the key so the card is stable, signed with the server's Ed25519 key over its 32 bytes). A saved
  `data/subduction-card.json` is used instead while it's for the same key.
- **Data:** `~/data/subduction`; the identity key is `~/data/subduction.key`, made on first start. Back it up: it is
  the server's identity.
- **Health:** `/health` answers once the server is listening.

Newer nightlies: run **Actions → subduction nightly** (empty for the newest, or a tag). It runs `pin.sh`, which pins
that nightly's tag and sha256s in `fetch.sh` and sets `VERSION` to `vMAJOR.MINOR.YYYYMMDD` (`v0.22.20261126`), then
commits to main and releases. Every nightly is a patch update to nook's store, and a new minor server version a minor
one, so servers take them by their update policy for the app. Locally: `packages/subduction/pin.sh [TAG]`.
