import Fastify from "fastify";
import { loadConfig } from "./config.js";
import { createAuthHook } from "./middleware/auth.js";
import { modelsRoute } from "./routes/models.js";
import { chatCompletionsRoute } from "./routes/chat-completions.js";
import { initPool, shutdownPool, getPool } from "./providers/claude.js";
import { connectValkey, disconnectValkey, getPoolStats } from "./providers/valkey.js";
import { ConversationStore } from "./providers/conversation.js";

const config = loadConfig();
const app = Fastify({ logger: true });
const conversations = new ConversationStore(config.conversationTtl);

// Health check (no auth)
app.get("/health", async () => ({ status: "ok" }));

// Pool + conversation stats (no auth — internal observability)
app.get("/stats", async () => {
  const poolStats = await getPoolStats();
  const pool = getPool();
  const convCount = await conversations.count();
  return {
    pool: poolStats ?? "valkey not connected",
    sessions: pool?.stats ?? "pool not initialized",
    conversations: convCount,
  };
});

// Auth middleware for /v1/* routes
app.addHook("onRequest", createAuthHook(config));

// Register routes
await modelsRoute(app);
await chatCompletionsRoute(app, config, conversations);

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

// Pre-warm session pool before accepting traffic
app.log.info(`Warming session pool (size=${config.poolSize})...`);
await initPool(config.defaultModel, config.poolSize);
app.log.info("Session pool ready — accepting requests");

// Graceful shutdown
const shutdown = () => {
  shutdownPool();
  disconnectValkey();
  process.exit(0);
};
process.on("SIGTERM", shutdown);
process.on("SIGINT", shutdown);

try {
  await app.listen({ port: config.port, host: "0.0.0.0" });
  app.log.info(`Axon gateway listening on port ${config.port}`);
} catch (err) {
  app.log.error(err);
  shutdownPool();
  await disconnectValkey();
  process.exit(1);
}
