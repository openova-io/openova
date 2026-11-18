// The one owner of the credential: one refresh timer, check-and-set write-back
// to OpenBao before the new token is used, the states, and the 503 answer.
import { test } from "node:test";
import assert from "node:assert/strict";
import { loadConfig } from "../src/config.js";
import { Credential, DOCUMENT_KEY, guarded, PERSIST_RETRY_MS, RETRY_LAST_MS, RETRY_MS, TOKEN_URL } from "../src/credential.js";
import { servers } from "../src/index.js";
import { Turns } from "../src/turns.js";
import { childEnv } from "../src/upstream.js";
import { FakeUpstream } from "./fake-upstream.js";

const SECRETS = ["acc-old-7f3a", "ref-old-91c2", "acc-new-55d0", "ref-new-0b8e", "sa-jwt-c4e1", "bao-tok-3a9f"];
const [ACCESS, REFRESH, NEW_ACCESS, NEW_REFRESH, JWT, BAO] = SECRETS;
const T0 = Date.parse("2026-12-01T09:00:00Z");
const HOUR = 3600000;
const CFG = loadConfig({}).credential;
const logs: string[] = [];
const doc = (expiresAt: number) => JSON.stringify({ [DOCUMENT_KEY]: { accessToken: ACCESS, refreshToken: REFRESH, expiresAt, scopes: ["user:inference"] } });

interface Call { url: string; method: string; headers: Record<string, string>; body: string }
type Answer = { status: number; body: unknown } | Promise<{ status: number; body: unknown }>;

/** A clock, the timers on it, and a fetch that answers the provider and OpenBao as scripted. */
function rig(file: string | undefined, script: { token?: Answer[]; write?: Answer[] } = {}) {
  const clock = { t: T0 };
  const timers: { fn: () => void; at: number }[] = [];
  const calls: Call[] = [];
  const answer = (a: Answer | undefined) => Promise.resolve(a ?? { status: 500, body: {} }).then(({ status, body }) => new Response(JSON.stringify(body), { status }));
  const fetch = (async (url: string, init: RequestInit = {}) => {
    calls.push({ url, method: init.method ?? "GET", headers: (init.headers ?? {}) as Record<string, string>, body: String(init.body ?? "") });
    if (url === TOKEN_URL) return answer(script.token?.shift());
    if (url.endsWith("/v1/auth/kubernetes/login")) return answer({ status: 200, body: { auth: { client_token: BAO } } });
    if (url.includes("/metadata/")) return answer({ status: 200, body: { data: { current_version: 3 } } });
    return answer(script.write?.shift());
  }) as typeof globalThis.fetch;
  const credential = new Credential({
    ...CFG,
    fetch,
    now: () => clock.t,
    readFile: (path) => (path === CFG.file ? file : JWT),
    timers: { set: (fn, ms) => timers.push({ fn, at: clock.t + ms }) && timers[timers.length - 1], clear: (h) => void (timers.includes(h as never) && timers.splice(timers.indexOf(h as never), 1)) },
    log: (line) => logs.push(line),
  });
  /** Moves the clock to the first timer and runs it. */
  const fire = async () => {
    const next = timers.shift();
    assert.ok(next, "a timer is armed");
    clock.t = Math.max(clock.t, next.at);
    next.fn();
    await settle();
  };
  const of = (pred: (c: Call) => boolean) => calls.filter(pred);
  return { clock, timers, calls, credential, fire, tokenCalls: () => of((c) => c.url === TOKEN_URL), writes: () => of((c) => c.url.endsWith(CFG.kvPath)) };
}

async function settle(): Promise<void> {
  for (let i = 0; i < 20; i++) await new Promise((resolve) => setImmediate(resolve));
}

function held<T>(value: T) {
  let release = () => {};
  const promise = new Promise<T>((resolve) => (release = () => resolve(value)));
  return { promise, release };
}

const tokenOk = { status: 200, body: { access_token: NEW_ACCESS, refresh_token: NEW_REFRESH, expires_in: 8 * 3600 } };
const writeOk = (version: number) => ({ status: 200, body: { data: { version } } });
const invalid = { status: 503, code: "simulation_credential_invalid" };

