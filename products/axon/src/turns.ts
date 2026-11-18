// The turn chain: one upstream run kept open across HTTP requests while the
// caller's tool calls are outstanding. A run is never reused for another
// conversation.
import { render, textOf } from "./transcript.js";
import type { Upstream, UpstreamRun, UpstreamTool, Usage } from "./upstream.js";
import type { ChatCompletionRequest, ChatCompletionUsage, ChatMessage, ToolCall } from "./types/openai.js";

export interface StepResult {
  text: string;
  toolCalls: ToolCall[];
  finishReason: "stop" | "tool_calls" | "length" | "content_filter";
  usage: ChatCompletionUsage;
  emulated: string[];
}

export class HttpError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public retryAfter?: number,
  ) {
    super(message);
  }
}

export interface TurnManager {
  /** Answers one chat completion request; throws HttpError. */
  step(req: ChatCompletionRequest, organization: string): Promise<StepResult>;
  stats(): { active: number; parked: number; queued: number };
  shutdown(): Promise<void>;
}

export interface TurnsOptions {
  upstream: Upstream;
  /** Upstream model name to the kit's model value. */
  modelMap: Record<string, string>;
  /** Longest wait for tool results before a parked run is closed. */
  parkTtlMs: number;
  /** The access token passed to each run; empty until the credential owner sets it. */
  accessToken?: () => string;
}

export const DEFAULT_SYSTEM_PROMPT = "You are a helpful assistant.";
export const NOT_EXECUTED = "Not executed: one tool call per step is allowed. Call it again if it is still needed.";
export const MAX_TOOL_NAME = 56;

interface Chain {
  run: UpstreamRun;
  organization: string;
  state: "active" | "parked" | "closed";
  /** Tool calls returned to the caller and not yet answered. */
  pending: string[];
  /** Tool calls not returned; answered on resume with NOT_EXECUTED. */
  internal: string[];
  timer?: ReturnType<typeof setTimeout>;
}

const EFFORTS = new Set(["low", "medium", "high"]);

/** Rule 1: the longest run of tool messages at the end of the conversation. */
function trailingTools(messages: ChatMessage[]): ChatMessage[] {
  let i = messages.length;
  while (i > 0 && messages[i - 1].role === "tool") i--;
  return messages.slice(i);
}

/** The system prompt: the caller's system messages joined with a blank line, and what is emulated by instruction. */
export function systemPrompt(req: ChatCompletionRequest, withTools: boolean): string {
  const parts = req.messages.filter((m) => m.role === "system").map((m) => textOf(m.content));
  let prompt = parts.length > 0 ? parts.join("\n\n") : DEFAULT_SYSTEM_PROMPT;
  const format = req.response_format;
  if (format?.type === "json_object") {
    prompt += "\n\nYou must respond with valid JSON only. No markdown, no explanation, just a JSON object.";
  } else if (format?.type === "json_schema" && format.json_schema) {
    prompt += `\n\nYou must respond with valid JSON matching this schema:\n${JSON.stringify(format.json_schema, null, 2)}\nNo markdown, no explanation, just a JSON object matching the schema.`;
  }
  const named = namedTool(req.tool_choice);
  if (withTools && req.tool_choice === "required") {
    prompt += "\n\nYou must call one of the tools before you answer.";
  } else if (withTools && named !== undefined) {
    prompt += `\n\nYou must call the tool ${named}.`;
  }
  return prompt;
}

/** The function a tool_choice object names; undefined for a string, for null (the same as auto) and for an object that names none. */
function namedTool(choice: ChatCompletionRequest["tool_choice"]): string | undefined {
  if (choice === null || typeof choice !== "object") return undefined;
  const name = (choice as { function?: { name?: unknown } }).function?.name;
  return typeof name === "string" && name !== "" ? name : undefined;
}

/** The prompt: one user message as it is, any longer conversation as a transcript. */
export function promptOf(messages: ChatMessage[]): string {
  const conversation = messages.filter((m) => m.role !== "system");
  if (conversation.length === 1 && conversation[0].role === "user") return textOf(conversation[0].content);
  return render(messages);
}

