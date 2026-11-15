# files

File storage for the apps on a nook server: APKs, installers, release downloads. CI (or you) uploads with the app's `NOOK_TOKEN`. People with a nook login download, and a share link lets anyone else download one file until it expires. Each app has its own folder, `/f/<app>/...`, and only that app's token can change it.

After installing, turn on the app's **Nook login** switch (Apps, Manage; nook 1.24+) so people can log in. Until then only deploy tokens get in, and share links still work.

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
| `PUT /v1/files/PATH` | token | the body is the file; `?alias=NAME` also puts it at NAME beside it. Replaces a file at PATH |
| `GET /v1/files` | token or login | a token gets its app's files; a login every app's, each with `app` (`?app=A` for one) |
| `GET /v1/files/PATH` | token | one file's details |
| `DELETE /v1/files/PATH` | token | removes it (an alias is its own link and stays) |
| `GET /f/APP/PATH` | token or login | the file; a token reads only its own app's |
| `POST /v1/shares` | token or login | JSON `{"path", "hours"}`, plus `"app"` with a login. `hours` is 1 to 720, 168 if left out. Returns `id`, `url`, `expires` |
| `GET /v1/shares` | token or login | the live links: a token its app's, a login all of them |
| `DELETE /v1/shares/ID` | token or login | stops a link |
| `GET /s/ID` | open | the shared file; 410 once the link has expired |

- **Who:** a login is a nook dashboard user allowed on the Files app (admins, or users given it). They see every app's files. A token sees only its own app's.
- **Paths:** up to 8 parts of letters, digits, `.`, `_` and `-`, none starting with a dot.
- **Downloads:** ranges work, so an interrupted download resumes. They send `X-Checksum-SHA256`. Share links send `noindex` and no referrer.
- **What opens in place:** images, video, audio, PDF, plain text and JSON. Everything else downloads as an attachment, including APKs and HTML. Nothing served can run script on this origin.
- **APKs:** sent as `application/vnd.android.package-archive`, so Android offers to install them.

## Settings

| | Default | |
|---|---|---|
| `FILES_MAX_MB` | 2047 | largest upload; 2047 is the most, since nook caps any one file an app writes at 2 GB |
| `FILES_URL` | `https://{site_host}` | the base of the URLs uploads and shares return |

Files are in `$HOME/data/files`, their checksums in `$HOME/data/meta`, share links in `$HOME/data/shares`.

## Development

`go test ./...` covers upload, alias, downloads with a login, a token and neither, ranges, checksums, per-app lists and deletes, share links (making, limits, who may, expiry, stopping), the page, refused paths, the size limit, and HTML that downloads instead of running.
