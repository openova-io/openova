import { v4 as uuidv4 } from "uuid";
import { chat, chatStream, chatV1, chatV1Stream } from "../providers/claude.js";
import { trackPoolMetric } from "../providers/valkey.js";
function makeCompletionId() {
    return `chatcmpl-${uuidv4()}`;
}
export async function chatCompletionsRoute(app, config, conversations) {
    app.post("/v1/chat/completions", async (request, reply) => {
        const body = request.body;
        const model = body.model ?? config.defaultModel;
        const newMessages = body.messages;
        const stream = body.stream ?? false;
        const conversationId = body.conversation_id;
        const maxTokens = body.max_tokens ?? body.max_completion_tokens;
        const includeUsage = stream && body.stream_options?.include_usage === true;
        // Route to V1 query() if caller explicitly requests thinking, effort, or profile=deep
        const useV1 = body.thinking !== undefined || body.effort !== undefined || body.profile === "deep";
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
        let allMessages;
        let convId;
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
        }
        else {
            allMessages = newMessages;
            convId = await conversations.create(newMessages, model, apiKey);
        }
        // Build ChatOptions — pass all OpenAI params through
        const chatOpts = {
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
            let text;
            if (useV1) {
                const v1Opts = {
                    messages: allMessages,
                    model,
                    thinking: body.thinking,
                    effort: body.effort,
                    maxTokens,
                    responseFormat: body.response_format,
                };
                text = await chatV1(v1Opts);
            }
            else {
                text = await chat(chatOpts);
            }
            trackPoolMetric("request", Date.now() - t0);
            await conversations.append(convId, { role: "assistant", content: text });
            const usage = {
                prompt_tokens: 0,
                completion_tokens: 0,
                total_tokens: 0,
            };
            const response = {
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
        function sendChunk(chunk) {
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
        }
        catch (err) {
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
    });
}
//# sourceMappingURL=chat-completions.js.map