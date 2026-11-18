// A scripted stand-in for the kit. Each step is one model message; after a
// message with tool uses the fake invokes its handlers, together or one after
// the other, and goes on only when every handler has its result.
import {
  ToolResults,
  type ToolUse,
  type Upstream,
  type UpstreamEvent,
  type UpstreamOptions,
  type UpstreamRun,
  type Usage,
} from "../src/upstream.js";

export interface FakeStep {
  text?: string;
  /** The text as separate deltas; replaces text. */
  deltas?: string[];
  tools?: ToolUse[];
  /** Per tool, its arguments as separate deltas; by default the JSON of its input in one. */
  argParts?: string[][];
  /** The fake waits for this after the text, before the tool uses and the end of the message. */
  hold?: Promise<void>;
  stop?: string;
  usage?: Usage;
}

export interface FakeScript {
  steps: FakeStep[];
  error?: { status?: number; message: string };
  handlers?: "together" | "sequential";
}

export const STEP_USAGE: Usage = { input: 1, cacheRead: 0, cacheWrite: 0, output: 1 };

export class FakeRun implements UpstreamRun {
  closed = false;
  ended = false;
  delivered: { id: string; text: string }[] = [];
  private events: UpstreamEvent[] = [];
  private readers: ((e: UpstreamEvent) => void)[] = [];
  private results = new ToolResults();

  constructor(
    public options: UpstreamOptions,
    private script: FakeScript,
  ) {
    void this.play();
  }

  private async play(): Promise<void> {
    for (const s of this.script.steps) {
      const tools = s.tools ?? [];
      for (const text of s.deltas ?? (s.text ? [s.text] : [])) this.push({ kind: "text_delta", text });
      if (s.hold) {
        await s.hold;
        if (this.closed) return;
      }
      tools.forEach((u, index) => {
        this.push({ kind: "tool_start", index, id: u.id, name: u.name });
        for (const json of s.argParts?.[index] ?? [JSON.stringify(u.input)]) this.push({ kind: "tool_args_delta", index, json });
      });
      const stopReason = s.stop ?? (tools.length > 0 ? "tool_use" : "end_turn");
      this.push({ kind: "message_end", stopReason, toolUses: tools, usage: s.usage ?? STEP_USAGE });
      if (tools.length === 0) continue;
      this.results.seen(tools);
      const call = async (u: ToolUse) => this.delivered.push({ id: u.id, text: await this.results.invoked(u.id, u.name, u.input) });
      if (this.script.handlers === "together") await Promise.all(tools.map(call));
      else for (const u of tools) await call(u);
      if (this.closed) return;
    }
    this.ended = true;
    this.push({ kind: "run_end", error: this.script.error });
  }

  private push(e: UpstreamEvent): void {
    const reader = this.readers.shift();
    if (reader) reader(e);
    else this.events.push(e);
  }

  next(): Promise<UpstreamEvent> {
    const e = this.events.shift();
    if (e) return Promise.resolve(e);
    return new Promise((resolve) => this.readers.push(resolve));
  }

  resolveTool(id: string, text: string): void {
    this.results.resolve(id, text);
  }

  /** Set once the close has finished, one turn of the event loop after close(). */
  interrupted = false;

  close(): Promise<void> {
    // As the kit does, a run closed before its end ends with an error.
    if (!this.closed && !this.ended) this.push({ kind: "run_end", error: { message: "closed" } });
    this.closed = true;
    return new Promise((resolve) =>
      setImmediate(() => {
        this.interrupted = true;
        resolve();
      }),
    );
  }
}

/** Hands out one scripted run per start call, in order. */
export class FakeUpstream implements Upstream {
  runs: FakeRun[] = [];

  constructor(private scripts: FakeScript[]) {}

  start(o: UpstreamOptions): UpstreamRun {
    const script = this.scripts[this.runs.length];
    if (!script) throw new Error(`no script for run ${this.runs.length + 1}`);
    const run = new FakeRun(o, script);
    this.runs.push(run);
    return run;
  }
}
