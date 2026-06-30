# whoop-mcp

[![CI](https://github.com/colesmcintosh/whoop-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/colesmcintosh/whoop-mcp/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/colesmcintosh/whoop-mcp.svg)](https://pkg.go.dev/github.com/colesmcintosh/whoop-mcp)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](./LICENSE)

A self-hosted [Model Context Protocol](https://modelcontextprotocol.io) server
for the [Whoop API v2](https://developer.whoop.com/), written in Go.

Exposes recovery, sleep, cycles, workouts, profile, and body measurements as
MCP tools so any MCP-capable AI assistant can read your Whoop data.

## How it works

`whoop-mcp` is **single-tenant and self-hosted**. You run it yourself — on
your laptop or in a container — connect your own Whoop account once through
the browser, and point any MCP client at the server's `/mcp` endpoint. There
is no hosted service, no per-user accounts, and no login CLI: the server runs
the OAuth flow, stores the single resulting token, and refreshes it
automatically.

```
   You ──browser──▶ http://localhost:8080/      (click "Connect Whoop")
                      → Whoop OAuth consent
                      → token stored on disk

   MCP client ──HTTP(S)──▶ http://localhost:8080/mcp
                             → reads your Whoop data
```

## 1. Create a Whoop app

Sign in at <https://developer-dashboard.whoop.com/apps/create>.

- **Name**: anything (`whoop-mcp` works).
- **Contacts**: your email.
- **Privacy policy URL**: required by the form; link to your repo or any page
  you control.
- **Redirect URLs**: add the callback URL for wherever you'll run it:
  - Local: `http://localhost:8080/oauth/callback`
  - Hosted/container: `https://<your-host>/oauth/callback`
- **Scopes**: enable `read:profile`, `read:body_measurement`, `read:cycles`,
  `read:recovery`, `read:sleep`, `read:workout`. (`offline` is requested by
  the server at OAuth time and is not configurable on the dashboard.)

Copy the **Client ID** and **Client Secret**.

## 2. Run it

### Local (Docker)

```sh
docker build -t whoop-mcp .
docker run --rm -p 8080:8080 \
  -e WHOOP_CLIENT_ID=... \
  -e WHOOP_CLIENT_SECRET=... \
  -v whoop-data:/data \
  whoop-mcp
```

### Local (from source)

```sh
go build -o bin/whoop-mcp ./cmd/whoop-mcp

export WHOOP_CLIENT_ID=...
export WHOOP_CLIENT_SECRET=...
./bin/whoop-mcp                  # serves on :8080 by default
```

Then open <http://localhost:8080/>, click **Connect Whoop**, and approve at
Whoop. That's it — the page now shows your MCP endpoint URL.

### Container app / VPS

Set `PUBLIC_URL` (and put TLS in front), then deploy. See
[DEPLOY.md](./DEPLOY.md) for Railway/Docker step-by-step. **When the server is
reachable over a network, set `AUTH_TOKEN`** (see [Securing a network
deployment](#securing-a-network-deployment)).

## 3. Connect your MCP client

Add a **remote / HTTP** MCP server pointing at `<your-host>/mcp`.

For Claude Code:

```sh
claude mcp add --transport http whoop http://localhost:8080/mcp
```

If you set `AUTH_TOKEN`, pass it as a bearer token, e.g.:

```sh
claude mcp add --transport http whoop https://<your-host>/mcp \
  --header "Authorization: Bearer $AUTH_TOKEN"
```

Then ask something like "what's my latest recovery?".

## Configuration

| Env var | Required | Purpose |
| --- | --- | --- |
| `WHOOP_CLIENT_ID` | yes | From the Whoop dashboard. |
| `WHOOP_CLIENT_SECRET` | yes | From the Whoop dashboard. |
| `PORT` *or* `MCP_HTTP_ADDR` | no | Listen address. Defaults to `:8080`. Platforms like Railway set `PORT`. |
| `PUBLIC_URL` | recommended for hosted | Externally-reachable `https://` URL, no trailing slash. Used to build the redirect URI and the MCP URL shown in the UI. Derived from the request when unset. |
| `WHOOP_REDIRECT_URI` | no | Override the OAuth callback URL. Defaults to `${PUBLIC_URL}/oauth/callback`, or `http://localhost:<port>/oauth/callback`. Must match a redirect URL on the Whoop app exactly. |
| `AUTH_TOKEN` | recommended for hosted | Shared secret. Gates the browser UI and the `/mcp` endpoint (sent as `Authorization: Bearer`). See below. |
| `WHOOP_TOKEN_BACKEND` | no | Where the token is stored. `file` (default) writes JSON to `WHOOP_TOKEN_FILE`; `keyring` uses the OS keychain (macOS Keychain / GNOME Keyring / Windows Credential Manager). |
| `WHOOP_TOKEN_FILE` | no | Token file path for the `file` backend. The Docker image defaults it to `/data/token.json` — keep that on a persistent volume. |
| `WHOOP_KEYRING_ACCOUNT` | no | Account name inside the keychain entry. Defaults to `default`. |

## Securing a network deployment

The server is single-tenant: once your Whoop account is connected, anyone who
can reach `/mcp` can read your data, and anyone who can reach `/login` can
hijack the connection. On localhost that's fine. **Anywhere reachable over a
network, set `AUTH_TOKEN` to a strong secret.** When set, it:

- gates the browser UI (you paste it once to unlock the connect/disconnect
  pages), and
- is required as `Authorization: Bearer <AUTH_TOKEN>` on the `/mcp` endpoint.

If `AUTH_TOKEN` is unset and the server is not bound to localhost, it logs a
warning at startup.

To disconnect, open `/` and click **Disconnect** (or `POST /disconnect`). This
revokes the OAuth grant with Whoop and deletes the stored token.

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
cmd/whoop-mcp     HTTP server: connect flow + /mcp endpoint
internal/auth     OAuth config, single-token storage, refreshing token source
internal/whoop    Whoop API v2 HTTP client
```

## Token rotation

Whoop rotates the refresh token on every refresh — the previous one is
invalidated immediately. The server persists the rotated token back to its
store on every refresh, so the connection survives restarts as long as the
storage location (`WHOOP_TOKEN_FILE`, or the keychain) is durable. In a
container, keep `WHOOP_TOKEN_FILE` on a persistent volume.

## Development

```sh
go test ./...
go vet ./...
golangci-lint run        # optional, CI runs this
docker build -t whoop-mcp .
```

## Security

See [SECURITY.md](./SECURITY.md).

## License

[MIT](./LICENSE). Not affiliated with WHOOP, Inc. — "Whoop" is referenced
nominatively as the API this connector targets.
