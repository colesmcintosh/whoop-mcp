import { afterEach, describe, expect, test } from "bun:test";
import { loadConfigFromEnv } from "../src/auth/config.ts";
import { buildAuthCodeUrl, exchangeCode, refreshAccessToken } from "../src/auth/oauth-client.ts";

const config = loadConfigFromEnv({
  WHOOP_CLIENT_ID: "id-123",
  WHOOP_CLIENT_SECRET: "secret-456",
  WHOOP_REDIRECT_URI: "http://localhost:8080/oauth/callback",
});

describe("buildAuthCodeUrl", () => {
  test("includes PKCE challenge, state, and default scopes", () => {
    const url = new URL(
      buildAuthCodeUrl(config, { state: "state-1", codeChallenge: "challenge-1" }),
    );
    expect(url.searchParams.get("response_type")).toBe("code");
    expect(url.searchParams.get("client_id")).toBe("id-123");
    expect(url.searchParams.get("redirect_uri")).toBe("http://localhost:8080/oauth/callback");
    expect(url.searchParams.get("state")).toBe("state-1");
    expect(url.searchParams.get("code_challenge")).toBe("challenge-1");
    expect(url.searchParams.get("code_challenge_method")).toBe("S256");
    expect(url.searchParams.get("scope")).toContain("offline");
  });
});

const originalFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = originalFetch;
});

describe("exchangeCode / refreshAccessToken", () => {
  test("exchangeCode posts the authorization_code grant and computes an ISO expiry", async () => {
    let capturedBody: URLSearchParams | undefined;
    globalThis.fetch = ((_url: string, init?: RequestInit) => {
      capturedBody = init?.body as URLSearchParams;
      return Promise.resolve(
        new Response(JSON.stringify({ access_token: "at", refresh_token: "rt", expires_in: 60 }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }) as unknown as typeof fetch;

    const before = Date.now();
    const token = await exchangeCode(config, "auth-code", "verifier-1");
    expect(token.access_token).toBe("at");
    expect(token.refresh_token).toBe("rt");
    expect(new Date(token.expiry!).getTime()).toBeGreaterThanOrEqual(before + 59_000);

    expect(capturedBody?.get("grant_type")).toBe("authorization_code");
    expect(capturedBody?.get("code")).toBe("auth-code");
    expect(capturedBody?.get("code_verifier")).toBe("verifier-1");
    expect(capturedBody?.get("client_id")).toBe("id-123");
    expect(capturedBody?.get("client_secret")).toBe("secret-456");
  });

  test("refreshAccessToken posts the refresh_token grant", async () => {
    let capturedBody: URLSearchParams | undefined;
    globalThis.fetch = ((_url: string, init?: RequestInit) => {
      capturedBody = init?.body as URLSearchParams;
      return Promise.resolve(
        new Response(JSON.stringify({ access_token: "at2", refresh_token: "rt2", expires_in: 120 }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }) as unknown as typeof fetch;

    const token = await refreshAccessToken(config, "old-refresh");
    expect(token.access_token).toBe("at2");
    expect(capturedBody?.get("grant_type")).toBe("refresh_token");
    expect(capturedBody?.get("refresh_token")).toBe("old-refresh");
  });

  test("throws with the status code and body on a non-2xx token response", async () => {
    globalThis.fetch = (() =>
      Promise.resolve(new Response("invalid_grant", { status: 400 }))) as unknown as typeof fetch;
    await expect(refreshAccessToken(config, "bad-refresh")).rejects.toThrow(/400/);
  });
});
