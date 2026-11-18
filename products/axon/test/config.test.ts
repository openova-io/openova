import { test } from "node:test";
import assert from "node:assert/strict";
import { loadConfig } from "../src/config.js";

test("the defaults serve sim-default and sim-fast and need no caller key", () => {
  assert.deepEqual(loadConfig({}), {
    port: 3000,
    modelMap: { "sim-default": "claude-sonnet-4-6", "sim-fast": "claude-haiku-4-5-20251001" },
    parkTtlMs: 600000,
  });
});

test("the model map and the park time come from the environment", () => {
  const c = loadConfig({ AXON_MODEL_MAP: '{"sim-x":"claude-opus-4-6"}', AXON_PARK_TTL_MS: "5000", AXON_PORT: "3100" });
  assert.deepEqual(c, { port: 3100, modelMap: { "sim-x": "claude-opus-4-6" }, parkTtlMs: 5000 });
});

test("a malformed setting stops the start", () => {
  assert.throws(() => loadConfig({ AXON_MODEL_MAP: "not json" }), /AXON_MODEL_MAP/);
  assert.throws(() => loadConfig({ AXON_MODEL_MAP: '["a"]' }), /AXON_MODEL_MAP/);
  assert.throws(() => loadConfig({ AXON_MODEL_MAP: '{"a":1}' }), /AXON_MODEL_MAP/);
  assert.throws(() => loadConfig({ AXON_PARK_TTL_MS: "0" }), /AXON_PARK_TTL_MS/);
});
