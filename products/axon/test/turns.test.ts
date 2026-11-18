import { test } from "node:test";
import assert from "node:assert/strict";
import { HttpError, NOT_EXECUTED, Turns } from "../src/turns.js";
import { render } from "../src/transcript.js";
import type { ChatCompletionRequest, ChatMessage, Tool } from "../src/types/openai.js";
import { FakeUpstream, type FakeScript } from "./fake-upstream.js";

const MAP = { "sim-default": "claude-sonnet-4-6", "sim-fast": "claude-haiku-4-5-20251001" };
const TOOLS: Tool[] = [
  {
    type: "function",
    function: { name: "lookup", description: "weather", parameters: { type: "object", properties: { city: { type: "string" } } } },
  },
  { type: "function", function: { name: "convert" } },
];
const LOOKUP = { id: "toolu_1", name: "lookup", input: { city: "Muscat" } };
const CONVERT = { id: "toolu_2", name: "convert", input: { from: "C", to: "F" } };

function setup(scripts: FakeScript[], parkTtlMs = 600000) {
  const upstream = new FakeUpstream(scripts);
  return { upstream, turns: new Turns({ upstream, modelMap: MAP, parkTtlMs }) };
}

function req(messages: ChatMessage[], extra: Partial<ChatCompletionRequest> = {}): ChatCompletionRequest {
  return { model: "sim-default", messages, ...extra };
}

const ASK: ChatMessage = { role: "user", content: "What is the weather in Muscat?" };

function answered(calls: { id: string; name: string; input: unknown }[], results: [string, string][]): ChatMessage[] {
  return [
    ASK,
    {
      role: "assistant",
      content: null,
      tool_calls: calls.map((c) => ({ id: c.id, type: "function", function: { name: c.name, arguments: JSON.stringify(c.input) } })),
    },
    ...results.map(([id, content]): ChatMessage => ({ role: "tool", tool_call_id: id, content })),
  ];
}

async function rejects(p: Promise<unknown>, status: number, code: string): Promise<HttpError> {
  try {
    await p;
  } catch (err) {
    assert.ok(err instanceof HttpError, `expected HttpError, got ${String(err)}`);
    assert.equal(err.status, status);
    assert.equal(err.code, code);
    return err;
  }
  assert.fail(`expected ${status} ${code}`);
}

test("plain text", async () => {
  const { upstream, turns } = setup([{ steps: [{ text: "Hello there." }] }]);
  const r = await turns.step(req([{ role: "user", content: "hi" }]), "");
  assert.equal(r.text, "Hello there.");
  assert.equal(r.finishReason, "stop");
  assert.deepEqual(r.toolCalls, []);
  const o = upstream.runs[0].options;
  assert.equal(o.model, "claude-sonnet-4-6");
  assert.equal(o.prompt, "hi");
  assert.equal(o.systemPrompt, "You are a helpful assistant.");
  assert.deepEqual(o.tools, []);
  assert.equal(upstream.runs[0].closed, true);
  assert.deepEqual(turns.stats(), { active: 0, parked: 0, queued: 0 });
});

test("a request with tools returns tool_calls and finish_reason tool_calls", async () => {
  const { upstream, turns } = setup([{ steps: [{ tools: [LOOKUP] }, { text: "unused" }] }]);
  const r = await turns.step(req([ASK], { tools: TOOLS }), "");
  assert.equal(r.finishReason, "tool_calls");
  assert.deepEqual(r.toolCalls, [{ id: "toolu_1", type: "function", function: { name: "lookup", arguments: '{"city":"Muscat"}' } }]);
  assert.deepEqual(upstream.runs[0].options.tools, [
    { name: "lookup", description: "weather", inputSchema: { type: "object", properties: { city: { type: "string" } } } },
    { name: "convert", description: undefined, inputSchema: { type: "object" } },
  ]);
  assert.equal(upstream.runs[0].closed, false);
  assert.deepEqual(turns.stats(), { active: 0, parked: 1, queued: 0 });
  await turns.shutdown();
  assert.equal(upstream.runs[0].closed, true);
  assert.equal(upstream.runs[0].interrupted, true);
});

