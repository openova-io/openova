export interface Config {
  port: number;
  apiKeys: string[];
  defaultModel: string;
}

export function loadConfig(): Config {
  const keys = process.env.AXON_API_KEYS ?? "";
  if (!keys) {
    throw new Error("AXON_API_KEYS must be set");
  }

  return {
    port: parseInt(process.env.AXON_PORT ?? "3000", 10),
    apiKeys: keys.split(",").map((k) => k.trim()),
    defaultModel: process.env.AXON_DEFAULT_MODEL ?? "claude-sonnet-4-6",
  };
}
