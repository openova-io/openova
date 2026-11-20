// Sign-on against the Organization's realm: the authorisation-code flow with PKCE of a public
// registration, no secret. The access token lives in the tab's session storage only.

import type { Config } from "./config.js";
import type { Fetch } from "./http.js";

export interface Endpoints {
  authorization_endpoint: string;
  token_endpoint: string;
}
export interface Session {
  token: string;
  expiresAt: number; // milliseconds since the epoch
}
export type Store = Pick<Storage, "getItem" | "setItem" | "removeItem">;

const SESSION = "cortex.session";
const PENDING = "cortex.signin";

function base64url(bytes: Uint8Array): string {
  let s = "";
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function randomString(): string {
  return base64url(crypto.getRandomValues(new Uint8Array(32)));
}

export async function challengeOf(verifier: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier));
  return base64url(new Uint8Array(digest));
}

export async function discover(cfg: Config, fetchFn: Fetch): Promise<Endpoints> {
  const res = await fetchFn(`${cfg.issuerURL}/.well-known/openid-configuration`);
  if (!res.ok) throw new Error(`sign-on discovery: HTTP ${res.status}`);
  const doc = (await res.json()) as Partial<Endpoints>;
  if (!doc.authorization_endpoint || !doc.token_endpoint) throw new Error("sign-on discovery: endpoints missing");
  return { authorization_endpoint: doc.authorization_endpoint, token_endpoint: doc.token_endpoint };
}

// signInURL remembers a new verifier and state and returns the address to send the browser to.
export async function signInURL(cfg: Config, ep: Endpoints, redirectURI: string, store: Store): Promise<string> {
  const verifier = randomString();
  const state = randomString();
  store.setItem(PENDING, JSON.stringify({ verifier, state }));
  const q = new URLSearchParams({
    response_type: "code",
    client_id: cfg.clientId,
    redirect_uri: redirectURI,
    scope: "openid",
    state,
    code_challenge: await challengeOf(verifier),
    code_challenge_method: "S256",
  });
  return `${ep.authorization_endpoint}?${q}`;
}

// finishSignIn trades the code of the redirect for a token. A state that is not the one this tab
// started is refused, and the remembered verifier is used once.
export async function finishSignIn(
  cfg: Config,
  ep: Endpoints,
  params: URLSearchParams,
  redirectURI: string,
  store: Store,
  fetchFn: Fetch,
  now: number,
): Promise<Session> {
  const pending = JSON.parse(store.getItem(PENDING) ?? "null") as { verifier: string; state: string } | null;
  store.removeItem(PENDING);
  const code = params.get("code");
  if (!pending || !code || params.get("state") !== pending.state) throw new Error("sign-on: the answer does not belong to this tab");
  const res = await fetchFn(ep.token_endpoint, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "authorization_code",
      code,
      redirect_uri: redirectURI,
      client_id: cfg.clientId,
      code_verifier: pending.verifier,
    }),
  });
  if (!res.ok) throw new Error(`sign-on: HTTP ${res.status}`);
  const body = (await res.json()) as { access_token?: string; expires_in?: number };
  if (!body.access_token || typeof body.expires_in !== "number") throw new Error("sign-on: no access token");
  const session = { token: body.access_token, expiresAt: now + body.expires_in * 1000 };
  store.setItem(SESSION, JSON.stringify(session));
  return session;
}

// currentSession is the stored session, or null when there is none or it has expired.
export function currentSession(store: Store, now: number): Session | null {
  const s = JSON.parse(store.getItem(SESSION) ?? "null") as Session | null;
  if (!s || typeof s.token !== "string" || !(s.expiresAt > now)) return null;
  return s;
}

export function signOut(store: Store): void {
  store.removeItem(SESSION);
}
