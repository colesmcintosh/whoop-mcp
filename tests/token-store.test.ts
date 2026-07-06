import { describe, expect, test } from "bun:test";
import { mkdtemp, readdir, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import {
  loadToken,
  saveToken,
  seedFromRefreshTokenIfMissing,
  tokenStorePath,
} from "../src/auth/token-store.ts";

async function tempEnv(): Promise<NodeJS.ProcessEnv> {
  const dir = await mkdtemp(path.join(tmpdir(), "whoop-mcp-test-"));
  return { WHOOP_TOKEN_FILE: path.join(dir, "nested", "token.json") };
}

describe("token-store", () => {
  test("round-trips a token with 0600 perms and no stray tmp file", async () => {
    const env = await tempEnv();
    const token = { access_token: "abc", refresh_token: "def", expiry: new Date().toISOString() };
    await saveToken(token, env);

    expect(await loadToken(env)).toEqual(token);

    const file = tokenStorePath(env);
    const info = await stat(file);
    expect(info.mode & 0o777).toBe(0o600);

    const entries = await readdir(path.dirname(file));
    expect(entries.some((e) => e.endsWith(".tmp"))).toBe(false);
  });

  test("loadToken rejects when no token file exists", async () => {
    const env = await tempEnv();
    await expect(loadToken(env)).rejects.toThrow();
  });

  test("seedFromRefreshTokenIfMissing is a guaranteed no-op for an empty string", async () => {
    const env = await tempEnv();
    await seedFromRefreshTokenIfMissing("", env);
    await expect(loadToken(env)).rejects.toThrow();
  });

  test("seedFromRefreshTokenIfMissing seeds a placeholder token when none exists", async () => {
    const env = await tempEnv();
    await seedFromRefreshTokenIfMissing("seed-refresh", env);
    const loaded = await loadToken(env);
    expect(loaded.refresh_token).toBe("seed-refresh");
    expect(loaded.access_token).toBe("");
  });

  test("seedFromRefreshTokenIfMissing never overwrites an existing token", async () => {
    const env = await tempEnv();
    await saveToken({ access_token: "real", refresh_token: "real-refresh" }, env);
    await seedFromRefreshTokenIfMissing("seed-refresh", env);
    const loaded = await loadToken(env);
    expect(loaded.refresh_token).toBe("real-refresh");
    expect(loaded.access_token).toBe("real");
  });
});
