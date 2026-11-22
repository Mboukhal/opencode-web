# opencode web

Phone-friendly web UI for running OpenCode sessions inside GitHub repositories
cloned on this server — and for pairing [OpenCode Mobile](https://opencode.ai/mobile)
with the very same sessions.

## What it does

- Google sign-in, allow-listed to a single seed email (`lios80466@gmail.com`).
- Lists your public GitHub repositories with their local state:
  `not loaded`, `loaded`, `update available`, `local changes`, `error`.
- Clones with an SSH deploy key, pulls fast-forward only, never touches dirty trees.
- **Pushes** the working tree and branch back to GitHub over SSH, refusing to
  overwrite commits that only exist on the remote.
- Runs prompts through the shared **OpenCode server** (`opencode serve`) instead
  of a throwaway `--standalone` CLI per run. The server keeps sessions, skills,
  credentials and tool execution, so a session started in the browser keeps
  running here and can be picked up from the terminal, the desktop app or the phone.
- **Mobile pairing**: mints a one-time `opencode pair` link, shows it as a
  scannable QR code, and lists the apps to install.
- Deploys **local skills** from a source directory into the OpenCode config dir
  and pushes **provider API keys** into OpenCode credentials on boot.
- Writes per-project `.opencode/opencode.json` + `.opencode/agents/<id>.md` so each
  repository gets its own model and agent role.

## How it fits together

```
phone / browser ──► opencode-web (this app)  ──HTTP──► opencode serve (shared)
                          │                                │
                          └── git clone/pull/push ──► GitHub over SSH
                                                        │
                                          OpenCode Mobile / terminal / desktop
```

`/data/config/opencode/skills` is read by the server, so skills deployed there
are visible to every project and to every paired client. `/api/session` is scoped
by directory, which is how each clone keeps its own sessions.

## Layout

| Path | Purpose |
| --- | --- |
| `/data/projects` | clones, one directory per `owner/repo` |
| `/data/config` | OpenCode config: `opencode.json`, `skills/`, `agents/` |
| `/data/state` | OpenCode data/sessions, server address + password |
| `/data/skills` | local skill sources copied into the config dir on boot |
| `/data/install` | OpenCode CLI (pinned to v2.x) |
| `/data/ssh` | deploy key (`id_ed25519`) |
| `/data/app` | SQLite database |
| `/data/home` | HOME for the app user |

## Configuration

| Env | Required | Default |
| --- | --- | --- |
| `PUBLIC_BASE_URL` | yes | – |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` | yes | – |
| `ALLOWED_EMAILS` | no | `lios80466@gmail.com` (first = seed user) |
| `OPENCODE_MODEL` | no | `opencode/muse-spark-1.3` |
| `OPENCODE_SERVICE_HOST` | no | `0.0.0.0` |
| `OPENCODE_SERVICE_PORT` | no | free port chosen by OpenCode |
| `OPENCODE_SERVICE_ADDR` | no | – (attach to a server you run yourself) |
| `OPENCODE_SERVICE_PASSWORD` | no | generated, stored under `/data/state` |
| `MOBILE_PAIR_URL` | **yes for pairing** | `PUBLIC_BASE_URL` (must route to the **OpenCode server**, not this app) |
| `COOLIFY_PAIR_FQDN` | no | `opencode.mboukhal.dev` (Traefik host for the OpenCode server) |
| `SKILLS_SRC` | no | `/data/skills` |
| `PROVIDER_KEYS` | no | – (`openrouter=sk-...;openai=sk-...`) |
| `OPENCODE_ENV` | no | – (extra env for the server and agents, `KEY=value;…`) |
| `OPENCODE_TIMEOUT` | no | `30m` |
| `GITHUB_ORGS` | no | `login:Mboukhal` |
| `SSH_KEY_PATH` | no | `/data/ssh/id_ed25519` |
| `GIT_AUTHOR_NAME` / `GIT_AUTHOR_EMAIL` | no | Mohammed Boukhal / lios80466@gmail.com |
| `FORCE_HTTPS` | no | off (`1` redirects plain HTTP to HTTPS) |
| `HSTS_MAX_AGE` | no | 180 days when `PUBLIC_BASE_URL` is https, else off |
| `EXTRA_CSP_ORIGINS` | no | – (space-separated origins added to the CSP) |

## Mobile pairing

Pairing needs the phone to reach the **OpenCode server**, not this web app. The
link is `https://<MOBILE_PAIR_URL>/auth/connect/<code>`, and that host must be
routed to the OpenCode server port (`OPENCODE_SERVICE_PORT`, default `4096`).

In the shipped compose file Traefik publishes it on `opencode.mboukhal.dev`
while `code.mboukhal.dev` keeps serving the web UI. If `MOBILE_PAIR_URL` points
at the web app, `/mobile` shows a red warning — the phone would get a 404 because
the app has no `/auth/connect` route.

1. Open `/mobile` in the browser and press **Generate pairing link**.
2. Scan the QR code with OpenCode Mobile (iPhone TestFlight or Android), or
   paste the link. It works once and expires after 5 minutes.
3. Sessions started from `/projects` now appear in the app, and turns started on
   the phone appear in the web UI.

Pairing codes are minted through `POST /api/pair`; the code is exchanged at
`/auth/connect/<code>`. The web app never renders the OpenCode service password —
that token is only ever sent to the phone by the OpenCode client itself.

## Security headers

Every response carries a hardening header set (`web/security.go`):

| Header | Value |
| --- | --- |
| `Content-Security-Policy` | `default-src 'self'`, same-origin scripts/styles/images, `frame-ancestors 'none'`, `form-action 'self'`, `object-src 'none'` |
| `X-Frame-Options` | `DENY` |
| `X-Content-Type-Options` | `nosniff` |
| `Referrer-Policy` | `same-origin` (never leaks the one-time pairing link) |
| `Cross-Origin-Opener-Policy` / `-Resource-Policy` | `same-origin` |
| `Permissions-Policy` | camera, microphone, geolocation, payment, usb, motion sensors disabled |
| `Strict-Transport-Security` | sent only over TLS (6 months, `includeSubDomains`) |
| `Cache-Control` | `no-store, private` on signed-in pages |

Set `FORCE_HTTPS=1` to redirect plain HTTP to HTTPS. The redirect is skipped for
`/healthz`, for requests a proxy already terminated (`X-Forwarded-Proto: https`,
so Coolify/Traefik do not loop) and when the `Host` is not `PUBLIC_BASE_URL`.

## Google OAuth

Create a Web application OAuth client, then add
`<PUBLIC_BASE_URL>/auth/google/callback` as an authorised redirect URI.

## GitHub SSH

Clones, pulls and pushes use the deploy key at `SSH_KEY_PATH`. Add its public key
(`<path>.pub`) to the repository or account as a deploy key; `/settings` shows the
fingerprint and whether `ssh -T git@github.com` succeeds.

## Development

```sh
go test ./...
go run . # needs the env vars above
```

The server starts OpenCode itself. Point `OPENCODE_BIN` at your local CLI
(`$HOME/.opencode/bin/opencode`) and `DATA_DIR` at a scratch directory.
