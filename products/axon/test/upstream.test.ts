import { test } from "node:test";
import assert from "node:assert/strict";
import type { SDKMessage } from "@anthropic-ai/claude-agent-sdk";
import { EventMapper, ToolResults } from "../src/upstream.js";

function raw(event: object, parent: string | null = null): SDKMessage {
  return { type: "stream_event", event, parent_tool_use_id: parent, uuid: "u", session_id: "s" } as unknown as SDKMessage;
}

test("raw stream events give one message_end with the tool uses and the raw usage", () => {
  const m = new EventMapper();
  const messages: SDKMessage[] = [
    raw({ type: "message_start", message: { id: "msg_1", usage: { input_tokens: 1006, cache_read_input_tokens: 3006, cache_creation_input_tokens: 2006, output_tokens: 1 } } }),
    raw({ type: "content_block_start", index: 0, content_block: { type: "text", text: "" } }),
    raw({ type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "Checking." } }),
    raw({ type: "content_block_start", index: 1, content_block: { type: "tool_use", id: "toolu_1", name: "mcp__t__lookup", input: {} } }),
    raw({ type: "content_block_delta", index: 1, delta: { type: "input_json_delta", partial_json: '{"city":' } }),
    raw({ type: "content_block_delta", index: 1, delta: { type: "input_json_delta", partial_json: '"Muscat"}' } }),
    raw({ type: "content_block_start", index: 2, content_block: { type: "tool_use", id: "toolu_2", name: "Read", input: {} } }),
    raw({ type: "content_block_delta", index: 2, delta: { type: "input_json_delta", partial_json: '{"file_path":"/etc/passwd"}' } }),
    { type: "assistant", message: { id: "msg_1", content: [], usage: { input_tokens: 1006, output_tokens: 1 } }, parent_tool_use_id: null } as unknown as SDKMessage,
    raw({ type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "ignored" } }, "toolu_9"),
    raw({ type: "message_delta", delta: { stop_reason: "tool_use" }, usage: { output_tokens: 4006 } }),
    raw({ type: "message_stop" }),
  ];
  const events = messages.flatMap((msg) => m.map(msg));
  assert.deepEqual(events, [
    { kind: "text_delta", text: "Checking." },
    { kind: "tool_start", index: 0, id: "toolu_1", name: "lookup" },
    { kind: "tool_args_delta", index: 0, json: '{"city":' },
    { kind: "tool_args_delta", index: 0, json: '"Muscat"}' },
    {
      kind: "message_end",
      stopReason: "tool_use",
      toolUses: [{ id: "toolu_1", name: "lookup", input: { city: "Muscat" } }],
      usage: { input: 1006, cacheRead: 3006, cacheWrite: 2006, output: 4006 },
    },
  ]);
});

test("each model message starts its usage and its tool uses afresh", () => {
  const m = new EventMapper();
  const one = [
    raw({ type: "message_start", message: { usage: { input_tokens: 5, output_tokens: 1 } } }),
    raw({ type: "message_delta", delta: { stop_reason: "end_turn" }, usage: { output_tokens: 7 } }),
    raw({ type: "message_stop" }),
  ];
  m.map(raw({ type: "content_block_start", index: 0, content_block: { type: "tool_use", id: "toolu_old", name: "mcp__t__x", input: {} } }));
  const events = one.flatMap((msg) => m.map(msg));
  assert.deepEqual(events, [{ kind: "message_end", stopReason: "end_turn", toolUses: [], usage: { input: 5, cacheRead: 0, cacheWrite: 0, output: 7 } }]);
});

test("the result message ends the run, with the upstream status on an error", () => {
  const m = new EventMapper();
  const ok = { type: "result", subtype: "success", is_error: false, result: "fine" } as unknown as SDKMessage;
  const limited = { type: "result", subtype: "success", is_error: true, api_error_status: 429, result: "rate limited" } as unknown as SDKMessage;
  const failed = { type: "result", subtype: "error_during_execution", is_error: true, errors: ["child exited"] } as unknown as SDKMessage;
  assert.deepEqual(m.map(ok), [{ kind: "run_end" }]);
  assert.deepEqual(m.map(limited), [{ kind: "run_end", error: { status: 429, message: "rate limited" } }]);
  assert.deepEqual(m.map(failed), [{ kind: "run_end", error: { status: undefined, message: "child exited" } }]);
});

test("a result that arrives before its handler is kept until the handler takes it", async () => {
  const r = new ToolResults();
  r.seen([{ id: "a", name: "lookup", input: {} }]);
  r.resolve("a", "first");
  assert.equal(await r.invoked("a", "lookup", {}), "first");
});

test("a handler that is invoked before its result waits for it", async () => {
  const r = new ToolResults();
  r.seen([{ id: "a", name: "lookup", input: {} }]);
  const p = r.invoked("a", "lookup", {});
  r.resolve("a", "later");
  assert.equal(await p, "later");
});

test("a handler without an identifier takes the first pending use with its name and input", async () => {
  const r = new ToolResults();
  const early = r.invoked(undefined, "lookup", { city: "Sur" });
  r.seen([
    { id: "a", name: "lookup", input: { city: "Muscat" } },
    { id: "b", name: "lookup", input: { city: "Sur" } },
    { id: "c", name: "lookup", input: { city: "Sur" } },
  ]);
  const late = r.invoked(undefined, "lookup", { city: "Sur" });
  r.resolve("c", "for c");
  r.resolve("b", "for b");
  assert.equal(await early, "for b");
  assert.equal(await late, "for c");
});
