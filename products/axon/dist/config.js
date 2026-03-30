export function loadConfig() {
    const keys = process.env.AXON_API_KEYS ?? "";
    if (!keys) {
        throw new Error("AXON_API_KEYS must be set");
    }
    return {
        port: parseInt(process.env.AXON_PORT ?? "3000", 10),
        apiKeys: keys.split(",").map((k) => k.trim()),
        defaultModel: process.env.AXON_DEFAULT_MODEL ?? "claude-sonnet-4-6",
        poolSize: parseInt(process.env.AXON_POOL_SIZE ?? "3", 10),
        valkeyUrl: process.env.AXON_VALKEY_URL ?? "redis://localhost:6379",
        conversationTtl: parseInt(process.env.AXON_CONVERSATION_TTL ?? "604800", 10), // 7 days
    };
}
//# sourceMappingURL=config.js.map