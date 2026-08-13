import { describe, expect, test } from "bun:test";
import type { AddressInfo } from "node:net";
import type { Server } from "node:http";
import { createHttpApp, HEALTH_PATH, MCP_PATH, parseListenAddr } from "../src/http/server.ts";
import { createServer as createMcpServer } from "../src/mcp/create-server.ts";
import { WhoopClient } from "../src/whoop/client.ts";

function stubClient(): WhoopClient {
  return new WhoopClient({ getAccessToken: () => Promise.resolve("token") });
}

async function withApp(authToken: string, fn: (baseUrl: string) => Promise<void>): Promise<void> {
  const app: Server = createHttpApp(() => createMcpServer(stubClient()), { authToken });
  await new Promise<void>((resolve) => app.listen(0, "127.0.0.1", resolve));
  const address = app.address() as AddressInfo;
  try {
    await fn(`http://127.0.0.1:${address.port}`);
  } finally {
    await new Promise<void>((resolve) => app.close(() => resolve()));
  }
}

const initBody = {
  jsonrpc: "2.0",
  id: 1,
  method: "initialize",
  params: {
    protocolVersion: "2025-06-18",
    capabilities: {},
    clientInfo: { name: "test-client", version: "0.0.0" },
  },
};

function mcpHeaders(authToken: string, sessionId?: string): Record<string, string> {
  const headers: Record<string, string> = {
    Authorization: `Bearer ${authToken}`,
    "Content-Type": "application/json",
    Accept: "application/json, text/event-stream",
  };
  if (sessionId) headers["mcp-session-id"] = sessionId;
  return headers;
}

describe("parseListenAddr", () => {
  test("defaults host when only a port is given", () => {
    expect(parseListenAddr(":8080")).toEqual({ host: "0.0.0.0", port: 8080 });
    expect(parseListenAddr("8080")).toEqual({ host: "0.0.0.0", port: 8080 });
  });

  test("parses host:port", () => {
    expect(parseListenAddr("127.0.0.1:9090")).toEqual({ host: "127.0.0.1", port: 9090 });
  });

  test("rejects a missing or out-of-range port", () => {
    expect(() => parseListenAddr("localhost")).toThrow(/invalid listen address/);
    expect(() => parseListenAddr(":0")).toThrow(/invalid listen address/);
    expect(() => parseListenAddr(":99999")).toThrow(/invalid listen address/);
  });
});

describe("http server", () => {
  test("/healthz responds 200 without needing auth", async () => {
    await withApp("secret", async (baseUrl) => {
      const res = await fetch(`${baseUrl}${HEALTH_PATH}`);
      expect(res.status).toBe(200);
    });
  });

  test("unknown paths 404", async () => {
    await withApp("secret", async (baseUrl) => {
      const res = await fetch(`${baseUrl}/nope`);
      expect(res.status).toBe(404);
    });
  });

  test("the mcp endpoint rejects a missing or wrong bearer token", async () => {
    await withApp("secret", async (baseUrl) => {
      const noAuth = await fetch(`${baseUrl}${MCP_PATH}`, { method: "POST" });
      expect(noAuth.status).toBe(401);

      const wrongAuth = await fetch(`${baseUrl}${MCP_PATH}`, {
        method: "POST",
        headers: { Authorization: "Bearer wrong" },
      });
      expect(wrongAuth.status).toBe(401);
    });
  });

  test("the mcp endpoint lets the correct bearer token through to the transport", async () => {
    await withApp("secret", async (baseUrl) => {
      const res = await fetch(`${baseUrl}${MCP_PATH}`, {
        method: "POST",
        headers: mcpHeaders("secret"),
        body: JSON.stringify(initBody),
      });
      expect(res.status).not.toBe(401);
    });
  });

  test("two initialize requests each get their own session", async () => {
    await withApp("secret", async (baseUrl) => {
      const a = await fetch(`${baseUrl}${MCP_PATH}`, {
        method: "POST",
        headers: mcpHeaders("secret"),
        body: JSON.stringify(initBody),
      });
      const b = await fetch(`${baseUrl}${MCP_PATH}`, {
        method: "POST",
        headers: mcpHeaders("secret"),
        body: JSON.stringify({ ...initBody, id: 2 }),
      });
      expect(a.status).not.toBe(400);
      expect(b.status).not.toBe(400);
      const idA = a.headers.get("mcp-session-id");
      const idB = b.headers.get("mcp-session-id");
      expect(idA).toBeTruthy();
      expect(idB).toBeTruthy();
      expect(idA).not.toBe(idB);
    });
  });

  test("a follow-up request reuses the session id from initialize", async () => {
    await withApp("secret", async (baseUrl) => {
      const init = await fetch(`${baseUrl}${MCP_PATH}`, {
        method: "POST",
        headers: mcpHeaders("secret"),
        body: JSON.stringify(initBody),
      });
      const sessionId = init.headers.get("mcp-session-id");
      expect(sessionId).toBeTruthy();

      const listed = await fetch(`${baseUrl}${MCP_PATH}`, {
        method: "POST",
        headers: mcpHeaders("secret", sessionId!),
        body: JSON.stringify({ jsonrpc: "2.0", id: 2, method: "tools/list", params: {} }),
      });
      expect(listed.status).not.toBe(400);
      expect(listed.status).not.toBe(401);
    });
  });

  test("invalid JSON on /mcp is a 400 parse error, not a 500", async () => {
    await withApp("secret", async (baseUrl) => {
      const res = await fetch(`${baseUrl}${MCP_PATH}`, {
        method: "POST",
        headers: mcpHeaders("secret"),
        body: "{not-json",
      });
      expect(res.status).toBe(400);
      const body = (await res.json()) as { error: { code: number } };
      expect(body.error.code).toBe(-32700);
    });
  });
});
