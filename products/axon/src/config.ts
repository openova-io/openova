export interface Config {
  /** The model listener (/v1/* only) and the admin listener (/healthz, /health, /stats). */
  port: number;
  adminPort: number;
  /** Upstream model name to the kit's model value; a name outside the map is not served. */
  modelMap: Record<string, string>;
  /** Runs generating at once; runs waiting for tool results (beyond it the oldest is closed). */
  maxActive: number;
  maxParked: number;
  /** Requests waiting for an active slot (beyond it 429 at once), and the longest wait before 429. */
  queueMax: number;
  queueWaitMs: number;
  /** Longest wait for tool results before a parked run is closed. */
  parkTtlMs: number;
  /** Longest single model step before 504. */
  stepTimeoutMs: number;
}

/** The start values. A Sovereign changes them through the environment, never here. */
export const DEFAULTS = {
  port: 3000,
  adminPort: 3001,
  maxActive: 4,
  maxParked: 8,
  queueMax: 16,
  queueWaitMs: 30000,
  parkTtlMs: 600000,
  stepTimeoutMs: 1800000,
} as const;

const DEFAULT_MODEL_MAP = '{"sim-default":"claude-sonnet-4-6","sim-fast":"claude-haiku-4-5-20251001"}';

function positiveInt(name: string, value: string): number {
  const n = Number(value);
  if (!Number.isInteger(n) || n <= 0) throw new Error(`${name} must be a positive whole number`);
  return n;
}

function modelMap(value: string): Record<string, string> {
  let map: unknown;
  try {
    map = JSON.parse(value);
  } catch {
    throw new Error("AXON_MODEL_MAP must be a JSON object");
  }
  if (typeof map !== "object" || map === null || Array.isArray(map)) {
    throw new Error("AXON_MODEL_MAP must be a JSON object");
  }
  const entries = Object.entries(map);
  if (entries.length === 0 || entries.some(([, v]) => typeof v !== "string" || v === "")) {
    throw new Error("AXON_MODEL_MAP must map at least one name to a model");
  }
  return Object.fromEntries(entries) as Record<string, string>;
}

export function loadConfig(env: NodeJS.ProcessEnv = process.env): Config {
  const int = (name: string, fallback: number) => positiveInt(name, env[name] ?? String(fallback));
  const config: Config = {
    port: int("AXON_PORT", DEFAULTS.port),
    adminPort: int("AXON_ADMIN_PORT", DEFAULTS.adminPort),
    modelMap: modelMap(env.AXON_MODEL_MAP ?? DEFAULT_MODEL_MAP),
    maxActive: int("AXON_MAX_ACTIVE", DEFAULTS.maxActive),
    maxParked: int("AXON_MAX_PARKED", DEFAULTS.maxParked),
    queueMax: int("AXON_QUEUE_MAX", DEFAULTS.queueMax),
    queueWaitMs: int("AXON_QUEUE_WAIT_MS", DEFAULTS.queueWaitMs),
    parkTtlMs: int("AXON_PARK_TTL_MS", DEFAULTS.parkTtlMs),
    stepTimeoutMs: int("AXON_STEP_TIMEOUT_MS", DEFAULTS.stepTimeoutMs),
  };
  if (config.adminPort === config.port) throw new Error("AXON_ADMIN_PORT must differ from AXON_PORT");
  return config;
}
