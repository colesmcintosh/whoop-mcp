# Deploy

`whoop-mcp` is single-tenant: one deployment reads one Whoop account — yours.
There's no multi-user login. You create a Whoop developer app, then either
authorize in the browser after `docker compose up`, or seed a refresh token
from a local `whoop-auth` run.

## 1. Create your Whoop app

At <https://developer-dashboard.whoop.com/apps/create>:

| Field | Value |
| --- | --- |
| Name | anything (`whoop-mcp` works) |
| Contacts | your email |
| Privacy policy URL | a link you control (your README works) |
| Redirect URLs | `http://localhost:8080/oauth/callback` (add your public HTTPS URL too if the server isn't on localhost) |
| Scopes | `read:profile`, `read:body_measurement`, `read:cycles`, `read:recovery`, `read:sleep`, `read:workout` |

Copy the **Client ID** and **Client Secret**.

## 2. Docker Compose (the usual path)

```sh
git clone https://github.com/colesmcintosh/whoop-mcp.git
cd whoop-mcp
cp .env.example .env
```

Put `WHOOP_CLIENT_ID`, `WHOOP_CLIENT_SECRET`, and `MCP_AUTH_TOKEN` in `.env`.
Generate the bearer secret with `openssl rand -hex 32`. Treat it like a
password; anyone who has it can read your Whoop data.

```sh
docker compose up -d
```

Open <http://localhost:8080>, paste `MCP_AUTH_TOKEN`, and authorize. Whoop
tokens live on the `whoop-data` volume and are refreshed automatically.

Point your MCP client at `http://localhost:8080/mcp` with
`Authorization: Bearer <MCP_AUTH_TOKEN>`. `GET /healthz` (no auth) is the
health check.

### Remote host

Set `WHOOP_REDIRECT_URI=https://<your-host>/oauth/callback` in `.env` and
register that same URL on the Whoop app. Put TLS in front of the container
(Caddy, nginx, a cloud load balancer). Keep port `8080:8080` unless you
also change the redirect URL.

### Why a volume is required

Whoop rotates the refresh token on every use. After the first authorization
the server persists each newly-rotated token to `WHOOP_TOKEN_FILE`
(`/data/token.json` in the image). Without a persistent volume, a restart
loses the rotated token and you have to reconnect.

## 3. Deploy on Railway

Authorize once locally (or use the HTTP setup page after the first deploy),
then:

```sh
git clone https://github.com/colesmcintosh/whoop-mcp.git
cd whoop-mcp

railway login
railway init --name whoop-mcp
railway add --service whoop-mcp \
  --variables "WHOOP_CLIENT_ID=<from step 1>" \
  --variables "WHOOP_CLIENT_SECRET=<from step 1>" \
  --variables "MCP_AUTH_TOKEN=<a long random string you generate>"
railway volume add --mount-path /data
railway up
railway domain
```

Optional: set `WHOOP_REFRESH_TOKEN` to skip the browser setup on a fresh
volume (copy `refresh_token` from a local `whoop-auth` token file). Set
`WHOOP_REDIRECT_URI` to `https://<railway-domain>/oauth/callback` if you
want to authorize through the deployed setup page instead.

## Docker without Compose

```sh
docker build -t whoop-mcp .
docker run --rm -p 8080:8080 \
  -e WHOOP_CLIENT_ID=... \
  -e WHOOP_CLIENT_SECRET=... \
  -e MCP_AUTH_TOKEN=... \
  -v whoop-data:/data \
  whoop-mcp
```

The image defaults `PORT=8080` and `WHOOP_TOKEN_FILE=/data/token.json`.

## Connect an MCP client

Point your client at `https://<your-host>/mcp` with an `Authorization: Bearer
<MCP_AUTH_TOKEN>` header.

## Environment variables

| Variable | Required in HTTP mode | Purpose |
| --- | --- | --- |
| `WHOOP_CLIENT_ID` | yes | From the Whoop developer dashboard. |
| `WHOOP_CLIENT_SECRET` | yes | From the Whoop developer dashboard. |
| `MCP_AUTH_TOKEN` | yes | Bearer secret gating `/mcp` and the setup form. Startup fails without it. |
| `PORT` *or* `MCP_HTTP_ADDR` | yes | Listening address. The Docker image and Railway both set `PORT`. |
| `WHOOP_REDIRECT_URI` | no | OAuth callback. Default `http://localhost:8080/oauth/callback`. |
| `WHOOP_REFRESH_TOKEN` | first boot only, optional | Seeds the token store if you already ran `whoop-auth`. Ignored once a token file exists. |
| `WHOOP_TOKEN_FILE` | no | Where the (continuously rotating) token is persisted. Defaults to `/data/token.json` in the image — must be on the mounted volume. |

## Post-deploy operations

- **Rotate the bearer secret**: generate a new one, update `.env` (or
  `railway variables --set "MCP_AUTH_TOKEN=<new>"`), recreate the container,
  and update your MCP client.
- **Reconnect Whoop**: open `/setup` and authorize again (needs
  `MCP_AUTH_TOKEN`).
- **Revoke Whoop access entirely**: visit your Whoop account's connected-apps
  settings and revoke `whoop-mcp` there.
- **Updates**: `git pull && docker compose up -d --build` (or `railway up`).
