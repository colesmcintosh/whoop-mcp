// Single-tenant HTTP transport for whoop-mcp. There is exactly one Whoop
// account behind this server (see src/auth); this module only adds a
// remotely-reachable transport in front of it, gated by a static bearer
// secret. There is no per-user OAuth flow or credential storage here.

import { randomUUID, timingSafeEqual } from "node:crypto";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";

export const MCP_PATH = "/mcp";
export const HEALTH_PATH = "/healthz";

export interface HttpServerOptions {
  authToken: string;
}

export async function createHttpApp(server: McpServer, opts: HttpServerOptions): Promise<Server> {
  const transport = new StreamableHTTPServerTransport({ sessionIdGenerator: () => randomUUID() });
  await server.connect(transport);

  return createServer((req, res) => {
    handleRequest(req, res, transport, opts).catch((err: unknown) => {
      if (!res.headersSent) {
        res.writeHead(500, { "Content-Type": "text/plain" });
      }
      res.end(err instanceof Error ? err.message : "internal error");
    });
  });
}

async function handleRequest(
  req: IncomingMessage,
  res: ServerResponse,
  transport: StreamableHTTPServerTransport,
  opts: HttpServerOptions,
): Promise<void> {
  const url = new URL(req.url ?? "/", "http://localhost");

  if (url.pathname === HEALTH_PATH) {
    res.writeHead(200, { "Content-Type": "text/plain" });
    res.end("ok");
    return;
  }

  if (url.pathname !== MCP_PATH) {
    res.writeHead(404, { "Content-Type": "text/plain" });
    res.end("not found");
    return;
  }

  if (!isAuthorized(req, opts.authToken)) {
    res.writeHead(401, { "Content-Type": "text/plain", "WWW-Authenticate": "Bearer" });
    res.end("unauthorized");
    return;
  }

  await transport.handleRequest(req, res);
}

function isAuthorized(req: IncomingMessage, authToken: string): boolean {
  const header = req.headers.authorization;
  if (!header?.startsWith("Bearer ")) return false;
  return timingSafeEqualStrings(header.slice("Bearer ".length), authToken);
}

function timingSafeEqualStrings(a: string, b: string): boolean {
  const bufA = Buffer.from(a);
  const bufB = Buffer.from(b);
  if (bufA.length !== bufB.length) return false;
  return timingSafeEqual(bufA, bufB);
}
