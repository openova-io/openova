import { query } from "@anthropic-ai/claude-agent-sdk";
import { SessionPool } from "./session-pool.js";
let pool;
const MODEL_MAP = {
    "gpt-4o": "claude-sonnet-4-6",
    "gpt-4o-mini": "claude-haiku-4-5-20251001",
    "gpt-4": "claude-opus-4-6",
    "gpt-4-turbo": "claude-sonnet-4-6",
    "gpt-3.5-turbo": "claude-haiku-4-5-20251001",
    "claude-haiku-4-5": "claude-haiku-4-5-20251001",
};
export function resolveModel(model) {
    return MODEL_MAP[model] ?? model;
}
export function getPool() {
    return pool;
}
export async function initPool(defaultModel, poolSize = 3) {
    pool = new SessionPool({ poolSize, warmupModel: defaultModel });
    await pool.warmup();
}
export function shutdownPool() {
    pool?.shutdown();
}
/**
 * Format a self-contained prompt with full conversation context.
 *
 * Uses XML-style tags that Claude recognises as structured prompt sections
 * (not user-injected content). The <new_conversation/> marker tells the model
 * to treat everything that follows as a fresh request, preventing residual
 * session context from bleeding through.
 */
export function formatPrompt(opts) {
    const parts = [];
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
    }
    else if (opts.responseFormat?.type === "json_schema" && opts.responseFormat.json_schema) {
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
    }
    else if (conversation.length > 1) {
        parts.push("<conversation>");
        for (const msg of conversation) {
            const content = typeof msg.content === "string" ? msg.content : "";
            if (msg.role === "tool") {
                parts.push(`Tool result (${msg.tool_call_id ?? msg.name ?? "unknown"}): ${content}`);
            }
            else {
                const label = msg.role === "user" ? "User" : "Assistant";
                parts.push(`${label}: ${content}`);
            }
        }
        parts.push("</conversation>\n\nRespond to the last User message above based on the context and conversation provided.");
    }
    return parts.join("\n");
}
function extractText(msg) {
    const message = msg.message;
    if (!message)
        return "";
    const content = message.content;
    if (!Array.isArray(content))
        return "";
    return content
        .filter((block) => block.type === "text")
        .map((block) => block.text)
        .join("");
}
export async function chat(opts) {
    const prompt = formatPrompt(opts);
    const session = await pool.acquire();
    try {
        await session.send(prompt);
        let resultText = "";
        for await (const msg of session.stream()) {
            if (msg.type === "result") {
                if (msg.subtype === "success") {
                    resultText = msg.result;
                }
                else {
                    const errMsg = msg;
                    throw new Error(`Claude error: ${errMsg.subtype} — ${JSON.stringify(errMsg.errors ?? "no details")}`);
                }
                break;
            }
        }
        pool.release(session);
        return resultText;
    }
    catch (err) {
        pool.discard(session);
        throw err;
    }
}
function buildV1Prompt(opts) {
    const systemParts = opts.messages
        .filter((m) => m.role === "system")
        .map((m) => (typeof m.content === "string" ? m.content : ""));
    const systemPrompt = systemParts.length > 0 ? systemParts.join("\n") : undefined;
    if (opts.responseFormat?.type === "json_object") {
        const jsonInstruction = "You must respond with valid JSON only. No markdown, no explanation, just a JSON object.";
        const combined = systemPrompt ? `${systemPrompt}\n\n${jsonInstruction}` : jsonInstruction;
        return { systemPrompt: combined, prompt: buildConversationPrompt(opts.messages) };
    }
    return { systemPrompt, prompt: buildConversationPrompt(opts.messages) };
}
function buildConversationPrompt(messages) {
    const conversation = messages.filter((m) => m.role !== "system");
    if (conversation.length === 1 && conversation[0].role === "user") {
        return typeof conversation[0].content === "string" ? conversation[0].content : "";
    }
    const lines = ["<conversation>"];
    for (const msg of conversation) {
        const content = typeof msg.content === "string" ? msg.content : "";
        const label = msg.role === "user" ? "User" : "Assistant";
        lines.push(`${label}: ${content}`);
    }
    lines.push("</conversation>\n\nRespond to the last User message.");
    return lines.join("\n");
}
function toSDKThinking(t) {
    if (!t)
        return undefined;
    if (t.type === "adaptive")
        return { type: "adaptive" };
    if (t.type === "disabled")
        return { type: "disabled" };
    return { type: "enabled", budgetTokens: t.budget_tokens };
}
export async function chatV1(opts) {
    const { systemPrompt, prompt } = buildV1Prompt(opts);
    const q = query({
        prompt,
        options: {
            model: opts.model,
            systemPrompt,
            thinking: toSDKThinking(opts.thinking),
            effort: opts.effort,
            persistSession: false,
            allowedTools: [],
            permissionMode: "dontAsk",
        },
    });
    let result = "";
    for await (const msg of q) {
        if (msg.type === "result" && msg.subtype === "success") {
            result = msg.result;
            break;
        }
        if (msg.type === "result" && msg.subtype !== "success") {
            throw new Error(`V1 query error: ${msg.subtype}`);
        }
    }
    return result;
}
export async function* chatV1Stream(opts) {
    const { systemPrompt, prompt } = buildV1Prompt(opts);
    const q = query({
        prompt,
        options: {
            model: opts.model,
            systemPrompt,
            thinking: toSDKThinking(opts.thinking),
            effort: opts.effort,
            persistSession: false,
            allowedTools: [],
            permissionMode: "dontAsk",
            includePartialMessages: true,
        },
    });
    for await (const msg of q) {
        if (msg.type === "stream_event") {
            const ev = msg.event;
            if (ev.type === "content_block_delta") {
                const delta = ev.delta;
                if (delta?.type === "text_delta" && typeof delta.text === "string") {
                    yield delta.text;
                }
            }
        }
        if (msg.type === "result")
            break;
    }
}
// ── V2 streaming (existing) ──────────────────────────────────────────
export async function* chatStream(opts) {
    const prompt = formatPrompt(opts);
    const session = await pool.acquire();
    try {
        await session.send(prompt);
        for await (const msg of session.stream()) {
            if (msg.type === "assistant") {
                const text = extractText(msg);
                if (text) {
                    // Split into 2-3 word chunks with small delays to produce a
                    // natural typing effect on the client side.
                    const words = text.split(/(\s+)/);
                    let buf = "";
                    let wordCount = 0;
                    const chunkSize = 2 + Math.floor(Math.random() * 2);
                    for (const word of words) {
                        buf += word;
                        if (/\S/.test(word))
                            wordCount++;
                        if (wordCount >= chunkSize) {
                            yield buf;
                            buf = "";
                            wordCount = 0;
                            await new Promise((r) => setTimeout(r, 25 + Math.random() * 35));
                        }
                    }
                    if (buf)
                        yield buf;
                }
            }
            if (msg.type === "result")
                break;
        }
        pool.release(session);
    }
    catch (err) {
        pool.discard(session);
        throw err;
    }
}
//# sourceMappingURL=claude.js.map