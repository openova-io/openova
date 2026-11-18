import { test } from "node:test";
import assert from "node:assert/strict";
import { loadConfig } from "../src/config.js";

test("the defaults serve sim-default and sim-fast and need no caller key", () => {
  assert.deepEqual(loadConfig({}), {
    port: 3000,
    adminPort: 3001,
    modelMap: { "sim-default": "claude-sonnet-4-6", "sim-fast": "claude-haiku-4-5-20251001" },
    maxActive: 4,
    maxParked: 8,
    queueMax: 16,
    queueWaitMs: 30000,
    parkTtlMs: 600000,
    stepTimeoutMs: 1800000,
    credential: {
      file: "/var/run/secrets/openova/model-credential",
      kvPath: "mounted/data/axon/sa/axon/model-credential/e1",
      role: "axon-credential",
      openbaoAddr: "http://openbao.openbao.svc.cluster.local:8200",
      refreshMarginMs: 600000,
    },
  });
});

test("the credential settings come from the environment, and the write-back path must be a KV data path", () => {
  const env = { AXON_CREDENTIAL_FILE: "/f", AXON_CREDENTIAL_KV_PATH: "kv/data/a", AXON_CREDENTIAL_ROLE: "r", OPENBAO_ADDR: "http://b:8200/" };
  assert.deepEqual(loadConfig({ ...env, AXON_REFRESH_MARGIN_MS: "5000" }).credential, { file: "/f", kvPath: "kv/data/a", role: "r", openbaoAddr: "http://b:8200", refreshMarginMs: 5000 });
  assert.throws(() => loadConfig({ AXON_CREDENTIAL_KV_PATH: "kv/a" }), /AXON_CREDENTIAL_KV_PATH/);
  assert.throws(() => loadConfig({ AXON_REFRESH_MARGIN_MS: "0" }), /AXON_REFRESH_MARGIN_MS/);
});

test("the model map and the park time come from the environment", () => {
  const c = loadConfig({ AXON_MODEL_MAP: '{"sim-x":"claude-opus-4-6"}', AXON_PARK_TTL_MS: "5000", AXON_PORT: "3100" });
  assert.deepEqual(c, { ...loadConfig({}), port: 3100, modelMap: { "sim-x": "claude-opus-4-6" }, parkTtlMs: 5000 });
});

test("the limits, the time-out and the admin port come from the environment", () => {
  const env = { AXON_ADMIN_PORT: "3101", AXON_MAX_ACTIVE: "2", AXON_MAX_PARKED: "3", AXON_QUEUE_MAX: "5", AXON_QUEUE_WAIT_MS: "7000" };
  const c = loadConfig({ ...env, AXON_STEP_TIMEOUT_MS: "9000" });
  assert.deepEqual([c.adminPort, c.maxActive, c.maxParked, c.queueMax, c.queueWaitMs, c.stepTimeoutMs], [3101, 2, 3, 5, 7000, 9000]);
});

test("a malformed setting stops the start", () => {
  assert.throws(() => loadConfig({ AXON_MODEL_MAP: "not json" }), /AXON_MODEL_MAP/);
  assert.throws(() => loadConfig({ AXON_MODEL_MAP: '["a"]' }), /AXON_MODEL_MAP/);
  assert.throws(() => loadConfig({ AXON_MODEL_MAP: '{"a":1}' }), /AXON_MODEL_MAP/);
  assert.throws(() => loadConfig({ AXON_PARK_TTL_MS: "0" }), /AXON_PARK_TTL_MS/);
  assert.throws(() => loadConfig({ AXON_MAX_ACTIVE: "0" }), /AXON_MAX_ACTIVE/);
  assert.throws(() => loadConfig({ AXON_ADMIN_PORT: "3000" }), /AXON_ADMIN_PORT/);
});
