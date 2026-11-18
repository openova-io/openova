// The upstream model behind Axon: one run of the agent kit per conversation.
// This is the only file that imports the kit. Tests replace Upstream with a fake.
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { isDeepStrictEqual } from "node:util";
import { createSdkMcpServer, query } from "@anthropic-ai/claude-agent-sdk";
import type { McpSdkServerConfigWithInstance, Options, Query, SDKMessage } from "@anthropic-ai/claude-agent-sdk";
import { CallToolRequestSchema, ListToolsRequestSchema } from "@modelcontextprotocol/sdk/types.js";

export interface ToolUse {
  id: string;
  name: string;
  input: unknown;
}

export interface Usage {
  input: number;
  cacheRead: number;
  cacheWrite: number;
  output: number;
}

export type UpstreamEvent =
  | { kind: "text_delta"; text: string }
  | { kind: "tool_start"; index: number; id: string; name: string }
  | { kind: "tool_args_delta"; index: number; json: string }
  // emitted at the raw message_stop event, once per model message
  | { kind: "message_end"; stopReason: string; toolUses: ToolUse[]; usage: Usage }
  | { kind: "run_end"; error?: { status?: number; message: string } };

export interface UpstreamRun {
  /** Resolves with the next event; after run_end, with run_end again. */
  next(): Promise<UpstreamEvent>;
  /** Stores the result for id, or answers the handler already waiting for it. */
  resolveTool(id: string, text: string): void;
  /** Ends the run and frees its child process; resolves once the run is interrupted. */
  close(): Promise<void>;
}

export interface UpstreamTool {
  name: string;
  description?: string;
  inputSchema: object;
}

export interface UpstreamOptions {
  model: string;
  systemPrompt: string;
  prompt: string;
  tools: UpstreamTool[];
  effort?: "low" | "medium" | "high";
  /** The access token for the child, its only credential. The refresh token never reaches a child. */
  accessToken: string;
}

export interface Upstream {
  start(o: UpstreamOptions): UpstreamRun;
}

/** The kit names the tools of the in-process server "t" with this prefix. */
const TOOL_PREFIX = "mcp__t__";
/** Where the kit puts the tool-use identifier in a tool call. */
const TOOL_USE_ID = "claudecode/toolUseId";

/**
 * Matches the kit's tool handlers to the caller's results. Results not yet
 * delivered are kept by tool-call identifier, handlers not yet answered wait.
 * This holds whether the kit invokes the handlers of one model message
 * together or one after another. A handler without an identifier is matched
 * to the first pending tool use with the same name and a deeply equal input.
 */
export class ToolResults {
  private stored = new Map<string, string>();
  private waiting = new Map<string, (text: string) => void>();
  private pending: ToolUse[] = [];
  private unmatched: { name: string; input: unknown; answer: (id: string) => void }[] = [];

  /** The caller's tool uses of one model message. */
  seen(uses: ToolUse[]): void {
    this.pending.push(...uses);
    for (const h of [...this.unmatched]) {
      const use = this.find(h.name, h.input);
      if (use) {
        this.unmatched.splice(this.unmatched.indexOf(h), 1);
        this.take(use.id);
        h.answer(use.id);
      }
    }
  }

  /** A handler invocation of the kit; resolves with the caller's result. */
  invoked(id: string | undefined, name: string, input: unknown): Promise<string> {
    return new Promise((resolve) => {
      const answer = (toolId: string) => {
        const text = this.stored.get(toolId);
        if (text === undefined) {
          this.waiting.set(toolId, resolve);
        } else {
          this.stored.delete(toolId);
          resolve(text);
        }
      };
      const use = id ? { id } : this.find(name, input);
      if (use) {
        this.take(use.id);
        answer(use.id);
      } else {
        this.unmatched.push({ name, input, answer });
      }
    });
  }

  /** The caller's result for one tool call. */
  resolve(id: string, text: string): void {
    const handler = this.waiting.get(id);
    if (handler) {
      this.waiting.delete(id);
      handler(text);
    } else {
      this.stored.set(id, text);
    }
  }

  private find(name: string, input: unknown): ToolUse | undefined {
    return this.pending.find((u) => u.name === name && isDeepStrictEqual(u.input, input));
  }

  private take(id: string): void {
    const i = this.pending.findIndex((u) => u.id === id);
    if (i >= 0) this.pending.splice(i, 1);
  }
}

/**
 * Turns the kit's messages into upstream events. It reads the raw stream
 * events only: the assembled assistant messages repeat one usage block per
 * content block, and their output count is not the real one.
 */
export class EventMapper {
  private usage: Usage = { input: 0, cacheRead: 0, cacheWrite: 0, output: 0 };
  private stopReason = "";
  private blocks = new Map<number, { index: number; id: string; name: string; json: string }>();

