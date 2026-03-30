import { unstable_v2_createSession, } from "@anthropic-ai/claude-agent-sdk";
import { trackPoolMetric } from "./valkey.js";
const MAX_IDLE_MS = 5 * 60 * 1000; // recycle sessions idle more than 5 minutes
export class SessionPool {
    idle = [];
    busy = new Set();
    pending = [];
    opts;
    env;
    maxTurns;
    constructor(opts) {
        this.opts = opts;
        this.maxTurns = opts.maxTurnsPerSession ?? 200;
        // Clean env to avoid nested session detection
        this.env = { ...process.env };
        delete this.env.CLAUDECODE;
    }
    async warmup() {
        console.log(`[pool] warming ${this.opts.poolSize} sessions with model=${this.opts.warmupModel}...`);
        const promises = [];
        for (let i = 0; i < this.opts.poolSize; i++) {
            promises.push(this.spawnSession());
        }
        // Wait for at least one session to be ready
        await Promise.race(promises);
        console.log("[pool] at least 1 session warm and ready");
    }
    async spawnSession() {
        const session = unstable_v2_createSession({
            model: this.opts.warmupModel,
            allowedTools: [],
            permissionMode: "dontAsk",
            env: this.env,
        });
        const entry = {
            session,
            ready: false,
            readyPromise: null,
            turns: 0,
            lastUsed: Date.now(),
        };
        this.pending.push(entry);
        entry.readyPromise = this.warmSession(entry);
        return entry.readyPromise;
    }
    async warmSession(entry) {
        try {
            await entry.session.send("Reply with exactly: READY");
            for await (const _msg of entry.session.stream()) {
                // Drain the warmup response
            }
            entry.ready = true;
            entry.turns = 1; // warmup counts as 1 turn
            entry.lastUsed = Date.now();
            // Move from pending to idle
            this.pending = this.pending.filter((e) => e !== entry);
            this.idle.push(entry);
            trackPoolMetric("warmup");
            console.log(`[pool] session warmed (idle: ${this.idle.length}, busy: ${this.busy.size})`);
        }
        catch (err) {
            console.error("[pool] session warmup failed:", err);
            this.pending = this.pending.filter((e) => e !== entry);
            // Retry with a new session
            this.spawnSession().catch(() => { });
        }
    }
    async acquire() {
        // Try to grab an idle session — skip any that have been idle too long
        while (this.idle.length > 0) {
            const entry = this.idle.shift();
            if (Date.now() - entry.lastUsed > MAX_IDLE_MS) {
                console.log(`[pool] evicting stale idle session (idle ${Math.round((Date.now() - entry.lastUsed) / 1000)}s)`);
                this.recycle(entry);
                continue; // try next idle entry
            }
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
    release(session) {
        // Find the entry in busy set
        let entry;
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
        entry.lastUsed = Date.now();
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
    discard(session) {
        // Session errored — kill it and spawn replacement
        for (const e of this.busy) {
            if (e.session === session) {
                this.busy.delete(e);
                this.recycle(e);
                return;
            }
        }
    }
    recycle(entry) {
        try {
            entry.session.close();
        }
        catch {
            // ignore
        }
        trackPoolMetric("recycle");
        this.spawnSession().catch(() => { });
    }
    get stats() {
        return {
            idle: this.idle.length,
            busy: this.busy.size,
            pending: this.pending.length,
        };
    }
    shutdown() {
        for (const entry of this.idle) {
            try {
                entry.session.close();
            }
            catch { /* ignore */ }
        }
        for (const entry of this.busy) {
            try {
                entry.session.close();
            }
            catch { /* ignore */ }
        }
        for (const entry of this.pending) {
            try {
                entry.session.close();
            }
            catch { /* ignore */ }
        }
        this.idle = [];
        this.busy.clear();
        this.pending = [];
    }
}
//# sourceMappingURL=session-pool.js.map