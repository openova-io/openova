package modelcontract

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// gateway is the model gateway in front of an in-process copy of the rules of the stub
// (core/services/model-meter/meter/stub.go): 401 without the token, 404 for a model that is not
// conformance-echo, GET /v1/models, and the stub's three rules and usage formula. parallel calls
// every tool offered, as a model does; fault breaks one clause.
type gateway struct {
	parallel bool
	fault    string // open: no token check; no-usage: no usage chunk; zero: completion_tokens 0;
	// no-echo: no x-request-id in the answer; code: an error with the code error; no-cached: no
	// cached_tokens; no-index: a tool-call delta without index; no-role: a first chunk without
	// delta.role; same-id: every call with the id call_stub_1
}

func (g gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if g.fault != "no-echo" {
		w.Header().Set("X-Request-Id", r.Header.Get("X-Request-Id"))
	}
	reply := func(status int, doc any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(doc)
	}
	failure := func(status int, code string) {
		if g.fault == "code" {
			code = "error"
		}
		reply(status, map[string]any{"error": map[string]string{"code": code}})
	}
	if r.Header.Get("Authorization") != "Bearer s3cr3t-jwt" && g.fault != "open" {
		failure(401, "invalid_token")
		return
	}
	if r.Header.Get("X-Request-Id") == "" { // the client sends one with every request
		failure(400, "invalid_request")
		return
	}
	if r.Method == http.MethodGet {
		reply(200, map[string]any{"data": []any{map[string]string{"id": ConformanceAlias, "owned_by": "stub"}}})
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Model    string
		Stream   bool
		Messages []struct{ Role, Content string }
		Tools    []struct{ Function struct{ Name string } }
	}
	if json.Unmarshal(body, &req) != nil || req.Model != ConformanceAlias {
		failure(404, "model_not_found")
		return
	}
	text, calls, last := "echo:", []map[string]any{}, req.Messages[len(req.Messages)-1]
	switch {
	case last.Role == "tool":
		text += last.Content
	case len(req.Tools) > 0:
		for i, t := range req.Tools {
			if i == 0 || g.parallel {
				calls = append(calls, map[string]any{"index": i, "id": fmt.Sprint("call_stub_", i+1), "type": "function",
					"function": map[string]string{"name": t.Function.Name, "arguments": "{}"}})
			}
		}
		for _, c := range calls {
			if g.fault == "no-index" {
				delete(c, "index")
			}
			if g.fault == "same-id" {
				c["id"] = "call_stub_1"
			}
		}
		text = ""
	default:
		text += last.Content
	}
	out, finish := len(text), "stop"
	if len(calls) > 0 {
		out, finish = 2*len(calls), "tool_calls"
	}
	if g.fault == "zero" {
		out = -3
	}
	details := map[string]int{"cached_tokens": 0}
	if g.fault == "no-cached" {
		details = map[string]int{}
	}
	usage := map[string]any{"prompt_tokens": (len(body) + 3) / 4, "completion_tokens": (out + 3) / 4,
		"total_tokens": (len(body)+3)/4 + (out+3)/4, "prompt_tokens_details": details}
	message := map[string]any{"role": "assistant", "content": text}
	if len(calls) > 0 {
		message = map[string]any{"role": "assistant", "tool_calls": calls}
	}
	if !req.Stream {
		reply(200, map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": finish}}, "usage": usage})
		return
	}
	if g.fault == "no-role" {
		delete(message, "role")
	}
	chunks := []any{map[string]any{"choices": []any{map[string]any{"delta": message}}},
		map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": finish}}}}
	if g.fault != "no-usage" {
		chunks = append(chunks, map[string]any{"choices": []any{}, "usage": usage})
	}
	for _, c := range chunks {
		b, _ := json.Marshal(c)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func TestProbes(t *testing.T) {
	type probe = func(Client, context.Context, string) (Reply, error)
	unknown := func(c Client, ctx context.Context, _ string) (Reply, error) { return c.UnknownModel(ctx) }
	stream := func(c Client, ctx context.Context, a string) (Reply, error) { return c.Stream(ctx, a, "hi") }
	cases := []struct {
		name  string
		g     gateway
		probe probe
		text  string // the text of the last answer, or the start of the error
	}{
		{"a tool round trip on the stub", gateway{}, Client.ToolRoundTrip, "echo:" + ToolResult},
		{"two tools on the stub give one call", gateway{}, Client.ParallelTools, "1.4: 1 tool calls in one answer, want 2"},
		{"two tools on a model give two calls", gateway{parallel: true}, Client.ParallelTools, "echo:" + ToolResult},
		{"a streamed call", gateway{}, stream, "echo:hi"},
		{"a stream with tool deltas", gateway{}, Client.StreamWithToolDeltas, ""},
		{"a stream without its usage chunk", gateway{fault: "no-usage"}, stream, "1.4: the stream did not end with a usage chunk"},
		{"a usage that is not zero", gateway{}, Client.UsageNonZero, "echo:Answer with one word: ready."},
		{"a usage with zero completion tokens", gateway{fault: "zero"}, Client.UsageNonZero, "1.4: usage is absent, has a zero"},
		{"no token gets 401", gateway{}, Client.Unauthorized, ""},
		{"no token is let through", gateway{fault: "open"}, Client.Unauthorized, "1.5: the answer is 200"},
		{"an unknown model gets 404", gateway{}, unknown, ""},
		{"an unknown model gets 404 with another code", gateway{fault: "code"}, unknown, `1.5: the answer is 404 "error"`},
		{"an answer without the request id", gateway{fault: "no-echo"}, Client.UsageNonZero, `1.4: the answer carries x-request-id ""`},
		{"a stream without the request id", gateway{fault: "no-echo"}, stream, `1.4: the answer carries x-request-id ""`},
		{"a usage without cached_tokens", gateway{fault: "no-cached"}, Client.UsageNonZero, "1.4: usage is absent"},
		{"a tool-call delta without an index", gateway{fault: "no-index"}, Client.StreamWithToolDeltas, "1.4: a tool-call delta has no index"},
		{"a first chunk without a role", gateway{fault: "no-role"}, stream, "1.4: the first chunk has no delta.role"},
		{"two calls with one id", gateway{parallel: true, fault: "same-id"}, Client.ParallelTools, `1.4: tool call "call_stub_1" of type "function" has no unique id`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.g)
			defer srv.Close()
			r, err := tc.probe(Client{BaseURL: srv.URL + "/v1", Token: "s3cr3t-jwt"}, context.Background(), ConformanceAlias)
			got := r.Text
			if err != nil {
				got = err.Error()
				if strings.Contains(got, "s3cr3t") {
					t.Fatalf("the error holds the token: %s", got)
				}
			}
			if !strings.HasPrefix(got, tc.text) || (tc.text == "" && err != nil) || r.RequestID == "" {
				t.Fatalf("got %q (request id %q), want %q", got, r.RequestID, tc.text)
			}
		})
	}
}

func TestModelOwner(t *testing.T) {
	srv := httptest.NewServer(gateway{})
	defer srv.Close()
	c := Client{BaseURL: srv.URL + "/v1", Token: "s3cr3t-jwt"}
	if owner, err := c.ModelOwner(context.Background(), ConformanceAlias); owner != "stub" || err != nil {
		t.Fatalf("ModelOwner = %q, %v; want stub", owner, err)
	}
	if _, err := c.ModelOwner(context.Background(), "agent-default"); err == nil || !strings.HasPrefix(err.Error(), "1.1: ") {
		t.Fatalf("ModelOwner of an alias the list lacks = %v, want a 1.1 error", err)
	}
}
