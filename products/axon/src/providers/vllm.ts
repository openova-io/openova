import type { VllmConfig } from "../config.js";
import type { ChatCompletionRequest, ModelListResponse } from "../types/openai.js";

export class VllmProvider {
  private baseUrl: string;
  private apiKey: string;
  private defaultModel: string;
  private availableModels: Set<string> = new Set();

  constructor(config: VllmConfig) {
    if (!config.baseUrl) throw new Error("AXON_VLLM_BASE_URL must be set when provider=vllm");
    if (!config.apiKey) throw new Error("AXON_VLLM_API_KEY must be set when provider=vllm");
    this.baseUrl = config.baseUrl.replace(/\/+$/, "");
    this.apiKey = config.apiKey;
    this.defaultModel = config.defaultModel;
  }

  async init(): Promise<void> {
    try {
      const list = await this.models();
      for (const m of list.data) this.availableModels.add(m.id);
      console.log(`[vllm] available models: ${[...this.availableModels].join(", ")}`);
    } catch (err) {
      console.warn("[vllm] could not fetch model list at init:", err);
    }
  }

  private resolveModel(requested?: string): string {
    if (!requested) return this.defaultModel;
    if (this.availableModels.size === 0) return this.defaultModel;
    if (this.availableModels.has(requested)) return requested;
    return this.defaultModel;
  }

  private cleanPayload(body: ChatCompletionRequest, stream: boolean): Record<string, unknown> {
    const payload: Record<string, unknown> = { ...body, model: this.resolveModel(body.model), stream };
    delete payload.conversation_id;
    delete payload.thinking;
    delete payload.effort;
    delete payload.profile;
    return payload;
  }

  async chat(body: ChatCompletionRequest): Promise<Response> {
    return fetch(`${this.baseUrl}/v1/chat/completions`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${this.apiKey}`,
      },
      body: JSON.stringify(this.cleanPayload(body, false)),
    });
  }

  async chatStream(body: ChatCompletionRequest): Promise<Response> {
    return fetch(`${this.baseUrl}/v1/chat/completions`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${this.apiKey}`,
      },
      body: JSON.stringify(this.cleanPayload(body, true)),
    });
  }

  async models(): Promise<ModelListResponse> {
    const res = await fetch(`${this.baseUrl}/v1/models`, {
      headers: { Authorization: `Bearer ${this.apiKey}` },
    });
    if (!res.ok) {
      throw new Error(`vLLM /v1/models returned ${res.status}: ${await res.text()}`);
    }
    return res.json() as Promise<ModelListResponse>;
  }

  async health(): Promise<{ status: string }> {
    const res = await fetch(`${this.baseUrl}/health`, {
      headers: { Authorization: `Bearer ${this.apiKey}` },
    });
    if (!res.ok) {
      return { status: `vllm_unhealthy_${res.status}` };
    }
    return { status: "ok" };
  }
}
