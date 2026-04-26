import type { VllmConfig } from "../config.js";
import type { ChatCompletionRequest, ModelListResponse } from "../types/openai.js";

export class VllmProvider {
  private baseUrl: string;
  private apiKey: string;
  private defaultModel: string;

  constructor(config: VllmConfig) {
    if (!config.baseUrl) throw new Error("AXON_VLLM_BASE_URL must be set when provider=vllm");
    if (!config.apiKey) throw new Error("AXON_VLLM_API_KEY must be set when provider=vllm");
    this.baseUrl = config.baseUrl.replace(/\/+$/, "");
    this.apiKey = config.apiKey;
    this.defaultModel = config.defaultModel;
  }

  async chat(body: ChatCompletionRequest): Promise<Response> {
    const payload = { ...body, model: body.model ?? this.defaultModel, stream: false };
    delete (payload as Record<string, unknown>).conversation_id;
    delete (payload as Record<string, unknown>).thinking;
    delete (payload as Record<string, unknown>).effort;
    delete (payload as Record<string, unknown>).profile;

    return fetch(`${this.baseUrl}/v1/chat/completions`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${this.apiKey}`,
      },
      body: JSON.stringify(payload),
    });
  }

  async chatStream(body: ChatCompletionRequest): Promise<Response> {
    const payload = { ...body, model: body.model ?? this.defaultModel, stream: true };
    delete (payload as Record<string, unknown>).conversation_id;
    delete (payload as Record<string, unknown>).thinking;
    delete (payload as Record<string, unknown>).effort;
    delete (payload as Record<string, unknown>).profile;

    return fetch(`${this.baseUrl}/v1/chat/completions`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${this.apiKey}`,
      },
      body: JSON.stringify(payload),
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
