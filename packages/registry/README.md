# registry

A private npm registry for the apps on a [nook](https://github.com/sparkstonepdx/nook) server: publish and install your own packages with the `NOOK_TOKEN` each app's CI already has.

- **No new secrets:** nook checks the token before a request gets here (`{"auth": "nook-token"}`), and this server only sees which app is asking. Every path needs a token, so nothing here is public.
- **Serves nothing from npmjs.org:** only what was published here. Apps point only their private scope here (`@scope:registry=...`), and npm gets everything else from npmjs.org itself.
- **No name collisions:** before a name's first publish here, it asks npmjs.org whether that name exists, read-only. A public name is refused, so an app whose `.npmrc` is missing the scope line can't get the other package by mistake. If npmjs.org can't be asked, a new name waits (502) rather than skip the check. New versions of a name already here don't ask.
- **One registry per scope:** npm picks a registry per scope, so every package a scope's apps install must be here. A scope with public packages on npmjs.org (like `@sparkstone/css`) can't also point here; use a separate scope for private packages, or publish the public ones here too.
- **Owners:** the app whose token first publishes a package owns it. Only that token can publish more versions or move its tags; any token can install.
- **Published is final:** a version can't be republished, unpublished or changed, as on npmjs.org.

Install it from the nook store and set `NPM_SCOPES` to the scopes you'll publish, like `@sparkstone`. The source lives here in `packages/registry`; a change to `VERSION` on main releases it as `registry-vX.Y.Z`. It's npm only for now; Docker images are proposed in nook#64.

## Publishing from CI

Give the publishing repo a deploy token as `NOOK_TOKEN` (any app's; that app becomes the package's owner) and write an `.npmrc` before publishing:

```yaml
- name: Use the nook registry for @sparkstone
  run: |
    echo "@sparkstone:registry=https://npm.example.com/" >> .npmrc
    echo "//npm.example.com/:_authToken=\${NOOK_TOKEN}" >> .npmrc
- run: pnpm publish --no-git-checks   # or changeset publish
  env:
    NOOK_TOKEN: ${{ secrets.NOOK_TOKEN }}
```

In the package's `package.json`, `"publishConfig": {"registry": "https://npm.example.com/"}` keeps a stray `npm publish` from going to npmjs.org. Leave provenance off: it's for the public registry.

## Installing in an app

```ini
# .npmrc, committed
@sparkstone:registry=https://npm.example.com/
//npm.example.com/:_authToken=${NOOK_TOKEN}
```

```yaml
- run: pnpm install --frozen-lockfile
  env:
    NOOK_TOKEN: ${{ secrets.NOOK_TOKEN }}
```

On your own machine, export a token as `NOOK_TOKEN` (make one on the dashboard's **Deploy** page), or put it in `~/.npmrc` instead. If pnpm's `minimumReleaseAge` is set, it holds back new versions; to take your own packages as soon as they're out, add `minimumReleaseAgeExclude: ["@sparkstone/*"]` to `pnpm-workspace.yaml`.

## API

The parts of the npm registry API that `npm` and `pnpm` use to install and publish:

| | |
|---|---|
| `GET /@scope%2fname` | the package document; from the public registry if it isn't published here |
| `GET /@scope/name/-/name-1.0.0.tgz` | a tarball |
| `PUT /@scope%2fname` | publish one version (`npm publish`) |
| `GET`, `PUT`, `DELETE /-/package/@scope%2fname/dist-tags[/TAG]` | `npm dist-tag ls`, `add`, `rm`; `latest` can move but not go |
| `GET /-/whoami` | the app the token belongs to (`npm whoami`) |
| `GET /-/ping` | health, without a token |

## Settings

| Setting | Default | |
|---|---|---|
| `NPM_SCOPES` | any | scopes that may be published, comma-separated (`@sparkstone,@other`). Unscoped packages never can |
| `NPM_CHECK` | `https://registry.npmjs.org` | the public registry a new name is checked against; `off` for no check |
| `NPM_URL` | `https://` + the request's host | this registry's address, for tarball links |
| `NPM_MAX_MB` | 50 | the largest tarball |

## Data

`$HOME/data/npm/packages/@scope/name/`: `package.json` (the versions as published, the tags and the owner) and the `.tgz` files, all 0600 in 0700 folders. Back it up; tarballs are kept nowhere else.

## Development

`go test ./...` covers publishing, the owner rules, tags, name collisions (including npmjs.org being down), and a 404 for anything not published here. With `npm` and `pnpm` installed it also publishes with the real `npm` and installs with both, through a stand-in for nook's token check.

## Owning the scope on npmjs.org too

The collision check covers names as they're published. To also stop someone else from taking a name in your scope on npmjs.org later, create the scope there as an organization (free for public packages, and it can stay empty). Then nobody else can publish under it.
