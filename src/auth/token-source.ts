// An auto-refreshing accessor for the locally stored Whoop token. Whoop
// rotates the refresh token on every use, so every refresh is persisted
// back to the token store immediately (and concurrent callers share a
// single in-flight refresh so a rotated refresh token is never spent
// twice).

import type { Config } from "./config.ts";
import { refreshAccessToken, type Token } from "./oauth-client.ts";
import { loadToken, saveToken } from "./token-store.ts";

// Mirrors golang.org/x/oauth2's default early-refresh skew.
const EXPIRY_SKEW_MS = 10_000;

function isValid(token: Token): boolean {
  if (!token.access_token || !token.expiry) return false;
  return new Date(token.expiry).getTime() - EXPIRY_SKEW_MS > Date.now();
}

export interface TokenSource {
  getAccessToken(): Promise<string>;
}

export function createTokenSource(config: Config, env: NodeJS.ProcessEnv = process.env): TokenSource {
  let cached: Token | null = null;
  let refreshing: Promise<Token> | null = null;

  async function ensureLoaded(): Promise<Token> {
    if (!cached) {
      cached = await loadToken(env);
    }
    return cached;
  }

  function refresh(current: Token): Promise<Token> {
    const refreshToken = current.refresh_token;
    if (!refreshToken) {
      return Promise.reject(
        new Error("token expired and no refresh token available (run whoop-auth again)"),
      );
    }
    if (!refreshing) {
      refreshing = (async () => {
        const refreshed = await refreshAccessToken(config, refreshToken);
        await saveToken(refreshed, env);
        cached = refreshed;
        return refreshed;
      })().finally(() => {
        refreshing = null;
      });
    }
    return refreshing;
  }

  return {
    async getAccessToken(): Promise<string> {
      const token = await ensureLoaded();
      if (isValid(token)) return token.access_token;
      const refreshed = await refresh(token);
      return refreshed.access_token;
    },
  };
}
