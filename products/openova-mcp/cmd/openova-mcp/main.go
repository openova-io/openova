// openova-mcp is the OpenOva MCP server (#3988) — first vertical slice.
//
// It exposes the OpenOva product surface (Applications, Environments,
// Organizations) as MCP tools whose surface is RBAC-scoped per user so the
// agent's tool set is IDENTICAL to what that user can do in the console
// UI. It is a THIN facade: every data tool forwards the caller's bearer to
// the LIVE catalyst-api, so the endpoint's own authz is the final word.
//
// Wire model: JSON-RPC 2.0 over one of two transports that share the SAME
// dispatch + auth core (see core.handle):
//
//   - stdio (DEFAULT): NDJSON- or Content-Length-framed on os.Stdin/os.Stdout,
//     the same proven transport the sandbox MCP uses. The bearer is supplied
//     per `tools/call` (and `tools/list`) via the `_auth.token` argument OR
//     the OPENOVA_MCP_BEARER env fallback.
//   - HTTP/SSE (opt-in, #3988 §5 / #899): the MCP Streamable-HTTP transport —
//     a POST /mcp JSON-RPC endpoint + a GET /mcp server→client SSE stream,
//     with the bearer in the `Authorization: Bearer` header, plus /healthz for
//     the chart's probes. Selected by OPENOVA_MCP_HTTP_ADDR (or --http <addr>);
//     when unset the binary runs stdio, byte-for-byte unaffected. This is what
//     the bp-openova-mcp chart's httpTransport.enabled path serves.
//
// Either way the bearer is validated into the shared auth.Claims and resolved
// into the (context, tier, scope) identity that drives both RBAC layers.
// chepherd injection stays DEFERRED to a follow-up (#3988 §4.3).
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
//	OPENOVA_MCP_HTTP_ADDR         when set (e.g. ":8080"), serve the HTTP/SSE
//	                              transport on this address instead of stdio.
//	                              Equivalent to the --http <addr> flag. Unset =
//	                              stdio (default).
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
	c := &core{reg: reg, resolver: resolver, fallbackBearer: os.Getenv("OPENOVA_MCP_BEARER")}
	tenantHostLog := ""
	if api != nil {
		tenantHostLog = api.TenantHost()
	}
	log.Printf("env: catalyst_api=%q context_pin=%q verify=%q bearer_fallback=%v tenant_host=%q",
		apiURL, os.Getenv("OPENOVA_MCP_CONTEXT"), verifyMode(), os.Getenv("OPENOVA_MCP_BEARER") != "", tenantHostLog)

	// Transport selection: HTTP/SSE when an address is configured (env or
	// --http), else stdio (the DEFAULT — byte-for-byte unchanged from the
	// forked-child path agenity bakes). Keeping stdio the default makes the
	// HTTP transport strictly ADDITIVE and matches the chart's
	// httpTransport.enabled opt-in gate.
	if addr := resolveHTTPAddr(os.Args[1:]); addr != "" {
		log.Printf("transport: HTTP/SSE on %s", addr)
		if err := serveHTTP(addr, c); err != nil {
			log.Fatalf("http transport: %v", err)
		}
		return
	}

	log.Printf("transport: stdio")
	serveStdio(c)
}

// serveStdio runs the JSON-RPC-over-stdio loop (the default transport). It is
// the original main loop, unchanged: read a frame, dispatch through the shared
// core, reply in the same framing the peer used (#4111).
func serveStdio(c *core) {
	srv := &server{core: c, out: os.Stdout}
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

// server adapts the shared core to the stdio transport: it owns only the
// framing (the writer + the latched framing mode) and delegates every message
// to core.handle. The auth/dispatch/RBAC logic lives in core so the HTTP
// transport reuses it verbatim.
type server struct {
	core *core

	out io.Writer

	// lineDelimited records the framing the most-recent inbound message
	// used: true = newline-delimited JSON (the MCP stdio spec default),
	// false = LSP Content-Length framing. writeFrame mirrors it so the
	// peer's transport reader can parse the reply (#4111).
	lineDelimited bool
}

func (s *server) dispatch(raw []byte) error {
	// stdio supplies no out-of-band bearer, so core.handle falls back to the
	// per-call _auth.token / OPENOVA_MCP_BEARER path exactly as before.
	resp := s.core.handle(context.Background(), "", raw)
	if resp == nil {
		return nil // notification — no reply
	}
	return s.writeFrame(*resp)
}

// ── transport-agnostic dispatch + auth core ──────────────────────────────

// core is the transport-agnostic MCP dispatch + auth engine. Both the stdio
// transport (server) and the HTTP/SSE transport (httpTransport) call
// core.handle, so the resolve→two-layer-RBAC→dispatch flow is IDENTICAL on
// every wire (#3988). It holds no writer and no framing state.
type core struct {
	reg            *tools.Registry
	resolver       *identity.Resolver
	fallbackBearer string
}

// handle dispatches one raw JSON-RPC message and returns the response to send,
// or nil for a notification (which takes no reply). headerBearer is an
// out-of-band bearer (the HTTP Authorization header); it is "" for stdio, in
// which case the bearer is taken from the per-call _auth.token argument or the
// OPENOVA_MCP_BEARER fallback — preserving the stdio behaviour byte-for-byte.
func (c *core) handle(ctx context.Context, headerBearer string, raw []byte) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return errorResp(nil, -32700, "parse error", err.Error())
	}
	switch req.Method {
	case "initialize":
		return c.handleInitialize(req)
	case "tools/list":
		return c.handleToolsList(headerBearer, req)
	case "tools/call":
		return c.handleToolsCall(ctx, headerBearer, req)
	case "ping":
		return resultResp(req.ID, map[string]any{"pong": true})
	case "notifications/initialized", "notifications/cancelled":
		return nil
	default:
		return errorResp(req.ID, -32601, "method not found", req.Method)
	}
}

