// Single-tenant HTTP transport for whoop-mcp. There is exactly one Whoop
// account behind this server (see src/auth); this module only adds a
// remotely-reachable transport in front of it, gated by a static bearer
// secret. There is no per-user OAuth flow or credential storage here.
//
// Each MCP client session gets its own McpServer + Streamable HTTP
// transport. Sharing one transport across clients (or reconnects) makes
// the second initialize fail.

import { createHash, randomUUID, timingSafeEqual } from "node:crypto";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { isInitializeRequest } from "@modelcontextprotocol/sdk/types.js";

export const MCP_PATH = "/mcp";
export const HEALTH_PATH = "/healthz";

export interface HttpServerOptions {
  authToken: string;
}

interface Session {
  server: McpServer;
  transport: StreamableHTTPServerTransport;
}

/** Parse `host:port`, `:port`, or `port` into a listen address. */
export function parseListenAddr(addr: string): { host: string; port: number } {
  const idx = addr.lastIndexOf(":");
  const host = idx > 0 ? addr.slice(0, idx) : "0.0.0.0";
  const port = Number(addr.slice(idx + 1));
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error(`invalid listen address: ${addr}`);
  }
  return { host, port };
}

/** Bearer-gated HTTP server; `createMcpServer` is invoked once per MCP session. */
export function createHttpApp(createMcpServer: () => McpServer, opts: HttpServerOptions): Server {
  const sessions = new Map<string, Session>();

  return createServer((req, res) => {
    handleRequest(req, res, createMcpServer, sessions, opts).catch((err: unknown) => {
      console.error("whoop-mcp http:", err instanceof Error ? err.message : err);
      if (!res.headersSent) {
        res.writeHead(500, { "Content-Type": "application/json" });
        res.end(jsonRpcError(-32603, "Internal server error"));
      }
    });
  });
}

async function handleRequest(
  req: IncomingMessage,
  res: ServerResponse,
  createMcpServer: () => McpServer,
  sessions: Map<string, Session>,
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

  const sessionId = headerValue(req.headers["mcp-session-id"]);
  const existing = sessionId ? sessions.get(sessionId) : undefined;

  if (req.method === "POST") {
    let body: unknown;
    try {
      body = await readJsonBody(req);
    } catch {
      res.writeHead(400, { "Content-Type": "application/json" });
      res.end(jsonRpcError(-32700, "Parse error"));
      return;
    }

    if (existing) {
      await existing.transport.handleRequest(req, res, body);
      return;
    }

    if (!sessionId && isInitializeRequest(body)) {
      const session = await openSession(createMcpServer, sessions);
      await session.transport.handleRequest(req, res, body);
      return;
    }

    res.writeHead(400, { "Content-Type": "application/json" });
    res.end(jsonRpcError(-32000, "Bad Request: No valid session ID provided"));
    return;
  }

  if (existing && (req.method === "GET" || req.method === "DELETE")) {
    await existing.transport.handleRequest(req, res);
    return;
  }

  res.writeHead(405, { "Content-Type": "text/plain" });
  res.end("method not allowed");
}

async function openSession(
  createMcpServer: () => McpServer,
  sessions: Map<string, Session>,
): Promise<Session> {
  const server = createMcpServer();
  const transport = new StreamableHTTPServerTransport({
    sessionIdGenerator: () => randomUUID(),
    onsessioninitialized: (id) => {
      sessions.set(id, { server, transport });
    },
  });
  transport.onclose = () => {
    const id = transport.sessionId;
    if (id) sessions.delete(id);
    void server.close();
  };
  await server.connect(transport);
  return { server, transport };
}

async function readJsonBody(req: IncomingMessage): Promise<unknown> {
  const raw = await new Promise<string>((resolve, reject) => {
    const acc: string[] = [];
    req.setEncoding("utf8");
    req.on("data", (chunk: string) => acc.push(chunk));
    req.on("end", () => resolve(acc.join("")));
    req.on("error", reject);
  });
  const trimmed = raw.trim();
  if (!trimmed) return undefined;
  return JSON.parse(trimmed) as unknown;
}

function isAuthorized(req: IncomingMessage, authToken: string): boolean {
  const header = req.headers.authorization;
  if (!header?.startsWith("Bearer ")) return false;
  return timingSafeEqualStrings(header.slice("Bearer ".length), authToken);
}

function timingSafeEqualStrings(a: string, b: string): boolean {
  const digest = (value: string) => createHash("sha256").update(value).digest();
  return timingSafeEqual(digest(a), digest(b));
}

function headerValue(value: string | string[] | undefined): string | undefined {
  return Array.isArray(value) ? value[0] : value;
}

function jsonRpcError(code: number, message: string): string {
  return JSON.stringify({ jsonrpc: "2.0", error: { code, message }, id: null });
}
