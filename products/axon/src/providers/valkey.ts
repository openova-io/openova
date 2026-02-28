import { Redis } from "ioredis";

let client: Redis | null = null;

export async function connectValkey(url: string): Promise<void> {
  client = new Redis(url, {
    maxRetriesPerRequest: 3,
    retryStrategy(times: number) {
      if (times > 5) return null;
      return Math.min(times * 200, 2000);
    },
    lazyConnect: true,
  });

  try {
    await client.connect();
    console.log("[valkey] connected");
  } catch (err) {
    console.warn("[valkey] connection failed, metrics disabled:", (err as Error).message);
    client = null;
  }
}

export function getValkey(): Redis | null {
  return client;
}

export async function disconnectValkey(): Promise<void> {
  if (client) {
    await client.quit();
    client = null;
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
