import { describe, expect, test } from "bun:test";
import {
  clearCookie,
  readCookie,
  serializeCookie,
  signCookieValue,
  verifyCookieValue,
} from "../src/http/cookie.ts";

describe("signed cookies", () => {
  test("round-trips a payload", () => {
    const signed = signCookieValue('{"state":"abc"}', "secret");
    expect(verifyCookieValue(signed, "secret")).toBe('{"state":"abc"}');
  });

  test("rejects a payload edited in the browser", () => {
    const signed = signCookieValue('{"state":"abc"}', "secret");
    const [, signature] = signed.split(".");
    const forged = `${Buffer.from('{"state":"evil"}').toString("base64url")}.${signature}`;
    expect(verifyCookieValue(forged, "secret")).toBeNull();
  });

  test("rejects a value signed with a different secret", () => {
    const signed = signCookieValue("payload", "old-secret");
    expect(verifyCookieValue(signed, "new-secret")).toBeNull();
  });

  test("rejects a value with no signature at all", () => {
    expect(verifyCookieValue("payload", "secret")).toBeNull();
    expect(verifyCookieValue("", "secret")).toBeNull();
  });
});

describe("cookie headers", () => {
  test("omits Secure so the cookie survives a plain-http localhost callback", () => {
    const insecure = serializeCookie("n", "v", { maxAgeSeconds: 300, secure: false });
    expect(insecure).not.toContain("Secure");
    expect(serializeCookie("n", "v", { maxAgeSeconds: 300, secure: true })).toContain("Secure");
  });

  test("clearCookie expires the value immediately", () => {
    expect(clearCookie("n", { secure: true })).toContain("Max-Age=0");
  });

  test("reads one cookie out of a multi-cookie header", () => {
    expect(readCookie("a=1; whoop_mcp_oauth=xyz; b=2", "whoop_mcp_oauth")).toBe("xyz");
    expect(readCookie("a=1", "whoop_mcp_oauth")).toBeUndefined();
    expect(readCookie(undefined, "whoop_mcp_oauth")).toBeUndefined();
  });
});