/** Rule 6: the usage of the model messages of one step, in the contract's fields. */
function usageOf(u: Usage): ChatCompletionUsage {
  const prompt = u.input + u.cacheRead + u.cacheWrite;
  return {
    prompt_tokens: prompt,
    completion_tokens: u.output,
    total_tokens: prompt + u.output,
    prompt_tokens_details: { cached_tokens: u.cacheRead, cache_write_tokens: u.cacheWrite },
  };
}

function finishOf(stopReason: string): StepResult["finishReason"] {
  if (stopReason === "max_tokens") return "length";
  if (stopReason === "refusal") return "content_filter";
  return "stop";
}

/** Rule 8: an upstream error, as the contract's error. */
function upstreamError(error?: { status?: number; message: string }): HttpError {
  if (error?.status === 429) return new HttpError(429, "rate_limit_exceeded", "the upstream rate limit was hit", 30);
  if (error?.status === 401) return new HttpError(503, "simulation_credential_invalid", "the simulation credential was rejected");
  return new HttpError(502, "backend_error", "the backend failed");
}

export class Turns implements TurnManager {
  private chains = new Set<Chain>();
  private parked = new Map<string, Chain>();

  constructor(private o: TurnsOptions) {}

  async step(req: ChatCompletionRequest, organization: string): Promise<StepResult> {
    const { model, tools } = this.validate(req);
    const chain = this.resume(req, organization) ?? this.start(req, model, tools, organization);
    const choice = req.tool_choice;
    const emulated = tools.length > 0 && (choice === "required" || namedTool(choice) !== undefined) ? ["tool_choice"] : [];
    return this.read(chain, req, emulated);
  }

  stats(): { active: number; parked: number; queued: number } {
    const chains = [...this.chains];
    return {
      active: chains.filter((c) => c.state === "active").length,
      parked: chains.filter((c) => c.state === "parked").length,
      queued: 0,
    };
  }

  async shutdown(): Promise<void> {
    await Promise.all([...this.chains].map((c) => this.close(c)));
  }

  private validate(req: ChatCompletionRequest): { model: string; tools: UpstreamTool[] } {
    if (!Array.isArray(req.messages) || req.messages.length === 0) {
      throw new HttpError(400, "invalid_request", "messages is required and must not be empty");
    }
    if (typeof req.model !== "string" || !Object.hasOwn(this.o.modelMap, req.model)) {
      throw new HttpError(404, "model_not_found", "model not found");
    }
    const model = this.o.modelMap[req.model];
    for (const m of req.messages) {
      if (Array.isArray(m.content) && m.content.some((p) => p.type !== "text")) {
        throw new HttpError(400, "unsupported_content", "only text content is supported");
      }
    }
    const tools: UpstreamTool[] = [];
    for (const t of req.tools ?? []) {
      if (typeof t?.function?.name !== "string" || t.function.name === "") {
        throw new HttpError(400, "invalid_request", "every tool needs a function name");
      }
      if (t.function.name.length > MAX_TOOL_NAME) {
        throw new HttpError(400, "tool_name_too_long", `a tool name may have at most ${MAX_TOOL_NAME} characters`);
      }
      tools.push({
        name: t.function.name,
        description: t.function.description,
        inputSchema: t.function.parameters ?? { type: "object" },
      });
    }
    const choice = req.tool_choice;
    if (choice !== null && typeof choice === "object" && namedTool(choice) === undefined) {
      throw new HttpError(400, "invalid_request", "a tool_choice object must name a function");
    }
    return { model, tools: req.tool_choice === "none" ? [] : tools };
  }

  /** Rule 2: the parked chain these tool results answer, moved to active; undefined when there is none. */
  private resume(req: ChatCompletionRequest, organization: string): Chain | undefined {
    const results = trailingTools(req.messages);
    const chain = results.map((m) => this.parked.get(m.tool_call_id ?? "")).find((c) => c !== undefined);
    if (!chain) return undefined;
    if (chain.organization !== organization) throw new HttpError(404, "model_not_found", "model not found");
    const byId = new Map(results.map((m) => [m.tool_call_id ?? "", textOf(m.content)]));
    const missing = chain.pending.filter((id) => !byId.has(id));
    if (missing.length > 0) {
      throw new HttpError(400, "missing_tool_results", `no tool result for: ${missing.join(", ")}`);
    }
    this.unpark(chain);
    chain.state = "active";
    for (const id of chain.pending) chain.run.resolveTool(id, byId.get(id) as string);
    for (const id of chain.internal) chain.run.resolveTool(id, NOT_EXECUTED);
    chain.pending = [];
    chain.internal = [];
    return chain;
  }

