# files

File storage for the apps on a nook server: APKs, installers, release downloads. CI (or you) uploads with the app's `NOOK_TOKEN`. People with a nook login download. A public file has a link anyone can use, for as long as it stays public, and a share link lets anyone download one file until it expires. Each app has its own folder, `/f/<app>/...`, and only that app's token can change it.

Needs nook 1.25 or later: the app asks for nook's login itself (`"auth": "nook-login"`), so its Nook login switch is on from the start. Until the dashboard's address is set, only deploy tokens and share links get in.

## Upload from CI

```yaml
- name: Publish the APK
  run: |
    curl -fsS -H "Authorization: Bearer $NOOK_TOKEN" -T app/build/outputs/apk/release/app-release.apk \
      "https://files.example.com/v1/files/android/kinlog-${GITHUB_REF_NAME}.apk?alias=kinlog.apk"
  env:
    NOOK_TOKEN: ${{ secrets.NOOK_TOKEN }}
```

The response has the file's `url`, `size` and `sha256`. `?alias=kinlog.apk` also puts it at a stable name in the same folder, so `https://files.example.com/f/kinlog/android/kinlog.apk` is always the latest.

## Public files

Add `&public=1` to the upload and the file, and its alias, get a link for anyone: `publicUrl` (and `aliasPublicUrl`) in the response, `https://files.example.com/p/kinlog/android/kinlog.apk`. A path stays public until it's made private, so the next upload to `kinlog.apk` without `public` keeps the same public link serving the new file. New version files like `kinlog-1.5.0.apk` are private unless asked. On the page, each file has a **Public** switch and **Delete**, which asks once more first.

```yaml
- run: |
    curl -fsS -H "Authorization: Bearer $NOOK_TOKEN" -T out/app.apk \
      "https://files.example.com/v1/files/android/kinlog-${GITHUB_REF_NAME}.apk?alias=kinlog.apk&public=1"
```

`/p/...` answers 404 for a private file, the same as a missing one. Deleting a file ends its public link; uploading to the path again starts private.

## Share a file

Open the site and log in: every app's files are listed, each with **Share** and a choice of 1 hour, 1 day, 7 days or 30 days. The link, `/s/<id>`, works for anyone until then. **Share links** lists the live ones, with **Stop sharing**. A share follows its path, so sharing `kinlog.apk` serves whatever is the latest until the link expires.

From CI, with the token (the app's own files only):

```sh
curl -fsS -H "Authorization: Bearer $NOOK_TOKEN" -H "Content-Type: application/json" \
  -d '{"path": "android/kinlog.apk", "hours": 24}' https://files.example.com/v1/shares
```

## API

| | | |
|---|---|---|
| `PUT /v1/files/PATH` | token | the body is the file; `?alias=NAME` also puts it at NAME beside it; `?public=1` or `0` sets both public or private. Replaces a file at PATH |
| `GET /v1/files` | token or login | a token gets its app's files; a login every app's, each with `app` (`?app=A` for one) |
| `GET /v1/files/PATH` | token | one file's details |
| `DELETE /v1/files/PATH` | token or login | removes it, with `?app=A` for a login; its public link and share links end (an alias is its own file and stays) |
| `GET /f/APP/PATH` | token or login | the file; a token reads only its own app's |
| `POST /v1/shares` | token or login | JSON `{"path", "hours"}`, plus `"app"` with a login. `hours` is 1 to 720, 168 if left out. Returns `id`, `url`, `expires` |
| `GET /v1/shares` | token or login | the live links: a token its app's, a login all of them |
| `DELETE /v1/shares/ID` | token or login | stops a link |
| `POST /v1/public` | token or login | JSON `{"path", "public": true}` (plus `"app"` with a login): makes a file public or private. Returns the file with `publicUrl` |
| `GET /p/APP/PATH` | open | a public file; 404 otherwise |
| `GET /s/ID` | open | the shared file; 410 once the link has expired |

- **Who:** a login is a nook dashboard user allowed on the Files app (admins, or users given it). They see every app's files. A token sees only its own app's.
- **Paths:** up to 8 parts of letters, digits, `.`, `_` and `-`, none starting with a dot.
- **Downloads:** ranges work, so an interrupted download resumes. They send `X-Checksum-SHA256`. Public files are cached for 5 minutes. Share links send `noindex` and no referrer.
- **What opens in place:** images, video, audio, PDF, plain text and JSON. Everything else downloads as an attachment, including APKs and HTML. Nothing served can run script on this origin.
- **APKs:** sent as `application/vnd.android.package-archive`, so Android offers to install them.

## Settings

| | Default | |
|---|---|---|
| `FILES_MAX_MB` | 2047 | largest upload; 2047 is the most, since nook caps any one file an app writes at 2 GB |
| `FILES_URL` | `https://{site_host}` | the base of the URLs uploads and shares return |

Files are in `$HOME/data/files`, their checksums in `$HOME/data/meta`, share links in `$HOME/data/shares`.

## Development

`go test ./...` covers upload, alias, downloads with a login, a token and neither, ranges, checksums, per-app lists and deletes, public files (upload, aliases keeping it, who may, private looking missing, delete ending it), share links (making, limits, who may, expiry, stopping), the page, refused paths, the size limit, and HTML that downloads instead of running.
