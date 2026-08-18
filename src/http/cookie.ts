// HMAC-signed cookies, used to carry the in-flight PKCE session between the
// /oauth/start redirect and the Whoop callback.
//
// Holding that state in server memory works for a single long-lived
// process, but not on a serverless platform, where the two requests are
// routinely served by different instances and the callback would find no
// pending session. Signing the state into a short-lived cookie makes the
// flow stateless without weakening it: the value is tamper-evident, and it
// only ever travels between the browser and this server.

import { createHmac } from "node:crypto";
import { timingSafeEqualStrings } from "./secret.ts";

export interface CookieOptions {
  maxAgeSeconds: number;
  secure: boolean;
}

/** `<base64url payload>.<hmac>`; the payload is readable but not forgeable. */
export function signCookieValue(payload: string, secret: string): string {
  const encoded = Buffer.from(payload, "utf8").toString("base64url");
  return `${encoded}.${sign(encoded, secret)}`;
}

/** Returns the payload, or null if the value is malformed or unsigned by us. */
export function verifyCookieValue(value: string, secret: string): string | null {
  const dot = value.lastIndexOf(".");
  if (dot <= 0) return null;
  const encoded = value.slice(0, dot);
  if (!timingSafeEqualStrings(value.slice(dot + 1), sign(encoded, secret))) return null;
  return Buffer.from(encoded, "base64url").toString("utf8");
}

export function serializeCookie(name: string, value: string, opts: CookieOptions): string {
  const parts = [
    `${name}=${value}`,
    "Path=/",
    "HttpOnly",
    "SameSite=Lax",
    `Max-Age=${opts.maxAgeSeconds}`,
  ];
  // Whoop redirects back over whatever scheme the callback URL uses; marking
  // the cookie Secure on a plain-http localhost deploy would drop it.
  if (opts.secure) parts.push("Secure");
  return parts.join("; ");
}

export function clearCookie(name: string, opts: Pick<CookieOptions, "secure">): string {
  return serializeCookie(name, "", { maxAgeSeconds: 0, secure: opts.secure });
}

export function readCookie(header: string | undefined, name: string): string | undefined {
  if (!header) return undefined;
  for (const pair of header.split(";")) {
    const eq = pair.indexOf("=");
    if (eq < 0) continue;
    if (pair.slice(0, eq).trim() === name) return pair.slice(eq + 1).trim();
  }
  return undefined;
}

function sign(encoded: string, secret: string): string {
  return createHmac("sha256", secret).update(encoded).digest("base64url");
}
