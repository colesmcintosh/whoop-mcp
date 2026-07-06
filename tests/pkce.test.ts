import { describe, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { generateCodeChallenge, generateCodeVerifier, generateState } from "../src/auth/pkce.ts";

describe("pkce", () => {
  test("generateCodeVerifier produces distinct, url-safe strings", () => {
    const a = generateCodeVerifier();
    const b = generateCodeVerifier();
    expect(a).not.toBe(b);
    expect(a.length).toBeGreaterThanOrEqual(43);
    expect(/^[A-Za-z0-9_-]+$/.test(a)).toBe(true);
  });

  test("generateCodeChallenge computes the S256 hash of the verifier", () => {
    const verifier = "some-arbitrary-verifier-value";
    const expected = createHash("sha256").update(verifier).digest("base64url");
    expect(generateCodeChallenge(verifier)).toBe(expected);
  });

  test("generateCodeChallenge matches the RFC 7636 appendix B test vector", () => {
    const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk";
    expect(generateCodeChallenge(verifier)).toBe("E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM");
  });

  test("generateState produces distinct tokens", () => {
    expect(generateState()).not.toBe(generateState());
  });
});
