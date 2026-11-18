package meter

// Tests of the scripted back end: its answers, its counts, its streams, and the stub behind the
// meter.

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// stubDo sends one request to the stub and returns the status, the content type and the body.
func stubDo(t *testing.T, method, path, body string) (int, string, string) {
	t.Helper()
	w := httptest.NewRecorder()
	NewStub().ServeHTTP(w, httptest.NewRequest(method, "http://model-stub"+path, strings.NewReader(body)))
	return w.Code, w.Header().Get("Content-Type"), w.Body.String()
}

// stubCompletion is the part of an unstreamed answer the tests read.
type stubCompletion struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content   *string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens *int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

const stubTools = `"tools":[{"type":"function","function":{"name":"list_applications","parameters":{"type":"object"}}},{"type":"function","function":{"name":"other"}}]`

func TestStubAnswersTheFourCases(t *testing.T) {
	if StubAddr != ":8081" || StubToolCallID != "call_stub_1" {
		t.Fatalf("StubAddr %q, StubToolCallID %q; want :8081 and call_stub_1", StubAddr, StubToolCallID)
	}
	cases := []struct {
		name, body, text, tool string
	}{
		{"no tools: echo of the last user text",
			`{"model":"conformance-echo","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"first"},{"role":"assistant","content":"x"},{"role":"user","content":"ping"}]}`,
			"echo:ping", ""},
		{"tools and no tool message: one call of the first tool",
			`{"model":"conformance-echo","messages":[{"role":"user","content":"list"}],` + stubTools + `}`,
			"", "list_applications"},
		{"last message tool: echo of the tool result, the tools notwithstanding",
			`{"model":"conformance-echo","messages":[{"role":"user","content":"list"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_stub_1","type":"function","function":{"name":"list_applications","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_stub_1","content":"wordpress-1"}],` + stubTools + `}`,
			"echo:wordpress-1", ""},
		{"content parts: the text parts joined",
			`{"model":"conformance-echo","messages":[{"role":"user","content":[{"type":"text","text":"a "},{"type":"image_url","image_url":{"url":"data:,"}},{"type":"other","text":"skip"},{"type":"text","text":"b"}]}]}`,
			"echo:a b", ""},
	}
	for _, c := range cases {
		status, ctype, body := stubDo(t, http.MethodPost, "/v1/chat/completions", c.body)
		var got stubCompletion
		if status != http.StatusOK || ctype != "application/json" || json.Unmarshal([]byte(body), &got) != nil || len(got.Choices) != 1 {
			t.Fatalf("%s: %d %s %s", c.name, status, ctype, body)
		}
		ch, out := got.Choices[0], c.text
		if c.tool == "" {
			if ch.Message.Content == nil || *ch.Message.Content != c.text || len(ch.Message.ToolCalls) != 0 || ch.FinishReason != "stop" {
				t.Fatalf("%s: answer %s, want the text %q", c.name, body, c.text)
			}
		} else {
			out = "{}"
			tc := ch.Message.ToolCalls
			if len(tc) != 1 || tc[0].ID != StubToolCallID || tc[0].Type != "function" || tc[0].Function.Name != c.tool || tc[0].Function.Arguments != "{}" || ch.FinishReason != "tool_calls" {
				t.Fatalf("%s: answer %s, want one call of %s", c.name, body, c.tool)
			}
		}
		u := got.Usage
		if got.Model != "conformance-echo" || u.PromptTokens != int64((len(c.body)+3)/4) || u.CompletionTokens != int64((len(out)+3)/4) || u.PromptTokensDetails.CachedTokens == nil || *u.PromptTokensDetails.CachedTokens != 0 {
			t.Fatalf("%s: model %q usage %+v, want prompt %d completion %d cached 0", c.name, got.Model, u, (len(c.body)+3)/4, (len(out)+3)/4)
		}
	}
}

func TestStubCountsRoundUp(t *testing.T) {
	for n, want := range map[int]int64{0: 0, 1: 1, 4: 1, 5: 2, 8: 2, 9: 3} {
		if got := quarters(n); got != want {
			t.Fatalf("quarters(%d) = %d, want %d", n, got, want)
		}
	}
	// "echo:abc" is 8 bytes: 2 tokens; "echo:abcd" is 9 bytes: 3 tokens.
	for text, want := range map[string]int64{"abc": 2, "abcd": 3} {
		_, _, body := stubDo(t, http.MethodPost, "/v1/chat/completions", `{"messages":[{"role":"user","content":"`+text+`"}]}`)
		if u := ParseJSON([]byte(body)); !u.Complete || u.CompletionTokens != want || u.CachedTokens != 0 {
			t.Fatalf("%q: usage %+v, want completion %d", text, u, want)
		}
	}
}

