import { afterEach, describe, expect, test } from "bun:test";
import { redisConfigFromEnv } from "../src/auth/redis-store.ts";
import { loadToken, saveToken, seedFromRefreshTokenIfMissing, tokenExists, tokenStoreLocation } from "../src/auth/token-store.ts";

const originalFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = originalFetch;
});

const env: NodeJS.ProcessEnv = {
  KV_REST_API_URL: "https://example.upstash.io",
  KV_REST_API_TOKEN: "kv-token",
};

interface Call {
  url: string;
  authorization: string | null;
  args: string[];
}

/** Stubs Upstash with a one-key in-memory store and records every command. */
function stubRedis(): { calls: Call[] } {
  const calls: Call[] = [];
  let stored: string | null = null;
  globalThis.fetch = ((url: string | URL, init?: RequestInit) => {
    const args = JSON.parse(init?.body as string) as string[];
    calls.push({
      url: String(url),
      authorization: new Headers(init?.headers).get("authorization"),
      args,
    });
    let result: unknown = null;
    if (args[0] === "GET") {
      result = stored;
    } else if (args[0] === "SET") {
      stored = args[2] ?? null;
      result = "OK";
    }
    return Promise.resolve(
      new Response(JSON.stringify({ result }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  }) as typeof fetch;
  return { calls };
}

describe("redisConfigFromEnv", () => {
  test("is null unless a complete URL + token pair is present", () => {
    expect(redisConfigFromEnv({})).toBeNull();
    expect(redisConfigFromEnv({ KV_REST_API_URL: "https://example.upstash.io" })).toBeNull();
    expect(redisConfigFromEnv({ KV_REST_API_TOKEN: "t" })).toBeNull();
  });

  test("accepts either the Vercel KV or the Upstash variable names", () => {
    expect(redisConfigFromEnv(env)?.token).toBe("kv-token");
    expect(
      redisConfigFromEnv({
        UPSTASH_REDIS_REST_URL: "https://example.upstash.io",
        UPSTASH_REDIS_REST_TOKEN: "upstash-token",
      })?.token,
    ).toBe("upstash-token");
  });

  test("trims a trailing slash and defaults the key", () => {
    const config = redisConfigFromEnv({ ...env, KV_REST_API_URL: "https://example.upstash.io/" });
    expect(config?.url).toBe("https://example.upstash.io");
    expect(config?.key).toBe("whoop-mcp:token");
    expect(redisConfigFromEnv({ ...env, WHOOP_TOKEN_KEY: "custom" })?.key).toBe("custom");
  });
});

describe("token-store on redis", () => {
  test("is selected over the file backend when credentials are present", () => {
    expect(tokenStoreLocation(env)).toBe("redis key whoop-mcp:token at example.upstash.io");
    expect(tokenStoreLocation({ WHOOP_TOKEN_FILE: "/data/token.json" })).toBe("/data/token.json");
  });

  test("round-trips a token as an authorized Upstash command", async () => {
    const redis = stubRedis();
    const token = { access_token: "at", refresh_token: "rt" };
    await saveToken(token, env);

    expect(redis.calls[0]?.url).toBe("https://example.upstash.io");
    expect(redis.calls[0]?.authorization).toBe("Bearer kv-token");
    expect(redis.calls[0]?.args[0]).toBe("SET");
    expect(await loadToken(env)).toEqual(token);
  });

  test("reports an empty key as no token rather than failing", async () => {
    stubRedis();
    expect(await tokenExists(env)).toBe(false);
    await expect(loadToken(env)).rejects.toThrow(/no Whoop token stored/);
  });

  test("seeds an empty key but never overwrites a stored token", async () => {
    stubRedis();
    await seedFromRefreshTokenIfMissing("seed-refresh", env);
    expect((await loadToken(env)).refresh_token).toBe("seed-refresh");

    await saveToken({ access_token: "real", refresh_token: "rotated" }, env);
    await seedFromRefreshTokenIfMissing("seed-refresh", env);
    expect((await loadToken(env)).refresh_token).toBe("rotated");
  });

  test("surfaces an Upstash error instead of silently reporting no token", async () => {
    globalThis.fetch = (() =>
      Promise.resolve(new Response("WRONGPASS", { status: 401 }))) as unknown as typeof fetch;
    await expect(tokenExists(env)).rejects.toThrow(/status 401/);
  });
});
