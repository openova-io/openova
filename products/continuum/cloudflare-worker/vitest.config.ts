// vitest.config.ts — uses @cloudflare/vitest-pool-workers to run the
// tests INSIDE a real Workers runtime (workerd), with a real KV
// namespace bound. This is the canonical way to test a Worker without
// mocking Web APIs by hand.
//
// The pool spawns a per-test workerd instance. KV is implemented by
// Miniflare under the hood — same storage semantics as production but
// in-memory + per-test isolated.

import { defineWorkersConfig } from "@cloudflare/vitest-pool-workers/config";

export default defineWorkersConfig({
  test: {
    poolOptions: {
      workers: {
        // The Worker source under test. The pool loads it as the
        // default-export `fetch` handler.
        main: "./src/index.ts",
        // wrangler.toml supplies the KV binding declaration; the
        // pool wires up an in-memory KV under that binding name.
        wrangler: { configPath: "./wrangler.toml" },
        miniflare: {
          // Override env vars set in wrangler.toml so each test starts
          // with a deterministic allow-list — the actual production
          // tokens are operator-supplied via `wrangler secret put`,
          // but in tests we use this fixed value.
          bindings: {
            BEARER_TOKENS_CSV: "test-token,second-token",
            LOG_LEVEL: "error", // suppress chatter during tests
          },
        },
      },
    },
  },
});
