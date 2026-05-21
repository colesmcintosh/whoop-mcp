# Deploy

`whoop-mcp` is a single static Go binary in a distroless container. It runs
anywhere Docker runs and only needs one persistent volume (for the per-user
token files).

## Deploy on Railway (recommended)

[![Deploy on Railway](https://railway.com/button.svg)](https://railway.com/new/template?template=https://github.com/colesmcintosh/whoop-mcp)

> The button above only works once the repo is public. While it's private,
> follow the CLI steps below.

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
  --variables "WHOOP_CLIENT_SECRET=<from step 1>"
railway volume add --mount-path /data
railway up
```

### 3. Get the public URL and wire callback

```sh
railway domain                    # mints a public https URL
PUBLIC_URL=https://<that-domain>

railway variables \
  --set "PUBLIC_URL=$PUBLIC_URL" \
  --set "WHOOP_REDIRECT_URI=$PUBLIC_URL/oauth/callback" \
  --set "USER_STORE_DIR=/data/users"
```

Go back to the Whoop dashboard and add `$PUBLIC_URL/oauth/callback` to the
app's **Redirect URLs**. Save.

### 4. Verify

```sh
curl https://<your-host>/healthz       # → 200
open https://<your-host>/              # → connect page
```

Click **Connect Whoop**, approve, and the success page shows your personal
MCP URL. Paste it into any MCP client.

## Deploy with Docker (any host)

```sh
docker build -t whoop-mcp .
docker run --rm -p 8080:8080 \
  -e WHOOP_CLIENT_ID=... \
  -e WHOOP_CLIENT_SECRET=... \
  -e WHOOP_REDIRECT_URI=https://your.host/oauth/callback \
  -e PUBLIC_URL=https://your.host \
  -v whoop-data:/data \
  whoop-mcp
```

Put a TLS-terminating reverse proxy (Caddy, nginx, Cloudflare Tunnel, etc.)
in front of the container so `PUBLIC_URL` is reachable on https.

## Environment variables

| Variable | Required in HTTP mode | Purpose |
| --- | --- | --- |
| `WHOOP_CLIENT_ID` | yes | From the Whoop developer dashboard. |
| `WHOOP_CLIENT_SECRET` | yes | From the Whoop developer dashboard. |
| `PUBLIC_URL` | yes | Externally-reachable `https://` URL of the server, no trailing slash. |
| `WHOOP_REDIRECT_URI` | yes | Must equal `${PUBLIC_URL}/oauth/callback` and match a Whoop app redirect URI exactly. |
| `PORT` | yes (or `MCP_HTTP_ADDR`) | Listening port. Railway/Fly/Render set this automatically. |
| `USER_STORE_DIR` | no | Per-user token files. Default `/data/users`. Must live on a persistent volume. |
| `MCP_HTTP_ADDR` | no | Override the listen address; e.g. `:8080`. Wins over `PORT`. |

## Post-deploy operations

- **Rotate the client secret**: regenerate it on the Whoop dashboard, then
  `railway variables --set "WHOOP_CLIENT_SECRET=<new>"`. Existing user
  refresh tokens stay valid.
- **Clear a user**: `rm /data/users/<id>.json` on the host, or visit
  `/disconnect/<id>` in a browser.
- **Rotate everything**: empty `/data/users/` — every visitor will need to
  reconnect.

## Updates

```sh
git pull
railway up
```

Health check at `/healthz` keeps Railway from routing traffic to a failed
deploy.
