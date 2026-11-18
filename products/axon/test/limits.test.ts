// Limits, queue, step time-out, the admin listener and SIGTERM.
import { test } from "node:test";
import assert from "node:assert/strict";
import type { AddressInfo } from "node:net";
import { loadConfig } from "../src/config.js";
import { servers, stop, SHUTDOWN_GRACE_MS } from "../src/index.js";
import { Disconnected, Turns, type TurnsOptions } from "../src/turns.js";
import type { ChatCompletionRequest, ChatMessage, Tool } from "../src/types/openai.js";
import type { Upstream } from "../src/upstream.js";
import { FakeUpstream, type FakeScript } from "./fake-upstream.js";

const MAP = { "sim-default": "claude-sonnet-4-6" };
const TOOLS: Tool[] = [{ type: "function", function: { name: "lookup" } }];
const ASK: ChatMessage = { role: "user", content: "hi" };
const IDLE = { active: 0, parked: 0, queued: 0 };
const BUSY = { status: 429, code: "rate_limit_exceeded", retryAfter: 5 };
const STOPPED = { status: 502, code: "backend_error" };

function setup(scripts: FakeScript[], limits: Partial<TurnsOptions> = {}) {
  const upstream = new FakeUpstream(scripts);
  return { upstream, turns: new Turns({ upstream, modelMap: MAP, parkTtlMs: 600000, ...limits }) };
}

const req = (messages: ChatMessage[] = [ASK], tools?: Tool[]): ChatCompletionRequest => ({ model: "sim-default", messages, tools });
const parks = (id: string): FakeScript => ({ steps: [{ tools: [{ id, name: "lookup", input: {} }] }, { text: `resumed ${id}` }] });
const answer = (id: string): ChatMessage[] => [
  ASK,
  { role: "assistant", content: null, tool_calls: [{ id, type: "function", function: { name: "lookup", arguments: "{}" } }] },
  { role: "tool", tool_call_id: id, content: "result" },
];

/** A hold point for a fake step: the run waits until release() is called. */
function held() {
  let release = () => {};
  const hold = new Promise<void>((resolve) => (release = resolve));
  return { hold, release };
}

async function settle(): Promise<void> {
  for (let i = 0; i < 20; i++) await new Promise((resolve) => setImmediate(resolve));
}

test("the fifth concurrent step waits until an active slot is free", async () => {
  const gates = [held(), held(), held(), held(), held()];
  const { upstream, turns } = setup(gates.map((g, i) => ({ steps: [{ text: `r${i}`, hold: g.hold }] })));
  const steps = gates.map(() => turns.step(req(), ""));
  await settle();
  assert.equal(upstream.runs.length, 4);
  assert.deepEqual(turns.stats(), { active: 4, parked: 0, queued: 1 });
  gates[0].release();
  assert.equal((await steps[0]).text, "r0");
  await settle();
  assert.equal(upstream.runs.length, 5);
  assert.deepEqual(turns.stats(), { active: 4, parked: 0, queued: 0 });
  for (const g of gates) g.release();
  assert.deepEqual((await Promise.all(steps)).map((r) => r.text), ["r0", "r1", "r2", "r3", "r4"]);
  assert.deepEqual(turns.stats(), IDLE);
});

test("the queue's seventeenth entry gets 429 with Retry-After 5 at once", async () => {
  const gate = held();
  const { upstream, turns } = setup(Array.from({ length: 20 }, () => ({ steps: [{ text: "r", hold: gate.hold }] })));
  const steps = Array.from({ length: 20 }, () => turns.step(req(), ""));
  await settle();
  assert.deepEqual(turns.stats(), { active: 4, parked: 0, queued: 16 });
  await assert.rejects(Promise.race([turns.step(req(), ""), settle().then(() => "still waiting")]), BUSY);
  assert.equal(turns.counters().rejected429, 1);
  gate.release();
  assert.equal((await Promise.all(steps)).length, 20);
  assert.equal(upstream.runs.length, 20);
});

test("a wait in the queue longer than the limit gets 429 with Retry-After 5", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const gate = held();
  const { upstream, turns } = setup([{ steps: [{ text: "first", hold: gate.hold }] }], { maxActive: 1, queueWaitMs: 1000 });
  const first = turns.step(req(), "");
  const second = turns.step(req(), "");
  await settle();
  t.mock.timers.tick(999);
  await settle();
  assert.deepEqual(turns.stats(), { active: 1, parked: 0, queued: 1 });
  t.mock.timers.tick(1);
  await assert.rejects(second, BUSY);
  assert.deepEqual(turns.stats(), { active: 1, parked: 0, queued: 0 });
  gate.release();
  assert.equal((await first).text, "first");
  assert.equal(upstream.runs.length, 1);
});

test("a step longer than the time-out gets 504 and its run is closed", async () => {
  const { upstream, turns } = setup([{ steps: [{ text: "slow", hold: new Promise<void>(() => {}) }] }], { stepTimeoutMs: 30 });
  const alive = setTimeout(() => {}, 5000); // the deadline does not hold the process open; in Axon the listeners do
  await assert.rejects(turns.step(req(), ""), { status: 504, code: "backend_timeout" });
  clearTimeout(alive);
  assert.equal(upstream.runs[0].closed, true);
  assert.deepEqual(turns.stats(), IDLE);
  assert.equal(turns.counters().upstreamErrors, 1);
});

