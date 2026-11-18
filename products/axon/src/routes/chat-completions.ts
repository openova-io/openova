import type { FastifyInstance } from "fastify";
import { randomUUID } from "node:crypto";
import { chat, chatStream, type ChatOptions } from "../providers/claude.js";
import type { Config } from "../config.js";
import type {
  ChatCompletionRequest,
  ChatCompletionResponse,
  ChatCompletionChunk,
  ChatCompletionUsage,
} from "../types/openai.js";

function makeCompletionId(): string {
  return `chatcmpl-${randomUUID()}`;
}

export async function chatCompletionsRoute(
  app: FastifyInstance,
  config: Config,
): Promise<void> {
  app.post<{ Body: ChatCompletionRequest }>(
    "/v1/chat/completions",
    async (request, reply) => {
      const body = request.body;
      const model = body.model ?? config.defaultModel;
      const messages = body.messages;
      const stream = body.stream ?? false;
      const includeUsage = stream && body.stream_options?.include_usage === true;

      if (!messages || messages.length === 0) {
        return reply.code(400).send({
          error: {
            message: "messages is required and must not be empty",
            type: "invalid_request_error",
          },
        });
      }

      // ── Claude provider: one kit run per request ─────────────
      const chatOpts: ChatOptions = {
        messages,
        model,
        responseFormat: body.response_format,
      };

      if (!stream) {
        const text = await chat(chatOpts);

        const usage: ChatCompletionUsage = {
          prompt_tokens: 0,
          completion_tokens: 0,
          total_tokens: 0,
        };

        const response: ChatCompletionResponse = {
          id: makeCompletionId(),
          object: "chat.completion",
          created: Math.floor(Date.now() / 1000),
          model,
          system_fingerprint: null,
          choices: [
            {
              index: 0,
              message: { role: "assistant", content: text, refusal: null },
              logprobs: null,
              finish_reason: "stop",
            },
          ],
          usage,
        };

        return response;
      }

      // ── Streaming (Claude) ────────────────────────────────────
      const id = makeCompletionId();
      const created = Math.floor(Date.now() / 1000);

      reply.raw.writeHead(200, {
        "Content-Type": "text/event-stream",
        "Cache-Control": "no-cache",
        Connection: "keep-alive",
      });

      function sendChunk(chunk: ChatCompletionChunk): void {
        reply.raw.write(`data: ${JSON.stringify(chunk)}\n\n`);
      }

      sendChunk({
        id,
        object: "chat.completion.chunk",
        created,
        model,
        system_fingerprint: null,
        choices: [
          {
            index: 0,
            delta: { role: "assistant", content: "" },
            logprobs: null,
            finish_reason: null,
          },
        ],
      });

      try {
        for await (const text of chatStream(chatOpts)) {
          sendChunk({
            id,
            object: "chat.completion.chunk",
            created,
            model,
            system_fingerprint: null,
            choices: [
              {
                index: 0,
                delta: { content: text },
                logprobs: null,
                finish_reason: null,
              },
            ],
          });
        }
      } catch (err) {
        app.log.error(err, "streaming error");
      }

      sendChunk({
        id,
        object: "chat.completion.chunk",
        created,
        model,
        system_fingerprint: null,
        choices: [
          {
            index: 0,
            delta: {},
            logprobs: null,
            finish_reason: "stop",
          },
        ],
      });

      if (includeUsage) {
        sendChunk({
          id,
          object: "chat.completion.chunk",
          created,
          model,
          system_fingerprint: null,
          choices: [],
          usage: {
            prompt_tokens: 0,
            completion_tokens: 0,
            total_tokens: 0,
          },
        });
      }

      reply.raw.write("data: [DONE]\n\n");
      reply.raw.end();
    },
  );
}
