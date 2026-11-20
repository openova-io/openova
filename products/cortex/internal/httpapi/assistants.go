package httpapi

// The assistant routes, in the OpenAI shape for chat front ends: GET /v1/models lists the
// assistants the caller may use, and POST /v1/chat/completions answers with one of them, the
// model of the request being the assistant's id. An answer carries the top-level field
// citations; a streamed answer is sent as one chunk with the text and a last chunk with the
// finish reason and the citations. A request that offers tools is refused.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/cortex/internal/assistant"
	"github.com/openova-io/openova/products/cortex/internal/auth"
	"github.com/openova-io/openova/products/cortex/internal/gateway"
	"github.com/openova-io/openova/products/cortex/internal/search"
)

// maxChat bounds the body of a chat request, which holds the whole conversation.
const maxChat = 1 << 20

func (s *Server) models(w http.ResponseWriter, _ *http.Request, c auth.Caller) {
	list, err := s.cfg.Assistant.Usable(c.Principals)
	if err != nil {
		s.cfg.Log.Error("models failed", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	data := make([]map[string]any, 0, len(list))
	for _, a := range list {
		data = append(data, map[string]any{"id": a.ID, "object": "model", "created": 0, "owned_by": "cortex"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// chatRequest is the part of an OpenAI chat request that cortex reads; other fields are ignored.
type chatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Stream    bool            `json:"stream"`
	Tools     json.RawMessage `json:"tools"`
	Functions json.RawMessage `json:"functions"`
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	var req chatRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChat))
	if err := dec.Decode(&req); err != nil || !errors.Is(dec.Decode(new(json.RawMessage)), io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid-request")
		return
	}
	if (len(req.Tools) > 0 && string(req.Tools) != "null") || (len(req.Functions) > 0 && string(req.Functions) != "null") {
		writeError(w, http.StatusBadRequest, "tools-not-supported")
		return
	}
	msgs := make([]gateway.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		text, ok := contentText(m.Content)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid-request")
			return
		}
		msgs = append(msgs, gateway.Message{Role: m.Role, Content: text})
	}
	a, err := s.cfg.Assistant.Answer(r.Context(), c.Principals, req.Model, msgs)
	var limited *gateway.RateLimited
	switch {
	case errors.As(err, &limited):
		if limited.RetryAfter != "" {
			w.Header().Set("Retry-After", limited.RetryAfter)
		}
		writeError(w, http.StatusTooManyRequests, "rate-limited")
	case errors.Is(err, assistant.ErrInvalid), errors.Is(err, search.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid-request")
	case errors.Is(err, assistant.ErrNotFound):
		writeError(w, http.StatusNotFound, "assistant-not-found")
	case errors.Is(err, assistant.ErrExternal):
		writeError(w, http.StatusConflict, "external-models-not-allowed")
	case errors.Is(err, gateway.ErrUnavailable), errors.Is(err, search.ErrUnavailable):
		s.cfg.Log.Warn("chat: the gateway failed", "error", err.Error())
		writeError(w, http.StatusServiceUnavailable, "gateway-unavailable")
	case err != nil:
		s.cfg.Log.Error("chat failed", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal")
	case req.Stream:
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		for _, f := range []map[string]any{
			{"choices": []map[string]any{{"index": 0, "delta": map[string]string{"role": "assistant", "content": a.Content}, "finish_reason": nil}}},
			{"choices": []map[string]any{{"index": 0, "delta": map[string]string{}, "finish_reason": a.FinishReason}}, "usage": a.Usage, "citations": a.Citations},
		} {
			f["id"], f["object"], f["created"], f["model"] = a.ID, "chat.completion.chunk", a.Created, req.Model
			b, _ := json.Marshal(f)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	default:
		choice := map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": a.Content}, "finish_reason": a.FinishReason}
		writeJSON(w, http.StatusOK, map[string]any{"id": a.ID, "object": "chat.completion", "created": a.Created, "model": req.Model,
			"choices": []map[string]any{choice}, "usage": a.Usage, "citations": a.Citations})
	}
}

// contentText reads the content of a message: a string, or a list of text parts.
func contentText(raw json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, true
	}
	var parts []struct{ Type, Text string }
	if json.Unmarshal(raw, &parts) != nil {
		return "", false
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type != "text" {
			return "", false
		}
		b.WriteString(p.Text)
	}
	return b.String(), true
}
