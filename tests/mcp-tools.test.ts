import { describe, expect, test } from "bun:test";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { createServer as createMcpServer } from "../src/mcp/create-server.ts";
import { WhoopClient } from "../src/whoop/client.ts";

async function withFixture(
  handler: (req: IncomingMessage, res: ServerResponse) => void,
  fn: (baseUrl: string) => Promise<void>,
): Promise<void> {
  const server: Server = createServer(handler);
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address() as AddressInfo;
  try {
    await fn(`http://127.0.0.1:${address.port}`);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
}

async function connectedClient(whoopClient: WhoopClient): Promise<Client> {
  const server = createMcpServer(whoopClient);
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
  const client = new Client({ name: "test-client", version: "0.0.0" });
  await Promise.all([server.connect(serverTransport), client.connect(clientTransport)]);
  return client;
}

describe("registerTools via a real MCP client", () => {
  test("lists all 11 tools", async () => {
    const whoopClient = new WhoopClient({ getAccessToken: () => Promise.resolve("t") });
    const client = await connectedClient(whoopClient);
    const { tools } = await client.listTools();
    expect(tools.map((t) => t.name).sort()).toEqual(
      [
        "get_body_measurement",
        "get_cycle",
        "get_cycle_recovery",
        "get_cycle_sleep",
        "get_profile",
        "get_sleep",
        "get_workout",
        "list_cycles",
        "list_recovery",
        "list_sleep",
        "list_workouts",
      ].sort(),
    );
  });

  test("get_profile round-trips the Whoop API response as tool text content", async () => {
    await withFixture(
      (_req, res) => {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ first_name: "Ada", last_name: "Lovelace" }));
      },
      async (baseUrl) => {
        const whoopClient = new WhoopClient({ getAccessToken: () => Promise.resolve("t") }, baseUrl);
        const client = await connectedClient(whoopClient);
        const result = await client.callTool({ name: "get_profile", arguments: {} });
        const content = result.content as Array<{ type: string; text: string }>;
        expect(JSON.parse(content[0]!.text)).toEqual({ first_name: "Ada", last_name: "Lovelace" });
      },
    );
  });

  test("get_cycle passes the cycle_id argument through to the Whoop client", async () => {
    let capturedUrl: string | undefined;
    await withFixture(
      (req, res) => {
        capturedUrl = req.url;
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ id: "cycle-42" }));
      },
      async (baseUrl) => {
        const whoopClient = new WhoopClient({ getAccessToken: () => Promise.resolve("t") }, baseUrl);
        const client = await connectedClient(whoopClient);
        await client.callTool({ name: "get_cycle", arguments: { cycle_id: "cycle-42" } });
      },
    );
    expect(capturedUrl).toBe("/v2/cycle/cycle-42");
  });

  test("a Whoop API failure surfaces as a tool error result, not a thrown protocol error", async () => {
    await withFixture(
      (_req, res) => {
        res.writeHead(500, { "Content-Type": "text/plain" });
        res.end("boom");
      },
      async (baseUrl) => {
        const whoopClient = new WhoopClient({ getAccessToken: () => Promise.resolve("t") }, baseUrl);
        const client = await connectedClient(whoopClient);
        const result = await client.callTool({ name: "get_profile", arguments: {} });
        expect(result.isError).toBe(true);
      },
    );
  });
});
