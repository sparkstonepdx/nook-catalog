# files

Public file storage for the apps on a nook server: APKs, installers, release downloads, anything with a link. CI (or you) uploads with the app's `NOOK_TOKEN`; anyone can download. Each app has its own folder, `/f/<app>/...`, and only that app's token can change it.

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

## API

| | | |
|---|---|---|
| `PUT /v1/files/PATH` | token | the body is the file; `?alias=NAME` also puts it at NAME beside it. Replaces a file at PATH |
| `GET /v1/files` | token | every file of the app's: path, URL, size, SHA-256, type, modified |
| `GET /v1/files/PATH` | token | one file's details |
| `DELETE /v1/files/PATH` | token | removes it (an alias is its own link and stays) |
| `GET /f/APP/PATH` | open | the file |

- **Paths:** up to 8 parts of letters, digits, `.`, `_` and `-`, none starting with a dot.
- **Downloads:** ranges work, so an interrupted download resumes. They send `X-Checksum-SHA256` and are cached for 5 minutes.
- **What opens in place:** images, video, audio, PDF, plain text and JSON. Everything else downloads as an attachment, including APKs and HTML. Nothing served can run script on this origin.
- **APKs:** sent as `application/vnd.android.package-archive`, so Android offers to install them.

## Settings

| | Default | |
|---|---|---|
| `FILES_MAX_MB` | 2047 | largest upload; 2047 is the most, since nook caps any one file an app writes at 2 GB |
| `FILES_URL` | `https://{site_host}` | the base of the URLs uploads return |

Files are in `$HOME/data/files`, with their checksums in `$HOME/data/meta`.

## Development

`go test ./...` covers upload, alias, download with a range, checksums, per-app lists and deletes, refused paths, the size limit, and HTML that downloads instead of running.
