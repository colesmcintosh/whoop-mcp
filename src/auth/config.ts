// Whoop OAuth 2.0 configuration shared by the auth CLI and the MCP server.

export const AUTH_URL = "https://api.prod.whoop.com/oauth/oauth2/auth";
export const TOKEN_URL = "https://api.prod.whoop.com/oauth/oauth2/token";

// Read-only access to every endpoint the MCP tools surface, plus "offline"
// so Whoop issues a refresh token.
export const DEFAULT_SCOPES = [
  "read:recovery",
  "read:cycles",
  "read:sleep",
  "read:workout",
  "read:profile",
  "read:body_measurement",
  "offline",
];

export interface Config {
  clientId: string;
  clientSecret: string;
  redirectUri: string;
}

export function loadConfigFromEnv(env: NodeJS.ProcessEnv = process.env): Config {
  const clientId = env.WHOOP_CLIENT_ID;
  const clientSecret = env.WHOOP_CLIENT_SECRET;
  if (!clientId || !clientSecret) {
    throw new Error("WHOOP_CLIENT_ID and WHOOP_CLIENT_SECRET must be set");
  }
  return {
    clientId,
    clientSecret,
    redirectUri: env.WHOOP_REDIRECT_URI || "http://localhost:8080/oauth/callback",
  };
}