test("refresh happens once at the margin, with the same token endpoint, and the next one is armed for the new expiry", async () => {
  const r = rig(doc(T0 + HOUR), { token: [tokenOk], write: [writeOk(4)] });
  r.credential.load();
  await r.credential.start();
  assert.deepEqual(r.timers.map((t) => t.at - T0), [HOUR - CFG.refreshMarginMs]);
  await r.fire();
  assert.equal(r.tokenCalls().length, 1);
  const body = JSON.parse(r.tokenCalls()[0].body);
  assert.deepEqual([body.grant_type, body.refresh_token, typeof body.client_id], ["refresh_token", REFRESH, "string"]);
  assert.deepEqual(r.timers.map((t) => t.at), [r.clock.t + 8 * HOUR - CFG.refreshMarginMs]);
  assert.equal(r.credential.accessToken(), NEW_ACCESS);
});

test("a second timer never runs while one refresh is in flight", async () => {
  const token = held(tokenOk);
  const r = rig(doc(T0 + HOUR), { token: [token.promise], write: [writeOk(4)] });
  r.credential.load();
  await r.credential.start();
  await r.fire();
  assert.equal(r.timers.length, 0);
  await r.credential.start();
  assert.deepEqual([r.timers.length, r.calls.filter((c) => c.url.includes("/metadata/")).length], [0, 1]);
  token.release();
  await settle();
  assert.equal(r.tokenCalls().length, 1);
  assert.equal(r.timers.length, 1);
});

test("write-back with check-and-set happens before accessToken() returns the new value", async () => {
  const write = held(writeOk(4));
  const r = rig(doc(T0 + HOUR), { token: [tokenOk, tokenOk], write: [write.promise, writeOk(5)] });
  r.credential.load();
  await r.credential.start();
  await r.fire();
  assert.equal(r.writes().length, 1);
  assert.equal(r.credential.accessToken(), ACCESS);
  write.release();
  await settle();
  assert.equal(r.credential.accessToken(), NEW_ACCESS);
  const w = r.writes()[0];
  assert.equal(w.url, `${CFG.openbaoAddr}/v1/${CFG.kvPath}`);
  assert.equal(w.headers["x-vault-token"], BAO);
  const sent = JSON.parse(w.body);
  assert.equal(sent.options.cas, 3);
  assert.deepEqual(JSON.parse(sent.data.document)[DOCUMENT_KEY], { accessToken: NEW_ACCESS, refreshToken: NEW_REFRESH, expiresAt: r.clock.t + 8 * HOUR, scopes: ["user:inference"] });
  const login = JSON.parse(r.calls.find((c) => c.url.endsWith("/login"))!.body);
  assert.deepEqual(login, { role: CFG.role, jwt: JWT });
  await r.fire();
  assert.equal(JSON.parse(r.writes()[1].body).options.cas, 4);
  assert.equal(r.calls.filter((c) => c.url.includes("/metadata/")).length, 1);
  assert.equal(r.credential.state().state, "valid");
});

test("a check-and-set failure sets conflict and stops refreshing", async () => {
  const cas = { status: 400, body: { errors: ["check-and-set parameter did not match the current version"] } };
  const r = rig(doc(T0 + HOUR), { token: [tokenOk], write: [cas] });
  r.credential.load();
  await r.credential.start();
  await r.fire();
  assert.equal(r.credential.state().state, "conflict");
  assert.throws(() => r.credential.accessToken(), invalid);
  assert.equal(r.timers.length, 0);
});

test("a provider 400 sets rejected, writes nothing and stops refreshing", async () => {
  const r = rig(doc(T0 + HOUR), { token: [{ status: 400, body: { error: "invalid_grant" } }] });
  r.credential.load();
  await r.credential.start();
  await r.fire();
  assert.equal(r.credential.state().state, "rejected");
  assert.throws(() => r.credential.accessToken(), invalid);
  assert.deepEqual([r.writes().length, r.timers.length], [0, 0]);
});

test("a failed refresh is retried after 60, 120, 240 s and then every 600 s; past expiry the state is expired", async () => {
  const r = rig(doc(T0 + 5 * 60000), { token: [{ status: 502, body: {} }, { status: 502, body: {} }, { status: 502, body: {} }, { status: 502, body: {} }, tokenOk], write: [writeOk(4)] });
  r.credential.load();
  await r.credential.start();
  await r.fire();
  const delays: number[] = [];
  const states: string[] = [];
  for (let i = 0; i < 4; i++) {
    delays.push(r.timers[0].at - r.clock.t);
    states.push(r.credential.state().state);
    if (i < 3) await r.fire();
  }
  assert.deepEqual(delays, [...RETRY_MS, RETRY_LAST_MS]);
  assert.deepEqual(states, ["valid", "valid", "valid", "expired"]);
  assert.throws(() => r.credential.accessToken(), invalid);
  await r.fire();
  assert.equal(r.tokenCalls().length, 5);
  assert.equal(r.credential.accessToken(), NEW_ACCESS);
});

