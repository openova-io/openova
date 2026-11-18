import { test } from "node:test";
import assert from "node:assert/strict";
import { render, textOf } from "../src/transcript.js";
import { promptOf, systemPrompt } from "../src/turns.js";

test("one user message is the prompt itself", () => {
  assert.equal(promptOf([{ role: "user", content: "hello" }]), "hello");
});

test("a system message does not enter the prompt", () => {
  assert.equal(
    promptOf([
      { role: "system", content: "be brief" },
      { role: "user", content: "hello" },
    ]),
    "hello",
  );
});

test("a longer conversation is the transcript, an assistant's text before its tool calls", () => {
  const messages = [
    { role: "system" as const, content: "be brief" },
    { role: "user" as const, content: "hi" },
    {
      role: "assistant" as const,
      content: "Let me check.",
      tool_calls: [{ id: "call_1", type: "function" as const, function: { name: "lookup", arguments: '{"q":"x"}' } }],
    },
    { role: "tool" as const, content: "42", tool_call_id: "call_1" },
    { role: "assistant" as const, content: "It is 42." },
    { role: "user" as const, content: [{ type: "text" as const, text: "and" }, { type: "image_url" as const, image_url: { url: "data:," } }] },
  ];
  assert.equal(promptOf(messages), render(messages));
  assert.equal(
    render(messages),
    [
      "<conversation>",
      "User: hi",
      "Assistant: Let me check.",
      'Assistant called tool lookup with id call_1 and arguments {"q":"x"}',
      "Tool result for call_1: 42",
      "Assistant: It is 42.",
      "User: and\n[image omitted]",
      "</conversation>",
      "",
      "Continue the conversation. Respond to the last entry above.",
    ].join("\n"),
  );
});

test("text parts are joined with a newline", () => {
  assert.equal(textOf([{ type: "text", text: "a" }, { type: "text", text: "b" }]), "a\nb");
  assert.equal(textOf(null), "");
});

test("system messages are joined with a blank line into the system prompt", () => {
  const system = systemPrompt(
    {
      model: "m",
      messages: [
        { role: "system", content: "a" },
        { role: "system", content: "b" },
        { role: "user", content: "hello" },
      ],
    },
    false,
  );
  assert.equal(system, "a\n\nb");
});

test("without a system message the system prompt is the default", () => {
  assert.equal(systemPrompt({ model: "m", messages: [{ role: "user", content: "hello" }] }, false), "You are a helpful assistant.");
});

test("a JSON response format is asked for in the system prompt", () => {
  const system = systemPrompt(
    { model: "m", messages: [{ role: "user", content: "hello" }], response_format: { type: "json_object" } },
    false,
  );
  assert.equal(
    system,
    "You are a helpful assistant.\n\nYou must respond with valid JSON only. No markdown, no explanation, just a JSON object.",
  );
  const schema = { type: "object", properties: { a: { type: "string" } } };
  const schemaSystem = systemPrompt(
    { model: "m", messages: [{ role: "user", content: "hello" }], response_format: { type: "json_schema", json_schema: schema } },
    false,
  );
  assert.equal(
    schemaSystem,
    `You are a helpful assistant.\n\nYou must respond with valid JSON matching this schema:\n${JSON.stringify(schema, null, 2)}\nNo markdown, no explanation, just a JSON object matching the schema.`,
  );
});
