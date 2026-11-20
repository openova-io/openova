import assert from "node:assert/strict";
import { test } from "node:test";
import { challengeOf, currentSession, finishSignIn, signInURL, type Endpoints, type Store } from "./auth.js";
import { parseConfig } from "./config.js";
import { CFG, fakeFetch, json } from "./fakes.js";

const EP: Endpoints = { authorization_endpoint: "https://auth.acme.example/auth", token_endpoint: "https://auth.acme.example/token" };
const REDIRECT = "https://cortex.acme.example/";

function memory(): Store & { m: Map<string, string> } {
  const m = new Map<string, string>();
  return { m, getItem: (k) => m.get(k) ?? null, setItem: (k, v) => void m.set(k, v), removeItem: (k) => void m.delete(k) };
}

test("the PKCE challenge is the S256 of the verifier (RFC 7636, appendix B)", async () => {
  assert.equal(await challengeOf("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"), "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM");
});

test("sign-on asks for a code with PKCE and no secret, and trades it for the token", async () => {
  const store = memory();
  const url = new URL(await signInURL(CFG, EP, REDIRECT, store));
  const q = url.searchParams;
  assert.equal(`${url.origin}${url.pathname}`, EP.authorization_endpoint);
  assert.equal(q.get("response_type"), "code");
  assert.equal(q.get("client_id"), "cortex");
  assert.equal(q.get("code_challenge_method"), "S256");
  const pending = JSON.parse(store.m.get("cortex.signin")!) as { verifier: string; state: string };
  assert.equal(q.get("code_challenge"), await challengeOf(pending.verifier));
  const f = fakeFetch(() => json(200, { access_token: "at", expires_in: 300 }));
  const back = new URLSearchParams({ code: "c1", state: q.get("state")! });
  const s = await finishSignIn(CFG, EP, back, REDIRECT, store, f.fetch, 1000);
  assert.deepEqual(s, { token: "at", expiresAt: 301000 });
  const sent = new URLSearchParams(String(f.seen[0].body));
  assert.equal(sent.get("code_verifier"), pending.verifier);
  assert.equal(sent.get("grant_type"), "authorization_code");
  assert.equal(sent.has("client_secret"), false);
  assert.deepEqual(currentSession(store, 2000), s);
  assert.equal(currentSession(store, 301000), null, "an expired token is no session");
});

test("sign-on refuses an answer whose state this tab did not start", async () => {
  const store = memory();
  await signInURL(CFG, EP, REDIRECT, store);
  const f = fakeFetch(() => json(200, { access_token: "at", expires_in: 300 }));
  await assert.rejects(finishSignIn(CFG, EP, new URLSearchParams({ code: "c1", state: "forged" }), REDIRECT, store, f.fetch, 0), /does not belong to this tab/);
  assert.equal(f.seen.length, 0);
});

test("config.json names every setting; only the cortex address may be empty", () => {
  assert.deepEqual(parseConfig({ ...CFG, cortexURL: "", platformURL: "https://console.sovereign.example/" }), { ...CFG, cortexURL: "" });
  assert.throws(() => parseConfig({ ...CFG, namespace: "" }), /namespace is missing/);
  assert.throws(() => parseConfig({ ...CFG, issuerURL: "auth" }), /issuerURL is not an address/);
});
