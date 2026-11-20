package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/cortex/internal/assistant"
	"github.com/openova-io/openova/products/cortex/internal/gateway"
	"github.com/openova-io/openova/products/cortex/internal/search"
)

// chatFake is the chat side of the fixture's gateway and the definitions the assistants read.
type chatFake struct {
	defs    assistant.Config
	owner   string // owned_by of the chat alias in the gateway's model list; "-": not listed
	limited bool   // the chat route answers 429
	alias   string
	asked   []gateway.Message
}

func newChatFake() *chatFake {
	return &chatFake{owner: "selfhosted", defs: assistant.Config{ChatAlias: "agent-fast", Assistants: []assistant.Assistant{{ID: "helpdesk",
		Title: "Helpdesk", Instructions: "Answer from the passages.", Sources: []assistant.Source{{ID: "hr", Tier: "primary"}}, TopK: 8,
		Users: []string{"group:staff"}}}}}
}

func (c *chatFake) service(svc *search.Service, gw *gateway.Client) *assistant.Service {
	return &assistant.Service{Config: func() (assistant.Config, error) { return c.defs, nil }, Search: svc, Gateway: gw}
}

// serve answers the two chat routes of the gateway; it returns false for another route.
func (c *chatFake) serve(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/v1/models":
		data := []map[string]string{{"id": "embed-default", "owned_by": "embedding"}}
		if c.owner != "-" {
			data = append(data, map[string]string{"id": "agent-fast", "owned_by": c.owner})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	case "/v1/chat/completions":
		if c.limited {
			w.Header().Set("Retry-After", "7")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return true
		}
		var in struct {
			Model    string
			Messages []gateway.Message
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		c.alias, c.asked = in.Model, in.Messages
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "c-1", "created": 1700000000, "usage": map[string]int{"total_tokens": 9},
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "Monthly [1]."}, "finish_reason": "stop"}}})
	default:
		return false
	}
	return true
}

func TestModelsRoute(t *testing.T) {
	f := newFixture(t)
	for groups, want := range map[string]string{"staff": `{"data":[{"created":0,"id":"helpdesk","object":"model","owned_by":"cortex"}],"object":"list"}`,
		"finance": `{"data":[],"object":"list"}`} {
		w := f.call(t, f.srv.Handler(), "GET", "/v1/models", f.token(t, map[string]any{"groups": []string{groups}}), "")
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != want {
			t.Errorf("groups %s: %d %s", groups, w.Code, w.Body)
		}
	}
}

// TestChatRoute answers with citations, plain and streamed, and refuses in the cases of the
// route table; the question never reaches the log.
func TestChatRoute(t *testing.T) {
	f := newFixture(t)
	tok := f.token(t, nil)
	ask := func(more string) string {
		return `{"model":"helpdesk","temperature":0.2,"messages":[{"role":"user","content":"` + marker + `"}]` + more + `}`
	}
	w := f.call(t, f.srv.Handler(), "POST", "/v1/chat/completions", tok, ask(""))
	want := `{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"Monthly [1].","role":"assistant"}}],` +
		`"citations":[{"index":1,"documentId":"d1","title":"Leave policy","uri":"https://docs.acme.example/leave","page":3}],` +
		`"created":1700000000,"id":"c-1","model":"helpdesk","object":"chat.completion","usage":{"total_tokens":9}}`
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if a := f.chat.asked; f.chat.alias != "agent-fast" || len(a) != 2 || a[0].Role != "system" ||
		a[0].Content != "Answer from the passages.\n\nPassages:\n\n[1] Leave policy\nLeave accrues monthly." || a[1].Content != marker {
		t.Fatalf("the gateway was asked %q with %q", f.chat.alias, a)
	}
	w = f.call(t, f.srv.Handler(), "POST", "/v1/chat/completions", tok, ask(`,"stream":true`))
	frames := strings.Split(w.Body.String(), "\n\n")
	if w.Header().Get("Content-Type") != "text/event-stream" || len(frames) != 4 || frames[2] != "data: [DONE]" ||
		!strings.Contains(frames[0], `"delta":{"content":"Monthly [1].","role":"assistant"}`) ||
		!strings.Contains(frames[1], `"citations":[{"index":1,"documentId":"d1"`) || !strings.Contains(frames[1], `"finish_reason":"stop"`) {
		t.Fatalf("the stream is %d %q", w.Code, frames)
	}
	for what, c := range map[string]struct {
		set    func()
		body   string
		status int
		code   string
	}{
		"tools":               {nil, ask(`,"tools":[{"type":"function"}]`), 400, "tools-not-supported"},
		"another assistant":   {nil, strings.Replace(ask(""), "helpdesk", "finance", 1), 404, "assistant-not-found"},
		"a caller not a user": {func() { f.chat.defs.Assistants[0].Users = []string{"group:hr"} }, ask(""), 404, "assistant-not-found"},
		"no question last":    {nil, strings.Replace(ask(""), `"user"`, `"assistant"`, 1), 400, "invalid-request"},
		"simulation":          {func() { f.chat.owner = "simulation" }, ask(""), 409, "external-models-not-allowed"},
		"no owner":            {func() { f.chat.owner = "" }, ask(""), 409, "external-models-not-allowed"},
		"alias not listed":    {func() { f.chat.owner = "-" }, ask(""), 409, "external-models-not-allowed"},
		"external allowed":    {func() { f.chat.owner, f.chat.defs.AllowExternalModels = "simulation", true }, ask(""), 200, "citations"},
		"rate limited":        {func() { f.chat.limited = true }, ask(""), 429, "rate-limited"},
		"gateway away":        {func() { *f.gwFail = true }, ask(""), 503, "gateway-unavailable"},
	} {
		*f.chat, *f.gwFail = *newChatFake(), false
		if c.set != nil {
			c.set()
		}
		w := f.call(t, f.srv.Handler(), "POST", "/v1/chat/completions", tok, c.body)
		if w.Code != c.status || !strings.Contains(w.Body.String(), c.code) || (c.status == 429 && w.Header().Get("Retry-After") != "7") {
			t.Errorf("%s: %d %s, want %d %s", what, w.Code, w.Body, c.status, c.code)
		}
	}
	if strings.Contains(f.log.String(), marker) || !strings.Contains(f.log.String(), "chat: the gateway failed") {
		t.Fatalf("the log holds the question or not the gateway's failure:\n%s", f.log)
	}
}
