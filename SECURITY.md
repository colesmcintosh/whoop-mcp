# Security policy

## Reporting a vulnerability

If you find a security issue in `whoop-mcp`, please report it privately rather
than opening a public issue.

- Open a **draft** [GitHub Security Advisory](https://github.com/colesmcintosh/whoop-mcp/security/advisories/new) on this repo, **or**
- Email the maintainer at the address listed in the GitHub profile.

Please include enough detail to reproduce the issue and, where possible, a
suggested fix.

We aim to acknowledge reports within 72 hours. There is no bug bounty.

## Supported versions

`whoop-mcp` is small and lives on `main`. Security fixes land on `main` and
are deployed by whoever runs the instance. There is no LTS branch.

## Threat model (in scope)

`whoop-mcp` is single-tenant: each deployment (or local stdio instance)
belongs to exactly one Whoop account, set up by the person running it. There
is no multi-user credential store.

- The OAuth authorization-code flow uses PKCE (RFC 7636, S256). Even if an
  authorization code is intercepted on the local redirect, it cannot be
  exchanged without the per-flow code verifier held in memory.
- `whoop-auth` only accepts a `localhost`/`127.0.0.1` redirect URI — it
  can't be pointed at a remote callback.
- In HTTP/Docker mode, `GET /` and `GET /setup` are unauthenticated HTML
  (no Whoop data). Starting the browser setup flow (`POST /oauth/start`)
  requires `MCP_AUTH_TOKEN`. The callback is bound to a single in-memory
  PKCE session (5 minute TTL). Set `WHOOP_REDIRECT_URI` to the public URL
  you registered on the Whoop app if the server isn't reached as localhost.
- The locally/volume-persisted OAuth token (`WHOOP_TOKEN_FILE`) is written
  with `0600` permissions. Anyone with filesystem access to that path can
  read it.
- In HTTP mode, `/mcp` is gated by `MCP_AUTH_TOKEN`, checked with a
  constant-time comparison. **This bearer token is the credential** —
  anyone who has it can read your Whoop data through the deployed server.
  Startup fails if it isn't set, so an HTTP deployment can't accidentally
  run unauthenticated.
- `/healthz` is intentionally unauthenticated (health checks need to reach
  it) and returns no Whoop data. `/` and `/setup` are status/setup pages
  and do not expose Whoop data. `/mcp` returns `503` until a Whoop token
  is stored, then requires the bearer secret.

## Out of scope

- DoS resilience. Run behind a platform-level WAF/rate-limit if the
  deployment is reachable from the open internet.
- Encryption of the token at rest — it's a plain JSON file with restrictive
  permissions, not further encrypted. If that matters, put it on a disk
  encrypted by the host OS or platform.
- Compromise of the underlying host. If the box is rooted, the token on it
  is compromised; revoke access from your Whoop account settings and
  re-authorize.

## Operational guidance

- Treat `WHOOP_CLIENT_SECRET` and `MCP_AUTH_TOKEN` as high-value secrets.
  Set them via the platform's secret manager, never check them into git.
- Rotate `MCP_AUTH_TOKEN` if you suspect it leaked; the deployment fails
  closed (won't start) without one.
