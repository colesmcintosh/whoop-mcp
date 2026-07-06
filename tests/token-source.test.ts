import { afterEach, describe, expect, test } from "bun:test";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { loadConfigFromEnv } from "../src/auth/config.ts";
import { createTokenSource } from "../src/auth/token-source.ts";
import { loadToken, saveToken } from "../src/auth/token-store.ts";

const config = loadConfigFromEnv({ WHOOP_CLIENT_ID: "id", WHOOP_CLIENT_SECRET: "secret" });

async function tempEnv(): Promise<NodeJS.ProcessEnv> {
  const dir = await mkdtemp(path.join(tmpdir(), "whoop-mcp-test-"));
  return { WHOOP_TOKEN_FILE: path.join(dir, "token.json") };
}

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

const originalFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = originalFetch;
});

describe("createTokenSource", () => {
  test("returns the cached access token without refreshing while still valid", async () => {
    const env = await tempEnv();
    await saveToken(
      { access_token: "still-valid", refresh_token: "r1", expiry: new Date(Date.now() + 60_000).toISOString() },
      env,
    );
    let fetchCalls = 0;
    globalThis.fetch = (() => {
      fetchCalls++;
      throw new Error("should not have refreshed");
    }) as unknown as typeof fetch;

    const source = createTokenSource(config, env);
    expect(await source.getAccessToken()).toBe("still-valid");
    expect(fetchCalls).toBe(0);
  });

  test("refreshes and persists the rotated token when expired", async () => {
    const env = await tempEnv();
    await saveToken(
      { access_token: "old", refresh_token: "r1", expiry: new Date(Date.now() - 1000).toISOString() },
      env,
    );
    globalThis.fetch = (() =>
      Promise.resolve(jsonResponse({ access_token: "new", refresh_token: "r2", expires_in: 3600 }))) as unknown as typeof fetch;

    const source = createTokenSource(config, env);
    expect(await source.getAccessToken()).toBe("new");

    const persisted = await loadToken(env);
    expect(persisted.access_token).toBe("new");
    expect(persisted.refresh_token).toBe("r2");
  });

  test("dedupes concurrent refreshes so a rotated refresh token is only spent once", async () => {
    const env = await tempEnv();
    await saveToken(
      { access_token: "old", refresh_token: "r1", expiry: new Date(Date.now() - 1000).toISOString() },
      env,
    );
    let calls = 0;
    globalThis.fetch = (() => {
      calls++;
      return Promise.resolve(jsonResponse({ access_token: `new-${calls}`, refresh_token: "r2", expires_in: 3600 }));
    }) as unknown as typeof fetch;

    const source = createTokenSource(config, env);
    const [a, b] = await Promise.all([source.getAccessToken(), source.getAccessToken()]);
    expect(a).toBe(b);
    expect(calls).toBe(1);
  });

  test("throws a clear error when expired with no refresh token available", async () => {
    const env = await tempEnv();
    await saveToken({ access_token: "old", expiry: new Date(Date.now() - 1000).toISOString() }, env);
    const source = createTokenSource(config, env);
    await expect(source.getAccessToken()).rejects.toThrow(/refresh token/);
  });
});
