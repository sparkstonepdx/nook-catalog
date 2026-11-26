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

Upgrading Subduction: change the tag and both sums in `fetch.sh`, then `VERSION`. VERSION follows the server's version (subduction_cli 0.22 is v0.22.x), with the patch number for changes here, so the store sees each release as newer than the last (the first catalog entry was `0.22.0-nightly.2026-11-25`).
