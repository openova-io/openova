import type { FastifyInstance } from "fastify";
import type { ModelListResponse } from "../types/openai.js";
import type { VllmProvider } from "../providers/vllm.js";

const CLAUDE_MODELS: ModelListResponse = {
  object: "list",
  data: [
    {
      id: "claude-opus-4-6",
      object: "model",
      created: 1700000000,
      owned_by: "anthropic",
    },
    {
      id: "claude-sonnet-4-6",
      object: "model",
      created: 1700000000,
      owned_by: "anthropic",
    },
    {
      id: "claude-haiku-4-5",
      object: "model",
      created: 1700000000,
      owned_by: "anthropic",
    },
  ],
};

export async function modelsRoute(app: FastifyInstance, vllm?: VllmProvider): Promise<void> {
  app.get("/v1/models", async () => {
    if (vllm) {
      return vllm.models();
    }
    return CLAUDE_MODELS;
  });
}
