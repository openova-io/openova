// Real streaming (rule 7 of the turn chain): one chunk per upstream event, as it arrives.
import { test } from "node:test";
import assert from "node:assert/strict";
import { request as httpRequest, type IncomingMessage } from "node:http";
import type { AddressInfo } from "node:net";
import { setTimeout as delay } from "node:timers/promises";
import Fastify from "fastify";
import { chatCompletionsRoute } from "../src/routes/chat-completions.js";
import { Turns } from "../src/turns.js";
import { FakeUpstream, type FakeScript } from "./fake-upstream.js";

const MAP = { "sim-default": "claude-sonnet-4-6", "sim-fast": "claude-haiku-4-5-20251001" };
const TOOLS = [
  { type: "function", function: { name: "lookup", parameters: { type: "object" } } },
  { type: "function", function: { name: "convert" } },
];
const USAGE = { input: 10, cacheRead: 20, cacheWrite: 30, output: 40 };
const ASK = [{ role: "user", content: "hi" }];
const ROLE = { role: "assistant", content: "" };

async function server(scripts: FakeScript[]) {
  const upstream = new FakeUpstream(scripts);
  const turns = new Turns({ upstream, modelMap: MAP, parkTtlMs: 600000 });
  const app = Fastify();
  await chatCompletionsRoute(app, turns);
  return { app, upstream, turns };
}

function post(body: object) {
  return { method: "POST" as const, url: "/v1/chat/completions", payload: { model: "sim-default", stream: true, ...body } };
}

/** The data lines of an event stream, each parsed, and whether it ended with [DONE]. */
function events(body: string): { chunks: any[]; done: boolean } {
  const lines = body.split("\n\n").filter((e) => e !== "");
  for (const l of lines) assert.match(l, /^data: /);
  const done = lines.at(-1) === "data: [DONE]";
  const chunks = (done ? lines.slice(0, -1) : lines).map((l) => JSON.parse(l.slice("data: ".length)));
  return { chunks, done };
}

/** The delta of every chunk that has a choice, in order. */
function deltas(chunks: any[]): any[] {
  return chunks.filter((c) => c.choices.length > 0 && c.choices[0].finish_reason === null).map((c) => c.choices[0].delta);
}

test("a text answer streams the role chunk, one content chunk per delta, the finish chunk, the usage chunk and DONE", async () => {
  const { app } = await server([{ steps: [{ deltas: ["Hel", "lo", "!"], usage: USAGE }] }]);
  const res = await app.inject(post({ model: "sim-fast", stream_options: { include_usage: true }, messages: ASK }));
  assert.equal(res.statusCode, 200);
  assert.match(String(res.headers["content-type"]), /^text\/event-stream/);
  const { chunks, done } = events(res.body);
  assert.equal(done, true);
  assert.equal(chunks.length, 6);
  assert.deepEqual(deltas(chunks), [ROLE, { content: "Hel" }, { content: "lo" }, { content: "!" }]);
  assert.deepEqual(chunks[4].choices, [{ index: 0, delta: {}, logprobs: null, finish_reason: "stop" }]);
  assert.deepEqual(chunks[5].choices, []);
  for (const c of chunks) {
    assert.equal(c.object, "chat.completion.chunk");
    assert.equal(c.id, chunks[0].id);
    assert.equal(c.created, chunks[0].created);
    assert.equal(c.model, "sim-fast");
  }
  assert.match(chunks[0].id, /^chatcmpl-[0-9a-f-]{36}$/);
});

