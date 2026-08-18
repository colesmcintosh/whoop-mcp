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
`/mcp` returns `503` until that first authorization.

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

## 3. Deploy on Vercel

`api/index.ts` is the Vercel entry point. It serves the same app as the
container — Vercel's Node.js runtime captures the `node:http` server — with
two adjustments the platform requires:

- **The Whoop token goes in Redis, not on disk.** A function's filesystem
  does not outlive the invocation, and Whoop rotates the refresh token on
  every use, so a rotation written to disk is lost and the deployment
  breaks the next time it needs to refresh. Attach a Redis store and the
  token is kept there instead.
- **`/mcp` runs stateless.** Two requests from one MCP client are not
  guaranteed to reach the same instance, so there is no server-side session
  map: every POST is answered on its own, and `GET /mcp` (the optional
  server-to-client SSE stream) returns `405`. Clients that only send
  JSON-RPC requests — which is all of the common ones — are unaffected.

### Set it up

```sh
npm i -g vercel
git clone https://github.com/colesmcintosh/whoop-mcp.git
cd whoop-mcp
vercel link
```

**Add a Redis store.** In the project's **Storage** tab, create an Upstash
Redis database from the Marketplace and connect it. That injects
`KV_REST_API_URL` and `KV_REST_API_TOKEN`; the token store picks them up on
its own. (A standalone Upstash database works too — set
`UPSTASH_REDIS_REST_URL` and `UPSTASH_REDIS_REST_TOKEN` instead.)

**Add the secrets**, to Production and Preview:

```sh
vercel env add WHOOP_CLIENT_ID
vercel env add WHOOP_CLIENT_SECRET
vercel env add MCP_AUTH_TOKEN       # openssl rand -hex 32
```

**Deploy, then point the OAuth callback at the domain you get back.** The
redirect URI has to be a fixed, registered URL, and you don't know the
domain until the first deploy:

```sh
vercel deploy --prod
# note the production domain, e.g. whoop-mcp.vercel.app
vercel env add WHOOP_REDIRECT_URI   # https://<domain>/oauth/callback
vercel deploy --prod                # redeploy so the new value is picked up
```

Register that same `https://<domain>/oauth/callback` on the Whoop app.
Use the **production** domain, not a preview URL — preview URLs change on
every deployment, and Whoop only accepts callbacks it has on file.

**Connect Whoop.** Open `https://<domain>/`, paste `MCP_AUTH_TOKEN`, and
authorize. The token lands in Redis and refreshes from there. `/mcp`
returns `503` until this is done.

Optional: set `WHOOP_REFRESH_TOKEN` to skip the browser step entirely
(copy `refresh_token` from a local `whoop-auth` token file). It seeds the
Redis key on the first request and is ignored once a token is stored.

### Vercel notes

- The deployment is as sensitive as `MCP_AUTH_TOKEN`. If the project has
  Vercel Authentication (deployment protection) enabled, MCP clients get
  Vercel's login page instead of `/mcp`; turn it off for this project and
  rely on the bearer secret, or add a protection bypass token.
- `vercel.json` rewrites every path to the function, so `/mcp`, `/healthz`,
  `/`, `/setup`, and the OAuth callback all work at the domain root.
- Free-plan functions cap out at 60s per request, which is far more than any
  Whoop call needs.

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
| `MCP_AUTH_TOKEN` | yes | Bearer secret gating `/mcp` and starting the setup flow. Startup fails without it. |
| `PORT` *or* `MCP_HTTP_ADDR` | yes | Listening address. The Docker image and Vercel both set `PORT`. |
| `WHOOP_REDIRECT_URI` | no | OAuth callback. Default `http://localhost:8080/oauth/callback`. Required on Vercel — set it to `https://<domain>/oauth/callback`. |
| `WHOOP_REFRESH_TOKEN` | first boot only, optional | Seeds the token store if you already ran `whoop-auth`. Ignored once a token is stored. |
| `WHOOP_TOKEN_FILE` | no | Where the (continuously rotating) token is persisted when using the file backend. Defaults to `/data/token.json` in the image — must be on the mounted volume. |
| `KV_REST_API_URL` / `KV_REST_API_TOKEN` | Vercel only | Redis REST credentials. Set automatically by the Vercel Upstash integration. Their presence switches the token store from a file to Redis. |
| `UPSTASH_REDIS_REST_URL` / `UPSTASH_REDIS_REST_TOKEN` | no | Same thing, under the names a standalone Upstash database uses. |
| `WHOOP_TOKEN_KEY` | no | Redis key holding the token. Default `whoop-mcp:token`. |

## Post-deploy operations

- **Rotate the bearer secret**: generate a new one, update `.env` (or
  `vercel env rm MCP_AUTH_TOKEN && vercel env add MCP_AUTH_TOKEN`), recreate
  the container or redeploy, and update your MCP client.
- **Reconnect Whoop**: open `/setup` and authorize again (needs
  `MCP_AUTH_TOKEN` to start the flow). `/mcp` returns `503` until a token is
  stored.
- **Revoke Whoop access entirely**: visit your Whoop account's connected-apps
  settings and revoke `whoop-mcp` there. On Vercel, also delete the
  `whoop-mcp:token` Redis key.
- **Updates**: `git pull && docker compose up -d --build`, or push to the
  branch Vercel is tracking.
