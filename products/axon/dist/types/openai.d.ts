export interface FunctionDefinition {
    name: string;
    description?: string;
    parameters?: Record<string, unknown>;
    strict?: boolean;
}
export interface Tool {
    type: "function";
    function: FunctionDefinition;
}
export interface FunctionCall {
    name: string;
    arguments: string;
}
export interface ToolCall {
    id: string;
    type: "function";
    function: FunctionCall;
}
export interface ChatMessage {
    role: "system" | "user" | "assistant" | "tool";
    content: string | null;
    name?: string;
    tool_calls?: ToolCall[];
    tool_call_id?: string;
}
export interface ResponseFormat {
    type: "text" | "json_object" | "json_schema";
    json_schema?: Record<string, unknown>;
}
export interface StreamOptions {
    include_usage?: boolean;
}
export interface ChatCompletionRequest {
    model?: string;
    messages: ChatMessage[];
    stream?: boolean;
    max_tokens?: number;
    max_completion_tokens?: number;
    temperature?: number;
    top_p?: number;
    n?: number;
    stop?: string | string[] | null;
    frequency_penalty?: number;
    presence_penalty?: number;
    logit_bias?: Record<string, number> | null;
    logprobs?: boolean;
    top_logprobs?: number | null;
    seed?: number | null;
    tools?: Tool[];
    tool_choice?: "none" | "auto" | "required" | {
        type: "function";
        function: {
            name: string;
        };
    };
    parallel_tool_calls?: boolean;
    response_format?: ResponseFormat;
    stream_options?: StreamOptions | null;
    user?: string;
    store?: boolean;
    metadata?: Record<string, string>;
    conversation_id?: string;
    thinking?: {
        type: "adaptive";
    } | {
        type: "enabled";
        budget_tokens?: number;
    } | {
        type: "disabled";
    };
    effort?: "low" | "medium" | "high" | "max";
    profile?: "fast" | "deep";
}
export interface ChatCompletionUsage {
    prompt_tokens: number;
    completion_tokens: number;
    total_tokens: number;
}
export interface ChatCompletionChoice {
    index: number;
    message: {
        role: "assistant";
        content: string | null;
        tool_calls?: ToolCall[];
        refusal?: string | null;
    };
    logprobs: null;
    finish_reason: "stop" | "length" | "tool_calls" | "content_filter" | null;
}
export interface ChatCompletionResponse {
    id: string;
    object: "chat.completion";
    created: number;
    model: string;
    system_fingerprint: string | null;
    choices: ChatCompletionChoice[];
    usage: ChatCompletionUsage;
    conversation_id?: string;
}
export interface ChatCompletionChunkDelta {
    role?: "assistant";
    content?: string | null;
    tool_calls?: ToolCall[];
}
export interface ChatCompletionChunkChoice {
    index: number;
    delta: ChatCompletionChunkDelta;
    logprobs: null;
    finish_reason: "stop" | "length" | "tool_calls" | "content_filter" | null;
}
export interface ChatCompletionChunk {
    id: string;
    object: "chat.completion.chunk";
    created: number;
    model: string;
    system_fingerprint: string | null;
    choices: ChatCompletionChunkChoice[];
    usage?: ChatCompletionUsage | null;
    conversation_id?: string;
}
export interface ModelObject {
    id: string;
    object: "model";
    created: number;
    owned_by: string;
}
export interface ModelListResponse {
    object: "list";
    data: ModelObject[];
}
//# sourceMappingURL=openai.d.ts.map