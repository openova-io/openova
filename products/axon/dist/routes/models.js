const MODELS = {
    object: "list",
    data: [
        {
            id: "claude-opus-4-6",
            object: "model",
            created: 1700000000,
            owned_by: "anthropic",
        },
        {
            id: "claude-sonnet-4-6",
            object: "model",
            created: 1700000000,
            owned_by: "anthropic",
        },
        {
            id: "claude-haiku-4-5",
            object: "model",
            created: 1700000000,
            owned_by: "anthropic",
        },
    ],
};
export async function modelsRoute(app) {
    app.get("/v1/models", async () => {
        return MODELS;
    });
}
//# sourceMappingURL=models.js.map