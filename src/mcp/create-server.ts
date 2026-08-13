import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { SERVER_NAME, SERVER_VERSION } from "../version.ts";
import type { WhoopClient } from "../whoop/client.ts";
import { registerTools } from "./tools.ts";

export { SERVER_NAME, SERVER_VERSION };

export function createServer(client: WhoopClient): McpServer {
  const server = new McpServer({ name: SERVER_NAME, version: SERVER_VERSION });
  registerTools(server, client);
  return server;
}
