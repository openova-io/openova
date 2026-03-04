import type { ChatMessage, ResponseFormat, Tool } from "../types/openai.js";
import { SessionPool } from "./session-pool.js";

let pool: SessionPool;

const MODEL_MAP: Record<string, string> = {
  "gpt-4o": "claude-sonnet-4-6",
  "gpt-4o-mini": "claude-haiku-4-5-20251001",
  "gpt-4": "claude-opus-4-6",
  "gpt-4-turbo": "claude-sonnet-4-6",
  "gpt-3.5-turbo": "claude-haiku-4-5-20251001",
  "claude-haiku-4-5": "claude-haiku-4-5-20251001",
};

export function resolveModel(model: string): string {
  return MODEL_MAP[model] ?? model;
}

export function getPool(): SessionPool {
  return pool;
}

export async function initPool(defaultModel: string, poolSize = 3): Promise<void> {
  pool = new SessionPool({ poolSize, warmupModel: defaultModel });
  await pool.warmup();
}

export function shutdownPool(): void {
  pool?.shutdown();
}

export interface ChatOptions {
  messages: ChatMessage[];
  model: string;
  temperature?: number;
  maxTokens?: number;
  topP?: number;
  stop?: string | string[] | null;
  frequencyPenalty?: number;
  presencePenalty?: number;
  responseFormat?: ResponseFormat;
  tools?: Tool[];
}

/**
 * Format a self-contained prompt with full conversation context.
 * Includes response_format instructions and tool definitions when provided.
 */
export function formatPrompt(opts: ChatOptions): string {
  const parts: string[] = [];

  // System instructions from messages
  const systemParts = opts.messages
    .filter((m) => m.role === "system")
    .map((m) => typeof m.content === "string" ? m.content : "");
  if (systemParts.length > 0) {
    parts.push(`[System instructions]\n${systemParts.join("\n")}\n[End system instructions]\n`);
  }

  // Response format constraint
  if (opts.responseFormat?.type === "json_object") {
    parts.push("[Output format]\nYou must respond with valid JSON only. No markdown, no explanation, just a JSON object.\n[End output format]\n");
  } else if (opts.responseFormat?.type === "json_schema" && opts.responseFormat.json_schema) {
    parts.push(`[Output format]\nYou must respond with valid JSON matching this schema:\n${JSON.stringify(opts.responseFormat.json_schema, null, 2)}\nNo markdown, no explanation, just a JSON object matching the schema.\n[End output format]\n`);
  }

  // Tool definitions
  if (opts.tools && opts.tools.length > 0) {
    parts.push("[Available tools]");
    for (const tool of opts.tools) {
      parts.push(`Function: ${tool.function.name}`);
      if (tool.function.description) {
        parts.push(`Description: ${tool.function.description}`);
      }
      if (tool.function.parameters) {
        parts.push(`Parameters: ${JSON.stringify(tool.function.parameters)}`);
      }
      parts.push("");
    }
    parts.push("If you need to call a tool, respond with a JSON object: {\"tool_calls\": [{\"function\": {\"name\": \"<name>\", \"arguments\": \"<json_string>\"}}]}");
    parts.push("[End available tools]\n");
  }

  // Stop sequences hint
  if (opts.stop) {
    const seqs = Array.isArray(opts.stop) ? opts.stop : [opts.stop];
    if (seqs.length > 0) {
      parts.push(`[Stop sequences: ${seqs.map(s => JSON.stringify(s)).join(", ")}]\n`);
    }
  }

  // Format conversation
  const conversation = opts.messages.filter((m) => m.role !== "system");

  if (conversation.length === 1 && conversation[0].role === "user") {
    parts.push(typeof conversation[0].content === "string" ? conversation[0].content : "");
  } else if (conversation.length > 1) {
    parts.push("Respond based ONLY on the conversation below. Do not reference anything outside it.\n");
    for (const msg of conversation) {
      const content = typeof msg.content === "string" ? msg.content : "";
      if (msg.role === "tool") {
        parts.push(`Tool result (${msg.tool_call_id ?? msg.name ?? "unknown"}): ${content}`);
      } else {
        const label = msg.role === "user" ? "User" : "Assistant";
        parts.push(`${label}: ${content}`);
      }
    }
  }

  return parts.join("\n");
}

function extractText(msg: Record<string, unknown>): string {
  const message = msg.message as Record<string, unknown> | undefined;
  if (!message) return "";
  const content = message.content as Array<Record<string, unknown>> | undefined;
  if (!Array.isArray(content)) return "";
  return content
    .filter((block) => block.type === "text")
    .map((block) => block.text as string)
    .join("");
}

export interface ChatTrace {
  formatPromptMs: number;
  acquireMs: number;
  sendMs: number;
  firstMsgMs: number;
  streamMs: number;
  releaseMs: number;
  totalMs: number;
}

export async function chat(opts: ChatOptions): Promise<{ text: string; trace: ChatTrace }> {
  const t0 = performance.now();

  const prompt = formatPrompt(opts);
  const tFormat = performance.now();

  const session = await pool.acquire();
  const tAcquire = performance.now();

  try {
    await session.send(prompt);
    const tSend = performance.now();

    let resultText = "";
    let tFirstMsg = 0;
    for await (const msg of session.stream()) {
      if (!tFirstMsg) tFirstMsg = performance.now();
      if (msg.type === "result") {
        if (msg.subtype === "success") {
          resultText = msg.result;
        } else {
          const errMsg = msg as Record<string, unknown>;
          throw new Error(
            `Claude error: ${errMsg.subtype} — ${JSON.stringify(errMsg.errors ?? "no details")}`,
          );
        }
        break;
      }
    }
    const tStream = performance.now();

    pool.release(session);
    const tRelease = performance.now();

    const trace: ChatTrace = {
      formatPromptMs: Math.round(tFormat - t0),
      acquireMs: Math.round(tAcquire - tFormat),
      sendMs: Math.round(tSend - tAcquire),
      firstMsgMs: Math.round((tFirstMsg || tStream) - tSend),
      streamMs: Math.round(tStream - (tFirstMsg || tSend)),
      releaseMs: Math.round(tRelease - tStream),
      totalMs: Math.round(tRelease - t0),
    };

    return { text: resultText, trace };
  } catch (err) {
    pool.discard(session);
    throw err;
  }
}

export async function* chatStream(opts: ChatOptions): AsyncGenerator<string, void, undefined> {
  const prompt = formatPrompt(opts);
  const session = await pool.acquire();

  try {
    await session.send(prompt);

    for await (const msg of session.stream()) {
      if (msg.type === "assistant") {
        const text = extractText(msg as unknown as Record<string, unknown>);
        if (text) yield text;
      }
      if (msg.type === "result") break;
    }

    pool.release(session);
  } catch (err) {
    pool.discard(session);
    throw err;
  }
}
