#!/usr/bin/env bun
// Command whoop-mcp serves the Whoop API to MCP clients for a single
// account.
//
// Two transports, selected by the environment:
//   - stdio (default): the MCP client launches this as a subprocess.
//   - HTTP (when PORT or MCP_HTTP_ADDR is set): a single-tenant remote
//     endpoint at /mcp, gated by a bearer secret (MCP_AUTH_TOKEN).
//
// Both read the same locally stored OAuth token (see src/auth); there is
// no per-user login flow or server-side credential store.

import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { loadConfigFromEnv } from "../auth/config.ts";
import { createTokenSource } from "../auth/token-source.ts";
import { seedFromRefreshTokenIfMissing, tokenExists } from "../auth/token-store.ts";
import { createHttpApp } from "../http/server.ts";
import { createServer } from "../mcp/create-server.ts";
import { WhoopClient } from "../whoop/client.ts";

async function main(): Promise<void> {
  const config = loadConfigFromEnv();
  await seedFromRefreshTokenIfMissing(process.env.WHOOP_REFRESH_TOKEN);
  if (!(await tokenExists())) {
    throw new Error("no token stored; run whoop-auth first (or set WHOOP_REFRESH_TOKEN)");
  }

  const tokenSource = createTokenSource(config);
  const client = new WhoopClient(tokenSource);
  const server = createServer(client);

  const addr = httpListenAddr();
  if (!addr) {
    await server.connect(new StdioServerTransport());
    return;
  }

  const authToken = process.env.MCP_AUTH_TOKEN;
  if (!authToken) {
    throw new Error("MCP_AUTH_TOKEN must be set when running in HTTP mode");
  }
  const app = await createHttpApp(server, { authToken });
  const { host, port } = parseAddr(addr);
  await new Promise<void>((resolve, reject) => {
    app.once("error", reject);
    app.listen(port, host, resolve);
  });
  console.error(`whoop-mcp listening on ${host}:${port}`);
}

function httpListenAddr(): string | undefined {
  return process.env.MCP_HTTP_ADDR || (process.env.PORT ? `:${process.env.PORT}` : undefined);
}

function parseAddr(addr: string): { host: string; port: number } {
  const idx = addr.lastIndexOf(":");
  const host = idx > 0 ? addr.slice(0, idx) : "0.0.0.0";
  const port = Number(addr.slice(idx + 1));
  return { host, port };
}

main().catch((err: unknown) => {
  console.error("whoop-mcp:", err instanceof Error ? err.message : err);
  process.exit(1);
});
