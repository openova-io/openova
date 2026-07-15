// openova-mcp is the OpenOva MCP server (#3988) — first vertical slice.
//
// It exposes the OpenOva product surface (Applications, Environments,
// Organizations) as MCP tools whose surface is RBAC-scoped per user so the
// agent's tool set is IDENTICAL to what that user can do in the console
// UI. It is a THIN facade: every data tool forwards the caller's bearer to
// the LIVE catalyst-api, so the endpoint's own authz is the final word.
//
// Wire model (this slice): JSON-RPC 2.0 over stdio (Content-Length-framed),
// the same proven transport the sandbox MCP uses. The bearer is supplied
// per `tools/call` (and `tools/list`) via the `_auth.token` argument OR the
// OPENOVA_MCP_BEARER env fallback, validated into the shared
// auth.Claims, and resolved into the (context, tier, scope) identity that
// drives both RBAC layers. The streamable-HTTP/SSE transport + chepherd
// injection are DEFERRED to follow-ups (#3988 §4.3, §5).
//
// Env contract:
//
//	OPENOVA_MCP_CATALYST_API_URL  base URL of the catalyst-api / gateway
//	                              (e.g. https://console.openova.io). Required
//	                              for the data tools; whoami works without it.
//	OPENOVA_MCP_CONTEXT           "organization" | "sovereign" — pins the
//	                              instance context per the topology table.
//	                              Empty = derive per-token.
//	OPENOVA_MCP_VERIFY            "rs256" | "hs256" | "insecure" (default
//	                              rs256). Selects bearer verification.
//	OPENOVA_MCP_RS256_PUBKEY_PEM  PEM of the RS256 public key (the
//	                              Sovereign handover-jwt public key) when
//	                              verify=rs256.
//	OPENOVA_MCP_HS256_SECRET      HS256 shared secret when verify=hs256.
//	OPENOVA_MCP_BEARER            fallback bearer when a call omits _auth.
//	OPENOVA_MCP_EXPECTED_ISSUER   optional exact `iss` claim pin — the
//	                              instance-level trusted-realm boundary
//	                              (#3988 §4.3). Empty = no issuer pin.
//	OPENOVA_MCP_ORG_SCOPE         optional Organization slug pin for a
//	                              per-Org instance — a token minted for a
//	                              different Org is rejected outright.
//	OPENOVA_MCP_LISTEN            optional listen address (e.g. ":8080").
//	                              When set the server serves the
//	                              streamable-HTTP transport (#3988 §4.3):
//	                              POST /mcp (JSON-RPC request → JSON
//	                              response, bearer via Authorization) plus
//	                              GET /healthz + /readyz. Empty (default) =
//	                              the stdio transport below — the agenity
//	                              in-pod child contract is unchanged.
package main

import (
	"bufio"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/openova-io/openova/products/openova-mcp/internal/catalystapi"
	"github.com/openova-io/openova/products/openova-mcp/internal/identity"
	"github.com/openova-io/openova/products/openova-mcp/internal/tools"
)

