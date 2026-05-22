# whoop-mcp

[![CI](https://github.com/colesmcintosh/whoop-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/colesmcintosh/whoop-mcp/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/colesmcintosh/whoop-mcp.svg)](https://pkg.go.dev/github.com/colesmcintosh/whoop-mcp)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](./LICENSE)
[![Deploy on Railway](https://railway.com/button.svg)](https://railway.com/new/template?template=https://github.com/colesmcintosh/whoop-mcp)

A [Model Context Protocol](https://modelcontextprotocol.io) server for the
[Whoop API v2](https://developer.whoop.com/), written in Go.

Exposes recovery, sleep, cycles, workouts, profile, and body measurements as
MCP tools so any MCP-capable AI assistant can read Whoop data on a user's
behalf.

## Two modes

`whoop-mcp` runs in one of two modes, selected by the environment.

| Mode | When | Auth | How clients connect |
| --- | --- | --- | --- |
| **stdio** (default — no `PORT`/`MCP_HTTP_ADDR`) | Personal, single user, runs on your laptop | Local token file written by `whoop-auth` | The MCP client launches the binary as a subprocess |
| **HTTP** (`PORT` or `MCP_HTTP_ADDR` set) | Hosted, multi-tenant | Per-user OAuth flow at `/login`; each user gets a private `/connect/<id>` URL | The MCP client connects over HTTPS to `/connect/<id>` |

## HTTP mode (multi-tenant hosting)

The hosted flow gives every visitor their own connector URL.

```
                 ┌──────────────────────────────┐
                 │  https://your.host/login     │
   User opens →  │  → Whoop OAuth consent       │
                 │  → /oauth/callback           │
                 │  → /connect/<id> shown       │
                 └──────────────┬───────────────┘
                                │ paste into
                                ▼
                 ┌──────────────────────────────┐
                 │ Any MCP-capable client         │
                 │ (Claude, etc.)                 │
                 └──────────────┬───────────────┘
                                │ HTTPS
                                ▼
                 ┌──────────────────────────────┐
                 │ whoop-mcp /connect/<id>      │
                 │ → looks up that user's token │
                 │ → calls Whoop API            │
                 └──────────────────────────────┘
```

### 1. Create a Whoop app

Sign in at <https://developer-dashboard.whoop.com/apps/create>.

- **Name**: anything (`whoop-mcp` works).
- **Contacts**: your email.
- **Privacy policy URL**: required by the form; link to your repo or hosted
  privacy page.
- **Redirect URLs**: add `https://<your-host>/oauth/callback` (and
  `http://localhost:8080/oauth/callback` if you want local dev).
- **Scopes**: enable `read:profile`, `read:body_measurement`, `read:cycles`,
  `read:recovery`, `read:sleep`, `read:workout`. (`offline` is requested by
  the server at OAuth time and is not configurable on the dashboard.)

Copy the **Client ID** and **Client Secret**.

### 2. Configure

| Env var | Required | Purpose |
| --- | --- | --- |
| `WHOOP_CLIENT_ID` | yes | From the Whoop dashboard. |
| `WHOOP_CLIENT_SECRET` | yes | From the Whoop dashboard. |
| `WHOOP_REDIRECT_URI` | yes (HTTP mode) | Must equal `<PUBLIC_URL>/oauth/callback`. |
| `PUBLIC_URL` | yes (HTTP mode) | Externally reachable `https://` URL of the deployed server, no trailing slash. |
| `USER_STORE_DIR` | optional | Where per-user JSON token files live. Default `/data/users`. Put this on persistent storage. |
| `PORT` *or* `MCP_HTTP_ADDR` | yes (HTTP mode) | Listening address. Platforms like Railway set `PORT`. |
| `WHOOP_TOKEN_BACKEND` | optional (stdio mode) | Where the local OAuth token is stored. `file` (default) writes JSON to `WHOOP_TOKEN_FILE`; `keyring` uses the OS keychain (macOS Keychain / GNOME Keyring / Windows Credential Manager). HTTP mode is unaffected — per-user tokens always live in `USER_STORE_DIR`. |
| `WHOOP_KEYRING_ACCOUNT` | optional | Account name used inside the keychain entry. Defaults to `default`. Set this if you want multiple Whoop accounts on the same machine. |

### 3. Deploy

See [DEPLOY.md](./DEPLOY.md) for full step-by-step Railway and Docker
instructions. Short version for Railway:

```sh
railway login
railway init --name whoop-mcp
railway add --service whoop-mcp \
  --variables "WHOOP_CLIENT_ID=..." \
  --variables "WHOOP_CLIENT_SECRET=..." \
  --variables "WHOOP_REDIRECT_URI=https://<your>.up.railway.app/oauth/callback" \
  --variables "PUBLIC_URL=https://<your>.up.railway.app"
railway volume add --mount-path /data
railway up
railway domain
```

The included `Dockerfile` and `railway.toml` are picked up automatically.

### 4. Connect

Send users to `https://<your-host>/` — they click **Connect**, approve at
Whoop, and the server hands them a personal `/connect/<id>` URL. They paste
that URL into their MCP client.

To revoke, a user visits `https://<your-host>/disconnect/<id>` (GET shows a
confirmation, POST performs the revoke).

## stdio mode (personal local use)

If you don't want a hosted server and just want Whoop in your local Claude
Code or Claude Desktop:

```sh
go build -o bin/whoop-mcp  ./cmd/whoop-mcp
go build -o bin/whoop-auth ./cmd/whoop-auth

export WHOOP_CLIENT_ID=...
export WHOOP_CLIENT_SECRET=...
./bin/whoop-auth                 # one-time OAuth (PKCE), persists token to ~/.config/whoop-mcp/token.json
```

To keep the token out of the filesystem entirely, store it in the OS
keychain instead:

```sh
export WHOOP_TOKEN_BACKEND=keyring
./bin/whoop-auth                 # writes to macOS Keychain / libsecret / Credential Manager
./bin/whoop-mcp                  # reads from the same place
```

Then register the stdio server with Claude Code:

```sh
claude mcp add whoop \
  --env WHOOP_CLIENT_ID=$WHOOP_CLIENT_ID \
  --env WHOOP_CLIENT_SECRET=$WHOOP_CLIENT_SECRET \
  -- $PWD/bin/whoop-mcp
```

For the personal stdio app, register `http://localhost:8080/callback` as the
redirect URI on your Whoop app instead of (or in addition to) the hosted one.

## Tools exposed

| Tool | Whoop endpoint |
| --- | --- |
| `get_profile` | `GET /v2/user/profile/basic` |
| `get_body_measurement` | `GET /v2/user/measurement/body` |
| `list_cycles` | `GET /v2/cycle` |
| `get_cycle` | `GET /v2/cycle/{id}` |
| `get_cycle_recovery` | `GET /v2/cycle/{id}/recovery` |
| `get_cycle_sleep` | `GET /v2/cycle/{id}/sleep` |
| `list_recovery` | `GET /v2/recovery` |
| `list_sleep` | `GET /v2/activity/sleep` |
| `get_sleep` | `GET /v2/activity/sleep/{id}` |
| `list_workouts` | `GET /v2/activity/workout` |
| `get_workout` | `GET /v2/activity/workout/{id}` |

List endpoints accept `limit` (1–25), `start`/`end` (RFC3339), and
`next_token` for pagination.

## Layout

```
cmd/whoop-mcp     HTTP server + stdio MCP entry point
cmd/whoop-auth    One-shot OAuth login CLI (stdio mode bootstrap)
internal/auth     OAuth config, token storage, refreshing token source
internal/store    Per-user filesystem token store
internal/whoop    Whoop API v2 HTTP client
```

## Token rotation

Whoop rotates the refresh token on every refresh — the previous one is
invalidated immediately. Both modes persist the rotated token back to disk
on every refresh, so this works across restarts as long as the storage
location (`WHOOP_TOKEN_FILE` for stdio, `USER_STORE_DIR` for HTTP) is on
persistent storage.

## Development

```sh
go test ./...
go vet ./...
golangci-lint run        # optional, CI runs this
docker build -t whoop-mcp .
```

## Security

See [SECURITY.md](./SECURITY.md). Briefly:

- The `/connect/<id>` URL **is** the credential — treat it like a password.
- The OAuth state CSRF is in-memory; it survives a process lifetime but not
  restarts (the OAuth flow recovers gracefully).
- A baseline of HTTP security headers (CSP, X-Frame-Options, etc.) is set on
  HTML responses.
- `/login` is rate limited per IP.

## License

[MIT](./LICENSE). Not affiliated with WHOOP, Inc. — "Whoop" is referenced
nominatively as the API this connector targets.
