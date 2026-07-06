// A minimal HTTP client for Whoop API v2. Returns raw JSON text rather than
// parsed/typed objects so the caller (MCP server) can forward responses to
// a language model without needing to stay in lockstep with Whoop's
// evolving schema.

export const BASE_URL = "https://api.prod.whoop.com/developer";

export class ApiError extends Error {
  readonly statusCode: number;
  readonly body: string;

  constructor(statusCode: number, body: string) {
    super(`whoop api: status ${statusCode}: ${body}`);
    this.name = "ApiError";
    this.statusCode = statusCode;
    this.body = body;
  }
}

export interface TokenSource {
  getAccessToken(): Promise<string>;
}

export interface ListParams {
  /** Defaults to 10 server-side. Max 25. Omitted entirely unless > 0. */
  limit?: number;
  /** Inclusive lower bound on the record's start time. */
  start?: Date;
  /** Exclusive upper bound on the record's start time. */
  end?: Date;
  /** Pagination token from a prior response. */
  nextToken?: string;
}

function formatRFC3339(d: Date): string {
  return d.toISOString().replace(/\.\d{3}Z$/, "Z");
}

function listParamsToSearch(p: ListParams): URLSearchParams {
  const v = new URLSearchParams();
  if (p.limit && p.limit > 0) v.set("limit", String(p.limit));
  if (p.start) v.set("start", formatRFC3339(p.start));
  if (p.end) v.set("end", formatRFC3339(p.end));
  if (p.nextToken) v.set("nextToken", p.nextToken);
  return v;
}

export class WhoopClient {
  constructor(
    private readonly tokenSource: TokenSource,
    private readonly baseUrl: string = BASE_URL,
  ) {}

  private async get(path: string, query?: URLSearchParams): Promise<string> {
    const qs = query?.toString();
    const url = qs ? `${this.baseUrl}${path}?${qs}` : `${this.baseUrl}${path}`;
    const accessToken = await this.tokenSource.getAccessToken();

    const res = await fetch(url, {
      headers: {
        Accept: "application/json",
        Authorization: `Bearer ${accessToken}`,
      },
    });
    const text = await res.text();
    if (!res.ok) throw new ApiError(res.status, text);
    if (text.length === 0) throw new Error("empty response body");
    return text;
  }

  getProfile(): Promise<string> {
    return this.get("/v2/user/profile/basic");
  }

  getBodyMeasurement(): Promise<string> {
    return this.get("/v2/user/measurement/body");
  }

  listCycles(params: ListParams = {}): Promise<string> {
    return this.get("/v2/cycle", listParamsToSearch(params));
  }

  getCycle(cycleId: string): Promise<string> {
    return this.get(`/v2/cycle/${encodeURIComponent(cycleId)}`);
  }

  getCycleRecovery(cycleId: string): Promise<string> {
    return this.get(`/v2/cycle/${encodeURIComponent(cycleId)}/recovery`);
  }

  getCycleSleep(cycleId: string): Promise<string> {
    return this.get(`/v2/cycle/${encodeURIComponent(cycleId)}/sleep`);
  }

  listRecovery(params: ListParams = {}): Promise<string> {
    return this.get("/v2/recovery", listParamsToSearch(params));
  }

  listSleep(params: ListParams = {}): Promise<string> {
    return this.get("/v2/activity/sleep", listParamsToSearch(params));
  }

  getSleep(sleepId: string): Promise<string> {
    return this.get(`/v2/activity/sleep/${encodeURIComponent(sleepId)}`);
  }

  listWorkouts(params: ListParams = {}): Promise<string> {
    return this.get("/v2/activity/workout", listParamsToSearch(params));
  }

  getWorkout(workoutId: string): Promise<string> {
    return this.get(`/v2/activity/workout/${encodeURIComponent(workoutId)}`);
  }
}
