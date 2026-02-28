import type { FastifyInstance } from "fastify";
import { v4 as uuidv4 } from "uuid";
import { chat, chatStream, type ChatOptions } from "../providers/claude.js";
import { trackPoolMetric } from "../providers/valkey.js";
import type { ConversationStore } from "../providers/conversation.js";
import type { Config } from "../config.js";
import type {
  ChatCompletionRequest,
  ChatCompletionResponse,
  ChatCompletionChunk,
  ChatCompletionUsage,
  ChatMessage,
} from "../types/openai.js";

function makeCompletionId(): string {
  return `chatcmpl-${uuidv4()}`;
}

export async function chatCompletionsRoute(
  app: FastifyInstance,
  config: Config,
  conversations: ConversationStore,
): Promise<void> {
  app.post<{ Body: ChatCompletionRequest }>(
    "/v1/chat/completions",
    async (request, reply) => {
      const body = request.body;
      const model = body.model ?? config.defaultModel;
      const newMessages = body.messages;
      const stream = body.stream ?? false;
      const conversationId = body.conversation_id;
      const maxTokens = body.max_tokens ?? body.max_completion_tokens;
      const includeUsage = stream && body.stream_options?.include_usage === true;

      if (!newMessages || newMessages.length === 0) {
        return reply.code(400).send({
          error: {
            message: "messages is required and must not be empty",
            type: "invalid_request_error",
          },
        });
      }

      // Extract API key for conversation ownership tracking
      const authHeader = request.headers.authorization ?? "";
      const apiKey = authHeader.replace("Bearer ", "");

      // Build full message list: history + new messages
      let allMessages: ChatMessage[];
      let convId: string;

      if (conversationId) {
        const history = await conversations.getHistory(conversationId);
        if (history === null) {
          return reply.code(404).send({
            error: {
              message: `Conversation ${conversationId} not found`,
              type: "invalid_request_error",
            },
          });
        }
        allMessages = [...history, ...newMessages];
        convId = conversationId;

        for (const msg of newMessages) {
          await conversations.append(convId, msg);
        }
      } else {
        allMessages = newMessages;
        convId = await conversations.create(newMessages, model, apiKey);
      }

      // Build ChatOptions — pass all OpenAI params through
      const chatOpts: ChatOptions = {
        messages: allMessages,
        model,
        temperature: body.temperature,
        maxTokens,
        topP: body.top_p,
        stop: body.stop,
        frequencyPenalty: body.frequency_penalty,
        presencePenalty: body.presence_penalty,
        responseFormat: body.response_format,
        tools: body.tools,
      };

      if (!stream) {
        // ── Non-streaming ────────────────────────────────────────
        const t0 = Date.now();
        const text = await chat(chatOpts);
        trackPoolMetric("request", Date.now() - t0);

        await conversations.append(convId, { role: "assistant", content: text });

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
          conversation_id: convId,
        };

        return response;
      }

      // ── Streaming ──────────────────────────────────────────────
      const t0 = Date.now();
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

      // Initial chunk with role
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
        conversation_id: convId,
      });

      let fullText = "";

      try {
        for await (const text of chatStream(chatOpts)) {
          fullText += text;
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

      // Final chunk with finish_reason
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

      // Usage chunk (if stream_options.include_usage)
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
      trackPoolMetric("request", Date.now() - t0);

      if (fullText) {
        await conversations.append(convId, { role: "assistant", content: fullText });
      }
    },
  );
}
