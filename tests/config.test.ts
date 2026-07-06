import { describe, expect, test } from "bun:test";
import { loadConfigFromEnv } from "../src/auth/config.ts";

describe("loadConfigFromEnv", () => {
  test("throws when the client id or secret is missing", () => {
    expect(() => loadConfigFromEnv({})).toThrow();
    expect(() => loadConfigFromEnv({ WHOOP_CLIENT_ID: "id" })).toThrow();
    expect(() => loadConfigFromEnv({ WHOOP_CLIENT_SECRET: "secret" })).toThrow();
  });

  test("defaults the redirect URI when unset", () => {
    const cfg = loadConfigFromEnv({ WHOOP_CLIENT_ID: "id", WHOOP_CLIENT_SECRET: "secret" });
    expect(cfg.redirectUri).toBe("http://localhost:8080/oauth/callback");
  });

  test("uses an explicit redirect URI when set", () => {
    const cfg = loadConfigFromEnv({
      WHOOP_CLIENT_ID: "id",
      WHOOP_CLIENT_SECRET: "secret",
      WHOOP_REDIRECT_URI: "https://example.com/callback",
    });
    expect(cfg.redirectUri).toBe("https://example.com/callback");
  });
});