  map(msg: SDKMessage): UpstreamEvent[] {
    if (msg.type === "result") {
      const status = (msg as { api_error_status?: number | null }).api_error_status ?? undefined;
      if (msg.subtype === "success" && !msg.is_error) return [{ kind: "run_end" }];
      const message = msg.subtype === "success" ? msg.result : msg.errors.join("; ") || msg.subtype;
      return [{ kind: "run_end", error: { status, message } }];
    }
    if (msg.type !== "stream_event" || msg.parent_tool_use_id !== null) return [];
    const ev = msg.event;
    switch (ev.type) {
      case "message_start": {
        const u = ev.message.usage;
        this.usage = {
          input: u.input_tokens,
          cacheRead: u.cache_read_input_tokens ?? 0,
          cacheWrite: u.cache_creation_input_tokens ?? 0,
          output: u.output_tokens,
        };
        this.stopReason = "";
        this.blocks.clear();
        return [];
      }
      case "content_block_start": {
        const b = ev.content_block;
        if (b.type !== "tool_use" || !b.name.startsWith(TOOL_PREFIX)) return [];
        const block = { index: this.blocks.size, id: b.id, name: b.name.slice(TOOL_PREFIX.length), json: "" };
        this.blocks.set(ev.index, block);
        return [{ kind: "tool_start", index: block.index, id: block.id, name: block.name }];
      }
      case "content_block_delta": {
        if (ev.delta.type === "text_delta") return [{ kind: "text_delta", text: ev.delta.text }];
        const block = this.blocks.get(ev.index);
        if (ev.delta.type !== "input_json_delta" || !block) return [];
        block.json += ev.delta.partial_json;
        return [{ kind: "tool_args_delta", index: block.index, json: ev.delta.partial_json }];
      }
      case "message_delta":
        this.stopReason = ev.delta.stop_reason ?? this.stopReason;
        this.usage.output = ev.usage.output_tokens;
        return [];
      case "message_stop": {
        const toolUses = [...this.blocks.values()].map((b) => ({ id: b.id, name: b.name, input: parseInput(b.json) }));
        return [{ kind: "message_end", stopReason: this.stopReason, toolUses, usage: { ...this.usage } }];
      }
      default:
        return [];
    }
  }
}

function parseInput(json: string): unknown {
  if (json === "") return {};
  try {
    return JSON.parse(json);
  } catch {
    return {};
  }
}

function toolUseId(meta: unknown): string | undefined {
  const id = (meta as Record<string, unknown> | undefined)?.[TOOL_USE_ID];
  return typeof id === "string" && id !== "" ? id : undefined;
}

/** The in-process tool server: it lists the caller's tools and never executes one. */
function toolServer(tools: UpstreamTool[], results: ToolResults): McpSdkServerConfigWithInstance {
  const server = createSdkMcpServer({ name: "t" });
  const low = server.instance.server;
  low.registerCapabilities({ tools: {} });
  low.setRequestHandler(ListToolsRequestSchema, async () => ({
    tools: tools.map((t) => ({
      name: t.name,
      description: t.description,
      inputSchema: t.inputSchema as { type: "object" },
    })),
  }));
  low.setRequestHandler(CallToolRequestSchema, async (req, extra) => {
    const id = toolUseId(req.params._meta) ?? toolUseId(extra._meta);
    const text = await results.invoked(id, req.params.name, req.params.arguments ?? {});
    return { content: [{ type: "text" as const, text }] };
  });
  return server;
}

/** A child's environment: the access token alone, and an empty HOME where it finds no credential file to refresh. */
export function childEnv(parent: NodeJS.ProcessEnv, accessToken: string, home: string): Record<string, string | undefined> {
  const env: Record<string, string | undefined> = { ...parent, HOME: home, CLAUDE_CODE_OAUTH_TOKEN: accessToken };
  delete env.CLAUDECODE;
  return env;
}

/** One empty HOME for every child of this process, made at the first run. */
let childHome: string | undefined;
const emptyHome = () => (childHome ??= mkdtempSync(join(tmpdir(), "axon-child-")));

class KitRun implements UpstreamRun {
  private events: UpstreamEvent[] = [];
  private readers: ((e: UpstreamEvent) => void)[] = [];
  private ended: UpstreamEvent | undefined;
  private closing: Promise<void> | undefined;
  private results = new ToolResults();
  private mapper = new EventMapper();
  private abort = new AbortController();
  private q: Query;

  constructor(o: UpstreamOptions) {
    const env = childEnv(process.env, o.accessToken, emptyHome());
    const withTools = o.tools.length > 0;
    const options: Options = {
      model: o.model,
      systemPrompt: o.systemPrompt,
      tools: [],
      settingSources: [],
      mcpServers: withTools ? { t: toolServer(o.tools, this.results) } : {},
      allowedTools: withTools ? ["mcp__t"] : [],
      permissionMode: "dontAsk",
      persistSession: false,
      includePartialMessages: true,
      env,
      abortController: this.abort,
    };
    if (o.effort) options.effort = o.effort;
    this.q = query({ prompt: o.prompt, options });
    void this.pump();
  }

  private async pump(): Promise<void> {
    try {
      for await (const msg of this.q) {
        for (const e of this.mapper.map(msg)) {
          if (e.kind === "message_end") this.results.seen(e.toolUses);
          this.push(e);
        }
        if (msg.type === "result") break;
      }
      this.push({ kind: "run_end", error: { message: "the run ended without a result" } });
    } catch (err) {
      this.push({ kind: "run_end", error: { message: err instanceof Error ? err.message : String(err) } });
    }
  }

  private push(e: UpstreamEvent): void {
    if (this.ended) return;
    if (e.kind === "run_end") this.ended = e;
    const reader = this.readers.shift();
    if (reader) reader(e);
    else this.events.push(e);
  }

  next(): Promise<UpstreamEvent> {
    const e = this.events.shift() ?? this.ended;
    if (e) return Promise.resolve(e);
    return new Promise((resolve) => this.readers.push(resolve));
  }

  resolveTool(id: string, text: string): void {
    this.results.resolve(id, text);
  }

  close(): Promise<void> {
    if (this.closing) return this.closing;
    if (this.ended) {
      this.abort.abort();
      this.closing = Promise.resolve();
      return this.closing;
    }
    // Interrupt first: an abort alone sends one more model request with a
    // made-up empty result for the pending tool call.
    const closing = this.q
      .interrupt()
      .then(() => undefined)
      .catch(() => undefined)
      .finally(() => this.abort.abort());
    this.closing = closing;
    return closing;
  }
}

export class KitUpstream implements Upstream {
  start(o: UpstreamOptions): UpstreamRun {
    return new KitRun(o);
  }
}
