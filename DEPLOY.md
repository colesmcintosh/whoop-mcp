# Deploy

`whoop-mcp` is a single static Go binary in a distroless container. It runs
anywhere Docker runs and needs one persistent volume for the stored Whoop
token.

It is **single-tenant**: one running instance serves one Whoop account. Deploy
your own instance, connect your account once in the browser, and point your
MCP client at `/mcp`.

## Deploy with Docker (any host)

```sh
docker build -t whoop-mcp .
docker run --rm -p 8080:8080 \
  -e WHOOP_CLIENT_ID=... \
  -e WHOOP_CLIENT_SECRET=... \
  -e PUBLIC_URL=https://your.host \
  -e AUTH_TOKEN=$(openssl rand -hex 32) \
  -v whoop-data:/data \
  whoop-mcp
```

Put a TLS-terminating reverse proxy (Caddy, nginx, Cloudflare Tunnel, etc.) in
front so `PUBLIC_URL` is reachable over https. The Docker image stores the
token at `/data/token.json`, so mount `/data` on a persistent volume.

Then add `${PUBLIC_URL}/oauth/callback` to your Whoop app's **Redirect URLs**,
open `https://your.host/`, unlock with your `AUTH_TOKEN`, and click **Connect
Whoop**.

## Deploy on Railway

[![Deploy on Railway](https://railway.com/button.svg)](https://railway.com/new/template?template=https://github.com/colesmcintosh/whoop-mcp)

### 1. Create your Whoop app

At <https://developer-dashboard.whoop.com/apps/create>:

| Field | Value |
| --- | --- |
| Name | anything (`whoop-mcp` works) |
| Contacts | your email |
| Privacy policy URL | a link you control (your README works) |
| Redirect URLs | `https://<your-host>/oauth/callback` — set after step 3 |
| Scopes | `read:profile`, `read:body_measurement`, `read:cycles`, `read:recovery`, `read:sleep`, `read:workout` |

Copy the **Client ID** and **Client Secret**.

### 2. Provision on Railway

```sh
git clone https://github.com/colesmcintosh/whoop-mcp.git
cd whoop-mcp

railway login
railway init --name whoop-mcp
railway add --service whoop-mcp \
  --variables "WHOOP_CLIENT_ID=<from step 1>" \
  --variables "WHOOP_CLIENT_SECRET=<from step 1>" \
  --variables "AUTH_TOKEN=<a long random secret>"
railway volume add --mount-path /data
railway up
```

### 3. Get the public URL and wire callback

```sh
railway domain                    # mints a public https URL
PUBLIC_URL=https://<that-domain>

railway variables \
  --set "PUBLIC_URL=$PUBLIC_URL" \
  --set "WHOOP_REDIRECT_URI=$PUBLIC_URL/oauth/callback"
```

Go back to the Whoop dashboard and add `$PUBLIC_URL/oauth/callback` to the
app's **Redirect URLs**. Save.

### 4. Connect

```sh
curl https://<your-host>/healthz       # → 200
open https://<your-host>/              # → unlock, then connect page
```

Unlock with your `AUTH_TOKEN`, click **Connect Whoop**, approve, and the page
shows your MCP endpoint URL. Add it to your MCP client as a remote/HTTP server
with `Authorization: Bearer <AUTH_TOKEN>`.

## Environment variables

| Variable | Required | Purpose |
| --- | --- | --- |
| `WHOOP_CLIENT_ID` | yes | From the Whoop developer dashboard. |
| `WHOOP_CLIENT_SECRET` | yes | From the Whoop developer dashboard. |
| `PUBLIC_URL` | recommended | Externally-reachable `https://` URL of the server, no trailing slash. |
| `WHOOP_REDIRECT_URI` | no | Defaults to `${PUBLIC_URL}/oauth/callback`. Must match a Whoop app redirect URI exactly. |
| `AUTH_TOKEN` | recommended | Shared secret gating the browser UI and the `/mcp` bearer. Set this whenever the server is network-reachable. |
| `PORT` | no (or `MCP_HTTP_ADDR`) | Listening port. Defaults to `8080`. Railway/Fly/Render set this automatically. |
| `WHOOP_TOKEN_FILE` | no | Token file path. Defaults to `/data/token.json` in the image. Keep it on a persistent volume. |
| `MCP_HTTP_ADDR` | no | Override the listen address; e.g. `:8080`. Wins over `PORT`. |

## Post-deploy operations

- **Rotate the client secret**: regenerate it on the Whoop dashboard, then
  update `WHOOP_CLIENT_SECRET`. The stored token stays valid.
- **Disconnect**: open `/` and click **Disconnect**, or `POST /disconnect`.
  This revokes the grant and deletes the token. Reconnect any time via
  `/login`.
- **Reset**: delete `/data/token.json` (or the keychain entry) and reconnect.

## Updates

```sh
git pull
railway up
```

Health check at `/healthz` keeps the platform from routing traffic to a failed
deploy.
