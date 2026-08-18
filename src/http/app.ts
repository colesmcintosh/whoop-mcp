// Wiring shared by every HTTP entry point (the `whoop-mcp` CLI in HTTP
// mode, and the Vercel function in api/): config, token source, Whoop
// client, MCP server factory, and the bearer-gated transport in front of
// them.

import type { Server } from "node:http";
import { loadConfigFromEnv } from "../auth/config.ts";
import { createTokenSource } from "../auth/token-source.ts";
import { seedFromRefreshTokenIfMissing, tokenExists } from "../auth/token-store.ts";
import { createServer as createMcpServer } from "../mcp/create-server.ts";
import { WhoopClient } from "../whoop/client.ts";
import { OAuthSetup } from "./oauth-setup.ts";
import { createHttpApp } from "./server.ts";

export interface WhoopHttpAppOptions {
  env?: NodeJS.ProcessEnv;
  /** Serve /mcp without server-side sessions. Required on serverless. */
  stateless?: boolean;
}

/**
 * Builds the HTTP app synchronously so a serverless entry point can call
 * listen() at module scope. Seeding from WHOOP_REFRESH_TOKEN is the one
 * async step, so it runs in the background and every readiness check waits
 * on it — a request can never observe a half-seeded store.
 */
export function createWhoopHttpApp(opts: WhoopHttpAppOptions = {}): Server {
  const env = opts.env ?? process.env;
  const config = loadConfigFromEnv(env);

  const authToken = env.MCP_AUTH_TOKEN;
  if (!authToken) {
    throw new Error("MCP_AUTH_TOKEN must be set when running in HTTP mode");
  }

  const seeded = seedFromRefreshTokenIfMissing(env.WHOOP_REFRESH_TOKEN, env);
  // Park any seeding failure on the promise itself rather than letting it
  // surface as an unhandled rejection; isReady() re-raises it in request
  // context, where it becomes a 500 with a logged message.
  seeded.catch(() => undefined);

  const tokenSource = createTokenSource(config, env);
  const client = new WhoopClient(tokenSource);

  return createHttpApp(() => createMcpServer(client), {
    authToken,
    stateless: opts.stateless,
    isReady: async () => {
      await seeded;
      return tokenExists(env);
    },
    oauth: new OAuthSetup(config, authToken, env, () => {
      tokenSource.reload();
    }),
  });
}
