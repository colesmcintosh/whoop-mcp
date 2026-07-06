// Local JSON file storage for the single Whoop OAuth token this server
// uses. Writes are atomic (write-then-rename) with 0600 permissions.

import { promises as fs } from "node:fs";
import os from "node:os";
import path from "node:path";
import type { Token } from "./oauth-client.ts";

export function tokenStorePath(env: NodeJS.ProcessEnv = process.env): string {
  const override = env.WHOOP_TOKEN_FILE;
  if (override) return override;
  return path.join(userConfigDir(env), "whoop-mcp", "token.json");
}

export function tokenStoreLocation(env: NodeJS.ProcessEnv = process.env): string {
  return tokenStorePath(env);
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
  const data = await fs.readFile(tokenStorePath(env), "utf8");
  return JSON.parse(data) as Token;
}

export async function saveToken(token: Token, env: NodeJS.ProcessEnv = process.env): Promise<void> {
  const file = tokenStorePath(env);
  await fs.mkdir(path.dirname(file), { recursive: true, mode: 0o700 });
  const tmp = `${file}.${process.pid}.tmp`;
  await fs.writeFile(tmp, JSON.stringify(token, null, 2), { mode: 0o600 });
  await fs.rename(tmp, file);
}

/**
 * Seeds a token file containing only the given refresh token, if no token
 * is stored yet. On the first refresh, Whoop mints a fresh access+refresh
 * pair which the token source then writes back.
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
  if (await tokenExists(env)) return;
  await saveToken({ access_token: "", refresh_token: refreshToken }, env);
}

export async function tokenExists(env: NodeJS.ProcessEnv = process.env): Promise<boolean> {
  try {
    await loadToken(env);
    return true;
  } catch (err) {
    if (isNotFound(err)) return false;
    throw err;
  }
}

function isNotFound(err: unknown): boolean {
  return err instanceof Error && "code" in err && (err as NodeJS.ErrnoException).code === "ENOENT";
}
