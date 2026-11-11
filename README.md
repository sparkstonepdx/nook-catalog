# nook catalog

The official catalog for the [nook](https://github.com/sparkstonepdx/nook) store. Apps listed here show a green **Verified** check on every nook dashboard.

Anyone can run a community catalog the same way: copy this repo, swap in your own apps and key, and people add it on their Store page under **Catalogs** by typing `owner/repo`. Their dashboard names your catalog on every app that comes from it.

## How it works

1. One file per app in `apps/`.
2. The `catalog` workflow, daily and on every push, runs nook's `catalog-build`. For each app it finds the release on GitHub (the latest, or `source.tag`), picks the asset for each architecture, downloads it and pins its sha256.
3. It signs `index.json` with this catalog's key and publishes `index.json`, `index.json.sig` and `catalog.pub` to the `dist` branch.
4. A server fetches those from `https://raw.githubusercontent.com/OWNER/REPO/dist/` and checks the signature. A community catalog's key is pinned when it's added, so a later key change is refused. The official key is compiled into nook.
5. On install, the server checks each download against its pinned hash and takes the `binary` out in memory. It deploys it as a normal site with `files` and a generated `nook.json`, running as the app's own user. Root never unpacks the archive onto disk.

## Setting up a catalog (once)

1. Make the signing key from the repo root, with `gh` logged in: `sh scripts/setup-catalog-key.sh`. The private half goes straight into the `CATALOG_SIGNING_KEY` secret and is never written to disk. The public half is written to `catalog.pub`.
2. Commit `catalog.pub` and push. The workflow publishes the `dist` branch.
3. For the official catalog only: copy `catalog.pub` into nook's `internal/update/catalog.pub`, so servers trust it.

## App fields

| Field | |
|---|---|
| `id`, `name`, `description`, `homepage`, `license` | Shown in the Store |
| `kind` | `web` (the default; gets `site`'s hostname), `service`, or `cron` (with `schedule`) |
| `site` | The default site name: `ntfy` becomes ntfy.&lt;default domain&gt; |
| `binary` | The executable's file name inside the release asset |
| `files` | Other files for the site folder. A web app needs `run`, which gets `$PORT`, `$SITE_HOST` and `$HOME/data` |
| `env` | Fields asked for at install: `key`, `desc`, `default` (may use `{site_host}` and `{site}`), `secret`, `required` |
| `needs` | Shared services, bound automatically when exactly one provider is ready |
| `health` | A path that answers under 400 when the app is up; an update that fails it rolls back |
| `memory`, `cpu` | The app's limits |
| `source.github`, `source.assets` (GOARCH to regexp), `source.format` (`tar.gz`, `gz`, `raw`), `source.tag` | Where releases come from |

Try a new entry on a server (`nook-admin store install ID`) before relying on it. The health check and rollback guard updates, not first installs.

## The storefront

`site/` is a small Solid 2 site prerendered from the signed `index.json`: a page for the catalog and one per app, with **Install on nook**. That button asks for your dashboard's address once (only the browser keeps it) and opens the install sheet there, at `/store/install?app=ID`. A community copy links with `&source=owner/repo`, and the dashboard offers to add that catalog first.

The workflow builds it on every run and deploys it to a nook server when the `NOOK_TOKEN` secret and `NOOK_URL` variable are set. `NOOK_SITE` picks the site name, `apps` by default. Pull requests get a preview. Locally: `cd site && npm ci && npm run dev`, or `npm run build` with `INDEX=../index.json` for a built index.
