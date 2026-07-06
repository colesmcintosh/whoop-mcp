#!/usr/bin/env bun
// Command whoop-auth runs the OAuth 2.0 + PKCE authorization-code flow
// against the Whoop API once, on your own machine, and persists the
// resulting token locally so whoop-mcp can use it for subsequent
// requests (refreshing automatically as needed).

import { createServer, type Server } from "node:http";
import { loadConfigFromEnv } from "../auth/config.ts";
import { buildAuthCodeUrl, exchangeCode } from "../auth/oauth-client.ts";
import { generateCodeChallenge, generateCodeVerifier, generateState } from "../auth/pkce.ts";
import { saveToken, tokenStoreLocation } from "../auth/token-store.ts";
import { openBrowser } from "./open-browser.ts";

const CALLBACK_TIMEOUT_MS = 5 * 60 * 1000;

async function main(): Promise<void> {
  const config = loadConfigFromEnv();

  let redirectUrl: URL;
  try {
    redirectUrl = new URL(config.redirectUri);
  } catch {
    throw new Error(`invalid WHOOP_REDIRECT_URI: ${config.redirectUri}`);
  }
  if (redirectUrl.hostname !== "localhost" && redirectUrl.hostname !== "127.0.0.1") {
    throw new Error(`redirect URI must point at localhost for this CLI; got ${config.redirectUri}`);
  }
  const port = redirectUrl.port ? Number(redirectUrl.port) : 80;
  const callbackPath = redirectUrl.pathname || "/";

  const state = generateState();
  const verifier = generateCodeVerifier();
  const codeChallenge = generateCodeChallenge(verifier);
  const authUrl = buildAuthCodeUrl(config, { state, codeChallenge });

  const code = await waitForCallback({ hostname: redirectUrl.hostname, port, callbackPath, state, authUrl });

  console.log("\nExchanging authorization code for a token...");
  const token = await exchangeCode(config, code, verifier);
  await saveToken(token);

  console.log(`\nToken saved to ${tokenStoreLocation()}`);
  if (token.expiry) {
    const secondsLeft = Math.round((new Date(token.expiry).getTime() - Date.now()) / 1000);
    console.log(`Access token expires at ${token.expiry} (in ${secondsLeft}s)`);
  }
  if (token.refresh_token) {
    console.log("Refresh token stored — whoop-mcp will renew access tokens automatically.");
  }
}

interface WaitForCallbackOpts {
  hostname: string;
  port: number;
  callbackPath: string;
  state: string;
  authUrl: string;
}

function waitForCallback(opts: WaitForCallbackOpts): Promise<string> {
  return new Promise<string>((resolve, reject) => {
    let settled = false;

    function settle(fn: () => void): void {
      if (settled) return;
      settled = true;
      clearTimeout(timeout);
      server.close();
      fn();
    }

    const timeout = setTimeout(() => {
      settle(() => reject(new Error("timed out waiting for browser callback")));
    }, CALLBACK_TIMEOUT_MS);

    const server: Server = createServer((req, res) => {
      const url = new URL(req.url ?? "/", `http://${opts.hostname}`);
      if (url.pathname !== opts.callbackPath) {
        res.writeHead(404);
        res.end();
        return;
      }

      const errorParam = url.searchParams.get("error");
      if (errorParam) {
        const description = url.searchParams.get("error_description") ?? "";
        res.writeHead(400, { "Content-Type": "text/plain" });
        res.end(`authorization error: ${errorParam} — ${description}`);
        settle(() => reject(new Error(`callback: authorization error: ${errorParam} — ${description}`)));
        return;
      }

      const gotState = url.searchParams.get("state");
      if (gotState !== opts.state) {
        res.writeHead(400, { "Content-Type": "text/plain" });
        res.end("state mismatch");
        settle(() => reject(new Error(`callback: state mismatch: got ${JSON.stringify(gotState)}`)));
        return;
      }

      const code = url.searchParams.get("code");
      if (!code) {
        res.writeHead(400, { "Content-Type": "text/plain" });
        res.end("missing code");
        settle(() => reject(new Error("callback: missing code in callback")));
        return;
      }

      res.writeHead(200, { "Content-Type": "text/html" });
      res.end("<html><body><h2>Whoop MCP authorized.</h2><p>You may close this tab.</p></body></html>");
      settle(() => resolve(code));
    });

    server.on("error", (err) => settle(() => reject(err)));
    server.listen(opts.port, opts.hostname, () => {
      console.log("Opening browser to authorize Whoop access...");
      console.log(`If your browser doesn't open, visit:\n  ${opts.authUrl}\n`);
      openBrowser(opts.authUrl);
    });
  });
}

main().catch((err: unknown) => {
  console.error(err instanceof Error ? err.message : err);
  process.exit(1);
});