const (
	protocolVersion = "2024-11-05"
	serverName      = "openova-mcp"
	serverVersion   = "0.1.0-slice1"
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

// hostFromURL returns the bare host (no port) of a URL string, or "" if it
// cannot be parsed. Used to derive the X-Tenant-Host for the org-scoped
// install path from OPENOVA_MCP_CATALYST_API_URL (#4116).
func hostFromURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.LUTC)
	log.Printf("%s %s starting (protocol %s)", serverName, serverVersion, protocolVersion)

	resolver, err := buildResolver()
	if err != nil {
		log.Fatalf("resolver: %v", err)
	}
	apiURL := os.Getenv("OPENOVA_MCP_CATALYST_API_URL")
	var api *catalystapi.Client
	if apiURL != "" {
		api = catalystapi.New(apiURL)
		// #4116 — the org-scoped install path (/api/v1/org/applications)
		// passes X-Tenant-Host so the catalyst-api resolves the caller's own
		// Org namespace. Prefer the explicit OPENOVA_MCP_TENANT_HOST; else
		// fall back to the host of the catalyst-api URL itself (in the
		// agenity wiring that URL IS the Org console host, e.g.
		// console.demo.omani.homes), so a per-Org MCP instance is org-aware
		// with zero extra config.
		tenantHost := strings.TrimSpace(os.Getenv("OPENOVA_MCP_TENANT_HOST"))
		if tenantHost == "" {
			tenantHost = hostFromURL(apiURL)
		}
		if tenantHost != "" {
			api.WithTenantHost(tenantHost)
		}
	}
	reg := tools.NewRegistry(api)
	srv := &server{reg: reg, resolver: resolver, fallbackBearer: os.Getenv("OPENOVA_MCP_BEARER"), out: os.Stdout}
	tenantHostLog := ""
	if api != nil {
		tenantHostLog = api.TenantHost()
	}
	log.Printf("env: catalyst_api=%q context_pin=%q verify=%q bearer_fallback=%v tenant_host=%q issuer_pin=%q org_scope=%q listen=%q",
		apiURL, os.Getenv("OPENOVA_MCP_CONTEXT"), verifyMode(), os.Getenv("OPENOVA_MCP_BEARER") != "", tenantHostLog,
		os.Getenv("OPENOVA_MCP_EXPECTED_ISSUER"), os.Getenv("OPENOVA_MCP_ORG_SCOPE"), os.Getenv("OPENOVA_MCP_LISTEN"))

	// ── Streamable-HTTP transport (#3988 §4.3, the standalone Service
	// topology bp-openova-mcp deploys) — selected by OPENOVA_MCP_LISTEN.
	// Stdio below stays the default so the agenity in-pod child contract
	// (#4010/#4097) is byte-identical when the env is unset.
	if listen := strings.TrimSpace(os.Getenv("OPENOVA_MCP_LISTEN")); listen != "" {
		if err := serveHTTP(listen, reg, resolver, os.Getenv("OPENOVA_MCP_BEARER")); err != nil {
			log.Fatalf("http: %v", err)
		}
		return
	}

	in := bufio.NewReader(os.Stdin)
	for {
		msg, lineMode, err := readFrame(in)
		if err != nil {
			if errors.Is(err, io.EOF) {
				log.Printf("stdin EOF — exiting")
				return
			}
			log.Printf("read frame: %v", err)
			return
		}
		// Reply in the SAME framing the peer used. The MCP stdio
		// transport spec is newline-delimited JSON (claude-code, the
		// MCP TS/Python SDKs all send NDJSON); LSP-style Content-Length
		// framing is also accepted for backwards-compat. Latching the
		// mode per-message keeps a mixed stream honest (#4111).
		srv.lineDelimited = lineMode
		if err := srv.dispatch(msg); err != nil {
			log.Printf("dispatch: %v", err)
		}
	}
}

// server handles a single MCP peer over stdio.
type server struct {
	reg            *tools.Registry
	resolver       *identity.Resolver
	fallbackBearer string
	out            io.Writer

	// lineDelimited records the framing the most-recent inbound message
	// used: true = newline-delimited JSON (the MCP stdio spec default),
	// false = LSP Content-Length framing. writeFrame mirrors it so the
	// peer's transport reader can parse the reply (#4111).
	lineDelimited bool
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
		return nil
	default:
		return s.writeError(req.ID, -32601, "method not found", req.Method)
	}
}

func (s *server) handleInitialize(req rpcRequest) error {
	return s.writeResult(req.ID, map[string]any{
		"protocolVersion": protocolVersion,
		"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
	})
}

// handleToolsList resolves the caller and returns ONLY the tools visible
// to their (context, tier) — layer-1 RBAC. The bearer is read from the
// `_meta._auth.token` param OR the env fallback. An unauthenticated
// tools/list returns an empty surface (a caller with no identity sees
// nothing).
func (s *server) handleToolsList(req rpcRequest) error {
	id, _ := s.resolveFromParams(req.Params)
	return s.writeResult(req.ID, map[string]any{"tools": s.reg.List(id)})
}

