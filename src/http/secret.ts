import { createHash, timingSafeEqual } from "node:crypto";

/** Constant-time string compare via SHA-256 digests (equal length). */
export function timingSafeEqualStrings(a: string, b: string): boolean {
  const digest = (value: string) => createHash("sha256").update(value).digest();
  return timingSafeEqual(digest(a), digest(b));
}
