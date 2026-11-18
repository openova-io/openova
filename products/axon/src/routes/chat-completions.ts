import type { FastifyInstance, FastifyReply } from "fastify";
import { randomUUID } from "node:crypto";
import { Disconnected, emulatedOf, HttpError, type StepResult, type StepSink, type TurnManager } from "../turns.js";
import type {
  ChatCompletionChunk,
  ChatCompletionChunkDelta,
  ChatCompletionRequest,
  ChatCompletionResponse,
} from "../types/openai.js";

function errorType(status: number): string {
  if (status === 429) return "rate_limit_error";
  return status < 500 ? "invalid_request_error" : "server_error";
}

/** The error body of the model contract. */
export function errorBody(err: HttpError): { error: { message: string; type: string; code: string } } {
  return { error: { message: err.message, type: errorType(err.status), code: err.code } };
}

function completion(id: string, created: number, model: string, r: StepResult): ChatCompletionResponse {
  const calls = r.toolCalls.length > 0 ? { tool_calls: r.toolCalls } : {};
  return {
    id,
    object: "chat.completion",
    created,
    model,
    system_fingerprint: null,
    choices: [
      {
        index: 0,
        message: { role: "assistant", content: r.text === "" && calls.tool_calls ? null : r.text, ...calls, refusal: null },
        logprobs: null,
        finish_reason: r.finishReason,
      },
    ],
    usage: r.usage,
  };
}

/**
 * Writes one chunk per upstream event as it arrives (rule 7 of the turn chain):
 * the role chunk, content and tool-call deltas, then the finish chunk, the usage
 * chunk when asked for, and `data: [DONE]`. Nothing is sent before the first
 * event, so an error that comes first still gets its own status. An error after
 * that ends the stream with an error event and without `data: [DONE]`.
 */
class StreamWriter implements StepSink {
  opened = false;
  private base: Omit<ChatCompletionChunk, "choices" | "usage">;

  constructor(
    private reply: FastifyReply,
    id: string,
    created: number,
    model: string,
    private headers: Record<string, string>,
  ) {
    this.base = { id, object: "chat.completion.chunk", created, model, system_fingerprint: null };
  }

  role(): void {
    this.delta({ role: "assistant", content: "" });
  }

  text(delta: string): void {
    this.delta({ content: delta });
  }

  toolStart(index: number, id: string, name: string): void {
    this.delta({ tool_calls: [{ index, id, type: "function", function: { name, arguments: "" } }] });
  }

  toolArgs(index: number, json: string): void {
    this.delta({ tool_calls: [{ index, function: { arguments: json } }] });
  }

  finish(r: StepResult, includeUsage: boolean): void {
    this.send({ ...this.base, choices: [{ index: 0, delta: {}, logprobs: null, finish_reason: r.finishReason }] });
    if (includeUsage) this.send({ ...this.base, choices: [], usage: r.usage });
    this.end("data: [DONE]\n\n");
  }

  fail(err: HttpError): void {
    this.end(`data: ${JSON.stringify(errorBody(err))}\n\n`);
  }

  private delta(delta: ChatCompletionChunkDelta): void {
    this.send({ ...this.base, choices: [{ index: 0, delta, logprobs: null, finish_reason: null }] });
  }

  private open(): void {
    if (this.opened) return;
    this.opened = true;
    this.reply.hijack();
    this.reply.raw.writeHead(200, this.headers);
  }

  private send(chunk: ChatCompletionChunk): void {
    this.open();
    const raw = this.reply.raw;
    if (!raw.destroyed && !raw.writableEnded) raw.write(`data: ${JSON.stringify(chunk)}\n\n`);
  }

  private end(last: string): void {
    this.open();
    const raw = this.reply.raw;
    if (!raw.destroyed && !raw.writableEnded) raw.end(last);
  }
}

export async function chatCompletionsRoute(app: FastifyInstance, turns: TurnManager): Promise<void> {
  app.post<{ Body: ChatCompletionRequest }>("/v1/chat/completions", async (request, reply) => {
    const body = (request.body ?? {}) as ChatCompletionRequest;
    const header = request.headers["x-openova-organization"];
    const organization = typeof header === "string" ? header : "";
    const id = `chatcmpl-${randomUUID()}`;
    const created = Math.floor(Date.now() / 1000);
    const model = body.model as string;

    // A caller that goes away before the answer is written closes its chain.
    const gone = new AbortController();
    reply.raw.on("close", () => {
      if (!reply.raw.writableFinished) gone.abort();
    });

    let stream: StreamWriter | undefined;
    if (body.stream === true) {
      const headers: Record<string, string> = { "content-type": "text/event-stream", "cache-control": "no-cache" };
      const emulated = emulatedOf(body);
      if (emulated.length > 0) headers["x-openova-emulated"] = emulated.join(",");
      stream = new StreamWriter(reply, id, created, model, headers);
    }

    let result: StepResult;
    try {
      result = await turns.step(body, organization, stream, gone.signal);
    } catch (err) {
      if (err instanceof Disconnected) {
        if (!stream?.opened) reply.hijack();
        reply.raw.destroy();
        return reply;
      }
      if (stream?.opened) {
        if (!(err instanceof HttpError)) request.log.error(err);
        stream.fail(err instanceof HttpError ? err : new HttpError(502, "backend_error", "the backend failed"));
        return reply;
      }
      if (!(err instanceof HttpError)) throw err;
      if (err.retryAfter !== undefined) reply.header("retry-after", String(err.retryAfter));
      return reply.code(err.status).send(errorBody(err));
    }

    if (stream) {
      stream.finish(result, body.stream_options?.include_usage === true);
      return reply;
    }
    if (result.emulated.length > 0) reply.header("x-openova-emulated", result.emulated.join(","));
    return completion(id, created, model, result);
  });
}
