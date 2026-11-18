import type { FastifyInstance } from "fastify";
import type { ModelListResponse } from "../types/openai.js";

/** The served model names: the keys of the model map. */
export async function modelsRoute(app: FastifyInstance, modelMap: Record<string, string>): Promise<void> {
  const models: ModelListResponse = {
    object: "list",
    data: Object.keys(modelMap).map((id) => ({ id, object: "model", created: 1700000000, owned_by: "openova" })),
  };
  app.get("/v1/models", async () => models);
}
