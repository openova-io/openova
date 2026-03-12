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
 *
 * Uses XML-style tags that Claude recognises as structured prompt sections
 * (not user-injected content). The <new_conversation/> marker tells the model
 * to treat everything that follows as a fresh request, preventing residual
 * session context from bleeding through.
 */
export function formatPrompt(opts: ChatOptions): string {
  const parts: string[] = [];

  // Session boundary — tells Claude to ignore prior turns in the reused session
  parts.push("<new_conversation/>\n");

  // System / persona context
  const systemParts = opts.messages
    .filter((m) => m.role === "system")
    .map((m) => typeof m.content === "string" ? m.content : "");
  if (systemParts.length > 0) {
    parts.push(`<context>\n${systemParts.join("\n")}\n</context>\n`);
  }

  // Response format constraint
  if (opts.responseFormat?.type === "json_object") {
    parts.push("<output_format>\nYou must respond with valid JSON only. No markdown, no explanation, just a JSON object.\n</output_format>\n");
  } else if (opts.responseFormat?.type === "json_schema" && opts.responseFormat.json_schema) {
    parts.push(`<output_format>\nYou must respond with valid JSON matching this schema:\n${JSON.stringify(opts.responseFormat.json_schema, null, 2)}\nNo markdown, no explanation, just a JSON object matching the schema.\n</output_format>\n`);
  }

  // Tool definitions
  if (opts.tools && opts.tools.length > 0) {
    parts.push("<tools>");
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
    parts.push("</tools>\n");
  }

  // Stop sequences hint
  if (opts.stop) {
    const seqs = Array.isArray(opts.stop) ? opts.stop : [opts.stop];
    if (seqs.length > 0) {
      parts.push(`Stop sequences: ${seqs.map(s => JSON.stringify(s)).join(", ")}\n`);
    }
  }

  // Format conversation
  const conversation = opts.messages.filter((m) => m.role !== "system");

  if (conversation.length === 1 && conversation[0].role === "user") {
    parts.push(typeof conversation[0].content === "string" ? conversation[0].content : "");
  } else if (conversation.length > 1) {
    parts.push("<conversation>");
    for (const msg of conversation) {
      const content = typeof msg.content === "string" ? msg.content : "";
      if (msg.role === "tool") {
        parts.push(`Tool result (${msg.tool_call_id ?? msg.name ?? "unknown"}): ${content}`);
      } else {
        const label = msg.role === "user" ? "User" : "Assistant";
        parts.push(`${label}: ${content}`);
      }
    }
    parts.push("</conversation>\n\nRespond to the last User message above based on the context and conversation provided.");
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

export async function chat(opts: ChatOptions): Promise<string> {
  const prompt = formatPrompt(opts);
  const session = await pool.acquire();

  try {
    await session.send(prompt);

    let resultText = "";
    for await (const msg of session.stream()) {
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

    pool.release(session);
    return resultText;
  } catch (err) {
    pool.discard(session);
    throw err;
  }
}

/**
 * Stream chat response using real token deltas from the Claude Agent SDK.
 *
 * With includePartialMessages: true on the session, the SDK emits
 * SDKPartialAssistantMessage events (type: "stream_event") carrying
 * BetaRawMessageStreamEvent payloads — the same content_block_delta
 * events as the native Anthropic streaming API. This gives true
 * token-by-token TTFT instead of waiting for the full response.
 */
export async function* chatStream(opts: ChatOptions): AsyncGenerator<string, void, undefined> {
  const prompt = formatPrompt(opts);
  const session = await pool.acquire();

  try {
    await session.send(prompt);

    for await (const msg of session.stream()) {
      // Real token delta from includePartialMessages: true
      if (msg.type === "stream_event") {
        const event = (msg as Record<string, unknown>).event as Record<string, unknown> | undefined;
        if (event?.type === "content_block_delta") {
          const delta = event.delta as Record<string, unknown> | undefined;
          if (delta?.type === "text_delta" && typeof delta.text === "string" && delta.text) {
            yield delta.text;
          }
        }
        continue;
      }
      if (msg.type === "result") break;
    }

    pool.release(session);
  } catch (err) {
    pool.discard(session);
    throw err;
  }
}
