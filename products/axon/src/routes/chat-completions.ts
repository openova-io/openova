import type { FastifyInstance } from "fastify";
import { randomUUID } from "node:crypto";
import { HttpError, type StepResult, type TurnManager } from "../turns.js";
import type { ChatCompletionChunk, ChatCompletionRequest, ChatCompletionResponse } from "../types/openai.js";

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

/** The whole answer of one step as a stream: one content chunk, one finish chunk, the usage chunk when asked. */
function streamBody(id: string, created: number, model: string, r: StepResult, includeUsage: boolean): string {
  const base = { id, object: "chat.completion.chunk" as const, created, model, system_fingerprint: null };
  const toolCalls = r.toolCalls.map((c, index) => ({ index, ...c }));
  const chunks: ChatCompletionChunk[] = [
    {
      ...base,
      choices: [
        {
          index: 0,
          delta: { role: "assistant", content: r.text, ...(toolCalls.length > 0 ? { tool_calls: toolCalls } : {}) },
          logprobs: null,
          finish_reason: null,
        },
      ],
    },
    { ...base, choices: [{ index: 0, delta: {}, logprobs: null, finish_reason: r.finishReason }] },
  ];
  if (includeUsage) chunks.push({ ...base, choices: [], usage: r.usage });
  return chunks.map((c) => `data: ${JSON.stringify(c)}\n\n`).join("") + "data: [DONE]\n\n";
}

export async function chatCompletionsRoute(app: FastifyInstance, turns: TurnManager): Promise<void> {
  app.post<{ Body: ChatCompletionRequest }>("/v1/chat/completions", async (request, reply) => {
    const body = (request.body ?? {}) as ChatCompletionRequest;
    const header = request.headers["x-openova-organization"];
    const organization = typeof header === "string" ? header : "";

    let result: StepResult;
    try {
      result = await turns.step(body, organization);
    } catch (err) {
      if (!(err instanceof HttpError)) throw err;
      if (err.retryAfter !== undefined) reply.header("retry-after", String(err.retryAfter));
      return reply.code(err.status).send(errorBody(err));
    }

    if (result.emulated.length > 0) reply.header("x-openova-emulated", result.emulated.join(","));
    const id = `chatcmpl-${randomUUID()}`;
    const created = Math.floor(Date.now() / 1000);
    const model = body.model as string;
    if (body.stream !== true) return completion(id, created, model, result);

    const includeUsage = body.stream_options?.include_usage === true;
    reply.header("content-type", "text/event-stream").header("cache-control", "no-cache");
    return reply.send(streamBody(id, created, model, result, includeUsage));
  });
}
