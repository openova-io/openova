import type { FastifyRequest, FastifyReply } from "fastify";
import type { Config } from "../config.js";
export declare function createAuthHook(config: Config): (request: FastifyRequest, reply: FastifyReply) => Promise<void>;
//# sourceMappingURL=auth.d.ts.map