func (s *server) handleToolsCall(req rpcRequest) error {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return s.writeError(req.ID, -32602, "invalid params", err.Error())
	}

	id, err := s.resolveBearer(extractBearer(s.fallbackBearer, params.Arguments))
	if err != nil {
		return s.writeError(req.ID, -32001, "unauthenticated", err.Error())
	}
	args := stripAuthEnvelope(params.Arguments)

	result, callErr := s.reg.Call(context.Background(), id, params.Name, args)
	if callErr != nil {
		return s.toolError(req.ID, callErr)
	}
	body, _ := json.Marshal(result)
	return s.writeResult(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(body)}},
		"isError": false,
	})
}

// toolError maps a Call error to the right MCP error code so the parity
// semantics hold: a forbidden tool returns a distinct code carrying the
// 403-equivalent, an upstream catalyst-api status is surfaced verbatim.
func (s *server) toolError(id json.RawMessage, err error) error {
	switch {
	case errors.Is(err, tools.ErrForbidden):
		return s.writeError(id, -32003, "forbidden", map[string]any{"status": 403, "detail": err.Error()})
	case errors.Is(err, tools.ErrUnknownTool):
		return s.writeError(id, -32601, "unknown tool", err.Error())
	default:
		var apiErr *catalystapi.APIError
		if errors.As(err, &apiErr) {
			return s.writeError(id, -32010, "upstream error",
				map[string]any{"status": apiErr.Status, "body": apiErr.Body})
		}
		return s.writeError(id, -32000, "tool error", err.Error())
	}
}

// resolveFromParams pulls a bearer out of a generic params blob (the
// `_meta._auth.token` or `_auth.token` side-channel) for tools/list.
func (s *server) resolveFromParams(params json.RawMessage) (*identity.Identity, error) {
	return s.resolveBearer(extractBearer(s.fallbackBearer, params))
}

func (s *server) resolveBearer(bearer string) (*identity.Identity, error) {
	if bearer == "" {
		return nil, errors.New("no bearer (set _auth.token in arguments or OPENOVA_MCP_BEARER)")
	}
	return s.resolver.Resolve(bearer)
}

// ── bearer plumbing (mirrors the sandbox MCP _auth envelope) ─────────────

func extractBearer(fallback string, rawArgs json.RawMessage) string {
	if len(rawArgs) > 0 {
		// Accept both top-level `_auth.token` and `_meta._auth.token`.
		var env struct {
			Auth struct {
				Token string `json:"token"`
			} `json:"_auth"`
			Meta struct {
				Auth struct {
					Token string `json:"token"`
				} `json:"_auth"`
			} `json:"_meta"`
		}
		if err := json.Unmarshal(rawArgs, &env); err == nil {
			if env.Auth.Token != "" {
				return env.Auth.Token
			}
			if env.Meta.Auth.Token != "" {
				return env.Meta.Auth.Token
			}
		}
	}
	return fallback
}

func stripAuthEnvelope(rawArgs json.RawMessage) json.RawMessage {
	if len(rawArgs) == 0 {
		return rawArgs
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rawArgs, &m); err != nil {
		return rawArgs
	}
	if _, ok := m["_auth"]; !ok {
		return rawArgs
	}
	delete(m, "_auth")
	out, err := json.Marshal(m)
	if err != nil {
		return rawArgs
	}
	return out
}

// ── resolver construction ────────────────────────────────────────────────

func verifyMode() string {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("OPENOVA_MCP_VERIFY")))
	if v == "" {
		return "rs256"
	}
	return v
}

func contextPin() identity.Context {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OPENOVA_MCP_CONTEXT"))) {
	case "organization", "org":
		return identity.ContextOrganization
	case "sovereign":
		return identity.ContextSovereign
	default:
		return ""
	}
}

