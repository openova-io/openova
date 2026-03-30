import { unstable_v2_createSession } from "@anthropic-ai/claude-agent-sdk";
type SDKSession = ReturnType<typeof unstable_v2_createSession>;
export interface SessionPoolOptions {
    poolSize: number;
    warmupModel: string;
    maxTurnsPerSession?: number;
}
export declare class SessionPool {
    private idle;
    private busy;
    private pending;
    private opts;
    private env;
    private maxTurns;
    constructor(opts: SessionPoolOptions);
    warmup(): Promise<void>;
    private spawnSession;
    private warmSession;
    acquire(): Promise<SDKSession>;
    release(session: SDKSession): void;
    discard(session: SDKSession): void;
    private recycle;
    get stats(): {
        idle: number;
        busy: number;
        pending: number;
    };
    shutdown(): void;
}
export {};
//# sourceMappingURL=session-pool.d.ts.map