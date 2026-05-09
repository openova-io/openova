// env.d.ts — type augmentation so vitest tests can read `env.OPENOVA_LEASES`
// without `any` casts.
//
// `@cloudflare/vitest-pool-workers` exposes `env` from `cloudflare:test`
// typed as `ProvidedEnv` — a project-augmentable interface. We declare
// the bindings the Worker uses so test files get full typing on
// `env.OPENOVA_LEASES.delete(...)` etc.

declare module "cloudflare:test" {
  interface ProvidedEnv {
    OPENOVA_LEASES: KVNamespace;
    BEARER_TOKENS_CSV: string;
    LOG_LEVEL?: "error" | "info" | "debug";
  }
}
