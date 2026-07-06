import { describe, expect, test } from "bun:test";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { ApiError, WhoopClient, type TokenSource } from "../src/whoop/client.ts";

function fixedToken(token = "test-token"): TokenSource {
  return { getAccessToken: () => Promise.resolve(token) };
}

async function withServer(
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

describe("WhoopClient", () => {
  test("sends Accept and bearer Authorization headers, returns raw JSON text", async () => {
    let capturedAuth: string | undefined;
    let capturedAccept: string | undefined;
    await withServer(
      (req, res) => {
        capturedAuth = req.headers.authorization;
        capturedAccept = req.headers.accept;
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ ok: true }));
      },
      async (baseUrl) => {
        const client = new WhoopClient(fixedToken("abc123"), baseUrl);
        const body = await client.getProfile();
        expect(JSON.parse(body)).toEqual({ ok: true });
      },
    );
    expect(capturedAuth).toBe("Bearer abc123");
    expect(capturedAccept).toBe("application/json");
  });

  test("throws an ApiError carrying the status code on non-2xx", async () => {
    await withServer(
      (_req, res) => {
        res.writeHead(429, { "Content-Type": "text/plain" });
        res.end("slow down");
      },
      async (baseUrl) => {
        const client = new WhoopClient(fixedToken(), baseUrl);
        const err = await client.getProfile().catch((e: unknown) => e);
        expect(err).toBeInstanceOf(ApiError);
        expect((err as ApiError).statusCode).toBe(429);
        expect((err as Error).message).toContain("429");
      },
    );
  });

  test("treats an empty 200 response body as an error", async () => {
    await withServer(
      (_req, res) => {
        res.writeHead(200);
        res.end();
      },
      async (baseUrl) => {
        const client = new WhoopClient(fixedToken(), baseUrl);
        await expect(client.getProfile()).rejects.toThrow(/empty response body/);
      },
    );
  });

  test("serializes list params as UTC RFC3339 with camelCase nextToken, omitting limit<=0", async () => {
    let capturedUrl: string | undefined;
    await withServer(
      (req, res) => {
        capturedUrl = req.url;
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ records: [] }));
      },
      async (baseUrl) => {
        const client = new WhoopClient(fixedToken(), baseUrl);
        await client.listCycles({
          limit: 5,
          start: new Date("2026-05-01T00:00:00.000Z"),
          end: new Date("2026-05-02T00:00:00.000Z"),
          nextToken: "abc",
        });
      },
    );
    expect(capturedUrl).toContain("limit=5");
    expect(capturedUrl).toContain("start=2026-05-01T00%3A00%3A00Z");
    expect(capturedUrl).toContain("end=2026-05-02T00%3A00%3A00Z");
    expect(capturedUrl).toContain("nextToken=abc");
    expect(capturedUrl).not.toContain("next_token");
  });

  test("omits limit entirely when zero or unset", async () => {
    let capturedUrl: string | undefined;
    await withServer(
      (req, res) => {
        capturedUrl = req.url;
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ records: [] }));
      },
      async (baseUrl) => {
        const client = new WhoopClient(fixedToken(), baseUrl);
        await client.listCycles({ limit: 0 });
      },
    );
    expect(capturedUrl).not.toContain("limit");
  });

  test("URL-encodes path parameters", async () => {
    let capturedUrl: string | undefined;
    await withServer(
      (req, res) => {
        capturedUrl = req.url;
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ ok: true }));
      },
      async (baseUrl) => {
        const client = new WhoopClient(fixedToken(), baseUrl);
        await client.getCycle("abc def/../x");
      },
    );
    expect(capturedUrl).toBe(`/v2/cycle/${encodeURIComponent("abc def/../x")}`);
  });

  test("revoke-free surface: all 11 read endpoints hit the expected paths", async () => {
    const hit: string[] = [];
    await withServer(
      (req, res) => {
        hit.push(req.url ?? "");
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({}));
      },
      async (baseUrl) => {
        const client = new WhoopClient(fixedToken(), baseUrl);
        await client.getProfile();
        await client.getBodyMeasurement();
        await client.listCycles();
        await client.getCycle("c1");
        await client.getCycleRecovery("c1");
        await client.getCycleSleep("c1");
        await client.listRecovery();
        await client.listSleep();
        await client.getSleep("s1");
        await client.listWorkouts();
        await client.getWorkout("w1");
      },
    );
    expect(hit).toEqual([
      "/v2/user/profile/basic",
      "/v2/user/measurement/body",
      "/v2/cycle",
      "/v2/cycle/c1",
      "/v2/cycle/c1/recovery",
      "/v2/cycle/c1/sleep",
      "/v2/recovery",
      "/v2/activity/sleep",
      "/v2/activity/sleep/s1",
      "/v2/activity/workout",
      "/v2/activity/workout/w1",
    ]);
  });
});
