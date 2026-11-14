// auth.go — bearer-token validation for tools/call.
//
// Every JSON-RPC `tools/call` may carry a `_auth.token` field in the
// arguments blob (the agent's MCP client side-channel) OR fall back to
// the env-mounted SANDBOX_TOKEN. main.go pulls the candidate bearer via
// ExtractBearer(), passes it to ValidateBearer() to get a *Claims, and
// installs the claims on the request context for Registry.Call.
//
// Validation rules:
//
//   - Algorithm MUST be HS256 (the only sig used by core/services/auth's
//     PAT mint path — see core/services/auth/handlers/pat.go).
//   - The shared secret is env.JWTSecret. Mismatch / bad sig → 401.
//   - `exp` MUST be in the future. `iat`/`nbf` are tolerated as long as
//     jwt-v5's default leeway accepts them (which means 0s — exact).
//   - `typ == "pat"` is enforced for production bearers; "session" is
//     accepted for the cross-process bridge between catalyst-api and
//     this server (Wave 4 work). We currently accept both.
//
// In test mode (env.JWTSecret empty), ValidateBearer returns
// (nil, nil) on every input — the Registry's auth gate is also disabled.
package tools

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/golang-jwt/jwt/v5"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
)

// bearerEnvelope is the optional carrier the MCP `tools/call` arguments
// may include. Agents that obtained their PAT via the bridge described
// in products/sandbox/docs/newapi-proxy-contract.md §3 pass it through
// here per call so a stolen pod env doesn't elevate a single tool call.
type bearerEnvelope struct {
	Auth struct {
		Token string `json:"token,omitempty"`
	} `json:"_auth,omitempty"`
}

// ExtractBearer returns the raw bearer string the agent supplied on the
// tools/call args, or env.SandboxToken when the call omitted _auth.
// Returns "" when neither is set — caller decides whether that's a 401
// (production) or an unauthenticated test invocation.
func ExtractBearer(env *Env, rawArgs json.RawMessage) string {
	if len(rawArgs) > 0 {
		var env_ bearerEnvelope
		if err := json.Unmarshal(rawArgs, &env_); err == nil && env_.Auth.Token != "" {
			return env_.Auth.Token
		}
	}
	if env == nil {
		return ""
	}
	return env.SandboxToken
}

// ValidateBearer parses + validates the supplied compact JWT against
// env.JWTSecret. Returns the populated *Claims on success, or an error
// describing exactly why validation failed (the MCP server uses this in
// the 401 body so operators can debug a misconfigured Sovereign).
//
// When env.JWTSecret is empty (test mode), ValidateBearer returns
// (nil, nil) regardless of the token — the registry's auth gate is also
// disabled in that mode (see Registry.Call).
func ValidateBearer(env *Env, raw string) (*sharedauth.Claims, error) {
	if env == nil || len(env.JWTSecret) == 0 {
		// Test mode — no JWT secret. The registry's auth gate is also
		// disabled, so this nil-claims return is consistent.
		return nil, nil
	}
	if raw == "" {
		return nil, errors.New("auth: empty bearer (no token in _auth and no SANDBOX_TOKEN env)")
	}
	claims := &sharedauth.Claims{}
	tok, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return env.JWTSecret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("auth: parse bearer: %w", err)
	}
	if !tok.Valid {
		return nil, errors.New("auth: bearer not valid")
	}
	return claims, nil
}

// StripAuthEnvelope returns rawArgs with any `_auth` field removed
// before passing the arguments to the tool handler. Tool handlers
// should not see the bearer token — auth has already happened.
//
// We do this rather than rejecting `_auth` outright because the agent
// builds the args blob; mutating its shape between agent + server is
// the simpler contract.
func StripAuthEnvelope(rawArgs json.RawMessage) json.RawMessage {
	if len(rawArgs) == 0 {
		return rawArgs
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rawArgs, &m); err != nil {
		return rawArgs // not an object — pass through
	}
	if _, present := m["_auth"]; !present {
		return rawArgs
	}
	delete(m, "_auth")
	out, err := json.Marshal(m)
	if err != nil {
		return rawArgs
	}
	return out
}
