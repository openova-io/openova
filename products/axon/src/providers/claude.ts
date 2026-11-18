import { query } from "@anthropic-ai/claude-agent-sdk";
import type { Options } from "@anthropic-ai/claude-agent-sdk";
import type { ChatMessage, ResponseFormat } from "../types/openai.js";

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

export interface ChatOptions {
  messages: ChatMessage[];
  model: string;
  responseFormat?: ResponseFormat;
}

const DEFAULT_SYSTEM_PROMPT = "You are a helpful assistant.";

/**
 * The system prompt of one run: the caller's system messages joined with a
 * blank line, or a default when there is none. A JSON response format is
 * asked for by instruction.
 */
export function formatSystemPrompt(opts: ChatOptions): string {
  const systemParts = opts.messages
    .filter((m) => m.role === "system")
    .map((m) => m.content ?? "");
  let systemPrompt = systemParts.length > 0 ? systemParts.join("\n\n") : DEFAULT_SYSTEM_PROMPT;

  if (opts.responseFormat?.type === "json_object") {
    systemPrompt += "\n\nYou must respond with valid JSON only. No markdown, no explanation, just a JSON object.";
  } else if (opts.responseFormat?.type === "json_schema" && opts.responseFormat.json_schema) {
    systemPrompt += `\n\nYou must respond with valid JSON matching this schema:\n${JSON.stringify(opts.responseFormat.json_schema, null, 2)}\nNo markdown, no explanation, just a JSON object matching the schema.`;
  }
  return systemPrompt;
}

/**
 * The prompt of one run: the content of the one user message when the
 * conversation is exactly that, otherwise the conversation as plain text.
 */
export function formatPrompt(messages: ChatMessage[]): string {
  const conversation = messages.filter((m) => m.role !== "system");
  if (conversation.length === 1 && conversation[0].role === "user") {
    return conversation[0].content ?? "";
  }
  const lines: string[] = ["<conversation>"];
  for (const msg of conversation) {
    const content = msg.content ?? "";
    if (msg.role === "tool") {
      lines.push(`Tool result (${msg.tool_call_id ?? msg.name ?? "unknown"}): ${content}`);
    } else {
      lines.push(`${msg.role === "user" ? "User" : "Assistant"}: ${content}`);
    }
  }
  lines.push("</conversation>\n\nRespond to the last User message.");
  return lines.join("\n");
}

/** The options of one upstream run: no built-in tools, no settings from disk, no stored session. */
function runOptions(opts: ChatOptions): Options {
  const env: Record<string, string | undefined> = { ...process.env };
  delete env.CLAUDECODE;
  return {
    model: resolveModel(opts.model),
    systemPrompt: formatSystemPrompt(opts),
    tools: [],
    settingSources: [],
    allowedTools: [],
    permissionMode: "dontAsk",
    persistSession: false,
    includePartialMessages: true,
    env,
  };
}

export async function chat(opts: ChatOptions): Promise<string> {
  const q = query({ prompt: formatPrompt(opts.messages), options: runOptions(opts) });

  let result = "";
  for await (const msg of q) {
    if (msg.type === "result") {
      if (msg.subtype !== "success") {
        throw new Error(`Claude error: ${msg.subtype} — ${JSON.stringify(msg.errors)}`);
      }
      result = msg.result;
      break;
    }
  }
  return result;
}

/** Stream the text of one run as the model generates it. */
export async function* chatStream(opts: ChatOptions): AsyncGenerator<string, void, undefined> {
  const q = query({ prompt: formatPrompt(opts.messages), options: runOptions(opts) });

  for await (const msg of q) {
    if (msg.type === "stream_event") {
      const ev = msg.event;
      if (ev.type === "content_block_delta" && ev.delta.type === "text_delta") {
        yield ev.delta.text;
      }
    }
    if (msg.type === "result") {
      if (msg.subtype !== "success") {
        throw new Error(`Claude error: ${msg.subtype} — ${JSON.stringify(msg.errors)}`);
      }
      break;
    }
  }
}
