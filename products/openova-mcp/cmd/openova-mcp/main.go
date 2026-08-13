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
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

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
	id, err := s.resolver.Resolve(bearer)
	if err == nil {
		return id, nil
	}
	// #5175 — the MCP is configured with the mothership HANDOVER public key,
	// but real callers present catalyst-api SESSION tokens signed with a
	// deployment-local key the MCP does not hold. When local verify fails and
	// a catalyst-api client is configured, delegate the verdict to
	// catalyst-api's /whoami — it is the session-token authority. Fail-closed:
	// on any whoami error (401 / unreachable / not-verified) fall through to
	// the ORIGINAL local-verify error so a catalyst-api hiccup can never widen
	// the surface.
	wid, werr := s.whoamiFallback(bearer)
	if werr == nil {
		return wid, nil
	}
	// …with ONE exception, and it is not a relaxation: when catalyst-api
	// VERIFIED the session and this instance's own scope pin refused it
	// anyway (#5206 wrong-door, the per-Org org-pin, an absent org scope),
	// that refusal is the better-informed verdict and the local signature
	// miss is a claim the server already holds evidence against. Returning
	// the stale "token signature is invalid: crypto/rsa: verification error"
	// there reports a FALSEHOOD about a token the issuer just vouched for,
	// and points every reader at the key-pinning subsystem instead of the
	// door. That is the whole diagnosis cost of UAT rows 212/213: the rows
	// name NewRS256Resolver as the surface to look at, because that is what
	// the masked error accused. Still an error, still fail-closed — only the
	// REASON changes, and only when a positive whoami verdict exists.
	var verdict *whoamiVerdictError
	if errors.As(werr, &verdict) {
		return nil, werr
	}
	return nil, err
}

// whoamiVerdictError marks a refusal issued AFTER catalyst-api verified the
// session — i.e. the bearer is genuine and this instance's own context/org pin
// is what turned the caller away. It is distinguished from every other whoami
// failure (no client wired, transport error, upstream 401, not-verified)
// precisely because those carry no evidence about the token and must leave the
// original local-verify error standing.
type whoamiVerdictError struct{ err error }

func (e *whoamiVerdictError) Error() string { return e.err.Error() }

func (e *whoamiVerdictError) Unwrap() error { return e.err }

