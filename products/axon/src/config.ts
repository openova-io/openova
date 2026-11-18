export interface Config {
  port: number;
  /** Upstream model name to the kit's model value; a name outside the map is not served. */
  modelMap: Record<string, string>;
  /** Longest wait for tool results before a parked run is closed. */
  parkTtlMs: number;
}

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
  return {
    port: positiveInt("AXON_PORT", env.AXON_PORT ?? "3000"),
    modelMap: modelMap(env.AXON_MODEL_MAP ?? DEFAULT_MODEL_MAP),
    parkTtlMs: positiveInt("AXON_PARK_TTL_MS", env.AXON_PARK_TTL_MS ?? "600000"),
  };
}
