// Minimal OAuth 2.0 + PKCE client for the Whoop authorization server. There
// is no TypeScript ecosystem equivalent of golang.org/x/oauth2 worth adding
// as a dependency for this narrow use, so authorization-URL building and
// the two token-endpoint grants (authorization_code, refresh_token) are
// hand-rolled here with fetch.

import { AUTH_URL, DEFAULT_SCOPES, TOKEN_URL, type Config } from "./config.ts";

// Field names mirror the Whoop token response (and golang.org/x/oauth2's
// Token JSON encoding) so the persisted token file stays human-readable.
export interface Token {
  access_token: string;
  token_type?: string;
  refresh_token?: string;
  /** ISO 8601 timestamp; absent means unknown/never computed. */
  expiry?: string;
}

interface TokenResponse {
  access_token: string;
  token_type?: string;
  refresh_token?: string;
  expires_in?: number;
}

export function buildAuthCodeUrl(
  config: Config,
  opts: { state: string; codeChallenge: string; scopes?: string[] },
): string {
  const params = new URLSearchParams({
    response_type: "code",
    client_id: config.clientId,
    redirect_uri: config.redirectUri,
    state: opts.state,
    scope: (opts.scopes ?? DEFAULT_SCOPES).join(" "),
    code_challenge: opts.codeChallenge,
    code_challenge_method: "S256",
  });
  return `${AUTH_URL}?${params.toString()}`;
}

export async function exchangeCode(config: Config, code: string, codeVerifier: string): Promise<Token> {
  return requestToken(
    config,
    new URLSearchParams({
      grant_type: "authorization_code",
      code,
      redirect_uri: config.redirectUri,
      code_verifier: codeVerifier,
    }),
  );
}

export async function refreshAccessToken(config: Config, refreshToken: string): Promise<Token> {
  return requestToken(
    config,
    new URLSearchParams({
      grant_type: "refresh_token",
      refresh_token: refreshToken,
    }),
  );
}

async function requestToken(config: Config, body: URLSearchParams): Promise<Token> {
  body.set("client_id", config.clientId);
  body.set("client_secret", config.clientSecret);

  const res = await fetch(TOKEN_URL, {
    method: "POST",
    headers: {
      "Content-Type": "application/x-www-form-urlencoded",
      Accept: "application/json",
    },
    body,
  });
  const text = await res.text();
  if (!res.ok) {
    throw new Error(`whoop oauth: status ${res.status}: ${text}`);
  }

  const data = JSON.parse(text) as TokenResponse;
  const token: Token = {
    access_token: data.access_token,
    token_type: data.token_type,
    refresh_token: data.refresh_token,
  };
  if (typeof data.expires_in === "number") {
    token.expiry = new Date(Date.now() + data.expires_in * 1000).toISOString();
  }
  return token;
}
