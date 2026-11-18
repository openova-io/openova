import Fastify from "fastify";
import { loadConfig } from "./config.js";
import { createAuthHook } from "./middleware/auth.js";
import { modelsRoute } from "./routes/models.js";
import { chatCompletionsRoute } from "./routes/chat-completions.js";
import { refreshIfExpired, startPeriodicRefresh, stopPeriodicRefresh } from "./providers/token-refresh.js";

const config = loadConfig();
const app = Fastify({ logger: true });

app.log.info(`Provider: claude, default model: ${config.defaultModel}`);

// Liveness — process-death detection. ALWAYS returns 200 when the
// Fastify event loop is alive, independent of upstream state. Kubelet
// kills the pod ONLY when this returns non-200, which happens only when
// the JS event loop is stuck. Issue #2203 — without the liveness /
// readiness split, an upstream TLS/DNS blip caused 97 restarts in 6h08m
// on one Sovereign.
app.get("/healthz", async () => ({ status: "alive" }));

// Readiness — kubelet uses this to gate traffic via the Service endpoint.
app.get("/health", async () => ({ status: "ok" }));

// Auth middleware for /v1/* routes
app.addHook("onRequest", createAuthHook(config));

// Register routes
await modelsRoute(app);
await chatCompletionsRoute(app, config);

// Refresh the OAuth token; each request starts its own kit run
app.log.info("Checking OAuth token...");
const tokenOk = await refreshIfExpired();
if (!tokenOk) {
  app.log.warn("Token refresh failed — requests may fail to authenticate");
}
startPeriodicRefresh();

// Graceful shutdown
const shutdown = () => {
  stopPeriodicRefresh();
  process.exit(0);
};
process.on("SIGTERM", shutdown);
process.on("SIGINT", shutdown);

try {
  await app.listen({ port: config.port, host: "0.0.0.0" });
  app.log.info(`Axon gateway listening on port ${config.port}`);
} catch (err) {
  app.log.error(err);
  process.exit(1);
}
