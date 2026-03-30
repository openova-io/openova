import type { ChatMessage } from "../types/openai.js";
export interface ConversationMetadata {
    model: string;
    created: string;
    updated: string;
    message_count: number;
    api_key_hash: string;
}
export declare class ConversationStore {
    private ttlSeconds;
    constructor(ttlSeconds: number);
    /**
     * Create a new conversation with initial messages.
     * Returns the conversation ID.
     */
    create(messages: ChatMessage[], model: string, apiKey: string): Promise<string>;
    /**
     * Append a message to an existing conversation.
     * Refreshes TTL.
     */
    append(convId: string, message: ChatMessage): Promise<void>;
    /**
     * Get full message history for a conversation.
     * Returns null if conversation doesn't exist.
     */
    getHistory(convId: string): Promise<ChatMessage[] | null>;
    /**
     * Get conversation metadata.
     */
    getMetadata(convId: string): Promise<ConversationMetadata | null>;
    /**
     * Count active conversations (scan for keys).
     * Returns approximate count for /stats.
     */
    count(): Promise<number>;
}
//# sourceMappingURL=conversation.d.ts.map