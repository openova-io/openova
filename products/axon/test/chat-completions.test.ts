import { test } from "node:test";
import assert from "node:assert/strict";
import Fastify from "fastify";
import { chatCompletionsRoute } from "../src/routes/chat-completions.js";
import { modelsRoute } from "../src/routes/models.js";
import { Turns } from "../src/turns.js";
import { FakeUpstream, type FakeScript } from "./fake-upstream.js";

const MAP = { "sim-default": "claude-sonnet-4-6", "sim-fast": "claude-haiku-4-5-20251001" };
const TOOLS = [{ type: "function", function: { name: "lookup", parameters: { type: "object" } } }];
const USAGE = { input: 10, cacheRead: 20, cacheWrite: 30, output: 40 };

async function server(scripts: FakeScript[]) {
  const upstream = new FakeUpstream(scripts);
  const turns = new Turns({ upstream, modelMap: MAP, parkTtlMs: 600000 });
  const app = Fastify();
  await modelsRoute(app, MAP);
  await chatCompletionsRoute(app, turns);
  return { app, upstream, turns };
}

function post(body: object, headers: Record<string, string> = {}) {
  return { method: "POST" as const, url: "/v1/chat/completions", payload: body, headers };
}

test("a plain request needs no caller key and gets a chat.completion with real usage", async () => {
  const { app } = await server([{ steps: [{ text: "hello", usage: USAGE }] }]);
  const res = await app.inject(post({ model: "sim-default", messages: [{ role: "user", content: "hi" }] }));
  assert.equal(res.statusCode, 200);
  const body = res.json();
  assert.equal(body.object, "chat.completion");
  assert.equal(body.model, "sim-default");
  assert.match(body.id, /^chatcmpl-[0-9a-f-]{36}$/);
  assert.deepEqual(body.choices, [
    { index: 0, message: { role: "assistant", content: "hello", refusal: null }, logprobs: null, finish_reason: "stop" },
  ]);
  assert.deepEqual(body.usage, {
    prompt_tokens: 60,
    completion_tokens: 40,
    total_tokens: 100,
    prompt_tokens_details: { cached_tokens: 20, cache_write_tokens: 30 },
  });
  assert.equal(res.headers["x-openova-emulated"], undefined);
});

test("tool calls come back as tool_calls with no content", async () => {
  const { app, turns } = await server([{ steps: [{ tools: [{ id: "toolu_1", name: "lookup", input: { q: 1 } }] }, { text: "x" }] }]);
  const res = await app.inject(post({ model: "sim-default", messages: [{ role: "user", content: "hi" }], tools: TOOLS }));
  const choice = res.json().choices[0];
  assert.equal(choice.finish_reason, "tool_calls");
  assert.equal(choice.message.content, null);
  assert.deepEqual(choice.message.tool_calls, [{ id: "toolu_1", type: "function", function: { name: "lookup", arguments: '{"q":1}' } }]);
  await turns.shutdown();
});

test("an upstream 429 answers 429 with Retry-After 30 and the contract's error body", async () => {
  const { app } = await server([{ steps: [], error: { status: 429, message: "limited" } }]);
  const res = await app.inject(post({ model: "sim-default", messages: [{ role: "user", content: "hi" }] }));
  assert.equal(res.statusCode, 429);
  assert.equal(res.headers["retry-after"], "30");
  assert.deepEqual(res.json(), { error: { message: "the upstream rate limit was hit", type: "rate_limit_error", code: "rate_limit_exceeded" } });
});

test("an unknown model answers 404 model_not_found", async () => {
  const { app } = await server([]);
  const res = await app.inject(post({ model: "gpt-4o", messages: [{ role: "user", content: "hi" }] }));
  assert.equal(res.statusCode, 404);
  assert.deepEqual(res.json(), { error: { message: "model not found", type: "invalid_request_error", code: "model_not_found" } });
});

test("the Organization header decides who may resume a chain", async () => {
  const { app } = await server([{ steps: [{ tools: [{ id: "toolu_1", name: "lookup", input: {} }] }, { text: "done" }] }]);
  const ask = { role: "user", content: "hi" };
  await app.inject(post({ model: "sim-default", messages: [ask], tools: TOOLS }, { "x-openova-organization": "acme" }));
  const followUp = {
    model: "sim-default",
    tools: TOOLS,
    messages: [
      ask,
      { role: "assistant", content: null, tool_calls: [{ id: "toolu_1", type: "function", function: { name: "lookup", arguments: "{}" } }] },
      { role: "tool", tool_call_id: "toolu_1", content: "result" },
    ],
  };
  assert.equal((await app.inject(post(followUp, { "x-openova-organization": "globex" }))).statusCode, 404);
  const res = await app.inject(post(followUp, { "x-openova-organization": "acme" }));
  assert.equal(res.statusCode, 200);
  assert.equal(res.json().choices[0].message.content, "done");
});

test("an emulated tool_choice is named in the x-openova-emulated header", async () => {
  const { app } = await server([{ steps: [{ text: "ok" }] }]);
  const res = await app.inject(post({ model: "sim-default", tools: TOOLS, tool_choice: "required", messages: [{ role: "user", content: "hi" }] }));
  assert.equal(res.statusCode, 200);
  assert.equal(res.headers["x-openova-emulated"], "tool_choice");
});

test("tool_choice null answers 200, and a tool_choice object without a function answers 400", async () => {
  const { app } = await server([{ steps: [{ text: "ok" }] }]);
  const ok = await app.inject(post({ model: "sim-default", tools: TOOLS, tool_choice: null, messages: [{ role: "user", content: "hi" }] }));
  assert.equal(ok.statusCode, 200);
  assert.equal(ok.json().choices[0].message.content, "ok");
  const bad = await app.inject(post({ model: "sim-default", tools: TOOLS, tool_choice: { type: "function" }, messages: [{ role: "user", content: "hi" }] }));
  assert.equal(bad.statusCode, 400);
  assert.equal(bad.json().error.code, "invalid_request");
});

test("the model list is the keys of the model map", async () => {
  const { app } = await server([]);
  const res = await app.inject({ method: "GET", url: "/v1/models" });
  assert.deepEqual(res.json().data.map((m: { id: string }) => m.id), ["sim-default", "sim-fast"]);
});