// whoamiFallback resolves identity via catalyst-api's /whoami for tokens the
// local resolver cannot verify (session tokens signed by the deployment-local
// key — #5175). Returns an error (→ caller keeps the original verify failure)
// when no catalyst-api client is wired, the endpoint rejects the token, or
// the session is not verified.
func (s *server) whoamiFallback(bearer string) (*identity.Identity, error) {
	api := s.reg.API()
	if api == nil {
		return nil, errors.New("whoami fallback: no catalyst-api client configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w, err := api.Whoami(ctx, bearer)
	if err != nil {
		return nil, err
	}
	if !w.Verified {
		return nil, errors.New("whoami fallback: session not verified")
	}
	id, ferr := s.resolver.FromWhoami(identity.WhoamiIdentity{
		Email:         w.Email,
		Mode:          w.Mode,
		Tier:          w.Tier,
		Org:           w.Org,
		OrgScoped:     w.OrgScoped,
		DeploymentID:  w.DeploymentID,
		SovereignFQDN: w.SovereignFQDN,
	}, bearer)
	if ferr != nil {
		// Past this line catalyst-api has VOUCHED for the bearer; anything
		// FromWhoami refuses is this instance's own pin decision, not a
		// signature problem. Mark it so resolveBearer surfaces it instead of
		// the local-verify error it supersedes.
		return nil, &whoamiVerdictError{err: ferr}
	}
	return id, nil
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
		// The RS256 verify pubkey rides an OPTIONAL secretKeyRef in the chart
		// (#4228): the source PEM Secret (`catalyst-handover-jwt`) is absent
		// in sovereign-mode catalyst-system (only the JWK mirror
		// `catalyst-handover-jwt-public` exists there) and in an Org namespace
		// without the per-Org seed. An absent OR unparseable key must NEVER
		// crash the pod (#5114): a CrashLoop here trips the harbor-prewarm
		// cutover settle-gate (#4982) and FATALs the whole sovereignty
		// cutover at step-3. Degrade instead — the pod reaches Ready, serves
		// /healthz + /readyz + the empty unauthenticated tools/list, and
		// rejects tools/call 401 — until the key is wired.
		// #5175 / UAT rows 212-213: the verify surface is a SET, not a key.
		// OPENOVA_MCP_RS256_PUBKEY_PEM carries the Secret the chart has always
		// pointed at (`catalyst-handover-jwt-public` key `public.jwk` — which
		// on a Sovereign is the MOTHERSHIP-injected handover key, preserved
		// there by #4450); OPENOVA_MCP_RS256_PUBKEY_SET carries the Sovereign's
		// own catalyst-api signer set (`signers.jwks` on the same Secret). Both
		// are optional and both are unioned — see identity.Resolver.rsaPubs.
		pemStr := strings.TrimSpace(os.Getenv("OPENOVA_MCP_RS256_PUBKEY_PEM"))
		setStr := strings.TrimSpace(os.Getenv("OPENOVA_MCP_RS256_PUBKEY_SET"))
		pubs, perr := parseRSAPublicKeySet(pemStr, setStr)
		switch {
		case pemStr == "" && setStr == "":
			log.Printf("WARNING: verify=rs256 but neither OPENOVA_MCP_RS256_PUBKEY_PEM nor OPENOVA_MCP_RS256_PUBKEY_SET is set — starting in DEGRADED mode: /healthz + /readyz serve, tools/list is the empty unauthenticated surface, tools/call is rejected 401. Wire the Sovereign handover verify pubkey (PKIX PEM, RSA JWK, or a JWKS document) to enable authenticated tools. A missing OPTIONAL verify Secret must never CrashLoop the pod — the cutover settle-gate needs Ready.")
			r = identity.NewDegradedResolver("verify=rs256 but no RS256 pubkey env is set", pin)
		case len(pubs) == 0:
			log.Printf("WARNING: verify=rs256 pubkey present but NO key parsed out of it (%v) — starting in DEGRADED mode (see the absent-key note). Accepted values: a PKIX/PKCS1 RSA public-key PEM (one or more blocks), an RSA JWK ({\"kty\":\"RSA\",\"n\":…,\"e\":…} — the format of the `catalyst-handover-jwt-public` mirror, #5167), or a JWKS document ({\"keys\":[…]}).", perr)
			r = identity.NewDegradedResolver("verify=rs256 pubkey present but unparseable", pin)
		default:
			if perr != nil {
				// Some entries parsed and some did not. Serve with what
				// verified rather than degrade the whole surface, but say so
				// loudly — a silently-dropped key is how this class recurs.
				log.Printf("WARNING: verify=rs256 accepted %d key(s) but at least one entry did not parse: %v", len(pubs), perr)
			}
			log.Printf("verify=rs256 active with %d trusted key(s)", len(pubs))
			r = identity.NewRS256SetResolver(pubs, pin)
		}
	case "hs256":
		// Same #5114 degrade posture: the hs256 secret also rides an OPTIONAL
		// secretKeyRef, so an absent secret must degrade — never crash.
		secret := os.Getenv("OPENOVA_MCP_HS256_SECRET")
		if strings.TrimSpace(secret) == "" {
			log.Printf("WARNING: verify=hs256 but OPENOVA_MCP_HS256_SECRET is empty/absent — starting in DEGRADED mode (see the verify=rs256 note): the pod stays Ready, authenticated tools are unavailable until the secret is wired.")
			r = identity.NewDegradedResolver("verify=hs256 but OPENOVA_MCP_HS256_SECRET is absent", pin)
		} else {
			r = identity.NewHS256Resolver([]byte(secret), pin)
		}
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

// parseRSAPublicKey accepts EITHER a PKIX/PKCS1 RSA public-key PEM OR an RSA
// JWK JSON document ({"kty":"RSA","n":…,"e":…}).
//
// #5167 (north-star unblock): the ONLY verify-pubkey Secret a fresh Sovereign
// actually seeds is the JWK mirror `catalyst-handover-jwt-public` (key
// `public.jwk`) — the PEM Secret `catalyst-handover-jwt` the chart used to
// reference is never created, so verify=rs256 silently started DEGRADED on
// every fresh prov (hw264 live) and tools/call 401'd for everyone, killing the
// Agenity→MCP create_application north star. Accepting the JWK format the
// platform already publishes closes that loop with zero seeder changes; PEM
// stays supported for the per-Org seed / any future PEM source.
func parseRSAPublicKey(s string) (*rsa.PublicKey, error) {
	if strings.HasPrefix(strings.TrimSpace(s), "{") {
		return parseRSAPublicKeyJWK(s)
	}
	return parseRSAPublicKeyPEM(s)
}

// parseRSAPublicKeySet parses every source into ONE deduplicated key set — the
// consumer half of #5175 / UAT rows 212-213 (see identity.Resolver.rsaPubs for
// why a Sovereign needs a set at all).
//
// Each source may be:
//   - a JWKS document `{"keys":[{kty:RSA,n,e}, …]}` — what catalyst-api
//     publishes to `catalyst-handover-jwt-public` key `signers.jwks`, one entry
//     per catalyst-api handoverjwt signer on this Sovereign;
//   - a single RSA JWK `{"kty":"RSA","n":…,"e":…}` — the `public.jwk` mirror
//     (#5167);
//   - one OR MORE concatenated PKIX/PKCS1 PEM blocks.
//
// Returns every key it could parse plus a non-nil error naming what it could
// NOT. Callers serve on a non-empty set and degrade only on an empty one: a
// Secret that gains a malformed entry must not take the whole authenticated
// surface down, and a dropped key must not be silent.
func parseRSAPublicKeySet(sources ...string) ([]*rsa.PublicKey, error) {
	var out []*rsa.PublicKey
	var problems []string
	// Dedupe on the modulus+exponent so the same signer appearing in both
	// sources (the common case once `signers.jwks` includes the injected key)
	// is tried once, not twice.
	seen := map[string]struct{}{}
	add := func(k *rsa.PublicKey) {
		if k == nil || k.N == nil {
			return
		}
		id := k.N.String() + "/" + strconv.Itoa(k.E)
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, k)
	}

	for _, src := range sources {
		src = strings.TrimSpace(src)
		if src == "" {
			continue
		}
		if strings.HasPrefix(src, "{") {
			keys, err := parseRSAJWKDocument(src)
			if err != nil {
				problems = append(problems, err.Error())
				continue
			}
			for _, k := range keys {
				add(k)
			}
			continue
		}
		// PEM: walk every block in the value, not just the first. A Secret key
		// holding two concatenated PUBLIC KEY blocks is a legitimate way to
		// express the set without JSON.
		rest := []byte(src)
		found := 0
		for {
			var block *pem.Block
			block, rest = pem.Decode(rest)
			if block == nil {
				break
			}
			k, err := parsePEMBlockRSAPublicKey(block)
			if err != nil {
				problems = append(problems, err.Error())
				continue
			}
			found++
			add(k)
		}
		if found == 0 {
			problems = append(problems, "RS256 pubkey: no PEM block found")
		}
	}

	if len(problems) > 0 {
		return out, errors.New(strings.Join(problems, "; "))
	}
	return out, nil
}

// parseRSAJWKDocument accepts either a JWKS (`{"keys":[…]}`) or a bare RSA JWK
// and returns every RSA key in it. Non-RSA entries in a JWKS are SKIPPED rather
// than failing the document — a realm JWKS legitimately carries EC keys too.
func parseRSAJWKDocument(s string) ([]*rsa.PublicKey, error) {
	var doc struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal([]byte(s), &doc); err == nil && len(doc.Keys) > 0 {
		out := make([]*rsa.PublicKey, 0, len(doc.Keys))
		var problems []string
		for _, raw := range doc.Keys {
			k, err := parseRSAPublicKeyJWK(string(raw))
			if err != nil {
				// kty != RSA is expected in a mixed JWKS; only surface real
				// decode failures on RSA-shaped entries.
				if !strings.Contains(err.Error(), "kty=") {
					problems = append(problems, err.Error())
				}
				continue
			}
			out = append(out, k)
		}
		if len(out) == 0 {
			if len(problems) > 0 {
				return nil, errors.New(strings.Join(problems, "; "))
			}
			return nil, errors.New("RS256 pubkey: JWKS carries no RSA key")
		}
		return out, nil
	}
	k, err := parseRSAPublicKeyJWK(s)
	if err != nil {
		return nil, err
	}
	return []*rsa.PublicKey{k}, nil
}

// parsePEMBlockRSAPublicKey turns one decoded PEM block into an RSA public key,
// trying PKIX then PKCS1 — the same order parseRSAPublicKeyPEM uses.
func parsePEMBlockRSAPublicKey(block *pem.Block) (*rsa.PublicKey, error) {
	if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if rk, ok := key.(*rsa.PublicKey); ok {
			return rk, nil
		}
		return nil, errors.New("RS256 pubkey: PKIX key is not RSA")
	}
	rk, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("RS256 pubkey: parse: %w", err)
	}
	return rk, nil
}

