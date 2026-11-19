// Package modelcontract is a client of the model contract, docs/contracts/model-contract.md.
// Each probe sends requests to the model gateway and returns nil, or an error that names the
// section of the contract whose clause the answer broke first. The agent token is sent as a
// bearer and is never part of an error. Standard library only.
package modelcontract

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
)

// ConformanceAlias is the alias of the gate checks; its back end is the stub (section 1.2).
const ConformanceAlias = "conformance-echo"

// ToolResult is the result a probe sends back for every tool call.
const ToolResult = "probe-result-42"

// Client calls the gateway at BaseURL (http://<address>/v1) with the agent token Token.
type Client struct{ BaseURL, Token string }

var httpClient = &http.Client{Timeout: 60 * time.Second}

// Usage is the usage block of an answer (section 1.4). Cached is nil when the field is absent.
type Usage struct {
	Prompt     int64 `json:"prompt_tokens"`
	Completion int64 `json:"completion_tokens"`
	Total      int64 `json:"total_tokens"`
	Details    struct {
		Cached     *int64 `json:"cached_tokens"`
		CacheWrite int64  `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
}

// Call is one tool call of an answer.
type Call struct {
	Index    *int   `json:"index,omitempty"` // set in a streamed delta only
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Reply is what the last request of a probe returned, for the evidence of a check.
type Reply struct {
	Status    int
	Code      string // error.code of an error body
	RequestID string // the x-request-id the request carried
	Text      string
	Calls     []Call
	Finish    string
	Usage     *Usage
	echoedID  string // the x-request-id of the answer
}

type message = map[string]any

func tools(names ...string) []any {
	out := []any{}
	for _, n := range names {
		out = append(out, message{"type": "function", "function": message{"name": n, "description": "Returns a fact for the probe.",
			"parameters": message{"type": "object", "properties": message{"q": message{"type": "string"}}}}})
	}
	return out
}

// newRequestID is a new x-request-id that matches ^[A-Za-z0-9._-]{8,64}$ (section 3).
func newRequestID() string {
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	return "mc-" + hex.EncodeToString(id)
}

// send posts body to /chat/completions with a new x-request-id and the headers h; bearer false
// sends no token.
func (c Client) send(ctx context.Context, body message, h http.Header, bearer bool) (*http.Response, Reply, error) {
	r := Reply{RequestID: newRequestID()}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, r, err
	}
	for k, v := range h {
		req.Header[http.CanonicalHeaderKey(k)] = v
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", r.RequestID)
	if bearer {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, r, fmt.Errorf("POST /v1/chat/completions got no answer: %v", errors.Unwrap(err))
	}
	r.Status, r.echoedID = res.StatusCode, res.Header.Get("X-Request-Id")
	return res, r, nil
}

// exchange sends one unstreamed request and reads the answer.
func (c Client) exchange(ctx context.Context, body message, h http.Header, bearer bool) (Reply, error) {
	res, r, err := c.send(ctx, body, h, bearer)
	if err != nil {
		return r, err
	}
	defer func() { _ = res.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var e struct {
		Error struct{ Code string } `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil {
		r.Code = e.Error.Code
	}
	var doc struct {
		Choices []struct {
			Message struct {
				Content   *string `json:"content"`
				ToolCalls []Call  `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *Usage `json:"usage"`
	}
	if json.Unmarshal(data, &doc) == nil && len(doc.Choices) > 0 {
		ch := doc.Choices[0]
		if ch.Message.Content != nil {
			r.Text = *ch.Message.Content
		}
		r.Calls, r.Finish, r.Usage = ch.Message.ToolCalls, ch.FinishReason, doc.Usage
	}
	return r, nil
}

// judge applies section 1.4 to an answer: status 200, the x-request-id of the request, one call
// or more of the tools asked for (each with a unique id, type function, and a JSON object as
// arguments) or else a text, the finish reason that goes with it, and a usage that is not zero.
func judge(r Reply, toolNames []string) error {
	if r.Status != http.StatusOK {
		return fmt.Errorf("1.5: the answer is %d %q, want 200", r.Status, r.Code)
	}
	if r.echoedID != r.RequestID {
		return fmt.Errorf("1.4: the answer carries x-request-id %q, want %q", r.echoedID, r.RequestID)
	}
	seen := map[string]bool{}
	for _, c := range r.Calls {
		var args map[string]any
		switch {
		case c.ID == "" || seen[c.ID] || c.Type != "function":
			return fmt.Errorf("1.4: tool call %q of type %q has no unique id or is not of type function", c.ID, c.Type)
		case !slices.Contains(toolNames, c.Function.Name):
			return fmt.Errorf("1.4: tool call %q names %q, which the request did not offer", c.ID, c.Function.Name)
		case json.Unmarshal([]byte(c.Function.Arguments), &args) != nil || args == nil:
			return fmt.Errorf("1.4: the arguments of tool call %q are not a JSON object", c.ID)
		}
		seen[c.ID] = true
	}
	switch u := r.Usage; {
	case len(toolNames) > 0 && (len(r.Calls) == 0 || r.Finish != "tool_calls"):
		return fmt.Errorf("1.4: %d tool calls with finish_reason %q, want a call and tool_calls", len(r.Calls), r.Finish)
	case len(toolNames) == 0 && (len(r.Calls) > 0 || r.Text == "" || r.Finish != "stop"):
		return fmt.Errorf("1.4: %d tool calls, text of %d bytes, finish_reason %q; want a text and stop", len(r.Calls), len(r.Text), r.Finish)
	case u == nil || u.Prompt == 0 || u.Completion == 0 || u.Total == 0 || u.Details.Cached == nil:
		return errors.New("1.4: usage is absent, has a zero token count, or lacks prompt_tokens_details.cached_tokens")
	}
	return nil
}

// Chat sends text to alias unstreamed, with the extra headers h, and judges the answer.
func (c Client) Chat(ctx context.Context, alias, text string, h http.Header) (Reply, error) {
	r, err := c.exchange(ctx, message{"model": alias, "messages": []any{message{"role": "user", "content": text}}}, h, true)
	if err == nil {
		err = judge(r, nil)
	}
	return r, err
}

// UsageNonZero sends one question to alias; the answer has a text and a usage with no zero count.
func (c Client) UsageNonZero(ctx context.Context, alias string) (Reply, error) {
	return c.Chat(ctx, alias, "Answer with one word: ready.", nil)
}

// roundTrip offers the tools, expects calls of them, sends ToolResult back for each call and
// expects a text. want is the least number of calls in the first answer.
func (c Client) roundTrip(ctx context.Context, alias string, want int, names ...string) (Reply, error) {
	msgs := []any{message{"role": "user", "content": "Call " + strings.Join(names, " and ") + " with q set to probe, at once, then answer with the results."}}
	body := message{"model": alias, "messages": msgs, "tools": tools(names...)}
	r, err := c.exchange(ctx, body, nil, true)
	if err == nil {
		err = judge(r, names)
	}
	if err == nil && len(r.Calls) < want {
		err = fmt.Errorf("1.4: %d tool calls in one answer, want %d", len(r.Calls), want)
	}
	if err != nil {
		return r, err
	}
	calls := slices.Clone(r.Calls)
	for i := range calls {
		calls[i].Index = nil
	}
	msgs = append(msgs, message{"role": "assistant", "content": nil, "tool_calls": calls})
	for _, call := range calls {
		msgs = append(msgs, message{"role": "tool", "tool_call_id": call.ID, "content": ToolResult})
	}
	body["messages"] = msgs
	if r, err = c.exchange(ctx, body, nil, true); err == nil {
		err = judge(r, nil)
	}
	return r, err
}

// ToolRoundTrip offers one tool; a call of it comes back, its result goes in, a text comes out.
func (c Client) ToolRoundTrip(ctx context.Context, alias string) (Reply, error) {
	return c.roundTrip(ctx, alias, 1, "lookup")
}

// ParallelTools offers two tools and asks for both at once: one answer holds two calls.
func (c Client) ParallelTools(ctx context.Context, alias string) (Reply, error) {
	return c.roundTrip(ctx, alias, 2, "lookup", "clock")
}

// Stream sends text to alias streamed with include_usage and judges the stream.
func (c Client) Stream(ctx context.Context, alias, text string) (Reply, error) {
	return c.stream(ctx, message{"model": alias, "messages": []any{message{"role": "user", "content": text}}}, nil)
}

// StreamWithToolDeltas offers one tool streamed: the call comes as deltas with an index.
func (c Client) StreamWithToolDeltas(ctx context.Context, alias string) (Reply, error) {
	return c.stream(ctx, message{"model": alias, "tools": tools("lookup"),
		"messages": []any{message{"role": "user", "content": "Call lookup with q set to probe."}}}, []string{"lookup"})
}

// stream checks the order of section 1.4: a first chunk with delta.role, the deltas (tool-call
// deltas with an index, joined by it), a chunk with finish_reason, a usage chunk with empty
// choices, then data: [DONE]. The joined answer is judged as an unstreamed one.
func (c Client) stream(ctx context.Context, body message, toolNames []string) (Reply, error) {
	body["stream"], body["stream_options"] = true, message{"include_usage": true}
	res, r, err := c.send(ctx, body, nil, true)
	if err != nil || r.Status != http.StatusOK {
		if res != nil {
			_ = res.Body.Close()
		}
		return r, errors.Join(err, judge(r, toolNames))
	}
	defer func() { _ = res.Body.Close() }()
	calls, n, done := map[int]*Call{}, 0, false
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		switch {
		case !ok:
			continue
		case done || r.Usage != nil && line != "[DONE]":
			return r, errors.New("1.4: a chunk came after the usage chunk or after [DONE]")
		case line == "[DONE]":
			done = true
			continue
		}
		var ch struct {
			Choices []struct {
				Delta struct {
					Role, Content string
					ToolCalls     []Call `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *Usage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(line), &ch); err != nil {
			return r, errors.New("1.4: a chunk is not JSON")
		}
		if n++; n == 1 && (len(ch.Choices) == 0 || ch.Choices[0].Delta.Role == "") {
			return r, errors.New("1.4: the first chunk has no delta.role")
		}
		if ch.Usage != nil && (len(ch.Choices) > 0 || r.Finish == "") {
			return r, errors.New("1.4: the usage chunk has choices or came before the finish_reason")
		}
		r.Usage = ch.Usage
		for _, x := range ch.Choices {
			if r.Finish != "" && (x.Delta.Content != "" || len(x.Delta.ToolCalls) > 0) {
				return r, errors.New("1.4: a delta came after the finish_reason")
			}
			r.Text += x.Delta.Content
			if x.FinishReason != "" {
				r.Finish = x.FinishReason
			}
			for _, d := range x.Delta.ToolCalls {
				if d.Index == nil {
					return r, errors.New("1.4: a tool-call delta has no index")
				}
				if calls[*d.Index] == nil {
					calls[*d.Index] = &Call{ID: d.ID, Type: d.Type}
					calls[*d.Index].Function.Name = d.Function.Name
				}
				calls[*d.Index].Function.Arguments += d.Function.Arguments
			}
		}
	}
	if !done || r.Usage == nil {
		return r, errors.New("1.4: the stream did not end with a usage chunk and data: [DONE]")
	}
	keys := make([]int, 0, len(calls))
	for k := range calls {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		r.Calls = append(r.Calls, *calls[k])
	}
	return r, judge(r, toolNames)
}

// Unauthorized sends a request with no token: the gateway answers 401 (section 1.5).
func (c Client) Unauthorized(ctx context.Context, alias string) (Reply, error) {
	return c.refused(ctx, alias, false, http.StatusUnauthorized, "invalid_token")
}

// UnknownModel asks for the model nope: the gateway answers 404 (section 1.2).
func (c Client) UnknownModel(ctx context.Context) (Reply, error) {
	return c.refused(ctx, "nope", true, http.StatusNotFound, "model_not_found")
}

func (c Client) refused(ctx context.Context, alias string, bearer bool, status int, code string) (Reply, error) {
	r, err := c.exchange(ctx, message{"model": alias, "messages": []any{message{"role": "user", "content": "probe"}}}, nil, bearer)
	if err == nil && (r.Status != status || r.Code != code) {
		err = fmt.Errorf("1.5: the answer is %d %q, want %d %s", r.Status, r.Code, status, code)
	}
	return r, err
}

// ModelOwner returns owned_by of alias in GET /v1/models (section 1.1).
func (c Client) ModelOwner(ctx context.Context, alias string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/models", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-Request-Id", newRequestID())
	res, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET /v1/models got no answer: %v", errors.Unwrap(err))
	}
	defer func() { _ = res.Body.Close() }()
	var doc struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&doc)
	for _, m := range doc.Data {
		if m.ID == alias {
			return m.OwnedBy, nil
		}
	}
	return "", fmt.Errorf("1.1: GET /v1/models answered %d and does not list %s", res.StatusCode, alias)
}
