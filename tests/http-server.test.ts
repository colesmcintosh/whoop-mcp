import { describe, expect, test } from "bun:test";
import type { AddressInfo } from "node:net";
import type { Server } from "node:http";
import { createHttpApp, HEALTH_PATH, MCP_PATH } from "../src/http/server.ts";
import { createServer as createMcpServer } from "../src/mcp/create-server.ts";
import { WhoopClient } from "../src/whoop/client.ts";

function stubClient(): WhoopClient {
  return new WhoopClient({ getAccessToken: () => Promise.resolve("token") });
}

async function withApp(authToken: string, fn: (baseUrl: string) => Promise<void>): Promise<void> {
  const server = createMcpServer(stubClient());
  const app: Server = await createHttpApp(server, { authToken });
  await new Promise<void>((resolve) => app.listen(0, "127.0.0.1", resolve));
  const address = app.address() as AddressInfo;
  try {
    await fn(`http://127.0.0.1:${address.port}`);
  } finally {
    await new Promise<void>((resolve) => app.close(() => resolve()));
  }
}

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
        headers: {
          Authorization: "Bearer secret",
          "Content-Type": "application/json",
          Accept: "application/json, text/event-stream",
        },
        body: JSON.stringify({
          jsonrpc: "2.0",
          id: 1,
          method: "initialize",
          params: {
            protocolVersion: "2025-06-18",
            capabilities: {},
            clientInfo: { name: "test-client", version: "0.0.0" },
          },
        }),
      });
      // Whatever the transport decides, auth must not be what rejected it.
      expect(res.status).not.toBe(401);
    });
  });
});
