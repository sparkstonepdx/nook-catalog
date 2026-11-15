# directory

A public page of the apps a nook server lists: a card per app with its first screenshot (or its icon on its own colors), name, description and an **Open** in its color, a page per app with that screenshot in a phone frame, the rest of its screenshots and a big **Open**, and itself installable, so it doubles as a home-screen launcher. No JavaScript.

## Listing apps

On the nook dashboard, turn on **List in directory** on a site's sheet (or run `nook-admin directory add HOST`). Everything a card shows comes from the site's own web manifest, so the app decides how it looks:

| Manifest field | Where it shows |
|---|---|
| `name`, `short_name` | the card and its page (`short_name` on the Open button) |
| `description` | under the name |
| `theme_color` | the Open buttons; the text on them is near-black or white, whichever reads better |
| `background_color` | behind the icon, and behind the card's picture |
| `icons` | the one nearest 192px that isn't maskable-only |
| `screenshots` | the first `narrow` one (else the first) is the card's picture and the phone frame on the app's page; the rest scroll in a row below it |

A site without a manifest still shows, with its page title and initial.

## Settings

| | Default | |
|---|---|---|
| `DIRECTORY_SOURCE` | `{dashboard_url}/api/v1/directory` | where the list comes from (nook 1.23+) |
| `DIRECTORY_TITLE` | `Apps` | the page's heading and the installed app's name |
| `DIRECTORY_INTRO` | | a line under the heading |

It reads the list every minute and keeps the last good one if the dashboard is unreachable. Icons come through this app (only listed sites' icons, kept in memory); screenshots load from each app's own address. `GET /health` answers 503 until the list has been read once.

## Development

`go test ./...` covers cleaning the list (a URL that isn't the site's own https address, colors that break out of CSS, other hosts' screenshots, bad icon names), icons only for listed sites, the Open button's text color, escaping, the page's CSP, and a 404 for an app that isn't listed.

The look is a proposal until Adam signs it off.
