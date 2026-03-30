import type { FastifyInstance } from "fastify";
import type { ConversationStore } from "../providers/conversation.js";
import type { Config } from "../config.js";
export declare function chatCompletionsRoute(app: FastifyInstance, config: Config, conversations: ConversationStore): Promise<void>;
//# sourceMappingURL=chat-completions.d.ts.map