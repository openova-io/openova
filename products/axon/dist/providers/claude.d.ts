import type { ChatMessage, ResponseFormat, Tool } from "../types/openai.js";
import { SessionPool } from "./session-pool.js";
export declare function resolveModel(model: string): string;
export declare function getPool(): SessionPool;
export declare function initPool(defaultModel: string, poolSize?: number): Promise<void>;
export declare function shutdownPool(): void;
export interface ChatOptions {
    messages: ChatMessage[];
    model: string;
    temperature?: number;
    maxTokens?: number;
    topP?: number;
    stop?: string | string[] | null;
    frequencyPenalty?: number;
    presencePenalty?: number;
    responseFormat?: ResponseFormat;
    tools?: Tool[];
}
/**
 * Format a self-contained prompt with full conversation context.
 *
 * Uses XML-style tags that Claude recognises as structured prompt sections
 * (not user-injected content). The <new_conversation/> marker tells the model
 * to treat everything that follows as a fresh request, preventing residual
 * session context from bleeding through.
 */
export declare function formatPrompt(opts: ChatOptions): string;
export declare function chat(opts: ChatOptions): Promise<string>;
/**
 * Stream chat response.
 *
 * The Claude Agent SDK yields complete assistant messages rather than token
 * deltas. We emit the full text as a single chunk so the client receives
 * content as soon as Claude finishes generating (typically 2-4s).
 * Real token-level streaming requires includePartialMessages support in the
 * SDK session — to be enabled once confirmed working in this runtime version.
 */
export interface ChatV1Options {
    messages: ChatMessage[];
    model: string;
    thinking?: {
        type: "adaptive";
    } | {
        type: "enabled";
        budget_tokens?: number;
    } | {
        type: "disabled";
    };
    effort?: "low" | "medium" | "high" | "max";
    maxTokens?: number;
    responseFormat?: ResponseFormat;
}
export declare function chatV1(opts: ChatV1Options): Promise<string>;
export declare function chatV1Stream(opts: ChatV1Options): AsyncGenerator<string, void, undefined>;
export declare function chatStream(opts: ChatOptions): AsyncGenerator<string, void, undefined>;
//# sourceMappingURL=claude.d.ts.map