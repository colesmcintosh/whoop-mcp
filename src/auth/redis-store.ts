// Redis-backed token storage over the Upstash REST API.
//
// Serverless platforms (Vercel) give each invocation a fresh, read-only
// filesystem, but Whoop rotates the refresh token on *every* refresh — a
// rotation that is lost is an account that has to be reconnected by hand.
// So a hosted deploy needs somewhere durable to put the rotated token, and
// the Upstash REST API is reachable with plain fetch, no driver needed.
//
// Credentials come from whichever pair the platform happens to set: the
// Vercel Marketplace integration exports KV_REST_API_*, a directly-created
// Upstash database exports UPSTASH_REDIS_REST_*.

import type { Token } from "./oauth-client.ts";

export interface RedisConfig {
  url: string;
  token: string;
  key: string;
}

const DEFAULT_KEY = "whoop-mcp:token";

/** Returns null unless a complete REST URL + token pair is present. */
export function redisConfigFromEnv(env: NodeJS.ProcessEnv = process.env): RedisConfig | null {
  const url = env.KV_REST_API_URL || env.UPSTASH_REDIS_REST_URL;
  const token = env.KV_REST_API_TOKEN || env.UPSTASH_REDIS_REST_TOKEN;
  if (!url || !token) return null;
  return { url: url.replace(/\/+$/, ""), token, key: env.WHOOP_TOKEN_KEY || DEFAULT_KEY };
}

export function redisLocation(config: RedisConfig): string {
  return `redis key ${config.key} at ${hostOf(config.url)}`;
}

export async function redisGetToken(config: RedisConfig): Promise<Token | null> {
  const result = await command(config, ["GET", config.key]);
  if (typeof result !== "string") return null;
  return JSON.parse(result) as Token;
}

export async function redisSetToken(config: RedisConfig, token: Token): Promise<void> {
  await command(config, ["SET", config.key, JSON.stringify(token)]);
}

/**
 * Upstash accepts a command as a JSON array POSTed to the base URL, which
 * keeps argument escaping out of the URL path.
 */
async function command(config: RedisConfig, args: string[]): Promise<unknown> {
  const res = await fetch(config.url, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${config.token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify(args),
  });
  const text = await res.text();
  if (!res.ok) {
    throw new Error(`redis token store: ${args[0] ?? ""} failed with status ${res.status}: ${text}`);
  }

  const body = JSON.parse(text) as { result?: unknown; error?: string };
  if (body.error) {
    throw new Error(`redis token store: ${args[0] ?? ""} failed: ${body.error}`);
  }
  return body.result;
}

function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}
