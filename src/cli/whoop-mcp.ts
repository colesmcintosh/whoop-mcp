#!/usr/bin/env bun
// Command whoop-mcp serves the Whoop API to MCP clients for a single
// account.
//
// Two transports, selected by the environment:
//   - stdio (default): the MCP client launches this as a subprocess.
//   - HTTP (when PORT or MCP_HTTP_ADDR is set): a single-tenant remote
//     endpoint at /mcp, gated by a bearer secret (MCP_AUTH_TOKEN).
//     If no Whoop token is stored yet, open / in a browser to authorize.

import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { loadConfigFromEnv } from "../auth/config.ts";
import { createTokenSource } from "../auth/token-source.ts";
import { seedFromRefreshTokenIfMissing, tokenExists } from "../auth/token-store.ts";
import { OAuthSetup } from "../http/oauth-setup.ts";
import { createHttpApp, parseListenAddr } from "../http/server.ts";
import { createServer } from "../mcp/create-server.ts";
import { WhoopClient } from "../whoop/client.ts";

async function main(): Promise<void> {
  const config = loadConfigFromEnv();
  await seedFromRefreshTokenIfMissing(process.env.WHOOP_REFRESH_TOKEN);

  const addr = httpListenAddr();
  if (!addr) {
    if (!(await tokenExists())) {
      throw new Error("no token stored; run whoop-auth first (or set WHOOP_REFRESH_TOKEN)");
    }
    const tokenSource = createTokenSource(config);
    const client = new WhoopClient(tokenSource);
    const server = createServer(client);
    await server.connect(new StdioServerTransport());
    return;
  }

  const authToken = process.env.MCP_AUTH_TOKEN;
  if (!authToken) {
    throw new Error("MCP_AUTH_TOKEN must be set when running in HTTP mode");
  }

  const tokenSource = createTokenSource(config);
  const client = new WhoopClient(tokenSource);
  const app = createHttpApp(() => createServer(client), {
    authToken,
    isReady: () => tokenExists(),
    oauth: new OAuthSetup(config, authToken, process.env, () => tokenSource.reload()),
  });
  const { host, port } = parseListenAddr(addr);
  await new Promise<void>((resolve, reject) => {
    app.once("error", reject);
    app.listen(port, host, resolve);
  });
  console.error(`whoop-mcp listening on ${host}:${port}`);
  if (!(await tokenExists())) {
    console.error(`no Whoop token yet — open http://127.0.0.1:${port}/ to connect`);
  }
}

function httpListenAddr(): string | undefined {
  return process.env.MCP_HTTP_ADDR || (process.env.PORT ? `:${process.env.PORT}` : undefined);
}

main().catch((err: unknown) => {
  console.error("whoop-mcp:", err instanceof Error ? err.message : err);
  process.exit(1);
});