func buildResolver() (*identity.Resolver, error) {
	pin := contextPin()
	var r *identity.Resolver
	switch verifyMode() {
	case "rs256":
		pemStr := os.Getenv("OPENOVA_MCP_RS256_PUBKEY_PEM")
		if pemStr == "" {
			return nil, errors.New("verify=rs256 requires OPENOVA_MCP_RS256_PUBKEY_PEM")
		}
		pub, err := parseRSAPublicKeyPEM(pemStr)
		if err != nil {
			return nil, err
		}
		r = identity.NewRS256Resolver(pub, pin)
	case "hs256":
		secret := os.Getenv("OPENOVA_MCP_HS256_SECRET")
		if secret == "" {
			return nil, errors.New("verify=hs256 requires OPENOVA_MCP_HS256_SECRET")
		}
		r = identity.NewHS256Resolver([]byte(secret), pin)
	case "insecure":
		log.Printf("WARNING: verify=insecure — signatures NOT verified (test/trusted-transport only)")
		r = identity.NewInsecureResolver(pin)
	default:
		return nil, fmt.Errorf("unknown OPENOVA_MCP_VERIFY=%q", verifyMode())
	}
	// Instance-level trust pins (#3988 §4.3): the exact issuer this
	// instance trusts + the Org scope a per-Org instance is confined to.
	return r.WithExpectedIssuer(os.Getenv("OPENOVA_MCP_EXPECTED_ISSUER")).
		WithOrgPin(os.Getenv("OPENOVA_MCP_ORG_SCOPE")), nil
}

func parseRSAPublicKeyPEM(pemStr string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("RS256 pubkey: no PEM block found")
	}
	if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if rk, ok := key.(*rsa.PublicKey); ok {
			return rk, nil
		}
		return nil, errors.New("RS256 pubkey: PKIX key is not RSA")
	}
	// Fall back to PKCS1.
	rk, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("RS256 pubkey: parse: %w", err)
	}
	return rk, nil
}

// ── framing (identical to the proven sandbox MCP transport) ──────────────

func (s *server) writeResult(id json.RawMessage, result any) error {
	return s.writeFrame(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *server) writeError(id json.RawMessage, code int, msg string, data any) error {
	return s.writeFrame(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg, Data: data}})
}

func (s *server) writeFrame(resp rpcResponse) error {
	body, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	// Newline-delimited JSON when the peer spoke NDJSON (the MCP stdio
	// spec — claude-code + the official SDKs); LSP Content-Length framing
	// otherwise (#4111). A reply in the wrong framing is silently
	// undecodable by the peer's transport reader → "failed to connect".
	if s.lineDelimited {
		if _, err := s.out.Write(body); err != nil {
			return err
		}
		_, err = s.out.Write([]byte("\n"))
		return err
	}
	if _, err := fmt.Fprintf(s.out, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = s.out.Write(body)
	return err
}

// readFrame reads one JSON-RPC message off the stdio transport. It
// auto-detects the framing per-message (#4111): a frame whose first
// non-blank byte is '{' or '[' is a newline-delimited JSON message (the
// MCP stdio spec — claude-code, the MCP TS/Python SDKs); anything else
// is parsed as an LSP-style Content-Length-prefixed frame. The returned
// bool is true for NDJSON so the caller can reply in kind.
func readFrame(r *bufio.Reader) ([]byte, bool, error) {
	// Skip blank separator lines that some peers emit between frames,
	// then peek the first content byte to choose the framing.
	for {
		b, err := r.Peek(1)
		if err != nil {
			return nil, false, err
		}
		if b[0] == '\r' || b[0] == '\n' {
			if _, err := r.ReadByte(); err != nil {
				return nil, false, err
			}
			continue
		}
		break
	}

	first, err := r.Peek(1)
	if err != nil {
		return nil, false, err
	}

	// NDJSON: a single line that is itself a complete JSON value.
	if first[0] == '{' || first[0] == '[' {
		line, err := r.ReadBytes('\n')
		if err != nil && len(line) == 0 {
			return nil, false, err
		}
		// ReadBytes may return the final line without a trailing '\n' at
		// EOF; that's still a complete message. Trim the line terminator.
		line = []byte(strings.TrimRight(string(line), "\r\n"))
		if len(line) == 0 {
			// Defensive: a stray blank slipped through — recurse.
			return readFrame(r)
		}
		return line, true, nil
	}

	// LSP Content-Length framing (backwards-compat).
	tp := textproto.NewReader(r)
	header, err := tp.ReadMIMEHeader()
	if err != nil {
		return nil, false, err
	}
	clStr := header.Get("Content-Length")
	if clStr == "" {
		return nil, false, errors.New("missing Content-Length")
	}
	cl, err := strconv.Atoi(clStr)
	if err != nil {
		return nil, false, fmt.Errorf("bad Content-Length: %w", err)
	}
	body := make([]byte, cl)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, false, err
	}
	return body, false, nil
}