test("one tool call whose arguments arrive in three deltas streams a start delta and three argument deltas", async () => {
  const lookup = { id: "toolu_1", name: "lookup", input: { city: "Muscat" } };
  const parts = ['{"ci', 'ty":"Mu', 'scat"}'];
  const { app, turns } = await server([{ steps: [{ tools: [lookup], argParts: [parts] }, { text: "x" }] }]);
  const res = await app.inject(post({ tools: TOOLS, messages: ASK }));
  assert.equal(res.statusCode, 200);
  const { chunks, done } = events(res.body);
  assert.equal(done, true);
  assert.deepEqual(deltas(chunks), [
    ROLE,
    { tool_calls: [{ index: 0, id: "toolu_1", type: "function", function: { name: "lookup", arguments: "" } }] },
    ...parts.map((json) => ({ tool_calls: [{ index: 0, function: { arguments: json } }] })),
  ]);
  const args = deltas(chunks).slice(2).map((d) => d.tool_calls[0].function.arguments).join("");
  assert.deepEqual(JSON.parse(args), lookup.input);
  assert.equal(chunks.at(-1).choices[0].finish_reason, "tool_calls");
  assert.equal(chunks.filter((c) => c.usage).length, 0, "no usage chunk when none was asked for");
  assert.equal(turns.stats().parked, 1);
  await turns.shutdown();
});

test("the usage chunk comes after the finish chunk and carries the mapped numbers", async () => {
  const usage = { input: 1004, cacheRead: 3004, cacheWrite: 2004, output: 4004 };
  const { app } = await server([{ steps: [{ text: "ok", usage }] }]);
  const res = await app.inject(post({ stream_options: { include_usage: true }, messages: ASK }));
  const { chunks, done } = events(res.body);
  assert.equal(done, true);
  assert.equal(chunks.at(-2).choices[0].finish_reason, "stop");
  assert.deepEqual(chunks.at(-1).choices, []);
  assert.deepEqual(chunks.at(-1).usage, {
    prompt_tokens: 6012,
    completion_tokens: 4004,
    total_tokens: 10016,
    prompt_tokens_details: { cached_tokens: 3004, cache_write_tokens: 2004 },
  });
});

test("no timer is used by the stream path", async (t) => {
  const { app } = await server([{ steps: [{ deltas: ["a", "b", "c"], usage: USAGE }] }]);
  await app.ready();
  t.mock.timers.enable({ apis: ["setTimeout", "setInterval"] });
  const timeouts = t.mock.method(globalThis, "setTimeout");
  const intervals = t.mock.method(globalThis, "setInterval");
  let finished = false;
  const pending = app.inject(post({ stream_options: { include_usage: true }, messages: ASK })).then((res) => {
    finished = true;
    return res;
  });
  // A sleep on a mocked timer never fires: the stream would not finish.
  for (let i = 0; i < 1000 && !finished; i++) await new Promise((resolve) => setImmediate(resolve));
  assert.equal(timeouts.mock.callCount(), 0, "setTimeout was called by the stream path");
  assert.equal(intervals.mock.callCount(), 0, "setInterval was called by the stream path");
  assert.equal(finished, true, "the stream did not finish without a timer");
  const { chunks, done } = events((await pending).body);
  assert.equal(done, true);
  assert.deepEqual(deltas(chunks), [ROLE, { content: "a" }, { content: "b" }, { content: "c" }]);
});

test("an upstream error before the first delta answers with its own status, not a stream", async () => {
  const { app } = await server([{ steps: [], error: { status: 429, message: "limited" } }]);
  const res = await app.inject(post({ messages: ASK }));
  assert.equal(res.statusCode, 429);
  assert.equal(res.headers["retry-after"], "30");
  assert.deepEqual(res.json(), { error: { message: "the upstream rate limit was hit", type: "rate_limit_error", code: "rate_limit_exceeded" } });
});

test("a run that fails after its text ends the stream with the error and without a finish chunk or DONE", async () => {
  const { app } = await server([{ steps: [{ deltas: ["part"] }], error: { status: 500, message: "boom" } }]);
  const res = await app.inject(post({ stream_options: { include_usage: true }, messages: ASK }));
  assert.equal(res.statusCode, 200);
  const { chunks, done } = events(res.body);
  assert.equal(done, false);
  assert.deepEqual(deltas(chunks.slice(0, -1)), [ROLE, { content: "part" }]);
  assert.deepEqual(chunks.at(-1), { error: { message: "the backend failed", type: "server_error", code: "backend_error" } });
  assert.equal(chunks.filter((c) => c.choices?.[0]?.finish_reason).length, 0);
});

