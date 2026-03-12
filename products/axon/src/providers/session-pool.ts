import {
  unstable_v2_createSession,
} from "@anthropic-ai/claude-agent-sdk";
import { trackPoolMetric } from "./valkey.js";

type SDKSession = ReturnType<typeof unstable_v2_createSession>;

interface PoolEntry {
  session: SDKSession;
  ready: boolean;
  readyPromise: Promise<void>;
  turns: number;
}

export interface SessionPoolOptions {
  poolSize: number;
  warmupModel: string;
  maxTurnsPerSession?: number; // recycle after this many turns (default 200)
}

export class SessionPool {
  private idle: PoolEntry[] = [];
  private busy: Set<PoolEntry> = new Set();
  private pending: PoolEntry[] = [];
  private opts: SessionPoolOptions;
  private env: Record<string, string | undefined>;
  private maxTurns: number;

  constructor(opts: SessionPoolOptions) {
    this.opts = opts;
    this.maxTurns = opts.maxTurnsPerSession ?? 200;
    // Clean env to avoid nested session detection
    this.env = { ...process.env };
    delete this.env.CLAUDECODE;
  }

  async warmup(): Promise<void> {
    console.log(
      `[pool] warming ${this.opts.poolSize} sessions with model=${this.opts.warmupModel}...`,
    );
    const promises: Promise<void>[] = [];
    for (let i = 0; i < this.opts.poolSize; i++) {
      promises.push(this.spawnSession());
    }
    // Wait for at least one session to be ready
    await Promise.race(promises);
    console.log("[pool] at least 1 session warm and ready");
  }

  private async spawnSession(): Promise<void> {
    const session = unstable_v2_createSession({
      model: this.opts.warmupModel,
      allowedTools: [],
      permissionMode: "dontAsk",
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      includePartialMessages: true as any,
      env: this.env,
    } as any);

    const entry: PoolEntry = {
      session,
      ready: false,
      readyPromise: null as unknown as Promise<void>,
      turns: 0,
    };

    this.pending.push(entry);

    entry.readyPromise = this.warmSession(entry);
    return entry.readyPromise;
  }

  private async warmSession(entry: PoolEntry): Promise<void> {
    try {
      await entry.session.send("Reply with exactly: READY");
      for await (const _msg of entry.session.stream()) {
        // Drain the warmup response
      }
      entry.ready = true;
      entry.turns = 1; // warmup counts as 1 turn
      // Move from pending to idle
      this.pending = this.pending.filter((e) => e !== entry);
      this.idle.push(entry);
      trackPoolMetric("warmup");
      console.log(`[pool] session warmed (idle: ${this.idle.length}, busy: ${this.busy.size})`);
    } catch (err) {
      console.error("[pool] session warmup failed:", err);
      this.pending = this.pending.filter((e) => e !== entry);
      // Retry with a new session
      this.spawnSession().catch(() => {});
    }
  }

  async acquire(): Promise<SDKSession> {
    // Try to grab an idle session
    const entry = this.idle.shift();
    if (entry) {
      this.busy.add(entry);
      trackPoolMetric("acquire");
      console.log(`[pool] acquired session (idle: ${this.idle.length}, busy: ${this.busy.size})`);
      return entry.session;
    }

    // No idle sessions — wait for a pending one
    trackPoolMetric("miss");
    console.log("[pool] no idle sessions, waiting for pending...");

    if (this.pending.length > 0) {
      await this.pending[0].readyPromise;
      return this.acquire(); // retry now that one moved to idle
    }

    // Pool fully busy — spawn an overflow session
    console.log("[pool] all sessions busy, spawning overflow session");
    await this.spawnSession();
    return this.acquire();
  }

  release(session: SDKSession): void {
    // Find the entry in busy set
    let entry: PoolEntry | undefined;
    for (const e of this.busy) {
      if (e.session === session) {
        entry = e;
        break;
      }
    }

    if (!entry) {
      // Session not tracked (already killed or unknown) — ignore
      return;
    }

    this.busy.delete(entry);
    entry.turns++;

    if (entry.turns >= this.maxTurns) {
      // Recycle: kill old, spawn replacement
      console.log(`[pool] recycling session after ${entry.turns} turns`);
      this.recycle(entry);
      return;
    }

    // Return to idle pool
    this.idle.push(entry);
    trackPoolMetric("release");
    console.log(`[pool] released session (idle: ${this.idle.length}, busy: ${this.busy.size})`);
  }

  discard(session: SDKSession): void {
    // Session errored — kill it and spawn replacement
    for (const e of this.busy) {
      if (e.session === session) {
        this.busy.delete(e);
        this.recycle(e);
        return;
      }
    }
  }

  private recycle(entry: PoolEntry): void {
    try {
      entry.session.close();
    } catch {
      // ignore
    }
    trackPoolMetric("recycle");
    this.spawnSession().catch(() => {});
  }

  get stats(): { idle: number; busy: number; pending: number } {
    return {
      idle: this.idle.length,
      busy: this.busy.size,
      pending: this.pending.length,
    };
  }

  shutdown(): void {
    for (const entry of this.idle) {
      try { entry.session.close(); } catch { /* ignore */ }
    }
    for (const entry of this.busy) {
      try { entry.session.close(); } catch { /* ignore */ }
    }
    for (const entry of this.pending) {
      try { entry.session.close(); } catch { /* ignore */ }
    }
    this.idle = [];
    this.busy.clear();
    this.pending = [];
  }
}
