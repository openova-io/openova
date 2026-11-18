import { test } from "node:test";
import assert from "node:assert/strict";
import { formatPrompt, formatSystemPrompt } from "../src/providers/claude.js";

test("one user message is the prompt itself", () => {
  const prompt = formatPrompt([{ role: "user", content: "hello" }]);
  assert.equal(prompt, "hello");
});

test("a system message does not enter the prompt", () => {
  const prompt = formatPrompt([
    { role: "system", content: "be brief" },
    { role: "user", content: "hello" },
  ]);
  assert.equal(prompt, "hello");
});

test("a longer conversation is written as plain text", () => {
  const prompt = formatPrompt([
    { role: "user", content: "hi" },
    { role: "assistant", content: "hello" },
    { role: "tool", content: "42", tool_call_id: "call_1" },
    { role: "user", content: "how are you" },
  ]);
  assert.equal(
    prompt,
    "<conversation>\nUser: hi\nAssistant: hello\nTool result (call_1): 42\nUser: how are you\n</conversation>\n\nRespond to the last User message.",
  );
});

test("system messages are joined with a blank line into the system prompt", () => {
  const system = formatSystemPrompt({
    model: "m",
    messages: [
      { role: "system", content: "a" },
      { role: "system", content: "b" },
      { role: "user", content: "hello" },
    ],
  });
  assert.equal(system, "a\n\nb");
});

test("without a system message the system prompt is the default", () => {
  const system = formatSystemPrompt({ model: "m", messages: [{ role: "user", content: "hello" }] });
  assert.equal(system, "You are a helpful assistant.");
});

test("a JSON response format is asked for in the system prompt", () => {
  const system = formatSystemPrompt({
    model: "m",
    messages: [{ role: "user", content: "hello" }],
    responseFormat: { type: "json_object" },
  });
  assert.equal(
    system,
    "You are a helpful assistant.\n\nYou must respond with valid JSON only. No markdown, no explanation, just a JSON object.",
  );
  const schema = { type: "object", properties: { a: { type: "string" } } };
  const schemaSystem = formatSystemPrompt({
    model: "m",
    messages: [{ role: "user", content: "hello" }],
    responseFormat: { type: "json_schema", json_schema: schema },
  });
  assert.equal(
    schemaSystem,
    `You are a helpful assistant.\n\nYou must respond with valid JSON matching this schema:\n${JSON.stringify(schema, null, 2)}\nNo markdown, no explanation, just a JSON object matching the schema.`,
  );
});
