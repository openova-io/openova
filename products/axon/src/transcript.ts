import type { ChatMessage } from "./types/openai.js";

/** The text of a message: text parts joined with a newline, an image part as a placeholder. */
export function textOf(content: ChatMessage["content"]): string {
  if (content === null || content === undefined) return "";
  if (typeof content === "string") return content;
  return content.map((p) => (p.type === "text" ? p.text : "[image omitted]")).join("\n");
}

/**
 * The prompt of a new run that carries history: the conversation without its
 * system messages, one line group per message, in order.
 */
export function render(messages: ChatMessage[]): string {
  const lines = ["<conversation>"];
  for (const m of messages) {
    const text = textOf(m.content);
    if (m.role === "system") continue;
    if (m.role === "user") {
      lines.push(`User: ${text}`);
    } else if (m.role === "tool") {
      lines.push(`Tool result for ${m.tool_call_id ?? ""}: ${text}`);
    } else {
      const calls = m.tool_calls ?? [];
      if (text !== "" || calls.length === 0) lines.push(`Assistant: ${text}`);
      for (const c of calls) {
        lines.push(`Assistant called tool ${c.function.name} with id ${c.id} and arguments ${c.function.arguments}`);
      }
    }
  }
  lines.push("</conversation>", "", "Continue the conversation. Respond to the last entry above.");
  return lines.join("\n");
}