// parseRSAPublicKeyJWK parses an RSA JWK ({"kty":"RSA","n":…,"e":…}) into an
// rsa.PublicKey. n and e are base64url-without-padding per RFC 7518 §6.3.
func parseRSAPublicKeyJWK(s string) (*rsa.PublicKey, error) {
	var jwk struct {
		Kty string `json:"kty"`
		N   string `json:"n"`
		E   string `json:"e"`
	}
	if err := json.Unmarshal([]byte(s), &jwk); err != nil {
		return nil, fmt.Errorf("RS256 pubkey: JWK parse: %w", err)
	}
	if jwk.Kty != "RSA" {
		return nil, fmt.Errorf("RS256 pubkey: JWK kty=%q, want RSA", jwk.Kty)
	}
	if jwk.N == "" || jwk.E == "" {
		return nil, errors.New("RS256 pubkey: JWK missing n/e")
	}
	nb, err := base64.RawURLEncoding.DecodeString(jwk.N)
	if err != nil {
		return nil, fmt.Errorf("RS256 pubkey: JWK n: %w", err)
	}
	eb, err := base64.RawURLEncoding.DecodeString(jwk.E)
	if err != nil {
		return nil, fmt.Errorf("RS256 pubkey: JWK e: %w", err)
	}
	e := 0
	for _, b := range eb {
		e = e<<8 | int(b)
	}
	if e <= 1 {
		return nil, errors.New("RS256 pubkey: JWK exponent invalid")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}, nil
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