test("a tool use cut off by max_tokens returns no tool calls, finish_reason length, and closes the run", async () => {
  const { upstream, turns } = setup([{ steps: [{ text: "Let me look", tools: [LOOKUP], stop: "max_tokens" }, { text: "unused" }] }]);
  const r = await turns.step(req([ASK], { tools: TOOLS }), "");
  assert.equal(r.text, "Let me look");
  assert.equal(r.finishReason, "length");
  assert.deepEqual(r.toolCalls, []);
  assert.equal(upstream.runs[0].closed, true);
  assert.deepEqual(upstream.runs[0].delivered, []);
  assert.deepEqual(turns.stats(), { active: 0, parked: 0, queued: 0 });
});

test("a model message that calls only a tool outside the caller's list does not end the step", async () => {
  const { upstream, turns } = setup([{ steps: [{ text: "Checking. ", stop: "tool_use" }, { text: "Done." }] }]);
  const r = await turns.step(req([ASK], { tools: TOOLS }), "");
  assert.equal(r.text, "Checking. Done.");
  assert.equal(r.finishReason, "stop");
  assert.deepEqual(r.toolCalls, []);
  assert.equal(r.usage.prompt_tokens, 2);
  assert.equal(r.usage.completion_tokens, 2);
  assert.equal(upstream.runs[0].closed, true);
});

test("a run that fails after its last message answers the error, not the text", async () => {
  const { upstream, turns } = setup([{ steps: [{ text: "ok" }], error: { message: "child exited" } }]);
  await rejects(turns.step(req([ASK]), ""), 502, "backend_error");
  assert.equal(upstream.runs[0].closed, true);
});

test("tool_choice null is auto, and a tool_choice object without a function name gets 400", async () => {
  const { upstream, turns } = setup([{ steps: [{ text: "auto" }] }]);
  const r = await turns.step(req([ASK], { tools: TOOLS, tool_choice: null }), "");
  assert.equal(r.text, "auto");
  assert.deepEqual(r.emulated, []);
  assert.equal(upstream.runs[0].options.systemPrompt, "You are a helpful assistant.");
  const noName = { type: "function" } as unknown as ChatCompletionRequest["tool_choice"];
  await rejects(turns.step(req([ASK], { tools: TOOLS, tool_choice: noName }), ""), 400, "invalid_request");
  assert.equal(upstream.runs.length, 1);
});

test("a follow-up with the result resumes the same run and returns its text", async () => {
  const { upstream, turns } = setup([{ steps: [{ tools: [LOOKUP] }, { text: "It is sunny in Muscat." }] }]);
  await turns.step(req([ASK], { tools: TOOLS }), "");
  const r = await turns.step(req(answered([LOOKUP], [["toolu_1", "sunny, 31 C"]]), { tools: TOOLS }), "");
  assert.equal(r.text, "It is sunny in Muscat.");
  assert.equal(r.finishReason, "stop");
  assert.equal(upstream.runs.length, 1);
  assert.deepEqual(upstream.runs[0].delivered, [{ id: "toolu_1", text: "sunny, 31 C" }]);
  assert.equal(upstream.runs[0].closed, true);
});

test("two tool uses give two tool calls and both results are required", async () => {
  const { upstream, turns } = setup([{ steps: [{ tools: [LOOKUP, CONVERT] }, { text: "done" }], handlers: "together" }]);
  const r = await turns.step(req([ASK], { tools: TOOLS }), "");
  assert.deepEqual(r.toolCalls.map((c) => c.id), ["toolu_1", "toolu_2"]);
  await rejects(turns.step(req(answered([LOOKUP, CONVERT], [["toolu_1", "sunny"]]), { tools: TOOLS }), ""), 400, "missing_tool_results");
  const done = await turns.step(req(answered([LOOKUP, CONVERT], [["toolu_1", "sunny"], ["toolu_2", "88 F"]]), { tools: TOOLS }), "");
  assert.equal(done.text, "done");
  assert.deepEqual(upstream.runs[0].delivered, [
    { id: "toolu_1", text: "sunny" },
    { id: "toolu_2", text: "88 F" },
  ]);
});

