import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { WhoopClient } from "../whoop/client.ts";
import { registerTools } from "./tools.ts";

export const SERVER_NAME = "whoop-mcp";
export const SERVER_VERSION = "0.1.0";

export function createServer(client: WhoopClient): McpServer {
  const server = new McpServer({ name: SERVER_NAME, version: SERVER_VERSION });
  registerTools(server, client);
  return server;
}
