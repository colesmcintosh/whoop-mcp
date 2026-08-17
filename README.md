# whoop-mcp

[![CI](https://github.com/colesmcintosh/whoop-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/colesmcintosh/whoop-mcp/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](./LICENSE)
[![Deploy on Railway](https://railway.com/button.svg)](https://railway.com/new/template?template=https://github.com/colesmcintosh/whoop-mcp)

A [Model Context Protocol](https://modelcontextprotocol.io) server for the
[Whoop API v2](https://developer.whoop.com/), written in TypeScript for
[Bun](https://bun.sh).

Exposes recovery, sleep, cycles, workouts, profile, and body measurements as
read-only MCP tools. It's single-tenant: you create your own Whoop developer
app, authorize it yourself, and the server reads your Whoop data — there's no
shared hosting or per-user login system.

## Self-host with Docker

```sh
git clone https://github.com/colesmcintosh/whoop-mcp.git
cd whoop-mcp
cp .env.example .env
```

### 1. Create a Whoop app

At <https://developer-dashboard.whoop.com/apps/create>:

| Field | Value |
| --- | --- |
| Name | anything (`whoop-mcp` works) |
| Contacts | your email |
| Privacy policy URL | a link you control (this README works) |
| Redirect URLs | `http://localhost:8080/oauth/callback` |
| Scopes | `read:profile`, `read:body_measurement`, `read:cycles`, `read:recovery`, `read:sleep`, `read:workout` (`offline` is requested at OAuth time and isn't on the dashboard) |

Copy the **Client ID** and **Client Secret** into `.env`. Generate a bearer
secret with `openssl rand -hex 32` and paste it as `MCP_AUTH_TOKEN` — treat
it like a password:

```
WHOOP_CLIENT_ID=...
WHOOP_CLIENT_SECRET=...
MCP_AUTH_TOKEN=...
```

### 2. Start the container

```sh
docker compose up -d
```

Open <http://localhost:8080>, paste `MCP_AUTH_TOKEN`, and authorize with
Whoop. Tokens are stored on the `whoop-data` volume and refreshed
automatically after that. Until you authorize, `/mcp` returns `503`.
Reconnect later at `/setup`.

### 3. Point an MCP client at it

The endpoint is [Streamable HTTP](https://modelcontextprotocol.io/docs/concepts/transports):
each client gets its own MCP session (keyed by `mcp-session-id`), so a second
client or a reconnect after `initialize` does not collide with the first.

| Path | Auth | Purpose |
| --- | --- | --- |
| `GET /healthz` | none | liveness; body is `ok` |
| `GET /` | none | status page, or the setup form if Whoop isn't connected |
| `GET /setup` | none to view | reconnect form; starting OAuth requires `MCP_AUTH_TOKEN` |
| `/mcp` | `Authorization: Bearer <MCP_AUTH_TOKEN>` | MCP; `503` until Whoop is connected, `401` if the bearer is wrong |

**Claude Code:**

```sh
claude mcp add --transport http whoop http://localhost:8080/mcp \
  --header "Authorization: Bearer $MCP_AUTH_TOKEN"
```

**Cursor** (project `.cursor/mcp.json` or `~/.cursor/mcp.json`):

```json
{
  "mcpServers": {
    "whoop": {
      "url": "http://localhost:8080/mcp",
      "headers": {
        "Authorization": "Bearer ${env:MCP_AUTH_TOKEN}"
      }
    }
  }
}
```

Any MCP client that speaks Streamable HTTP works the same way — URL
`http://localhost:8080/mcp`, header `Authorization: Bearer <MCP_AUTH_TOKEN>`.

On a remote host, set `WHOOP_REDIRECT_URI` to
`https://<your-host>/oauth/callback` and register that same URL on the Whoop
app. Railway (including the volume UID the image needs), `docker run`, and
reconnect/rotate notes are in [DEPLOY.md](./DEPLOY.md).

## Local stdio setup

Requires [Bun](https://bun.sh). Same Whoop app as above. Then, without Docker:

```sh
bun install
export WHOOP_CLIENT_ID=...
export WHOOP_CLIENT_SECRET=...
bun run auth     # opens your browser, PKCE flow, saves the token
```

The token is written with `0600` permissions to a platform config path (or
wherever `WHOOP_TOKEN_FILE` points):

| OS | Default path |
| --- | --- |
| Linux | `~/.config/whoop-mcp/token.json` (`$XDG_CONFIG_HOME` if set) |
| macOS | `~/Library/Application Support/whoop-mcp/token.json` |
| Windows | `%AppData%\whoop-mcp\token.json` |

Access tokens refresh automatically. Whoop rotates the refresh token on
every use, so the file is rewritten after each refresh.

### Register the MCP server

**Claude Code:**

```sh
claude mcp add whoop \
  --env WHOOP_CLIENT_ID=$WHOOP_CLIENT_ID \
  --env WHOOP_CLIENT_SECRET=$WHOOP_CLIENT_SECRET \
  -- bun $PWD/src/cli/whoop-mcp.ts
```

**Cursor** (project `.cursor/mcp.json` or `~/.cursor/mcp.json`) and
**Claude Desktop** (`~/Library/Application Support/Claude/claude_desktop_config.json`
on macOS, `%APPDATA%\Claude\claude_desktop_config.json` on Windows):

```json
{
  "mcpServers": {
    "whoop": {
      "command": "bun",
      "args": ["/absolute/path/to/whoop-mcp/src/cli/whoop-mcp.ts"],
      "env": {
        "WHOOP_CLIENT_ID": "your-client-id",
        "WHOOP_CLIENT_SECRET": "your-client-secret"
      }
    }
  }
}
```

Any MCP client that can launch a subprocess (stdio transport) works the same
way. Client ID and secret are required at runtime — not just for `whoop-auth`
— because token refresh uses them. Ask it something like "what's my latest
recovery?"

## Running HTTP mode without Docker

```sh
export MCP_AUTH_TOKEN=$(openssl rand -hex 32)
export PORT=8080
bun start
```

Open `http://localhost:8080` to connect Whoop, then point your client at
`http://localhost:8080/mcp` with `Authorization: Bearer $MCP_AUTH_TOKEN`
(same Cursor JSON as the Docker section above). `/mcp` returns `503` until
Whoop is connected.

## Tools exposed

All 11 tools are read-only. List tools accept `limit` (1–25, default 10),
`start`/`end` (RFC3339 timestamps), and `next_token` for pagination.

| Tool | Whoop endpoint | Arguments |
| --- | --- | --- |
| `get_profile` | `GET /v2/user/profile/basic` | — |
| `get_body_measurement` | `GET /v2/user/measurement/body` | — |
| `list_cycles` | `GET /v2/cycle` | list |
| `get_cycle` | `GET /v2/cycle/{id}` | `cycle_id` |
| `get_cycle_recovery` | `GET /v2/cycle/{id}/recovery` | `cycle_id` |
| `get_cycle_sleep` | `GET /v2/cycle/{id}/sleep` | `cycle_id` |
| `list_recovery` | `GET /v2/recovery` | list |
| `list_sleep` | `GET /v2/activity/sleep` | list |
| `get_sleep` | `GET /v2/activity/sleep/{id}` | `id` |
| `list_workouts` | `GET /v2/activity/workout` | list |
| `get_workout` | `GET /v2/activity/workout/{id}` | `id` |

## Environment variables

| Env var | Required | Purpose |
| --- | --- | --- |
| `WHOOP_CLIENT_ID` | yes | From the Whoop dashboard. Needed at runtime for token refresh. |
| `WHOOP_CLIENT_SECRET` | yes | From the Whoop dashboard. Needed at runtime for token refresh. |
| `WHOOP_REDIRECT_URI` | no | OAuth callback. Defaults to `http://localhost:8080/oauth/callback`. Must be `localhost`/`127.0.0.1` for the `whoop-auth` CLI; HTTP/Docker mode accepts whatever you registered on the Whoop app. |
| `WHOOP_TOKEN_FILE` | no | Override the local token path. Default: platform config dir (see above), or `/data/token.json` in the Docker image. |
| `PORT` / `MCP_HTTP_ADDR` | no | Set either to run in HTTP mode instead of stdio. `MCP_HTTP_ADDR` wins if both are set (`:8080`, `8080`, or `host:port`). The Docker image sets `PORT=8080`. |
| `MCP_AUTH_TOKEN` | yes, in HTTP mode | Bearer secret required on every `/mcp` request, and to start the browser setup flow. Startup fails without it. |
| `WHOOP_REFRESH_TOKEN` | no | Seeds the token store on first boot in HTTP mode (e.g. a fresh volume) if you already authorized locally. Ignored once a token file exists. See [DEPLOY.md](./DEPLOY.md). |

## Layout

```
src/cli/      whoop-mcp (stdio/HTTP entry point), whoop-auth (local OAuth bootstrap)
src/auth/     OAuth+PKCE client, token storage, auto-refreshing token source
src/whoop/    Whoop API v2 HTTP client
src/mcp/      MCP tool registration
src/http/     single-tenant HTTP transport (per-session /mcp, /healthz, browser setup)
```

## Development

```sh
bun install
bun test
bun run typecheck
bun run lint
docker compose up --build
```

`bun run auth` is the OAuth bootstrap; `bun start` is the MCP server.

## Security

See [SECURITY.md](./SECURITY.md). Briefly:

- `MCP_AUTH_TOKEN` **is** the credential in HTTP mode — treat it like a
  password. Startup fails without it.
- The OAuth flow uses PKCE; `whoop-auth` only accepts a localhost redirect.
- The token file is written with `0600` permissions and rewritten whenever
  Whoop rotates the refresh token.
- A 401 from Whoop triggers one refresh-and-retry before the tool fails.

## License

[MIT](./LICENSE). Not affiliated with WHOOP, Inc. — "Whoop" is referenced
nominatively as the API this connector targets.
