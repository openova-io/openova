import type { FastifyInstance } from "fastify";
import { v4 as uuidv4 } from "uuid";
import { chat, chatStream, chatV1, chatV1Stream, type ChatOptions, type ChatV1Options } from "../providers/claude.js";
import { trackPoolMetric } from "../providers/valkey.js";
import type { VllmProvider } from "../providers/vllm.js";
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
  vllm?: VllmProvider,
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

      // ── vLLM provider: proxy passthrough ──────────────────────
      if (config.provider === "vllm" && vllm) {
        const proxyBody: ChatCompletionRequest = { ...body, messages: allMessages, model };

        if (!stream) {
          const t0 = Date.now();
          const res = await vllm.chat(proxyBody);
          if (!res.ok) {
            const errText = await res.text();
            app.log.error(`vLLM error: ${res.status} — ${errText}`);
            return reply.code(res.status).send({
              error: { message: `vLLM backend error: ${errText}`, type: "server_error" },
            });
          }
          const data = await res.json() as ChatCompletionResponse;
          trackPoolMetric("request", Date.now() - t0);

          const assistantContent = data.choices?.[0]?.message?.content ?? "";
          await conversations.append(convId, { role: "assistant", content: assistantContent });

          data.conversation_id = convId;
          return data;
        }

        // Streaming: pipe vLLM SSE stream through to client
        const t0 = Date.now();
        const res = await vllm.chatStream(proxyBody);
        if (!res.ok) {
          const errText = await res.text();
          app.log.error(`vLLM stream error: ${res.status} — ${errText}`);
          return reply.code(res.status).send({
            error: { message: `vLLM backend error: ${errText}`, type: "server_error" },
          });
        }

        reply.raw.writeHead(200, {
          "Content-Type": "text/event-stream",
          "Cache-Control": "no-cache",
          Connection: "keep-alive",
        });

        let fullText = "";
        const reader = res.body?.getReader();
        if (!reader) {
          reply.raw.write("data: [DONE]\n\n");
          reply.raw.end();
          return;
        }

        const decoder = new TextDecoder();
        let buffer = "";

        try {
          while (true) {
            const { done, value } = await reader.read();
            if (done) break;

            buffer += decoder.decode(value, { stream: true });
            const lines = buffer.split("\n");
            buffer = lines.pop() ?? "";

            for (const line of lines) {
              if (!line.startsWith("data: ")) continue;
              const payload = line.slice(6);

              if (payload === "[DONE]") {
                reply.raw.write("data: [DONE]\n\n");
                continue;
              }

              try {
                const chunk = JSON.parse(payload) as ChatCompletionChunk;
                const delta = chunk.choices?.[0]?.delta?.content;
                if (delta) fullText += delta;
                chunk.conversation_id = convId;
                reply.raw.write(`data: ${JSON.stringify(chunk)}\n\n`);
              } catch {
                reply.raw.write(`${line}\n\n`);
              }
            }
          }
        } catch (err) {
          app.log.error(err, "vLLM streaming error");
        }

        reply.raw.end();
        trackPoolMetric("request", Date.now() - t0);

        if (fullText) {
          await conversations.append(convId, { role: "assistant", content: fullText });
        }
        return;
      }

      // ── Claude provider (existing logic) ──────────────────────
      const useV1 = body.thinking !== undefined || body.effort !== undefined || body.profile === "deep";

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
        const t0 = Date.now();
        let text: string;
        if (useV1) {
          const v1Opts: ChatV1Options = {
            messages: allMessages,
            model,
            thinking: body.thinking,
            effort: body.effort,
            maxTokens,
            responseFormat: body.response_format,
          };
          text = await chatV1(v1Opts);
        } else {
          text = await chat(chatOpts);
        }
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

      // ── Streaming (Claude) ────────────────────────────────────
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

      const streamSource = useV1
        ? chatV1Stream({ messages: allMessages, model, thinking: body.thinking, effort: body.effort, maxTokens, responseFormat: body.response_format })
        : chatStream(chatOpts);

      try {
        for await (const text of streamSource) {
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
      trackPoolMetric("request", Date.now() - t0);

      if (fullText) {
        await conversations.append(convId, { role: "assistant", content: fullText });
      }
    },
  );
}