func (c *core) handleInitialize(req rpcRequest) *rpcResponse {
	return resultResp(req.ID, map[string]any{
		"protocolVersion": protocolVersion,
		"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
	})
}

// handleToolsList resolves the caller and returns ONLY the tools visible to
// their (context, tier) — layer-1 RBAC. The bearer is read from the out-of-band
// header (HTTP), the `_auth.token` param (stdio), or the env fallback. An
// unauthenticated tools/list returns an empty surface (no identity → no tools).
func (c *core) handleToolsList(headerBearer string, req rpcRequest) *rpcResponse {
	id, _ := c.resolveBearer(effectiveBearer(headerBearer, c.fallbackBearer, req.Params))
	return resultResp(req.ID, map[string]any{"tools": c.reg.List(id)})
}

func (c *core) handleToolsCall(ctx context.Context, headerBearer string, req rpcRequest) *rpcResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errorResp(req.ID, -32602, "invalid params", err.Error())
	}

	id, err := c.resolveBearer(effectiveBearer(headerBearer, c.fallbackBearer, params.Arguments))
	if err != nil {
		return errorResp(req.ID, -32001, "unauthenticated", err.Error())
	}
	args := stripAuthEnvelope(params.Arguments)

	result, callErr := c.reg.Call(ctx, id, params.Name, args)
	if callErr != nil {
		return c.toolError(req.ID, callErr)
	}
	body, _ := json.Marshal(result)
	return resultResp(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(body)}},
		"isError": false,
	})
}

// toolError maps a Call error to the right MCP error code so the parity
// semantics hold on every transport: a forbidden tool returns a distinct code
// carrying the 403-equivalent, an upstream catalyst-api status is surfaced
// verbatim.
func (c *core) toolError(id json.RawMessage, err error) *rpcResponse {
	switch {
	case errors.Is(err, tools.ErrForbidden):
		return errorResp(id, -32003, "forbidden", map[string]any{"status": 403, "detail": err.Error()})
	case errors.Is(err, tools.ErrUnknownTool):
		return errorResp(id, -32601, "unknown tool", err.Error())
	default:
		var apiErr *catalystapi.APIError
		if errors.As(err, &apiErr) {
			return errorResp(id, -32010, "upstream error",
				map[string]any{"status": apiErr.Status, "body": apiErr.Body})
		}
		return errorResp(id, -32000, "tool error", err.Error())
	}
}

func (c *core) resolveBearer(bearer string) (*identity.Identity, error) {
	if bearer == "" {
		return nil, errors.New("no bearer (set Authorization: Bearer, _auth.token in arguments, or OPENOVA_MCP_BEARER)")
	}
	return c.resolver.Resolve(bearer)
}

// effectiveBearer selects the bearer for a call. An out-of-band headerBearer
// (the HTTP Authorization header) wins; otherwise the per-call `_auth.token`
// side-channel or the OPENOVA_MCP_BEARER fallback is used (the stdio path).
// With headerBearer=="" this is identical to the original stdio extraction.
func effectiveBearer(headerBearer, fallback string, raw json.RawMessage) string {
	if h := strings.TrimSpace(strings.TrimPrefix(headerBearer, "Bearer ")); h != "" {
		return h
	}
	return extractBearer(fallback, raw)
}

// resultResp / errorResp build the JSON-RPC response envelopes both transports
// send. Split out so core.handle returns a value the caller frames for its wire.
func resultResp(id json.RawMessage, result any) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func errorResp(id json.RawMessage, code int, msg string, data any) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg, Data: data}}
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
		return identity.NewRS256Resolver(pub, pin), nil
	case "hs256":
		secret := os.Getenv("OPENOVA_MCP_HS256_SECRET")
		if secret == "" {
			return nil, errors.New("verify=hs256 requires OPENOVA_MCP_HS256_SECRET")
		}
		return identity.NewHS256Resolver([]byte(secret), pin), nil
	case "insecure":
		log.Printf("WARNING: verify=insecure — signatures NOT verified (test/trusted-transport only)")
		return identity.NewInsecureResolver(pin), nil
	default:
		return nil, fmt.Errorf("unknown OPENOVA_MCP_VERIFY=%q", verifyMode())
	}
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
