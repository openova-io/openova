import { test } from "node:test";
import assert from "node:assert/strict";
import { formatPrompt, formatSystemPrompt } from "../src/providers/claude.js";

test("one user message is the prompt itself", () => {
  const prompt = formatPrompt([{ role: "user", content: "hello" }]);
  assert.equal(prompt.split("\n").at(-1), "hello");
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
    { role: "user", content: "how are you" },
  ]);
  assert.match(prompt, /^User: hi$/m);
  assert.match(prompt, /^Assistant: hello$/m);
  assert.match(prompt, /^User: how are you$/m);
  assert.equal(prompt.split("\n").at(-1), "Respond to the last User message.");
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
  assert.match(system, /valid JSON only/);
});
