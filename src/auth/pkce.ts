// PKCE (RFC 7636) helpers for the authorization-code flow, plus the OAuth
// CSRF state token. Both are random base64url strings; the challenge is the
// S256 hash of the verifier.

import { createHash, randomBytes } from "node:crypto";

export function generateCodeVerifier(): string {
  return randomBytes(32).toString("base64url");
}

export function generateCodeChallenge(verifier: string): string {
  return createHash("sha256").update(verifier).digest("base64url");
}

export function generateState(): string {
  return randomBytes(24).toString("base64url");
}
