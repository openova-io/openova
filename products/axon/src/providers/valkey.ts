import { Redis } from "ioredis";

let client: Redis | null = null;
let valkeyUrl: string = "";

export async function connectValkey(url: string): Promise<void> {
  valkeyUrl = url;
  client = createValkeyClient(url);
}

function createValkeyClient(url: string): Redis {
  const c = new Redis(url, {
    maxRetriesPerRequest: 3, // fail commands fast; background reconnect handles recovery
    retryStrategy(times: number) {
      // Never give up — keep reconnecting with capped exponential backoff (max 30s)
      const delay = Math.min(times * 500, 30_000);
      console.log(`[valkey] reconnect attempt ${times}, next in ${delay}ms`);
      return delay;
    },
    lazyConnect: true,
  });

  c.on("connect", () => console.log("[valkey] connected"));
  c.on("error", (err: Error) => {
    // Log but don't crash — retryStrategy handles reconnection
    if (!err.message.includes("ECONNREFUSED") && !err.message.includes("Connection is closed")) {
      console.warn("[valkey] error:", err.message);
    }
  });
  c.on("end", () => {
    // ioredis stopped retrying (retryStrategy returned null) — restart the client
    console.warn("[valkey] connection ended permanently, restarting client...");
    client = null;
    setTimeout(() => {
      if (valkeyUrl) {
        client = createValkeyClient(valkeyUrl);
        client.connect().catch(() => {});
      }
    }, 5_000);
  });

  c.connect().catch((err: Error) => {
    console.warn("[valkey] initial connect failed:", err.message);
  });

  return c;
}

export function getValkey(): Redis | null {
  // Return null if client is permanently closed — callers treat null as "no persistence"
  if (client && (client.status === "end" || client.status === "close")) {
    return null;
  }
  return client;
}

export async function disconnectValkey(): Promise<void> {
  if (client) {
    try {
      await client.quit();
    } catch {
      // ignore on shutdown
    }
    client = null;
    valkeyUrl = "";
  }
}

// Pool metrics — tracks warm session stats in Valkey
const KEY_PREFIX = "axon:pool:";

export async function trackPoolMetric(
  event: "acquire" | "warmup" | "miss" | "request" | "release" | "recycle",
  latencyMs?: number,
): Promise<void> {
  if (!client) return;
  try {
    const multi = client.multi();
    multi.hincrby(`${KEY_PREFIX}counters`, event, 1);
    if (latencyMs !== undefined) {
      multi.lpush(`${KEY_PREFIX}latency:${event}`, String(Math.round(latencyMs)));
      multi.ltrim(`${KEY_PREFIX}latency:${event}`, 0, 99); // keep last 100
    }
    multi.set(`${KEY_PREFIX}last_${event}`, new Date().toISOString());
    await multi.exec();
  } catch {
    // non-critical, ignore
  }
}

export async function getPoolStats(): Promise<Record<string, unknown> | null> {
  if (!client) return null;
  try {
    const counters = await client.hgetall(`${KEY_PREFIX}counters`);
    const latencies = await client.lrange(`${KEY_PREFIX}latency:request`, 0, -1);
    const nums = latencies.map(Number).filter((n: number) => !isNaN(n));
    const avgLatency =
      nums.length > 0 ? nums.reduce((a: number, b: number) => a + b, 0) / nums.length : 0;
    return { counters, avgLatencyMs: Math.round(avgLatency), samples: nums.length };
  } catch {
    return null;
  }
}
