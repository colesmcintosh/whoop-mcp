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

`whoop-mcp` is small and lives on `main`. Security fixes land on `main` and are
deployed by the operator who self-hosts it. There is no LTS branch.

## Threat model (in scope)

`whoop-mcp` is a single-tenant, self-hosted connector: one instance serves one
Whoop account, run by the person whose account it is. The intended threat
model:

- The operator terminates TLS (locally, or via a platform / reverse proxy)
  whenever the server is exposed beyond localhost.
- `AUTH_TOKEN`, when set, is the credential. It gates the browser connect /
  disconnect UI (via an `HttpOnly` cookie set after the operator pastes it)
  and is required as `Authorization: Bearer <AUTH_TOKEN>` on the `/mcp`
  endpoint. Comparisons are constant-time.
- **If `AUTH_TOKEN` is unset, there is no application-level auth.** This is
  intended only for localhost / private-network use. The server logs a warning
  at startup when it is not bound to localhost and `AUTH_TOKEN` is unset.
  Anyone who can reach an unauthenticated instance can read the connected
  account's data and can hijack the connect flow.
- The single Whoop OAuth token is stored on disk at `WHOOP_TOKEN_FILE`
  (default `/data/token.json`) as readable JSON, or in the OS keychain when
  `WHOOP_TOKEN_BACKEND=keyring`. Anyone with access to that location can read
  it.
- The OAuth authorization-code flow uses PKCE (RFC 7636, S256). Even if an
  authorization code is intercepted on the redirect, it cannot be exchanged
  without the per-flow code verifier held in server memory.
- `/login` and `/unlock` are rate limited per IP to slow brute-force attempts.
- A baseline of HTTP security headers (CSP, `X-Frame-Options: DENY`, etc.) is
  applied to the HTML pages.

## Out of scope

- DoS resilience beyond the basic per-IP rate limit. Use a platform-level
  WAF/rate-limit if you expose the server publicly.
- Encryption of the token at rest. The `file` backend stores it as readable
  JSON; run on a disk encrypted by the host OS or platform if that matters, or
  use `WHOOP_TOKEN_BACKEND=keyring` to put it in the OS keychain instead.
- Compromise of the underlying host. If the box is rooted, the stored token is
  compromised; disconnect via `/disconnect` (or delete the token file) and
  reconnect.

## Operational guidance

- Treat `WHOOP_CLIENT_SECRET` and `AUTH_TOKEN` as high-value secrets. Set them
  via the platform's secret manager, never check them into git.
- Generate `AUTH_TOKEN` from a CSPRNG, e.g. `openssl rand -hex 32`.
- Keep the deployed `PUBLIC_URL` on HTTPS only — the OAuth state and admin
  cookies are marked `Secure` when the server is served over https.
