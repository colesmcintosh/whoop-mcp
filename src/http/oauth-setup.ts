// Browser-based Whoop OAuth+PKCE for HTTP and hosted deploys. Starting the
// flow requires MCP_AUTH_TOKEN (form field); the callback is bound to the
// PKCE session carried in a signed, short-lived cookie, so a stolen
// redirect cannot be exchanged. Keeping that session in the cookie rather
// than in server memory is what lets the flow survive a serverless deploy,
// where /oauth/start and the callback need not hit the same instance.

import type { IncomingMessage, ServerResponse } from "node:http";
import type { Config } from "../auth/config.ts";
import { createAuthRequest, exchangeCode } from "../auth/oauth-client.ts";
import { saveToken } from "../auth/token-store.ts";
import { clearCookie, readCookie, serializeCookie, signCookieValue, verifyCookieValue } from "./cookie.ts";
import { timingSafeEqualStrings } from "./secret.ts";

const PENDING_TTL_MS = 5 * 60 * 1000;
const FORM_BODY_LIMIT = 8 * 1024;
const PENDING_COOKIE = "whoop_mcp_oauth";

export const OAUTH_START_PATH = "/oauth/start";
export const SETUP_PATH = "/setup";

interface PendingAuth {
  state: string;
  verifier: string;
  expiresAt: number;
}

export class OAuthSetup {
  // Declared-and-assigned rather than constructor parameter properties, so
  // this file parses under type-stripping runtimes. See WhoopClient.
  private readonly config: Config;
  private readonly authToken: string;
  private readonly env: NodeJS.ProcessEnv;
  private readonly onAuthorized?: () => void;

  constructor(
    config: Config,
    authToken: string,
    env: NodeJS.ProcessEnv = process.env,
    onAuthorized?: () => void,
  ) {
    this.config = config;
    this.authToken = authToken;
    this.env = env;
    this.onAuthorized = onAuthorized;
  }

  callbackPath(): string {
    return new URL(this.config.redirectUri).pathname || "/";
  }

  async handleStart(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const params = await readFormBody(req);
    const token = params.get("token") ?? "";
    if (!timingSafeEqualStrings(token, this.authToken)) {
      html(res, 401, setupPage("That token didn't match MCP_AUTH_TOKEN. Try again."));
      return;
    }

    const { state, verifier, authUrl } = createAuthRequest(this.config);
    const pending: PendingAuth = { state, verifier, expiresAt: Date.now() + PENDING_TTL_MS };
    res.writeHead(302, {
      Location: authUrl,
      "Set-Cookie": serializeCookie(
        PENDING_COOKIE,
        signCookieValue(JSON.stringify(pending), this.authToken),
        { maxAgeSeconds: PENDING_TTL_MS / 1000, secure: this.secureCookies() },
      ),
    });
    res.end();
  }

  async handleCallback(req: IncomingMessage, url: URL, res: ServerResponse): Promise<void> {
    const pending = this.readPending(req);
    // The PKCE session is single-use whichever way this request ends.
    res.setHeader("Set-Cookie", clearCookie(PENDING_COOKIE, { secure: this.secureCookies() }));

    const errorParam = url.searchParams.get("error");
    if (errorParam) {
      const description = url.searchParams.get("error_description") ?? "";
      html(res, 400, resultPage("Authorization failed", `${errorParam} — ${description}`));
      return;
    }

    if (!pending || pending.expiresAt < Date.now()) {
      html(res, 400, resultPage("Authorization expired", "Start again from the setup page."));
      return;
    }
    if (url.searchParams.get("state") !== pending.state) {
      html(res, 400, resultPage("Authorization failed", "state mismatch"));
      return;
    }

    const code = url.searchParams.get("code");
    if (!code) {
      html(res, 400, resultPage("Authorization failed", "missing code"));
      return;
    }

    try {
      const token = await exchangeCode(this.config, code, pending.verifier);
      await saveToken(token, this.env);
      this.onAuthorized?.();
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      html(res, 502, resultPage("Token exchange failed", message));
      return;
    }

    html(res, 200, resultPage("Whoop connected", "You can close this tab. Point your MCP client at /mcp."));
  }

  private readPending(req: IncomingMessage): PendingAuth | null {
    const raw = readCookie(req.headers.cookie, PENDING_COOKIE);
    if (!raw) return null;
    const payload = verifyCookieValue(raw, this.authToken);
    if (!payload) return null;
    try {
      return JSON.parse(payload) as PendingAuth;
    } catch {
      return null;
    }
  }

  private secureCookies(): boolean {
    return this.config.redirectUri.startsWith("https:");
  }
}

export function statusPage(ready: boolean, canSetup: boolean): string {
  if (ready) return readyPage();
  if (canSetup) return setupPage();
  return layout(
    "whoop-mcp",
    "<p>No Whoop token stored. Run <code>whoop-auth</code> or set <code>WHOOP_REFRESH_TOKEN</code>.</p>",
  );
}

export function setupPage(error?: string): string {
  const err = error ? `<p class="err">${escapeHtml(error)}</p>` : "";
  return layout(
    "Connect Whoop",
    `${err}
    <p>Paste the same <code>MCP_AUTH_TOKEN</code> you set in the container env, then authorize with Whoop.</p>
    <form method="post" action="${OAUTH_START_PATH}">
      <label>MCP_AUTH_TOKEN
        <input type="password" name="token" required autocomplete="off" autofocus>
      </label>
      <button type="submit">Connect Whoop</button>
    </form>`,
  );
}

function readyPage(): string {
  return layout(
    "whoop-mcp",
    `<p>Connected. Point your MCP client at <code>/mcp</code> with <code>Authorization: Bearer &lt;MCP_AUTH_TOKEN&gt;</code>.</p>
    <p><a href="${SETUP_PATH}">Reconnect Whoop</a></p>`,
  );
}

function resultPage(title: string, detail: string): string {
  return layout(title, `<p>${escapeHtml(detail)}</p><p><a href="/">Back</a></p>`);
}

function layout(title: string, body: string): string {
  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>${escapeHtml(title)}</title>
  <style>
    body { font-family: system-ui, sans-serif; max-width: 36rem; margin: 3rem auto; padding: 0 1rem; line-height: 1.45; }
    code { font-size: .95em; }
    label { display: flex; flex-direction: column; gap: .35rem; margin: 1rem 0; }
    input { font: inherit; padding: .4rem .5rem; }
    button { font: inherit; padding: .4rem .8rem; cursor: pointer; }
    .err { color: #a40000; }
  </style>
</head>
<body>
  <h1>${escapeHtml(title)}</h1>
  ${body}
</body>
</html>`;
}

function html(res: ServerResponse, status: number, body: string): void {
  res.writeHead(status, { "Content-Type": "text/html; charset=utf-8" });
  res.end(body);
}

function escapeHtml(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}

async function readFormBody(req: IncomingMessage): Promise<URLSearchParams> {
  const raw = await new Promise<string>((resolve, reject) => {
    const acc: string[] = [];
    let size = 0;
    let settled = false;
    const fail = (err: Error) => {
      if (settled) return;
      settled = true;
      reject(err);
    };
    req.setEncoding("utf8");
    req.on("data", (chunk: string) => {
      size += chunk.length;
      if (size > FORM_BODY_LIMIT) {
        req.destroy();
        fail(new Error("request body too large"));
        return;
      }
      acc.push(chunk);
    });
    req.on("end", () => {
      if (settled) return;
      settled = true;
      resolve(acc.join(""));
    });
    req.on("error", fail);
  });
  return new URLSearchParams(raw);
}

export { html as writeHtml };