test("with handlers invoked one after the other, both results are delivered and the run ends", async () => {
  const { upstream, turns } = setup([{ steps: [{ tools: [LOOKUP, CONVERT] }, { text: "done" }], handlers: "sequential" }]);
  await turns.step(req([ASK], { tools: TOOLS }), "");
  const r = await turns.step(req(answered([LOOKUP, CONVERT], [["toolu_2", "88 F"], ["toolu_1", "sunny"]]), { tools: TOOLS }), "");
  assert.equal(r.text, "done");
  assert.deepEqual(upstream.runs[0].delivered, [
    { id: "toolu_1", text: "sunny" },
    { id: "toolu_2", text: "88 F" },
  ]);
  assert.equal(upstream.runs[0].ended, true);
  assert.equal(upstream.runs[0].closed, true);
});

test("parallel_tool_calls false returns one call and answers the other internally", async () => {
  const { upstream, turns } = setup([{ steps: [{ tools: [LOOKUP, CONVERT] }, { text: "done" }] }]);
  const r = await turns.step(req([ASK], { tools: TOOLS, parallel_tool_calls: false }), "");
  assert.deepEqual(r.toolCalls.map((c) => c.id), ["toolu_1"]);
  const done = await turns.step(req(answered([LOOKUP], [["toolu_1", "sunny"]]), { tools: TOOLS, parallel_tool_calls: false }), "");
  assert.equal(done.text, "done");
  assert.deepEqual(upstream.runs[0].delivered, [
    { id: "toolu_1", text: "sunny" },
    { id: "toolu_2", text: NOT_EXECUTED },
  ]);
});

test("a follow-up after expiry starts a new run whose prompt is the transcript", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const { upstream, turns } = setup([{ steps: [{ tools: [LOOKUP] }, { text: "unused" }] }, { steps: [{ text: "fresh" }] }], 1000);
  await turns.step(req([ASK], { tools: TOOLS }), "");
  t.mock.timers.tick(999);
  assert.equal(upstream.runs[0].closed, false);
  t.mock.timers.tick(1);
  assert.equal(upstream.runs[0].closed, true);
  assert.deepEqual(turns.stats(), { active: 0, parked: 0, queued: 0 });
  const messages = answered([LOOKUP], [["toolu_1", "sunny"]]);
  const r = await turns.step(req(messages, { tools: TOOLS }), "");
  assert.equal(r.text, "fresh");
  assert.equal(upstream.runs.length, 2);
  assert.equal(upstream.runs[1].options.prompt, render(messages));
  assert.equal(
    render(messages),
    '<conversation>\nUser: What is the weather in Muscat?\nAssistant called tool lookup with id toolu_1 and arguments {"city":"Muscat"}\nTool result for toolu_1: sunny\n</conversation>\n\nContinue the conversation. Respond to the last entry above.',
  );
});

test("a resume from another Organization gets 404", async () => {
  const { upstream, turns } = setup([{ steps: [{ tools: [LOOKUP] }, { text: "done" }] }]);
  await turns.step(req([ASK], { tools: TOOLS }), "acme");
  const followUp = req(answered([LOOKUP], [["toolu_1", "sunny"]]), { tools: TOOLS });
  const err = await rejects(turns.step(followUp, "globex"), 404, "model_not_found");
  assert.equal(err.message, "model not found");
  assert.deepEqual(upstream.runs[0].delivered, []);
  const r = await turns.step(followUp, "acme");
  assert.equal(r.text, "done");
  assert.equal(upstream.runs.length, 1);
});

test("an upstream 429 maps to 429 with Retry-After 30", async () => {
  const { upstream, turns } = setup([{ steps: [], error: { status: 429, message: "rate limited" } }]);
  const err = await rejects(turns.step(req([ASK]), ""), 429, "rate_limit_exceeded");
  assert.equal(err.retryAfter, 30);
  assert.equal(upstream.runs[0].closed, true);
});

test("an upstream authentication error maps to 503 and any other error to 502", async () => {
  const { turns } = setup([
    { steps: [], error: { status: 401, message: "invalid token" } },
    { steps: [], error: { message: "child exited" } },
  ]);
  const auth = await rejects(turns.step(req([ASK]), ""), 503, "simulation_credential_invalid");
  assert.equal(auth.retryAfter, undefined);
  await rejects(turns.step(req([ASK]), ""), 502, "backend_error");
});

test("usage of the response is the message_end usage mapped by rule 6", async () => {
  const usage = { input: 1006, cacheRead: 3006, cacheWrite: 2006, output: 4006 };
  const { turns } = setup([{ steps: [{ text: "ok", usage }] }]);
  const r = await turns.step(req([ASK]), "");
  assert.deepEqual(r.usage, {
    prompt_tokens: 6018,
    completion_tokens: 4006,
    total_tokens: 10024,
    prompt_tokens_details: { cached_tokens: 3006, cache_write_tokens: 2006 },
  });
});

