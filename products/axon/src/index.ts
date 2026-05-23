import Fastify from "fastify";
import { loadConfig } from "./config.js";
import { createAuthHook } from "./middleware/auth.js";
import { modelsRoute } from "./routes/models.js";
import { chatCompletionsRoute } from "./routes/chat-completions.js";
import { initPool, shutdownPool, getPool } from "./providers/claude.js";
import { connectValkey, disconnectValkey, getPoolStats } from "./providers/valkey.js";
import { ConversationStore } from "./providers/conversation.js";
import { refreshIfExpired, startPeriodicRefresh, stopPeriodicRefresh } from "./providers/token-refresh.js";
import { VllmProvider } from "./providers/vllm.js";

const config = loadConfig();
const app = Fastify({ logger: true });
const conversations = new ConversationStore(config.conversationTtl);

const isVllm = config.provider === "vllm";
let vllm: VllmProvider | undefined;

if (isVllm) {
  vllm = new VllmProvider(config.vllm);
  await vllm.init();
  app.log.info(`Provider: vllm (${config.vllm.baseUrl}), default model: ${config.vllm.defaultModel}`);
} else {
  app.log.info(`Provider: claude, default model: ${config.defaultModel}`);
}

// Liveness — process-death detection. ALWAYS returns 200 when the
// Fastify event loop is alive. Independent of upstream provider state
// (vLLM, Claude pool, Valkey) — those are dependencies, not the axon
// process itself. Kubelet kills the pod ONLY when this returns non-200,
// which happens only when the JS event loop is stuck. Upstream-down
// scenarios surface via /health (readiness) so traffic stops without a
// restart loop. Issue #2203 — without this split, an upstream TLS/DNS
// blip caused 97 restarts in 6h08m on one Sovereign.
app.get("/healthz", async () => ({ status: "alive" }));

// Readiness — dependency-state detection. Returns 200 only when upstream
// (vLLM or Claude pool) is reachable AND healthy. Kubelet uses this to
// gate traffic via the Service endpoint; FAILED readiness drops the pod
// out of rotation but does NOT trigger restart. Always returns HTTP 200
// with `status` body — never throws on upstream-down (vllm.health()
// itself was hardened to return `{ status: "vllm_unreachable",
// degraded: true }` instead of throwing).
app.get("/health", async () => {
  if (vllm) {
    return vllm.health();
  }
  return { status: "ok" };
});

// Pool + conversation stats (no auth — internal observability)
app.get("/stats", async () => {
  const poolStats = await getPoolStats();
  const convCount = await conversations.count();
  if (isVllm) {
    return {
      provider: "vllm",
      backend: config.vllm.baseUrl,
      model: config.vllm.defaultModel,
      conversations: convCount,
    };
  }
  const pool = getPool();
  return {
    provider: "claude",
    pool: poolStats ?? "valkey not connected",
    sessions: pool?.stats ?? "pool not initialized",
    conversations: convCount,
  };
});

// Auth middleware for /v1/* routes
app.addHook("onRequest", createAuthHook(config));

// Register routes
await modelsRoute(app, vllm);
await chatCompletionsRoute(app, config, conversations, vllm);

// GET /v1/conversations/:id — retrieve conversation history (debugging)
app.get<{ Params: { id: string } }>(
  "/v1/conversations/:id",
  async (request, reply) => {
    const { id } = request.params;
    const [metadata, history] = await Promise.all([
      conversations.getMetadata(id),
      conversations.getHistory(id),
    ]);

    if (!metadata) {
      return reply.code(404).send({
        error: {
          message: `Conversation ${id} not found`,
          type: "invalid_request_error",
        },
      });
    }

    return { id, ...metadata, messages: history };
  },
);

// Connect to Valkey (non-blocking — gateway works without it)
await connectValkey(config.valkeyUrl);

if (!isVllm) {
  // Claude provider: refresh OAuth token and warm session pool
  app.log.info("Checking OAuth token...");
  const tokenOk = await refreshIfExpired();
  if (!tokenOk) {
    app.log.warn("Token refresh failed — sessions may fail to authenticate");
  }
  startPeriodicRefresh();

  app.log.info(`Warming session pool (size=${config.poolSize})...`);
  await initPool(config.defaultModel, config.poolSize);
  app.log.info("Session pool ready — accepting requests");
} else {
  app.log.info("vLLM provider — skipping session pool and OAuth token refresh");
}

// Graceful shutdown
const shutdown = () => {
  if (!isVllm) {
    stopPeriodicRefresh();
    shutdownPool();
  }
  disconnectValkey();
  process.exit(0);
};
process.on("SIGTERM", shutdown);
process.on("SIGINT", shutdown);

try {
  await app.listen({ port: config.port, host: "0.0.0.0" });
  app.log.info(`Axon gateway listening on port ${config.port} (provider: ${config.provider})`);
} catch (err) {
  app.log.error(err);
  if (!isVllm) shutdownPool();
  await disconnectValkey();
  process.exit(1);
}