test("parallel_tool_calls false streams only the first tool call", async () => {
  const calls = [
    { id: "toolu_1", name: "lookup", input: { q: 1 } },
    { id: "toolu_2", name: "convert", input: { q: 2 } },
  ];
  const { app, turns } = await server([{ steps: [{ tools: calls }, { text: "x" }] }]);
  const res = await app.inject(post({ tools: TOOLS, parallel_tool_calls: false, messages: ASK }));
  const sent = deltas(events(res.body).chunks).flatMap((d) => d.tool_calls ?? []);
  assert.deepEqual(sent, [
    { index: 0, id: "toolu_1", type: "function", function: { name: "lookup", arguments: "" } },
    { index: 0, function: { arguments: '{"q":1}' } },
  ]);
  await turns.shutdown();
});

test("an emulated tool_choice is named in the header of the stream", async () => {
  const { app } = await server([{ steps: [{ text: "ok" }] }]);
  const res = await app.inject(post({ tools: TOOLS, tool_choice: "required", messages: ASK }));
  assert.equal(res.statusCode, 200);
  assert.equal(res.headers["x-openova-emulated"], "tool_choice");
});

/** Opens a streamed request on a listening server; received collects the bytes. */
async function open(port: number, body: object) {
  const state = { received: "", ended: false };
  const payload = JSON.stringify({ model: "sim-default", stream: true, messages: ASK, ...body });
  const req = httpRequest({ host: "127.0.0.1", port, method: "POST", path: "/v1/chat/completions", headers: { "content-type": "application/json" } });
  const responded = new Promise<IncomingMessage>((resolve, reject) => {
    req.on("response", resolve);
    req.on("error", reject);
    req.end(payload);
  });
  const late = delay(2000).then(() => {
    throw new Error("no response within 2 seconds");
  });
  const res = await Promise.race([responded, late]);
  res.setEncoding("utf8");
  res.on("data", (d: string) => (state.received += d));
  res.on("end", () => (state.ended = true));
  res.on("error", () => {});
  return { req, res, state };
}

/** Ends a listening test server even when a step is still waiting. */
async function stop(app: Awaited<ReturnType<typeof server>>["app"], turns: Turns): Promise<void> {
  app.server.closeAllConnections();
  await turns.shutdown();
  await app.close();
}

async function until(what: string, cond: () => boolean): Promise<void> {
  for (let i = 0; i < 400 && !cond(); i++) await delay(5);
  assert.ok(cond(), `timed out waiting for ${what}`);
}

test("the first deltas reach the caller before the model message ends", async (t) => {
  let release = () => {};
  const hold = new Promise<void>((resolve) => (release = resolve));
  const { app, upstream, turns } = await server([{ steps: [{ deltas: ["first"], hold, usage: USAGE }] }]);
  t.after(() => stop(app, turns));
  await app.listen({ port: 0, host: "127.0.0.1" });
  const { res, state } = await open((app.server.address() as AddressInfo).port, {});
  assert.equal(res.statusCode, 200);
  await until("the first content chunk", () => state.received.includes('"content":"first"'));
  assert.equal(state.ended, false);
  assert.equal(upstream.runs[0].ended, false, "the run is still generating");
  release();
  await until("the end of the stream", () => state.ended);
  assert.equal(events(state.received).done, true);
});

test("a caller that disconnects mid-stream closes the run", async (t) => {
  const hold = new Promise<void>(() => {});
  const { app, upstream, turns } = await server([{ steps: [{ deltas: ["first"], hold }] }]);
  t.after(() => stop(app, turns));
  await app.listen({ port: 0, host: "127.0.0.1" });
  const { req, state } = await open((app.server.address() as AddressInfo).port, {});
  await until("the first content chunk", () => state.received.includes('"content":"first"'));
  assert.deepEqual(turns.stats(), { active: 1, parked: 0, queued: 0 });
  req.destroy();
  await until("the run to be interrupted", () => upstream.runs[0].interrupted);
  assert.equal(upstream.runs[0].closed, true);
  assert.deepEqual(turns.stats(), { active: 0, parked: 0, queued: 0 });
});
