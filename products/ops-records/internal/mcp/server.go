// Package mcp serves the tool endpoint of the operations-records service.
//
// Transport: MCP streamable HTTP, POST /mcp, one JSON-RPC 2.0 message per request, stateless.
// A notification (a message without id) is answered 202 with no body. Methods: initialize,
// ping, tools/list and tools/call.
//
// The rules:
//
//   - Caller. Every request carries "Authorization: Bearer <agent token>", accepted by
//     Verifier: signed by a key of catalyst-api's key set, aud containing platform-tools.
//     Without one the answer is HTTP 401. There is no fallback bearer and no credential of
//     the service's own.
//   - Organization. The Organization is the token's claim org_id. Every tool reads and writes
//     that Organization only: a record of another Organization is "not found", exactly as one
//     that does not exist.
//   - Tiers. tools/list shows the tools of the token's tier, and tools/call checks it again.
//     Every tool here needs tier viewer.
//   - Writes. add_incident_note is the one tool that writes: one timeline entry "note" on an
//     incident, by the actor kind agent named by the token. It is a record tool: it changes no
//     desired state and nothing outside this service's database.
//   - Answers. Every answer is JSON in one text part. It passes agentguard.CleanValue with no
//     link host allowed (links removed, secrets redacted, outside text made inert) and has at
//     most agentguard.MaxAnswer characters: a longer list keeps its first and its newest
//     entries and says in "dropped" how many it left out.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openova-io/openova/products/ops-records/internal/agentguard"
	"github.com/openova-io/openova/products/ops-records/internal/api"
	"github.com/openova-io/openova/products/ops-records/internal/store"
)

// ProtocolVersions are the MCP versions the server speaks, newest first.
var ProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// maxBody is the largest request body.
const maxBody = 1 << 20

// JSON-RPC error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeNoMethod       = -32601
	codeInvalidParams  = -32602
	codeUnauthorized   = -32001
)

// Server is the tool endpoint.
type Server struct {
	Store   store.Store
	Verify  func(ctx context.Context, bearer string) (Identity, error)
	Version string
	Now     func() time.Time // nil means time.Now
	Hub     *api.Hub         // receives a change after every note; nil sends none
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func reply(w http.ResponseWriter, status int, id json.RawMessage, result any, e *rpcError) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	body := map[string]any{"jsonrpc": "2.0", "id": id}
	if e != nil {
		body["error"] = e
	} else {
		body["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// ServeHTTP answers POST /mcp.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "POST one JSON-RPC message", http.StatusMethodNotAllowed)
		return
	}
	bearer := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(bearer) > 7 && strings.EqualFold(bearer[:7], "bearer ") {
		bearer = strings.TrimSpace(bearer[7:])
	} else {
		bearer = ""
	}
	if bearer == "" {
		w.Header().Set("WWW-Authenticate", "Bearer")
		reply(w, http.StatusUnauthorized, nil, nil, &rpcError{codeUnauthorized, "an agent token is required"})
		return
	}
	who, err := s.Verify(r.Context(), bearer)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		reply(w, http.StatusUnauthorized, nil, nil, &rpcError{codeUnauthorized, "agent token refused: " + err.Error()})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		reply(w, http.StatusRequestEntityTooLarge, nil, nil, &rpcError{codeInvalidRequest, "the body is too large"})
		return
	}
	var req rpcRequest
	if t := bytes.TrimSpace(body); len(t) > 0 && t[0] == '[' {
		reply(w, http.StatusOK, nil, nil, &rpcError{codeInvalidRequest, "one message per request; batches are not served"})
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		reply(w, http.StatusOK, nil, nil, &rpcError{codeParse, "the body is not JSON-RPC: " + err.Error()})
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		reply(w, http.StatusOK, req.ID, nil, &rpcError{codeInvalidRequest, "jsonrpc must be 2.0 and method is required"})
		return
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, e := s.dispatch(r.Context(), who, req)
	reply(w, http.StatusOK, req.ID, result, e)
}

func (s *Server) dispatch(ctx context.Context, who Identity, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := ProtocolVersions[0]
		for _, v := range ProtocolVersions {
			if v == p.ProtocolVersion {
				version = v
			}
		}
		return map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo": map[string]any{"name": "ops-records", "version": s.Version}}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		list := []map[string]any{}
		for _, t := range tools {
			if allowed(who.Tier, t.minTier) {
				list = append(list, map[string]any{"name": t.name, "description": t.description, "inputSchema": json.RawMessage(t.schema)})
			}
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{codeInvalidParams, "params must hold name and arguments"}
		}
		for _, t := range tools {
			if t.name == p.Name && allowed(who.Tier, t.minTier) {
				return s.call(ctx, who, t, p.Arguments), nil
			}
		}
		return nil, &rpcError{codeInvalidParams, "unknown tool " + p.Name}
	}
	return nil, &rpcError{codeNoMethod, "unknown method " + req.Method}
}

func allowed(tier, minTier string) bool {
	return minTier == TierViewer || tier == minTier
}

// call runs one tool and returns its MCP result.
func (s *Server) call(ctx context.Context, who Identity, t tool, args json.RawMessage) map[string]any {
	if len(bytes.TrimSpace(args)) == 0 || string(bytes.TrimSpace(args)) == "null" {
		args = json.RawMessage("{}")
	}
	v, err := t.run(ctx, s, who, args)
	if err != nil {
		return map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true}
	}
	text, err := Render(v)
	if err != nil {
		return map[string]any{"content": []map[string]any{{"type": "text", "text": "the answer cannot be written"}}, "isError": true}
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
}

// listKeys are the fields whose list Render shortens, in this order, when an answer is too long.
var listKeys = []string{"items", "events", "signals"}

// Render returns v as JSON after agentguard.CleanValue with no link host allowed, in at most
// agentguard.MaxAnswer characters. A list under one of listKeys keeps its first and its newest
// entries, and "dropped" says how many were left out; any other answer that is still too long
// is cut by agentguard.Fit.
func Render(v any) (string, error) {
	raw, err := marshal(v)
	if err != nil {
		return "", err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return "", err
	}
	generic = agentguard.CleanValue(generic, nil)
	out, err := marshal(generic)
	if err != nil || utf8.RuneCount(out) <= agentguard.MaxAnswer {
		return string(out), err
	}
	if m, ok := generic.(map[string]any); ok {
		for _, key := range listKeys {
			list, ok := m[key].([]any)
			if !ok || len(list) == 0 {
				continue
			}
			sizes := make([]int, len(list))
			total := 0
			for i, e := range list {
				b, err := marshal(e)
				if err != nil {
					return "", err
				}
				sizes[i] = utf8.RuneCount(b) + 1
				total += sizes[i]
			}
			// The room for the list: what the rest of the answer leaves, less the field dropped.
			room := agentguard.MaxAnswer - (utf8.RuneCount(out) - total) - len(`,"dropped":1000000`)
			keep, dropped := agentguard.FitList(sizes, room)
			kept := make([]any, 0, len(keep))
			for _, i := range keep {
				kept = append(kept, list[i])
			}
			m[key], m["dropped"] = kept, dropped
			if out, err = marshal(m); err != nil || utf8.RuneCount(out) <= agentguard.MaxAnswer {
				return string(out), err
			}
		}
	}
	return agentguard.Fit(string(out), agentguard.MaxAnswer), nil
}

func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
