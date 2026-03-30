export function createAuthHook(config) {
    return async function authHook(request, reply) {
        if (request.url === "/health" || request.url === "/stats")
            return;
        const authHeader = request.headers.authorization;
        if (!authHeader) {
            reply.code(401).send({ error: { message: "Missing Authorization header", type: "invalid_request_error" } });
            return;
        }
        const match = authHeader.match(/^Bearer\s+(.+)$/);
        if (!match) {
            reply.code(401).send({ error: { message: "Invalid Authorization header format", type: "invalid_request_error" } });
            return;
        }
        const token = match[1];
        if (!config.apiKeys.includes(token)) {
            reply.code(401).send({ error: { message: "Invalid API key", type: "invalid_request_error" } });
            return;
        }
    };
}
//# sourceMappingURL=auth.js.map