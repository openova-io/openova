import type { VllmConfig } from "../config.js";
import type { ChatCompletionRequest, ChatMessage, ModelListResponse } from "../types/openai.js";

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

  private static readonly SYSTEM_MSG_MAX_CHARS = 4000;
  private static readonly ASSISTANT_MSG_MAX_CHARS = 800;
  private static readonly TOTAL_MSG_MAX_CHARS = 8000;

  private sanitizeMessages(messages: ChatMessage[]): ChatMessage[] {
    let seenSystem = false;
    const deduped: ChatMessage[] = [];
    for (const msg of messages) {
      if (msg.role === "system") {
        if (seenSystem) continue;
        seenSystem = true;
      }
      deduped.push(msg);
    }

    const trimmed = deduped.map((msg) => {
      if (!msg.content) return msg;
      let limit: number;
      if (msg.role === "system") limit = VllmProvider.SYSTEM_MSG_MAX_CHARS;
      else if (msg.role === "assistant") limit = VllmProvider.ASSISTANT_MSG_MAX_CHARS;
      else return msg;
      if (msg.content.length <= limit) return msg;
      const headSize = Math.floor(limit * 0.7);
      const tailSize = limit - headSize;
      return { ...msg, content: `${msg.content.slice(0, headSize)}\n\n[...condensed...]\n\n${msg.content.slice(-tailSize)}` };
    });

    const totalLimit = VllmProvider.TOTAL_MSG_MAX_CHARS;
    const totalChars = trimmed.reduce((sum, m) => sum + (m.content?.length ?? 0), 0);
    if (totalChars <= totalLimit) return trimmed;

    const systemMsgs = trimmed.filter((m) => m.role === "system");
    const nonSystemMsgs = trimmed.filter((m) => m.role !== "system");
    const sysChars = systemMsgs.reduce((sum, m) => sum + (m.content?.length ?? 0), 0);
    let budget = totalLimit - sysChars;
    const kept: ChatMessage[] = [];
    for (let i = nonSystemMsgs.length - 1; i >= 0; i--) {
      const len = nonSystemMsgs[i].content?.length ?? 0;
      if (budget - len < 0 && kept.length > 0) break;
      kept.unshift(nonSystemMsgs[i]);
      budget -= len;
    }
    return [...systemMsgs, ...kept];
  }

  private cleanPayload(body: ChatCompletionRequest, stream: boolean): Record<string, unknown> {
    const enableThinking = !!body.thinking;
    const payload: Record<string, unknown> = {
      ...body,
      model: this.resolveModel(body.model),
      messages: this.sanitizeMessages(body.messages),
      stream,
      chat_template_kwargs: { enable_thinking: enableThinking },
    };
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
