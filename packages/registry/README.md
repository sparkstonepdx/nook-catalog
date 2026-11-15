# registry

A private npm registry for the apps on a [nook](https://github.com/sparkstonepdx/nook) server: publish and install your own packages with the `NOOK_TOKEN` each app's CI already has.

- **No new secrets:** nook checks the token before a request gets here (`{"auth": "nook-token"}`), and this server only sees which app is asking. Every path needs a token, so nothing here is public.
- **One scope, two registries:** npm picks a registry per scope, so a package not published here is passed through from the public registry. `@sparkstone/kit` can live here while `@sparkstone/css` stays on npmjs.org, both under `@sparkstone:registry`.
- **No shadowing:** a name the public registry already has can't be published here, since it would hide the public one from every app on the server.
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
| `NPM_UPSTREAM` | `https://registry.npmjs.org` | where other packages come from, and what's checked before a new name; `off` for neither |
| `NPM_URL` | `https://` + the request's host | this registry's address, for tarball links |
| `NPM_MAX_MB` | 50 | the largest tarball |

If the public registry can't be reached, publishing a new name fails rather than skip the shadowing check. New versions of names already here don't ask it.

## Data

`$HOME/data/npm/packages/@scope/name/`: `package.json` (the versions as published, the tags and the owner) and the `.tgz` files, all 0600 in 0700 folders. Back it up; tarballs are kept nowhere else.

## Development

`go test ./...` covers publishing, the owner and shadowing rules, tags and the pass-through. With `npm` and `pnpm` installed it also publishes with the real `npm` and installs with both, through a stand-in for nook's token check.
