# runners

GitHub Actions runners on a nook server, for an organization or one repository. Each runner takes one job in a fresh folder, then is replaced, so nothing carries over between jobs and nothing stays registered when it stops.

Install it from the nook store with:

| Setting | |
|---|---|
| `GITHUB_SCOPE` | an organization (`sparkstonepdx`) or a repository (`sparkstonepdx/wordbag`) |
| `GITHUB_TOKEN` | a fine-grained token. For an organization: **Self-hosted runners**, read and write. For a repository: **Administration**, read and write |
| `RUNNERS` | how many jobs at once, 1 to 16 (default 2) |
| `RUNNER_LABELS` | labels besides `self-hosted`, `Linux` and the architecture (default `nook`) |
| `RUNNER_GROUP` | an organization's runner group id (default 1, Default) |

For more than one organization or repository, install it again under another name. In a workflow:

```yaml
runs-on: [self-hosted, nook]
```

## What jobs can do

Jobs run as the app's own user in nook's sandbox:
- **Works:** Node, Go, npm, Playwright with its downloaded browsers, the `setup-*` actions, and test servers on ports of their choosing (the app sets `"listen": "any"`; nook 1.22+).
- **Doesn't work:** no Docker (`container:` jobs, `services:`), no root (`sudo`, `apt-get`).
- **Shared tool cache:** `RUNNER_TOOL_CACHE` is shared by all of the app's runners, so `setup-node` and friends download a version once.
- **Fixed ports:** two jobs at once that both want the same fixed port collide; use `RUNNERS=1` for those repositories, or pick ports from the environment.

**Only for your own repositories.** A runner runs whatever a workflow gives it, and the app's jobs share one user and its data folder. Don't point it at a public repository that takes pull requests from forks.

## How it works

1. **The runner:** finds GitHub's latest runner release for this architecture and downloads it once. It checks the SHA-256 from the release's asset digest or its notes, refuses a mismatch, and unpacks it into `data/runners/runner/<version>/`. It checks for a newer one daily, because GitHub stops sending jobs to outdated runners. `RUNNER_VERSION` pins a tag; `RUNNER_SHA256` gives the checksum if a release lists none.
2. **Each job:**
   1. A fresh folder, hard-linked from the unpacked runner.
   2. Registration with a just-in-time config (one job, then GitHub removes it).
   3. `run.sh --jitconfig`, with an environment that doesn't include `GITHUB_TOKEN`.
   4. When the job ends, anything it left running is killed, its folder is deleted, and the next registration starts.
3. **Leftovers:** at start, it removes this app's runners that are registered but offline, left from a crash.
4. **Stopping:** ends the runners and removes their registrations.

`GET /health` (the app's own port) lists each runner (waiting, running a job, its job count) and answers 503 with the reason when the settings or the token are wrong, so the dashboard shows it as unhealthy.

## Development

`go test ./...` covers the scope, safe unpacking, checksums and a supervisor run against a fake GitHub with a fake runner. Nothing here has run against real GitHub yet.