  /** Rule 3: a new chain, one new upstream run. */
  private start(req: ChatCompletionRequest, model: string, tools: UpstreamTool[], organization: string): Chain {
    const effort = req.reasoning_effort;
    const run = this.o.upstream.start({
      model,
      systemPrompt: systemPrompt(req, tools.length > 0),
      prompt: promptOf(req.messages),
      tools,
      effort: effort && EFFORTS.has(effort) ? (effort as "low" | "medium" | "high") : undefined,
      accessToken: this.o.accessToken?.() ?? "",
    });
    const chain: Chain = { run, organization, state: "active", pending: [], internal: [] };
    this.chains.add(chain);
    return chain;
  }

  /** Rule 4: read upstream events until the model message ends. */
  private async read(chain: Chain, req: ChatCompletionRequest, emulated: string[]): Promise<StepResult> {
    const total: Usage = { input: 0, cacheRead: 0, cacheWrite: 0, output: 0 };
    let text = "";
    for (;;) {
      const e = await chain.run.next();
      if (e.kind === "text_delta") {
        text += e.text;
      } else if (e.kind === "run_end") {
        void this.close(chain);
        throw upstreamError(e.error);
      } else if (e.kind === "message_end") {
        total.input += e.usage.input;
        total.cacheRead += e.usage.cacheRead;
        total.cacheWrite += e.usage.cacheWrite;
        total.output += e.usage.output;
        if (e.stopReason === "tool_use") {
          // A call to a tool outside the caller's list is answered by the kit itself; the model goes on.
          if (e.toolUses.length === 0) continue;
          const calls: ToolCall[] = e.toolUses.map((u) => ({
            id: u.id,
            type: "function",
            function: { name: u.name, arguments: JSON.stringify(u.input ?? {}) },
          }));
          const returned = req.parallel_tool_calls === false ? calls.slice(0, 1) : calls;
          this.park(chain, returned.map((c) => c.id), calls.slice(returned.length).map((c) => c.id));
          return { text, toolCalls: returned, finishReason: "tool_calls", usage: usageOf(total), emulated };
        }
        // The last model message. A tool use in it was cut off (max_tokens): its arguments are
        // incomplete, so it is not returned, and the run is closed at once because the kit may
        // wait for its result. Otherwise the step waits for the end of the run and its error.
        if (e.toolUses.length === 0) {
          let end = await chain.run.next();
          while (end.kind !== "run_end") end = await chain.run.next();
          if (end.error) {
            void this.close(chain);
            throw upstreamError(end.error);
          }
        }
        void this.close(chain);
        return { text, toolCalls: [], finishReason: finishOf(e.stopReason), usage: usageOf(total), emulated };
      }
    }
  }

  private park(chain: Chain, pending: string[], internal: string[]): void {
    chain.state = "parked";
    chain.pending = pending;
    chain.internal = internal;
    for (const id of pending) this.parked.set(id, chain);
    const timer = setTimeout(() => void this.close(chain), this.o.parkTtlMs);
    if (typeof timer === "object" && typeof timer.unref === "function") timer.unref();
    chain.timer = timer;
  }

  private unpark(chain: Chain): void {
    if (chain.timer !== undefined) clearTimeout(chain.timer);
    chain.timer = undefined;
    for (const id of chain.pending) this.parked.delete(id);
  }

  /** Resolves once the run is interrupted. */
  private close(chain: Chain): Promise<void> {
    if (chain.state === "closed") return Promise.resolve();
    this.unpark(chain);
    chain.state = "closed";
    this.chains.delete(chain);
    return chain.run.close();
  }
}
