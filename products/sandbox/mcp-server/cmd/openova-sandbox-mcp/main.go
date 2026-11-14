// openova-sandbox-mcp is the per-Sandbox MCP server.
//
// Wire model: the agent process (claude, cursor-agent, qwen-code,
// aider, opencode) launches this binary as a stdio MCP server. JSON-RPC
// 2.0 frames (Content-Length-prefixed, one per message) flow over
// stdin/stdout. Logging goes to stderr only — anything written to
// stdout outside an RPC frame breaks the protocol.
//
// Wave 2 shipped the protocol skeleton + the tool catalogue stubs from
// products/sandbox/docs/architecture.md §3. Wave 8 swapped the stubs
// for real handlers on:
//
//   - gitea.repo.list / gitea.repo.get
//   - gitea.pr.list / gitea.pr.get
//   - k8s.read.get / k8s.read.list / k8s.read.watch
//   - sandbox.session.whoami / sandbox.session.info
//
// Other namespaces (sandbox.db.*, sandbox.auth.*, sandbox.stripe.*,
// sandbox.preview.*, k8s.write.*) remain not_implemented stubs until
// their respective backends ship. Hard rule: k8s.write.* never lands in
// the MCP surface — kubectl-apply is out of scope.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/textproto"
	"os"
	"strconv"

	"github.com/openova-io/openova/products/sandbox/mcp-server/internal/tools"
)

const (
	// protocolVersion is the MCP wire revision this scaffold targets.
	// The number is informational; clients negotiate via `initialize`.
	protocolVersion = "2024-11-05"
	serverName      = "openova-sandbox-mcp"
	serverVersion   = "0.2.0-wave8"
)

// JSON-RPC 2.0 envelopes.

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func main() {
	// Logging to stderr — never to stdout (stdout is the JSON-RPC wire).
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.LUTC)
	log.Printf("%s %s starting (protocol %s)", serverName, serverVersion, protocolVersion)

	env := tools.NewEnvFromOS()
	reg := tools.NewRegistry(env)
	srv := &server{tools: reg, env: env, out: os.Stdout}
	log.Printf("env: org_id=%q sandbox_id=%q namespace=%q sov=%q gitea_url=%q jwt_secret_set=%v",
		env.OrgID, env.SandboxID, env.SandboxNamespace, env.SovereignFQDN, env.GiteaBaseURL, len(env.JWTSecret) > 0)

	in := bufio.NewReader(os.Stdin)
	for {
		msg, err := readFrame(in)
		if err != nil {
			if errors.Is(err, io.EOF) {
				log.Printf("stdin EOF — exiting")
				return
			}
			log.Printf("read frame: %v", err)
			return
		}
		if err := srv.dispatch(msg); err != nil {
			log.Printf("dispatch: %v", err)
		}
	}
}

// server handles a single MCP peer over stdio.
type server struct {
	tools *tools.Registry
	env   *tools.Env
	out   io.Writer
}

func (s *server) dispatch(raw []byte) error {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return s.writeError(nil, -32700, "parse error", err.Error())
	}
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(req)
	case "ping":
		return s.writeResult(req.ID, map[string]any{"pong": true})
	case "notifications/initialized", "notifications/cancelled":
		return nil // notifications: no response
	default:
		return s.writeError(req.ID, -32601, "method not found", req.Method)
	}
}

func (s *server) handleInitialize(req rpcRequest) error {
	return s.writeResult(req.ID, map[string]any{
		"protocolVersion": protocolVersion,
		"serverInfo": map[string]any{
			"name":    serverName,
			"version": serverVersion,
		},
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
	})
}

func (s *server) handleToolsList(req rpcRequest) error {
	return s.writeResult(req.ID, map[string]any{"tools": s.tools.List()})
}

func (s *server) handleToolsCall(req rpcRequest) error {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return s.writeError(req.ID, -32602, "invalid params", err.Error())
	}

	// Wave 8: validate the per-call bearer + install Claims on ctx.
	// In test mode (no SANDBOX_JWT_SECRET), ValidateBearer returns
	// (nil, nil) and the registry's auth gate is bypassed too.
	bearer := tools.ExtractBearer(s.env, params.Arguments)
	claims, err := tools.ValidateBearer(s.env, bearer)
	if err != nil {
		return s.writeError(req.ID, -32001, "unauthenticated", err.Error())
	}
	args := tools.StripAuthEnvelope(params.Arguments)

	ctx := context.Background()
	result, err := s.tools.Call(ctx, params.Name, args, tools.CallOpts{Claims: claims})
	if err != nil {
		return s.writeError(req.ID, -32000, "tool error", err.Error())
	}
	// MCP wraps tool results in {content:[{type:"text", text:"..."}]}
	// — the agent's MCP client expects that envelope. The raw JSON
	// payload from the handler lives inside content[0].text.
	body, _ := json.Marshal(result)
	return s.writeResult(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(body)}},
		"isError": false,
	})
}

func (s *server) writeResult(id json.RawMessage, result any) error {
	return s.writeFrame(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *server) writeError(id json.RawMessage, code int, msg string, data any) error {
	return s.writeFrame(rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg, Data: data},
	})
}

func (s *server) writeFrame(resp rpcResponse) error {
	body, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.out, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = s.out.Write(body)
	return err
}

// readFrame reads one Content-Length-prefixed MCP message from r.
// Returns the raw body bytes.
func readFrame(r *bufio.Reader) ([]byte, error) {
	tp := textproto.NewReader(r)
	header, err := tp.ReadMIMEHeader()
	if err != nil {
		return nil, err
	}
	clStr := header.Get("Content-Length")
	if clStr == "" {
		return nil, errors.New("missing Content-Length")
	}
	cl, err := strconv.Atoi(clStr)
	if err != nil {
		return nil, fmt.Errorf("bad Content-Length: %w", err)
	}
	body := make([]byte, cl)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}
