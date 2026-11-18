# opencode web

Phone-friendly web UI for running OpenCode sessions inside GitHub repositories
cloned on this server.

## What it does

- Google sign-in, allow-listed to a single seed email (`lios80466@gmail.com`).
- Lists your public GitHub repositories with their local state:
  `not loaded`, `loaded`, `update available`, `local changes`, `error`.
- Clones with an SSH deploy key, pulls fast-forward only, never touches dirty trees.
- Runs `opencode run --standalone --format json` inside the project and streams
  output to the browser over SSE.
- Writes per-project `.opencode/opencode.json` + `.opencode/agents/<id>.md` so each
  repository gets its own model and agent role.

## Layout

| Path | Purpose |
| --- | --- |
| `/data/projects` | clones, one directory per `owner/repo` |
| `/data/config` | OpenCode config: `opencode.json`, `skills/`, `agents/` |
| `/data/state` | OpenCode data/sessions |
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
| `OPENCODE_MODEL` | no | `opencode/big-pickle` |
| `OPENCODE_ENV` | no | – (provider keys, `KEY=value` separated by `;`) |
| `OPENCODE_VERSION` | no | `2.3.2` |
| `OPENCODE_TIMEOUT` | no | `30m` |
| `GITHUB_ORGS` | no | `login:Mboukhal` |

## Google OAuth

Create a Web application OAuth client, then add
`<PUBLIC_BASE_URL>/auth/google/callback` as an authorised redirect URI.

## Development

```sh
go test ./...
go run . # needs the env vars above
```
