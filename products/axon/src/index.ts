import Fastify, { type FastifyInstance } from "fastify";
import { pathToFileURL } from "node:url";
import { type Config, loadConfig } from "./config.js";
import { KitUpstream } from "./upstream.js";
import { Turns, type TurnManager } from "./turns.js";
import { modelsRoute } from "./routes/models.js";
import { chatCompletionsRoute } from "./routes/chat-completions.js";
import { Credential, guarded, type CredentialReport } from "./credential.js";

/** The longest a SIGTERM may take before the process exits anyway. */
export const SHUTDOWN_GRACE_MS = 10000;

/** model (AXON_PORT): /v1/* only, for meter pods. admin (AXON_ADMIN_PORT): /healthz, /health, /stats. */
export interface Servers { model: FastifyInstance; admin: FastifyInstance; listen(host: string): Promise<void> }

/** What /stats reads of the credential owner; without one the state is absent. */
export interface CredentialView { state(): CredentialReport }
const NO_OWNER: CredentialView = { state: () => ({ state: "absent" }) };

export async function servers(config: Config, turns: TurnManager, logger = false, credential = NO_OWNER): Promise<Servers> {
  const model = Fastify({ logger });
  const admin = Fastify({ logger });
  let up = false;

  // Liveness: 200 while the event loop runs, whatever the upstream state. Issue #2203: without
  // the liveness / readiness split an upstream TLS/DNS blip caused 97 restarts in 6h08m.
  admin.get("/healthz", async () => ({ status: "alive" }));

  // Readiness — 200 once both listeners are up, whatever the credential state,
  // so that callers receive the precise error body and not a connection error.
  admin.get("/health", async (_request, reply) => (up ? { status: "ok" } : reply.code(503).send({ status: "starting" })));

  // No authentication: the network policy admits catalyst-api pods to this port and nothing else.
  admin.get("/stats", async () => ({
    credential: credential.state(),
    turns: { ...turns.stats(), maxActive: config.maxActive, maxParked: config.maxParked },
    counters: turns.counters(),
  }));

  // No caller keys: the network policy in front of Axon is the fence.
  await modelsRoute(model, config.modelMap);
  await chatCompletionsRoute(model, turns);

  return {
    model,
    admin,
    async listen(host: string) {
      await model.listen({ port: config.port, host });
      await admin.listen({ port: config.adminPort, host });
      up = true;
    },
  };
}

/** SIGTERM: stop accepting, close every chain (open requests get their error), exit; after graceMs exit anyway. */
export async function stop(
  s: Pick<Servers, "model" | "admin">,
  turns: TurnManager,
  exit: (code: number) => void,
  graceMs = SHUTDOWN_GRACE_MS,
): Promise<void> {
  let exited = false;
  const leave = (code: number) => {
    if (exited) return;
    exited = true;
    clearTimeout(timer);
    exit(code);
  };
  const timer = setTimeout(() => leave(1), graceMs);
  try {
    const closing = Promise.all([s.model.close(), s.admin.close()]);
    await turns.shutdown();
    await closing;
  } finally {
    leave(0);
  }
}

async function main(): Promise<void> {
  const config = loadConfig();
  // The one refresher of the credential; each run gets the access token alone.
  const credential = new Credential({ ...config.credential, log: (line) => console.log(`[credential] ${line}`) });
  credential.load();
  const turns = new Turns({ ...config, upstream: new KitUpstream(), accessToken: () => credential.accessToken() });
  const s = await servers(config, guarded(turns, credential), true, credential);
  const log = s.model.log;
  log.info(`Models: ${Object.keys(config.modelMap).join(", ")}`);
  void credential.start();

  let stopping = false;
  const shutdown = () => {
    if (stopping) return;
    stopping = true;
    credential.stop();
    void stop(s, turns, (code) => process.exit(code));
  };
  process.on("SIGTERM", shutdown);
  process.on("SIGINT", shutdown);

  try {
    await s.listen("0.0.0.0");
    log.info(`Axon listening on port ${config.port} (models) and ${config.adminPort} (admin)`);
  } catch (err) {
    log.error(err);
    process.exit(1);
  }
}

// Run only as the entry point: the tests import servers() and stop().
if (process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href) await main();
