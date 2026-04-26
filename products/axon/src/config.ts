export type Provider = "claude" | "vllm";

export interface VllmConfig {
  baseUrl: string;
  apiKey: string;
  defaultModel: string;
}

export interface Config {
  port: number;
  apiKeys: string[];
  provider: Provider;
  defaultModel: string;
  poolSize: number;
  valkeyUrl: string;
  conversationTtl: number; // seconds, default 7 days
  vllm: VllmConfig;
}

export function loadConfig(): Config {
  const keys = process.env.AXON_API_KEYS ?? "";
  if (!keys) {
    throw new Error("AXON_API_KEYS must be set");
  }

  const provider = (process.env.AXON_PROVIDER ?? "claude") as Provider;
  if (provider !== "claude" && provider !== "vllm") {
    throw new Error(`AXON_PROVIDER must be "claude" or "vllm", got "${provider}"`);
  }

  const vllmDefaultModel = process.env.AXON_VLLM_DEFAULT_MODEL ?? "qwen3-coder";

  return {
    port: parseInt(process.env.AXON_PORT ?? "3000", 10),
    apiKeys: keys.split(",").map((k) => k.trim()),
    provider,
    defaultModel: provider === "vllm"
      ? vllmDefaultModel
      : (process.env.AXON_DEFAULT_MODEL ?? "claude-sonnet-4-6"),
    poolSize: parseInt(process.env.AXON_POOL_SIZE ?? "3", 10),
    valkeyUrl: process.env.AXON_VALKEY_URL ?? "redis://localhost:6379",
    conversationTtl: parseInt(process.env.AXON_CONVERSATION_TTL ?? "604800", 10),
    vllm: {
      baseUrl: process.env.AXON_VLLM_BASE_URL ?? "",
      apiKey: process.env.AXON_VLLM_API_KEY ?? "",
      defaultModel: vllmDefaultModel,
    },
  };
}
