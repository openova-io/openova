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
  app.log.info(`Provider: vllm (${config.vllm.baseUrl}), default model: ${config.vllm.defaultModel}`);
} else {
  app.log.info(`Provider: claude, default model: ${config.defaultModel}`);
}

// Health check (no auth) — proxy to vLLM backend when using vllm provider
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