test("tool_choice none registers no tools", async () => {
  const { upstream, turns } = setup([{ steps: [{ text: "no tools" }] }]);
  const r = await turns.step(req([ASK], { tools: TOOLS, tool_choice: "none" }), "");
  assert.equal(r.text, "no tools");
  assert.deepEqual(upstream.runs[0].options.tools, []);
  assert.deepEqual(r.emulated, []);
});

test("tool_choice required or a named function is emulated by instruction", async () => {
  const { upstream, turns } = setup([{ steps: [{ text: "a" }] }, { steps: [{ text: "b" }] }]);
  const required = await turns.step(req([ASK], { tools: TOOLS, tool_choice: "required" }), "");
  assert.deepEqual(required.emulated, ["tool_choice"]);
  assert.ok(upstream.runs[0].options.systemPrompt.endsWith("You must call one of the tools before you answer."));
  const named = await turns.step(req([ASK], { tools: TOOLS, tool_choice: { type: "function", function: { name: "lookup" } } }), "");
  assert.deepEqual(named.emulated, ["tool_choice"]);
  assert.ok(upstream.runs[1].options.systemPrompt.endsWith("You must call the tool lookup."));
});

test("a tool name of 57 characters gets 400", async () => {
  const { upstream, turns } = setup([{ steps: [{ text: "ok" }] }]);
  const long = (n: number): Tool[] => [{ type: "function", function: { name: "t".repeat(n) } }];
  await rejects(turns.step(req([ASK], { tools: long(57) }), ""), 400, "tool_name_too_long");
  assert.equal(upstream.runs.length, 0);
  const r = await turns.step(req([ASK], { tools: long(56) }), "");
  assert.equal(r.text, "ok");
});

test("no run object is handed to two requests", async () => {
  const { upstream, turns } = setup([
    { steps: [{ text: "one" }] },
    { steps: [{ text: "two" }] },
    { steps: [{ tools: [LOOKUP] }, { text: "three" }] },
  ]);
  const [a, b] = await Promise.all([turns.step(req([{ role: "user", content: "1" }]), ""), turns.step(req([{ role: "user", content: "2" }]), "")]);
  assert.deepEqual([a.text, b.text], ["one", "two"]);
  await turns.step(req([ASK], { tools: TOOLS }), "");
  const c = await turns.step(req(answered([LOOKUP], [["toolu_1", "sunny"]]), { tools: TOOLS }), "");
  assert.equal(c.text, "three");
  assert.equal(upstream.runs.length, 3);
  assert.equal(new Set(upstream.runs).size, 3);
  assert.ok(upstream.runs.every((r) => r.closed));
});

test("a model outside the map gets 404 and an image part gets 400", async () => {
  const { upstream, turns } = setup([]);
  await rejects(turns.step(req([ASK], { model: "gpt-4o" }), ""), 404, "model_not_found");
  await rejects(turns.step(req([ASK], { model: undefined }), ""), 404, "model_not_found");
  const image: ChatMessage = { role: "user", content: [{ type: "text", text: "what is this" }, { type: "image_url", image_url: { url: "data:image/png;base64,AA==" } }] };
  await rejects(turns.step(req([image]), ""), 400, "unsupported_content");
  await rejects(turns.step(req([]), ""), 400, "invalid_request");
  await rejects(turns.step(req([ASK], { tools: [{ type: "function" } as Tool] }), ""), 400, "invalid_request");
  assert.equal(upstream.runs.length, 0);
});

test("text parts are joined and reasoning_effort sets the effort", async () => {
  const { upstream, turns } = setup([{ steps: [{ text: "ok" }] }, { steps: [{ text: "ok" }] }]);
  await turns.step(req([{ role: "user", content: [{ type: "text", text: "a" }, { type: "text", text: "b" }] }], { reasoning_effort: "high" }), "");
  assert.equal(upstream.runs[0].options.prompt, "a\nb");
  assert.equal(upstream.runs[0].options.effort, "high");
  await turns.step(req([ASK], { reasoning_effort: "max" }), "");
  assert.equal(upstream.runs[1].options.effort, undefined);
});
