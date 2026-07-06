# Deploy

`whoop-mcp` is single-tenant: one deployment reads one Whoop account — yours.
There's no server-side login flow or per-user credential store. You
authorize locally once, then hand the deployment your own tokens as env
vars.

## 1. Create your Whoop app

At <https://developer-dashboard.whoop.com/apps/create>:

| Field | Value |
| --- | --- |
| Name | anything (`whoop-mcp` works) |
| Contacts | your email |
| Privacy policy URL | a link you control (your README works) |
| Redirect URLs | `http://localhost:8080/oauth/callback` |
| Scopes | `read:profile`, `read:body_measurement`, `read:cycles`, `read:recovery`, `read:sleep`, `read:workout` |

Copy the **Client ID** and **Client Secret**.

## 2. Authorize locally

```sh
export WHOOP_CLIENT_ID=...
export WHOOP_CLIENT_SECRET=...
bun src/cli/whoop-auth.ts
```

This opens your browser, completes the OAuth+PKCE flow, and saves a token to
`~/.config/whoop-mcp/token.json` (or wherever `WHOOP_TOKEN_FILE` points). Open
that file and copy the `refresh_token` value — that's what the deployment
will use to bootstrap itself.

## 3. Deploy on Railway

```sh
git clone https://github.com/colesmcintosh/whoop-mcp.git
cd whoop-mcp

railway login
railway init --name whoop-mcp
railway add --service whoop-mcp \
  --variables "WHOOP_CLIENT_ID=<from step 1>" \
  --variables "WHOOP_CLIENT_SECRET=<from step 1>" \
  --variables "WHOOP_REFRESH_TOKEN=<from step 2>" \
  --variables "MCP_AUTH_TOKEN=<a long random string you generate>"
railway volume add --mount-path /data
railway up
railway domain
```

`MCP_AUTH_TOKEN` is a shared secret you make up (e.g. `openssl rand -hex 32`)
— it's the bearer token your MCP client will send. Treat it like a password;
anyone who has it can read your Whoop data.

## Deploy with Docker (any host)

```sh
docker build -t whoop-mcp .
docker run --rm -p 8080:8080 \
  -e PORT=8080 \
  -e WHOOP_CLIENT_ID=... \
  -e WHOOP_CLIENT_SECRET=... \
  -e WHOOP_REFRESH_TOKEN=... \
  -e MCP_AUTH_TOKEN=... \
  -v whoop-data:/data \
  whoop-mcp
```

## Connect an MCP client

Point your client at `https://<your-host>/mcp` with an `Authorization: Bearer
<MCP_AUTH_TOKEN>` header. `GET /healthz` (no auth) is what Railway/Docker
should use for health checks.

## Environment variables

| Variable | Required in HTTP mode | Purpose |
| --- | --- | --- |
| `WHOOP_CLIENT_ID` | yes | From the Whoop developer dashboard. |
| `WHOOP_CLIENT_SECRET` | yes | From the Whoop developer dashboard. |
| `WHOOP_REFRESH_TOKEN` | first boot only | Seeds the token store from the local `whoop-auth` run. Ignored once a token file already exists on the volume. |
| `MCP_AUTH_TOKEN` | yes | Bearer secret gating `/mcp`. Startup fails without it. |
| `PORT` *or* `MCP_HTTP_ADDR` | yes | Listening address. Railway sets `PORT` automatically. |
| `WHOOP_TOKEN_FILE` | no | Where the (continuously rotating) token is persisted. Defaults to `/data/token.json` in the image — must be on the mounted volume. |

## Why a volume is required

Whoop rotates the refresh token on every use. `WHOOP_REFRESH_TOKEN` only
seeds the *first* boot; from then on, the server persists each newly-rotated
token to `WHOOP_TOKEN_FILE`. Without a persistent volume, a restart would
have only the stale env var and fail to reauthenticate.

## Post-deploy operations

- **Rotate the bearer secret**: generate a new one, `railway variables --set
  "MCP_AUTH_TOKEN=<new>"`, and update your MCP client.
- **Revoke Whoop access entirely**: visit your Whoop account's connected-apps
  settings and revoke `whoop-mcp` there.
- **Updates**: `git pull && railway up`.
