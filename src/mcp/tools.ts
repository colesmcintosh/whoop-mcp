import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import { z } from "zod";
import type { ListParams, WhoopClient } from "../whoop/client.ts";

function textResult(text: string): CallToolResult {
  return { content: [{ type: "text", text }] };
}

const rfc3339 = z
  .string()
  .refine((value) => !Number.isNaN(Date.parse(value)), { message: "must be an RFC3339 timestamp" });

const idShape = { id: z.string().describe("the Whoop record id (UUID string)") };
const cycleIdShape = { cycle_id: z.string().describe("the Whoop cycle id (UUID string)") };
const listShape = {
  limit: z.number().int().min(1).max(25).optional().describe("max records to return (1-25, default 10)"),
  start: rfc3339.optional().describe("inclusive lower bound on start time (RFC3339, e.g. 2026-05-01T00:00:00Z)"),
  end: rfc3339.optional().describe("exclusive upper bound on start time (RFC3339)"),
  next_token: z.string().optional().describe("pagination token from a prior response"),
};

const annotations = { readOnlyHint: true, openWorldHint: true };

interface ListInput {
  limit?: number;
  start?: string;
  end?: string;
  next_token?: string;
}

function parseDate(value: string, field: "start" | "end"): Date {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    throw new Error(`invalid ${field} time: ${value}`);
  }
  return parsed;
}

function toListParams(input: ListInput): ListParams {
  return {
    limit: input.limit,
    start: input.start !== undefined ? parseDate(input.start, "start") : undefined,
    end: input.end !== undefined ? parseDate(input.end, "end") : undefined,
    nextToken: input.next_token,
  };
}

export function registerTools(server: McpServer, client: WhoopClient): void {
  server.registerTool(
    "get_profile",
    {
      description: "Get the authenticated user's basic profile (name, email, user id).",
      annotations,
    },
    async () => textResult(await client.getProfile()),
  );

  server.registerTool(
    "get_body_measurement",
    {
      description: "Get the user's body measurements: height, weight, and max heart rate.",
      annotations,
    },
    async () => textResult(await client.getBodyMeasurement()),
  );

  server.registerTool(
    "list_cycles",
    {
      description:
        "List physiological cycles in descending order by start time. Each cycle has strain and heart rate data.",
      inputSchema: listShape,
      annotations,
    },
    async (input) => textResult(await client.listCycles(toListParams(input))),
  );

  server.registerTool(
    "get_cycle",
    { description: "Get a single cycle by id.", inputSchema: cycleIdShape, annotations },
    async ({ cycle_id }) => textResult(await client.getCycle(cycle_id)),
  );

  server.registerTool(
    "get_cycle_recovery",
    { description: "Get the recovery score attached to a specific cycle.", inputSchema: cycleIdShape, annotations },
    async ({ cycle_id }) => textResult(await client.getCycleRecovery(cycle_id)),
  );

  server.registerTool(
    "get_cycle_sleep",
    { description: "Get the sleep record attached to a specific cycle.", inputSchema: cycleIdShape, annotations },
    async ({ cycle_id }) => textResult(await client.getCycleSleep(cycle_id)),
  );

  server.registerTool(
    "list_recovery",
    {
      description:
        "List recovery records (recovery score, resting HR, HRV, SpO2, skin temperature) paginated and filterable by time range.",
      inputSchema: listShape,
      annotations,
    },
    async (input) => textResult(await client.listRecovery(toListParams(input))),
  );

  server.registerTool(
    "list_sleep",
    {
      description: "List sleep records with stage durations and sleep performance.",
      inputSchema: listShape,
      annotations,
    },
    async (input) => textResult(await client.listSleep(toListParams(input))),
  );

  server.registerTool(
    "get_sleep",
    { description: "Get a single sleep record by id.", inputSchema: idShape, annotations },
    async ({ id }) => textResult(await client.getSleep(id)),
  );

  server.registerTool(
    "list_workouts",
    {
      description:
        "List workouts with sport classification, strain, average heart rate, distance, and HR zone durations.",
      inputSchema: listShape,
      annotations,
    },
    async (input) => textResult(await client.listWorkouts(toListParams(input))),
  );

  server.registerTool(
    "get_workout",
    { description: "Get a single workout by id.", inputSchema: idShape, annotations },
    async ({ id }) => textResult(await client.getWorkout(id)),
  );
}
