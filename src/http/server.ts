// Single-tenant HTTP transport for whoop-mcp. There is exactly one Whoop
// account behind this server (see src/auth); this module only adds a
// remotely-reachable transport in front of it, gated by a static bearer
// secret. Browser OAuth (when `oauth` is set) is how that one account
// connects; it is not a multi-user login system.
//
// Each MCP client session gets its own McpServer + Streamable HTTP
// transport. Sharing one transport across clients (or reconnects) makes
// the second initialize fail.
//
// In stateless mode the per-session map is skipped entirely: every POST
// builds a throwaway server+transport and tears it down again. That is the
// only mode that works on a serverless platform, where consecutive
// requests from one client land on different instances and an in-memory
// session map is a promise the deployment cannot keep.

import { randomUUID } from "node:crypto";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { isInitializeRequest } from "@modelcontextprotocol/sdk/types.js";
import {
  OAUTH_START_PATH,
  SETUP_PATH,
  setupPage,
  statusPage,
  writeHtml,
  type OAuthSetup,
} from "./oauth-setup.ts";
import { timingSafeEqualStrings } from "./secret.ts";

export const MCP_PATH = "/mcp";
export const HEALTH_PATH = "/healthz";

export interface HttpServerOptions {
  authToken: string;
  /** When false, /mcp returns 503 until a Whoop token is stored. */
  isReady?: () => Promise<boolean>;
  /** Browser OAuth+PKCE setup for Docker/HTTP deploys. */
  oauth?: OAuthSetup;
  /**
   * Serve /mcp without server-side sessions: one server+transport per
   * request, no SSE stream, no mcp-session-id. Required on serverless.
   */
  stateless?: boolean;
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

  if (opts.oauth) {
    const callbackPath = opts.oauth.callbackPath();
    const isCallback =
      url.pathname === callbackPath &&
      req.method === "GET" &&
      (url.searchParams.has("code") || url.searchParams.has("error") || url.searchParams.has("state"));
    if (isCallback) {
      await opts.oauth.handleCallback(req, url, res);
      return;
    }
    if (url.pathname === OAUTH_START_PATH && req.method === "POST") {
      await opts.oauth.handleStart(req, res);
      return;
    }
    if (url.pathname === SETUP_PATH && req.method === "GET") {
      writeHtml(res, 200, setupPage());
      return;
    }
  }

  if (url.pathname === "/" && req.method === "GET") {
    const ready = opts.isReady ? await opts.isReady() : true;
    writeHtml(res, 200, statusPage(ready, Boolean(opts.oauth)));
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

  if (opts.isReady && !(await opts.isReady())) {
    res.writeHead(503, { "Content-Type": "text/plain" });
    res.end("whoop-mcp: not connected to Whoop yet; open / in a browser to authorize");
    return;
  }

  if (opts.stateless) {
    await handleStatelessRequest(req, res, createMcpServer);
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

/**
 * One MCP exchange, start to finish, with no state kept between requests.
 * GET (the SSE stream) and DELETE (session teardown) have nothing to act
 * on here, so they are refused rather than silently doing nothing.
 */
async function handleStatelessRequest(
  req: IncomingMessage,
  res: ServerResponse,
  createMcpServer: () => McpServer,
): Promise<void> {
  if (req.method !== "POST") {
    res.writeHead(405, { "Content-Type": "application/json", Allow: "POST" });
    res.end(jsonRpcError(-32000, "Method not allowed: this endpoint is stateless, use POST"));
    return;
  }

  let body: unknown;
  try {
    body = await readJsonBody(req);
  } catch {
    res.writeHead(400, { "Content-Type": "application/json" });
    res.end(jsonRpcError(-32700, "Parse error"));
    return;
  }

  const server = createMcpServer();
  const transport = new StreamableHTTPServerTransport({ sessionIdGenerator: undefined });
  res.on("close", () => {
    void transport.close();
    void server.close();
  });
  await server.connect(transport);
  await transport.handleRequest(req, res, body);
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

function headerValue(value: string | string[] | undefined): string | undefined {
  return Array.isArray(value) ? value[0] : value;
}

function jsonRpcError(code: number, message: string): string {
  return JSON.stringify({ jsonrpc: "2.0", error: { code, message }, id: null });
}
