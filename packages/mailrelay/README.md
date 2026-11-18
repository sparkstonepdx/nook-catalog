# Mail relay

Email for every app on a nook server through one provider account: Amazon SES, SendGrid, or any SMTP relay. It provides nook's `email` service, so an app that `"needs": ["email"]` gets:

```
SMTP_URL=smtp://nook:<its own token>@127.0.0.1:<port>
```

and sends as it would to any mail server. No app holds the provider's login.

## What it checks
- **Who:** the password is the app's binding token (nook's `NOOK_CLIENTS`); the username is ignored.
- **From:** the envelope sender and every address in the `From:` header must be in `FROM_DOMAINS` or a subdomain of one.
- **How much:** `SENDS_PER_HOUR` recipients per app per hour (default 200), at most 50 a message, messages up to 10 MB.
- **On to the provider:** over STARTTLS (required unless the provider is on the same machine), port 587.

## Settings
| `MAIL_PROVIDER` | Needs |
|---|---|
| `ses` | `SES_REGION`, `SES_SMTP_USERNAME`, `SES_SMTP_PASSWORD` (SMTP credentials from the SES console, not an access key) |
| `sendgrid` | `SENDGRID_API_KEY` |
| `smtp` | `SMTP_HOST` (`:587` unless given), `SMTP_USERNAME`, `SMTP_PASSWORD` |

It listens on `127.0.0.1:$PORT` only, and nook's firewall lets only bound apps reach it. Each message is logged with the app, sender and recipient count, never the content.
