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
deployed by the hosting operator. There is no LTS branch.

## Threat model (in scope)

`whoop-mcp` is a hosted multi-tenant connector. The intended threat model:

- The hosted instance terminates TLS via the platform (Railway, Fly, your own).
- Each user's `/connect/<id>` URL is the credential. Anyone with that URL can
  read that user's Whoop data via the MCP tools. The id is 24 bytes of
  cryptographic randomness, so guessing is not practical.
- Whoop OAuth refresh tokens are stored on disk under the configured
  `USER_STORE_DIR`. Anyone with access to that directory can read them.
- The OAuth authorization-code flow uses PKCE (RFC 7636, S256). Even if an
  authorization code is intercepted on the redirect, it cannot be exchanged
  without the per-flow code verifier held in server (or CLI) memory.
- `/login` is rate limited per IP to defeat trivially repeated grants.
- A baseline of HTTP security headers (CSP, `X-Frame-Options: DENY`, etc.) is
  applied to the HTML pages.

## Out of scope

- DoS resilience beyond the basic per-IP rate limit. Use a platform-level
  WAF/rate-limit if you expect public traffic.
- Encryption of tokens at rest. Hosted (HTTP-mode) per-user tokens are stored
  as readable JSON files under `USER_STORE_DIR`. If this matters, run on disk
  encrypted by the host OS or platform. The local stdio CLI can opt into
  `WHOOP_TOKEN_BACKEND=keyring` to put the token in the OS keychain instead
  of a JSON file under `~/.config/whoop-mcp/`.
- Compromise of the underlying host. If the box is rooted, all user tokens
  on it are compromised; rotate them via `/disconnect/<id>` or by clearing
  the store directory.

## Operational guidance

- Treat `WHOOP_CLIENT_SECRET` as a high-value secret. Set it via the platform's
  secret manager, never check it into git.
- Keep the deployed `PUBLIC_URL` on HTTPS only — the OAuth state cookie is
  marked `Secure` when `PUBLIC_URL` begins with `https://`.
- Periodically clean up unused records in `USER_STORE_DIR`.
