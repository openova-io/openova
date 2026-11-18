package meter

// The scripted back end: the subcommand stub. It stands behind the alias conformance-echo, so
// that a gate check exercises the gateway, the meter and the bus with no credential and with
// token counts it can compute.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// StubAddr is the listen address of the subcommand stub.
const StubAddr = ":8081"

// StubToolCallID is the identifier of the one tool call the stub answers with.
const StubToolCallID = "call_stub_1"

// stubRequest is the part of a chat.completions request the stub reads.
type stubRequest struct {
	Model         string `json:"model"`
	Stream        bool   `json:"stream"`
	StreamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

// stubAnswer is what the stub answers: a text, or one call of the tool named.
type stubAnswer struct {
	text, tool string
}

// NewStub returns the scripted back end. It serves POST /v1/chat/completions and answers every
// other method or path with 404 not_found. Its rules, applied in this order:
//
//  1. When the last message has the role tool, it answers the text "echo:" followed by that
//     message's content.
//  2. Otherwise, when the request has tools, it answers one call of the first tool, with the
//     identifier StubToolCallID and the arguments {}.
//  3. Otherwise it answers the text "echo:" followed by the content of the last message with
//     the role user, or "echo:" alone when there is none.
//
// A content that is a list of parts is read as its text parts joined together. The answer is
// streamed when the request sets "stream": true; the usage chunk is sent only when it also sets
// "stream_options.include_usage": true. The usage is deterministic: prompt_tokens is the byte
// length of the request body divided by 4, rounded up; completion_tokens is the byte length of
// the answered text or arguments divided by 4, rounded up; cached_tokens is 0.
func NewStub() http.Handler {
	return http.HandlerFunc(serveStub)
}

func serveStub(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
		writeError(w, http.StatusNotFound, "not_found", "the stub serves POST /v1/chat/completions only")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes+1))
	if err != nil || len(body) > MaxRequestBytes {
		writeError(w, http.StatusBadRequest, "invalid_request", "the request body could not be read")
		return
	}
	var req stubRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not a chat.completions request")
		return
	}
	ans := req.answer()
	out := ans.text
	if ans.tool != "" {
		out = "{}"
	}
	usage := map[string]any{
		"prompt_tokens":         quarters(len(body)),
		"completion_tokens":     quarters(len(out)),
		"total_tokens":          quarters(len(body)) + quarters(len(out)),
		"prompt_tokens_details": map[string]int64{"cached_tokens": 0},
	}
	if req.Stream {
		writeStubStream(w, req.Model, ans, req.StreamOptions.IncludeUsage, usage)
		return
	}
	message := map[string]any{"role": "assistant", "content": ans.text}
	finish := "stop"
	if ans.tool != "" {
		message = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{ans.toolCall(false)}}
		finish = "tool_calls"
	}
	writeStubJSON(w, map[string]any{
		"id": "chatcmpl-stub", "object": "chat.completion", "created": 0, "model": req.Model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
		"usage":   usage,
	})
}

// answer applies the three rules of NewStub.
func (q stubRequest) answer() stubAnswer {
	if n := len(q.Messages); n > 0 && q.Messages[n-1].Role == "tool" {
		return stubAnswer{text: "echo:" + contentText(q.Messages[n-1].Content)}
	}
	if len(q.Tools) > 0 {
		return stubAnswer{tool: q.Tools[0].Function.Name}
	}
	for i := len(q.Messages) - 1; i >= 0; i-- {
		if q.Messages[i].Role == "user" {
			return stubAnswer{text: "echo:" + contentText(q.Messages[i].Content)}
		}
	}
	return stubAnswer{text: "echo:"}
}

// toolCall is the one tool call of the answer; a streamed delta carries its index.
func (a stubAnswer) toolCall(delta bool) map[string]any {
	c := map[string]any{"id": StubToolCallID, "type": "function", "function": map[string]string{"name": a.tool, "arguments": "{}"}}
	if delta {
		c["index"] = 0
	}
	return c
}

// contentText reads a message content: a string, or a list of parts whose text parts are joined
// together. Anything else reads as the empty text.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// quarters is n divided by 4, rounded up.
func quarters(n int) int64 { return int64((n + 3) / 4) }

func writeStubJSON(w http.ResponseWriter, doc any) {
	b, err := marshalNoEscape(doc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stub_error", "the answer could not be encoded")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

// writeStubStream writes the answer as server-sent events: one chunk with the delta, one with
// the finish reason, the usage chunk when asked, then [DONE]. Every chunk is flushed.
func writeStubStream(w http.ResponseWriter, model string, ans stubAnswer, withUsage bool, usage map[string]any) {
	delta := map[string]any{"role": "assistant", "content": ans.text}
	finish := "stop"
	if ans.tool != "" {
		delta = map[string]any{"role": "assistant", "tool_calls": []any{ans.toolCall(true)}}
		finish = "tool_calls"
	}
	chunk := func(choices []any) map[string]any {
		return map[string]any{"id": "chatcmpl-stub", "object": "chat.completion.chunk", "created": 0, "model": model, "choices": choices}
	}
	chunks := []map[string]any{
		chunk([]any{map[string]any{"index": 0, "delta": delta}}),
		chunk([]any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}),
	}
	if withUsage {
		last := chunk([]any{})
		last["usage"] = usage
		chunks = append(chunks, last)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	for _, c := range chunks {
		b, err := marshalNoEscape(c)
		if err != nil {
			return
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}
