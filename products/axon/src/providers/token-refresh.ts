import { readFileSync, writeFileSync, existsSync } from "fs";
import { join } from "path";

const OAUTH_TOKEN_URL = "https://console.anthropic.com/v1/oauth/token";
const CLIENT_ID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e";
const REFRESH_MARGIN_MS = 5 * 60 * 1000; // refresh 5 min before expiry

interface OAuthCredentials {
  claudeAiOauth: {
    accessToken: string;
    refreshToken: string;
    expiresAt: number;
    scopes?: string[];
    subscriptionType?: string;
    rateLimitTier?: string;
  };
}

function credentialsPath(): string {
  const home = process.env.HOME ?? "/home/axon";
  return join(home, ".claude", ".credentials.json");
}

function readCredentials(): OAuthCredentials {
  return JSON.parse(readFileSync(credentialsPath(), "utf-8"));
}

function writeCredentials(creds: OAuthCredentials): void {
  writeFileSync(credentialsPath(), JSON.stringify(creds), { mode: 0o600 });
}

export async function refreshIfExpired(): Promise<boolean> {
  const path = credentialsPath();
  if (!existsSync(path)) {
    console.warn("[token-refresh] No credentials file found — skipping refresh");
    return false;
  }

  const creds = readCredentials();
  const oauth = creds.claudeAiOauth;

  if (!oauth.refreshToken) {
    console.warn("[token-refresh] No refreshToken available — cannot refresh");
    return false;
  }

  const now = Date.now();
  if (oauth.expiresAt > now + REFRESH_MARGIN_MS) {
    console.log(
      `[token-refresh] Token valid for ${Math.round((oauth.expiresAt - now) / 60000)} more minutes`,
    );
    return true;
  }

  console.log("[token-refresh] Token expired or expiring soon — refreshing...");

  const res = await fetch(OAUTH_TOKEN_URL, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      grant_type: "refresh_token",
      refresh_token: oauth.refreshToken,
      client_id: CLIENT_ID,
    }),
  });

  if (!res.ok) {
    const body = await res.text();
    console.error(
      `[token-refresh] Refresh failed: HTTP ${res.status} — ${body}`,
    );
    return false;
  }

  const data = (await res.json()) as {
    access_token: string;
    refresh_token?: string;
    expires_in: number;
  };

  oauth.accessToken = data.access_token;
  oauth.expiresAt = Date.now() + data.expires_in * 1000;
  if (data.refresh_token) {
    oauth.refreshToken = data.refresh_token;
  }

  writeCredentials(creds);
  console.log(
    `[token-refresh] Token refreshed — valid until ${new Date(oauth.expiresAt).toISOString()}`,
  );
  return true;
}

let refreshTimer: ReturnType<typeof setInterval> | null = null;

export function startPeriodicRefresh(intervalMs = 4 * 60 * 60 * 1000): void {
  if (refreshTimer) return;
  refreshTimer = setInterval(() => {
    refreshIfExpired().catch((err) =>
      console.error("[token-refresh] Periodic refresh error:", err),
    );
  }, intervalMs);
  // Also set a shorter check for the first hour (every 30 min)
  // to handle the case where token expires soon after startup
  setTimeout(() => {
    refreshIfExpired().catch((err) =>
      console.error("[token-refresh] Follow-up refresh error:", err),
    );
  }, 30 * 60 * 1000);
}

export function stopPeriodicRefresh(): void {
  if (refreshTimer) {
    clearInterval(refreshTimer);
    refreshTimer = null;
  }
}
