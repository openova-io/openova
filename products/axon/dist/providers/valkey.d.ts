import { Redis } from "ioredis";
export declare function connectValkey(url: string): Promise<void>;
export declare function getValkey(): Redis | null;
export declare function disconnectValkey(): Promise<void>;
export declare function trackPoolMetric(event: "acquire" | "warmup" | "miss" | "request" | "release" | "recycle", latencyMs?: number): Promise<void>;
export declare function getPoolStats(): Promise<Record<string, unknown> | null>;
//# sourceMappingURL=valkey.d.ts.map