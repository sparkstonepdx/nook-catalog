# registry

A private npm registry and container image registry for the apps on a [nook](https://github.com/sparkstonepdx/nook) server: publish and install your own packages, push and pull your own images, with the `NOOK_TOKEN` each app's CI already has.

- **No new secrets:** nook checks the token before a request gets here (`{"auth": "nook-token"}`), and this server only sees which app is asking. Every path needs a token, so nothing here is public.
- **One scope, private and public together:** apps point their scope here (`@sparkstone:registry=...`). Private packages (`@sparkstone/kit`) come from here, and the scope's public ones (`@sparkstone/css`) are passed through from npmjs.org as it answers, tarball links included, so npm downloads those from npmjs.org without this registry's token. Only names in `NPM_SCOPES` pass through: it's never a general npm mirror, and npm gets every other package from npmjs.org itself.
- **No name collisions:** before a name's first publish here, it asks npmjs.org whether that name exists, read-only. A public name is refused, so an app whose `.npmrc` is missing the scope line can't get the other package by mistake. If npmjs.org can't be asked, a new name waits (502) rather than skip the check. New versions of a name already here don't ask.
- **Owners:** the app whose token first publishes a package owns it. Only that token can publish more versions or move its tags; any token can install.
- **Published is final:** a version can't be republished, unpublished or changed, as on npmjs.org.

Install it from the nook store and set `NPM_SCOPES` to the scopes you'll publish, like `@sparkstone`. The source lives here in `packages/registry`; a change to `VERSION` on main releases it as `registry-vX.Y.Z`. It also holds container images (below).

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

## Container images

The same address serves the OCI distribution API under `/v2/`, for k3s, CI runners or any other host that pulls images. nook doesn't run containers itself.

```sh
echo "$NOOK_TOKEN" | docker login npm.example.com -u ci --password-stdin   # any username
docker tag app npm.example.com/sparkstone/app:1.2.0
docker push npm.example.com/sparkstone/app:1.2.0
```

- **Owners:** the app whose token first pushes to a repository (`sparkstone/app`) owns it. Only its token can push, tag or delete there; any token can pull.
- **Layers are stored once,** however many repositories use them. A push that names a layer another repository has mounts it instead of sending it again.
- **Deleting:** `DELETE /v2/NAME/manifests/sha256:...` (the owner's token) removes that manifest and its tags; deleting a tag removes only the tag. Layers nothing refers to any more go an hour later, the next time something is deleted. Nothing is deleted on its own: keep-last-N is up to you.
- **Limits:** a layer is at most `OCI_MAX_MB` (2048). There's no quota per app: the dashboard's disk figures show what it holds.
- **Pulling on k3s:** an image pull secret of type `kubernetes.io/dockerconfigjson` with the registry's address, any username and a token.

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
| `/v2/...` | the OCI distribution API: blobs (chunked, monolithic and mounted uploads, range reads), manifests and indexes by tag or digest, `tags/list`, deleting manifests |

## Settings

| Setting | Default | |
|---|---|---|
| `NPM_SCOPES` | any | scopes that may be published, comma-separated (`@sparkstone,@other`). Unscoped packages never can |
| `NPM_PUBLIC` | `https://registry.npmjs.org` | the public registry: the scopes' public packages pass through from it, and new names are checked against it; `off` for neither |
| `NPM_URL` | `https://` + the request's host | this registry's address, for tarball links |
| `NPM_MAX_MB` | 50 | the largest tarball |
| `OCI_MAX_MB` | 2048 | the largest image layer |

## Data

`$HOME/data/npm/packages/@scope/name/`: `package.json` (the versions as published, the tags and the owner) and the `.tgz` files, all 0600 in 0700 folders. Images are in `$HOME/data/npm/oci/`: `blobs/sha256/` (layers, configs and manifests), `repos/NAME/repo.json` (owner, tags, manifests) and `uploads/` (pushes in progress, dropped after a day). Back it up; nothing here is kept anywhere else. Image layers are large: if a backup shouldn't carry them, leave `data/npm/oci` out with `backup.skip`.

## Development

`go test ./...` covers publishing, the owner rules, tags, name collisions (including npmjs.org being down), and passing through only the scopes' public packages. With `NPM_LIVE=1` it also installs a private and a public `@sparkstone` package together with real npm, against npmjs.org. With `npm` and `pnpm` installed it also publishes with the real `npm` and installs with both, through a stand-in for nook's token check.

## Owning the scope on npmjs.org too

The collision check covers names as they're published. To also stop someone else from taking a name in your scope on npmjs.org later, create the scope there as an organization (free for public packages, and it can stay empty). Then nobody else can publish under it.
