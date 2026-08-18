// Storage for the single Whoop OAuth token this server uses.
//
// Two backends, chosen by the environment:
//   - file (default): a local JSON file written atomically
//     (write-then-rename) with 0600 permissions. Used by the stdio CLI and
//     by container deploys with a mounted volume.
//   - redis: the Upstash REST API, when its credentials are present. Used
//     by serverless deploys, where the filesystem does not survive the
//     invocation and a rotated refresh token written to disk is lost.

import { promises as fs } from "node:fs";
import os from "node:os";
import path from "node:path";
import type { Token } from "./oauth-client.ts";
import { redisConfigFromEnv, redisGetToken, redisLocation, redisSetToken } from "./redis-store.ts";

/** Thrown by loadToken when the backend holds no token yet. */
export class TokenNotFoundError extends Error {
  constructor(location: string) {
    super(`no Whoop token stored at ${location}`);
    this.name = "TokenNotFoundError";
  }
}

interface TokenStore {
  /** Human-readable location, for CLI output and error messages. */
  location: string;
  /** Resolves to null when nothing is stored yet. */
  load(): Promise<Token | null>;
  save(token: Token): Promise<void>;
}

export function tokenStorePath(env: NodeJS.ProcessEnv = process.env): string {
  const override = env.WHOOP_TOKEN_FILE;
  if (override) return override;
  return path.join(userConfigDir(env), "whoop-mcp", "token.json");
}

/** Where this environment's tokens are kept: a file path, or a Redis key. */
export function tokenStoreLocation(env: NodeJS.ProcessEnv = process.env): string {
  return resolveStore(env).location;
}

function resolveStore(env: NodeJS.ProcessEnv): TokenStore {
  const redis = redisConfigFromEnv(env);
  if (redis) {
    return {
      location: redisLocation(redis),
      load: () => redisGetToken(redis),
      save: (token) => redisSetToken(redis, token),
    };
  }
  return fileStore(tokenStorePath(env));
}

function fileStore(file: string): TokenStore {
  return {
    location: file,
    async load(): Promise<Token | null> {
      try {
        return JSON.parse(await fs.readFile(file, "utf8")) as Token;
      } catch (err) {
        if (isNotFound(err)) return null;
        throw err;
      }
    },
    async save(token: Token): Promise<void> {
      await fs.mkdir(path.dirname(file), { recursive: true, mode: 0o700 });
      const tmp = `${file}.${process.pid}.tmp`;
      await fs.writeFile(tmp, JSON.stringify(token, null, 2), { mode: 0o600 });
      await fs.rename(tmp, file);
    },
  };
}

function userConfigDir(env: NodeJS.ProcessEnv): string {
  switch (process.platform) {
    case "darwin":
      return path.join(os.homedir(), "Library", "Application Support");
    case "win32":
      return env.AppData || path.join(os.homedir(), "AppData", "Roaming");
    default:
      return env.XDG_CONFIG_HOME || path.join(os.homedir(), ".config");
  }
}

export async function loadToken(env: NodeJS.ProcessEnv = process.env): Promise<Token> {
  const store = resolveStore(env);
  const token = await store.load();
  if (!token) throw new TokenNotFoundError(store.location);
  return token;
}

export async function saveToken(token: Token, env: NodeJS.ProcessEnv = process.env): Promise<void> {
  await resolveStore(env).save(token);
}

/**
 * Seeds a token containing only the given refresh token, if none is stored
 * yet. On the first refresh, Whoop mints a fresh access+refresh pair which
 * the token source then writes back.
 *
 * This is the bootstrap mechanism for hosted deployments where running the
 * browser-based OAuth flow on the server is impractical: authorize once
 * locally with whoop-auth, copy the refresh_token into a deploy-time env
 * var, and the server seeds on first start.
 */
export async function seedFromRefreshTokenIfMissing(
  refreshToken: string | undefined,
  env: NodeJS.ProcessEnv = process.env,
): Promise<void> {
  if (!refreshToken) return;
  const store = resolveStore(env);
  if (await store.load()) return;
  await store.save({ access_token: "", refresh_token: refreshToken });
}

export async function tokenExists(env: NodeJS.ProcessEnv = process.env): Promise<boolean> {
  return (await resolveStore(env).load()) !== null;
}

function isNotFound(err: unknown): boolean {
  return err instanceof Error && "code" in err && (err as NodeJS.ErrnoException).code === "ENOENT";
}
