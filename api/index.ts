// Vercel entry point. Vercel's Node.js runtime captures a node:http server
// that calls listen(), so this serves the same app the container does —
// with two adjustments the platform forces:
//
//   - stateless /mcp, because consecutive requests from one MCP client are
//     not guaranteed to reach the same instance;
//   - a Redis-backed token store (see src/auth/redis-store.ts), because the
//     function filesystem does not outlive the invocation and Whoop rotates
//     the refresh token on every use.

import { createWhoopHttpApp } from "../src/http/app.ts";

const app = createWhoopHttpApp({ stateless: true });

// vercel.json rewrites every path to this function, preserving the request
// path. A request aimed straight at the function's own route arrives with
// an /api prefix instead; strip it so both spellings hit the same routes.
app.prependListener("request", (req) => {
  const raw = req.url ?? "/";
  const q = raw.indexOf("?");
  const path = q < 0 ? raw : raw.slice(0, q);
  const query = q < 0 ? "" : raw.slice(q);
  if (path === "/api") {
    req.url = `/${query}`;
  } else if (path.startsWith("/api/")) {
    req.url = path.slice("/api".length) + query;
  }
});

app.listen(Number(process.env.PORT ?? 3000));