func TestStubStreamsTheUsageChunkOnlyWhenAsked(t *testing.T) {
	plain := `{"model":"conformance-echo","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	asked := `{"model":"conformance-echo","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}],` + stubTools + `}`
	for _, c := range []struct {
		body      string
		withUsage bool
		toolCalls int
	}{{plain, false, 0}, {asked, true, 1}} {
		status, ctype, body := stubDo(t, http.MethodPost, "/v1/chat/completions", c.body)
		if status != http.StatusOK || ctype != "text/event-stream" || !strings.HasSuffix(body, "data: [DONE]\n\n") {
			t.Fatalf("%s: %d %s %q", c.body, status, ctype, body)
		}
		if got := strings.Count(body, `"usage":`); got != map[bool]int{false: 0, true: 1}[c.withUsage] {
			t.Fatalf("%s: %d usage chunks in %q", c.body, got, body)
		}
		p := NewStreamParser(false)
		fwd := p.Write([]byte(body))
		rest, u := p.Close()
		if string(fwd)+string(rest) != body {
			t.Fatalf("the stream parser changed the stub's stream %q", body)
		}
		if !c.withUsage {
			if u.Complete || !strings.Contains(body, `"content":"echo:hi"`) {
				t.Fatalf("without include_usage: usage %+v, body %q", u, body)
			}
			continue
		}
		want := Usage{PromptTokens: int64((len(asked) + 3) / 4), CompletionTokens: 1, ToolCalls: 1, ResponseModel: "conformance-echo", Complete: true}
		if u != want || !strings.Contains(body, `"id":"`+StubToolCallID+`"`) {
			t.Fatalf("with include_usage: usage %+v, want %+v; body %q", u, want, body)
		}
	}
}

func TestStubAnswers404OutsideChatCompletions(t *testing.T) {
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/chat/completions"},
		{http.MethodPost, "/v1/completions"},
		{http.MethodPost, "/v1/models"},
		{http.MethodGet, "/healthz"},
		{http.MethodPost, "/"},
	} {
		status, _, body := stubDo(t, c.method, c.path, `{"messages":[]}`)
		if status != http.StatusNotFound || !strings.Contains(body, `"code":"not_found"`) {
			t.Fatalf("%s %s = %d %s, want 404 not_found", c.method, c.path, status, body)
		}
	}
	if status, _, body := stubDo(t, http.MethodPost, "/v1/chat/completions", `not json`); status != http.StatusBadRequest || !strings.Contains(body, `"code":"invalid_request"`) {
		t.Fatalf("a body that is not JSON = %d %s, want 400 invalid_request", status, body)
	}
}

// TestStubBehindTheMeterGivesTheMeasureFormula is the unit analogue of gate check AIP-G03: the
// meter's event for a call on the stub carries the stub's formula, computed on the body the stub
// received (the meter adds include_usage to a streamed request).
func TestStubBehindTheMeterGivesTheMeasureFormula(t *testing.T) {
	var mu sync.Mutex
	var received []string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = append(received, string(b))
		mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(b)))
		NewStub().ServeHTTP(w, r)
	}))
	t.Cleanup(stub.Close)
	pub := &fakePublisher{}
	h, err := NewHandler(Config{Backends: map[string]Backend{"meter-stub": {Name: "stub", URL: stub.URL}}, Publisher: pub,
		Source: testSource, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	calls := []struct{ id, body, text string }{
		{"req-unstream1", `{"model":"conformance-echo","messages":[{"role":"user","content":"metered"}]}`, "echo:metered"},
		{"req-stream001", `{"model":"conformance-echo","stream":true,"messages":[{"role":"user","content":"metered"}]}`, "echo:metered"},
		{"req-stream002", `{"model":"conformance-echo","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"metered"}]}`, "echo:metered"},
	}
	for i, c := range calls {
		req := httptest.NewRequest(http.MethodPost, "http://meter-stub/v1/chat/completions", strings.NewReader(c.body))
		for k, vs := range identityHeaders() {
			req.Header[k] = vs
		}
		req.Header.Set(HeaderAlias, "conformance-echo")
		req.Header.Set(HeaderRequestID, c.id)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), c.text) {
			t.Fatalf("%s: %d %s", c.id, w.Code, w.Body.String())
		}
		// The caller sees the usage chunk of a stream only when it asked for it.
		if shown := strings.Contains(w.Body.String(), `"usage":`); i > 0 && shown != (i == 2) {
			t.Fatalf("%s: usage chunk shown = %v, want %v; answer %q", c.id, shown, i == 2, w.Body.String())
		}
		evs := pub.events()
		if len(evs) != i+1 {
			t.Fatalf("%s: %d events, want %d", c.id, len(evs), i+1)
		}
		mu.Lock()
		got := received[i]
		mu.Unlock()
		want := []Measure{{MeterInput, int64((len(got) + 3) / 4)}, {MeterOutput, int64((len(c.text) + 3) / 4)}, {MeterRequests, 1}}
		if ev := evs[i]; ev.Organization != "acme" || ev.Alias != "conformance-echo" || !reflect.DeepEqual(ev.Measures, want) {
			t.Fatalf("%s: event %+v, want the measures %v of the stub's formula", c.id, ev, want)
		}
		rec, ok := h.Ring().Get(c.id)
		if !ok || !rec.Published || rec.Stream != (i > 0) || rec.Status != StatusOK || rec.Backend != "stub" {
			t.Fatalf("%s: ring entry %+v", c.id, rec)
		}
	}
	if !strings.Contains(received[1], `"include_usage":true`) {
		t.Fatalf("the meter forwarded %s without include_usage", received[1])
	}
}