test("a failed write-back keeps the new token in use and is retried every 30 s without a new refresh", async () => {
  const r = rig(doc(T0 + HOUR), { token: [tokenOk], write: [{ status: 500, body: {} }, writeOk(4)] });
  r.credential.load();
  await r.credential.start();
  await r.fire();
  assert.equal(r.credential.accessToken(), NEW_ACCESS);
  assert.equal(r.credential.state().lastPersistError, "write failed: HTTP 500");
  assert.deepEqual(r.timers.map((t) => t.at - r.clock.t), [PERSIST_RETRY_MS]);
  await r.fire();
  assert.deepEqual([r.tokenCalls().length, JSON.parse(r.writes()[1].body).options.cas], [1, 3]);
  assert.deepEqual([r.credential.state().lastPersistError, r.credential.state().lastPersistAt], ["", new Date(r.clock.t).toISOString()]);
});

test("an absent file sets absent and every step answers 503 simulation_credential_invalid", async () => {
  for (const file of [undefined, "", '{"other":1}', `{"${DOCUMENT_KEY}":{"accessToken":"${ACCESS}"`]) {
    const r = rig(file);
    r.credential.load();
    await r.credential.start();
    assert.deepEqual(r.credential.state(), { state: "absent" });
    assert.deepEqual([r.calls.length, r.timers.length], [0, 0]);
    const upstream = new FakeUpstream([]);
    const turns = new Turns({ upstream, modelMap: { "sim-default": "claude-sonnet-4-6" }, parkTtlMs: 600000, accessToken: () => r.credential.accessToken() });
    const { model, admin } = await servers(loadConfig({}), guarded(turns, r.credential), false, r.credential);
    for (const stream of [false, true]) {
      const res = await model.inject({ method: "POST", url: "/v1/chat/completions", payload: { model: "sim-default", stream, messages: [{ role: "user", content: "hi" }] } });
      assert.deepEqual([res.statusCode, res.json().error.code], [503, "simulation_credential_invalid"]);
    }
    assert.deepEqual((await admin.inject({ url: "/stats" })).json().credential, { state: "absent" });
    assert.equal(upstream.runs.length, 0);
  }
});

test("each run gets the access token in use when it starts, and a child gets that token and an empty HOME only", async () => {
  const r = rig(doc(T0 + HOUR), { token: [tokenOk], write: [writeOk(4)] });
  r.credential.load();
  await r.credential.start();
  const upstream = new FakeUpstream([{ steps: [{ text: "a" }] }, { steps: [{ text: "b" }] }]);
  const turns = guarded(new Turns({ upstream, modelMap: { "sim-default": "x" }, parkTtlMs: 600000, accessToken: () => r.credential.accessToken() }), r.credential);
  const req = { model: "sim-default", messages: [{ role: "user" as const, content: "hi" }] };
  await turns.step(req, "");
  await r.fire();
  await turns.step(req, "");
  assert.deepEqual(upstream.runs.map((run) => run.options.accessToken), [ACCESS, NEW_ACCESS]);
  const env = childEnv({ HOME: "/home/axon", CLAUDECODE: "1", PATH: "/bin" }, NEW_ACCESS, "/tmp/empty");
  assert.deepEqual(env, { HOME: "/tmp/empty", PATH: "/bin", CLAUDE_CODE_OAUTH_TOKEN: NEW_ACCESS });
  const s = await servers(loadConfig({}), turns, false, r.credential);
  const at = new Date(r.clock.t).toISOString();
  assert.deepEqual((await s.admin.inject({ url: "/stats" })).json().credential, {
    state: "valid", expiresAt: new Date(r.clock.t + 8 * HOUR).toISOString(), lastRefreshAt: at, lastPersistAt: at, lastPersistError: "",
  });
});

test("no log line of these tests contains a token", () => {
  assert.ok(logs.length > 10);
  for (const line of logs) for (const secret of SECRETS) assert.ok(!line.includes(secret), `a log line holds ${secret.slice(0, 7)}`);
});
