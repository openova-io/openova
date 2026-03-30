import { v4 as uuidv4 } from "uuid";
import { createHash } from "node:crypto";
import { getValkey } from "./valkey.js";
const KEY_PREFIX = "axon:conv:";
export class ConversationStore {
    ttlSeconds;
    constructor(ttlSeconds) {
        this.ttlSeconds = ttlSeconds;
    }
    /**
     * Create a new conversation with initial messages.
     * Returns the conversation ID.
     */
    async create(messages, model, apiKey) {
        const client = getValkey();
        if (!client) {
            // No Valkey — return a transient ID (conversation won't persist)
            return `conv-${uuidv4()}`;
        }
        const id = `conv-${uuidv4()}`;
        const now = new Date().toISOString();
        const keyHash = createHash("sha256").update(apiKey).digest("hex").slice(0, 16);
        const metaKey = `${KEY_PREFIX}${id}`;
        const msgsKey = `${KEY_PREFIX}${id}:messages`;
        const multi = client.multi();
        // Store metadata
        multi.hset(metaKey, {
            model,
            created: now,
            updated: now,
            message_count: String(messages.length),
            api_key_hash: keyHash,
        });
        // Store messages
        for (const msg of messages) {
            multi.rpush(msgsKey, JSON.stringify(msg));
        }
        // Set TTL on both keys
        multi.expire(metaKey, this.ttlSeconds);
        multi.expire(msgsKey, this.ttlSeconds);
        await multi.exec();
        return id;
    }
    /**
     * Append a message to an existing conversation.
     * Refreshes TTL.
     */
    async append(convId, message) {
        const client = getValkey();
        if (!client)
            return;
        const metaKey = `${KEY_PREFIX}${convId}`;
        const msgsKey = `${KEY_PREFIX}${convId}:messages`;
        const multi = client.multi();
        multi.rpush(msgsKey, JSON.stringify(message));
        multi.hset(metaKey, "updated", new Date().toISOString());
        multi.hincrby(metaKey, "message_count", 1);
        // Refresh TTL
        multi.expire(metaKey, this.ttlSeconds);
        multi.expire(msgsKey, this.ttlSeconds);
        await multi.exec();
    }
    /**
     * Get full message history for a conversation.
     * Returns null if conversation doesn't exist.
     */
    async getHistory(convId) {
        const client = getValkey();
        if (!client)
            return null;
        const msgsKey = `${KEY_PREFIX}${convId}:messages`;
        const raw = await client.lrange(msgsKey, 0, -1);
        if (raw.length === 0) {
            // Check if conversation exists at all
            const exists = await client.exists(`${KEY_PREFIX}${convId}`);
            if (!exists)
                return null;
            return []; // exists but empty (shouldn't happen in practice)
        }
        return raw.map((s) => JSON.parse(s));
    }
    /**
     * Get conversation metadata.
     */
    async getMetadata(convId) {
        const client = getValkey();
        if (!client)
            return null;
        const metaKey = `${KEY_PREFIX}${convId}`;
        const data = await client.hgetall(metaKey);
        if (!data || Object.keys(data).length === 0)
            return null;
        return {
            model: data.model,
            created: data.created,
            updated: data.updated,
            message_count: parseInt(data.message_count, 10),
            api_key_hash: data.api_key_hash,
        };
    }
    /**
     * Count active conversations (scan for keys).
     * Returns approximate count for /stats.
     */
    async count() {
        const client = getValkey();
        if (!client)
            return 0;
        let count = 0;
        let cursor = "0";
        do {
            const [next, keys] = await client.scan(cursor, "MATCH", `${KEY_PREFIX}conv-*`, "COUNT", 100);
            cursor = next;
            // Only count metadata keys (not :messages keys)
            count += keys.filter((k) => !k.includes(":messages")).length;
        } while (cursor !== "0");
        return count;
    }
}
//# sourceMappingURL=conversation.js.map