test("the ninth parked chain closes the oldest", async () => {
  const ids = ["a", "b", "c", "d", "e", "f", "g", "h", "i"];
  const { upstream, turns } = setup([...ids.map(parks), { steps: [{ text: "replayed" }] }]);
  for (let i = 0; i < 8; i++) await turns.step(req([ASK], TOOLS), "");
  assert.deepEqual(turns.stats(), { active: 0, parked: 8, queued: 0 });
  await turns.step(req([ASK], TOOLS), "");
  assert.deepEqual(upstream.runs.map((r) => r.closed), [true, false, false, false, false, false, false, false, false]);
  assert.deepEqual(turns.stats(), { active: 0, parked: 8, queued: 0 });
  // The closed chain's results start a new run with the transcript; the next oldest still resumes.
  assert.equal((await turns.step(req(answer("a"), TOOLS), "")).text, "replayed");
  assert.equal((await turns.step(req(answer("b"), TOOLS), "")).text, "resumed b");
  assert.deepEqual(turns.counters(), { requests: 11, resumed: 1, replayed: 1, rejected429: 0, upstreamErrors: 0 });
});

test("a resumed chain does not wait in the queue, and a caller that leaves the queue is taken out", async () => {
  const gate = held();
  const { upstream, turns } = setup([parks("a"), { steps: [{ text: "busy", hold: gate.hold }] }], { maxActive: 1 });
  await turns.step(req([ASK], TOOLS), "");
  const busy = turns.step(req(), "");
  const caller = new AbortController();
  const leaving = turns.step(req(), "", undefined, caller.signal);
  await settle();
  assert.deepEqual(turns.stats(), { active: 1, parked: 1, queued: 1 });
  assert.equal((await turns.step(req(answer("a"), TOOLS), "")).text, "resumed a");
  caller.abort();
  await assert.rejects(leaving, Disconnected);
  assert.deepEqual(turns.stats(), { active: 1, parked: 0, queued: 0 });
  gate.release();
  assert.equal((await busy).text, "busy");
  assert.equal(upstream.runs.length, 2);
});

test("shutdown answers the queued and the active request, closes every chain and refuses new requests", async () => {
  const gate = held();
  const { upstream, turns } = setup([parks("p"), { steps: [{ text: "slow", hold: gate.hold }] }], { maxActive: 1 });
  await turns.step(req([ASK], TOOLS), "");
  const answered = [turns.step(req(), ""), turns.step(req(), "")].map((p) => assert.rejects(p, STOPPED));
  await settle();
  assert.deepEqual(turns.stats(), { active: 1, parked: 1, queued: 1 });
  await turns.shutdown();
  await Promise.all(answered);
  await assert.rejects(turns.step(req(), ""), STOPPED);
  assert.ok(upstream.runs.every((r) => r.closed && r.interrupted));
  assert.deepEqual(turns.stats(), IDLE);
  assert.equal(turns.counters().upstreamErrors, 0);
});

test("/stats, /health and /healthz are on the admin port only, the model routes on the model port only", async () => {
  const { model, admin } = await servers(loadConfig({}), setup([]).turns);
  for (const url of ["/stats", "/health", "/healthz"]) assert.equal((await model.inject({ url })).statusCode, 404, url);
  assert.equal((await admin.inject({ url: "/v1/models" })).statusCode, 404);
  assert.equal((await admin.inject({ method: "POST", url: "/v1/chat/completions", payload: req() })).statusCode, 404);
  assert.equal((await model.inject({ url: "/v1/models" })).statusCode, 200);
  assert.equal((await admin.inject({ url: "/healthz" })).statusCode, 200);
  assert.deepEqual((await admin.inject({ url: "/stats" })).json(), {
    credential: { state: "absent" },
    turns: { active: 0, parked: 0, queued: 0, maxActive: 4, maxParked: 8 },
    counters: { requests: 0, resumed: 0, replayed: 0, rejected429: 0, upstreamErrors: 0 },
  });
});

test("/health answers once both listeners are up; on SIGTERM Axon stops accepting, ends the open request and exits 0", async () => {
  const gate = held();
  const { upstream, turns } = setup([parks("s"), { steps: [{ text: "slow", hold: gate.hold }] }]);
  const s = await servers({ ...loadConfig({}), port: 0, adminPort: 0 }, turns);
  assert.equal((await s.admin.inject({ url: "/health" })).statusCode, 503);
  await s.listen("127.0.0.1");
  assert.equal((await s.admin.inject({ url: "/health" })).statusCode, 200);
  const url = `http://127.0.0.1:${(s.model.server.address() as AddressInfo).port}`;
  await turns.step(req([ASK], TOOLS), "");
  const headers = { "content-type": "application/json", connection: "close" };
  const open = fetch(`${url}/v1/chat/completions`, { method: "POST", headers, body: JSON.stringify(req()) });
  while (upstream.runs.length < 2) await settle();
  const codes: number[] = [];
  await stop(s, turns, (code) => codes.push(code));
  assert.deepEqual(codes, [0]);
  const res = await open;
  assert.equal(res.status, 502);
  assert.equal(((await res.json()) as { error: { code: string } }).error.code, "backend_error");
  assert.ok(upstream.runs.every((r) => r.closed && r.interrupted));
  await assert.rejects(fetch(`${url}/v1/models`));
});

test("when a run does not close, Axon still exits at the end of the grace time, with code 1", async () => {
  assert.equal(SHUTDOWN_GRACE_MS, 10000);
  const stuck: Upstream = {
    start: () => ({ next: () => new Promise(() => {}), resolveTool: () => {}, close: () => new Promise<void>(() => {}) }),
  };
  const turns = new Turns({ upstream: stuck, modelMap: MAP, parkTtlMs: 600000 });
  void turns.step(req(), "").catch(() => {});
  await settle();
  const s = await servers(loadConfig({}), turns);
  const started = Date.now();
  const code = await new Promise<number>((resolve) => void stop(s, turns, resolve, 50));
  assert.equal(code, 1);
  assert.ok(Date.now() - started < 1000);
});
