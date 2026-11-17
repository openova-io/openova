// Package agenttoken issues agent tokens and confines them at catalyst-api.
//
// An agent token is the short-lived identity of one agent of one
// Organization. It is a JWT signed RS256 by the session signer of this
// pod (internal/handoverjwt). It differs from a session in three ways: its
// header names the key (kid), it carries the audience claim aud, and it
// carries typ "agent". The format, the two routes and the checks are
// those of docs/contracts/model-contract.md section 2:
//
//   - POST /api/v1/internal/agent-tokens (HandleIssue). The caller is an
//     in-cluster workload: its projected ServiceAccount token is judged by
//     a TokenReview (internal/sacaller), and its user name must be the
//     caller of one entry of CATALYST_AGENT_TOKEN_CALLERS. The entry names
//     the Organizations, the agents and the highest tier it may ask for.
//   - GET /api/v1/auth/signers.jwks (HandleJWKS). The public key of the
//     signer of this pod, with its key identifier. No authentication.
//   - Restrict, the middleware of the session route group after the
//     Organization scope guard. An agent token whose audience lacks
//     catalyst-api is refused; it may read; it may change state only on a
//     route of MutationsAllowed.
//
// The token is never logged and never stored. Every issue writes one audit
// event of type agent-token.issue.
package agenttoken

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"k8s.io/client-go/kubernetes"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/sacaller"
)

const (
	// CallersEnv names the workloads that may ask for a token: a JSON list
	// of Caller. The chart sets it from the value agentTokens.callers.
	CallersEnv = "CATALYST_AGENT_TOKEN_CALLERS"

	// Type is the claim typ of an agent token.
	Type = "agent"

	// The three audiences of every agent token.
	AudienceGateway = "model-gateway"
	AudienceTools   = "platform-tools"
	AudienceAPI     = "catalyst-api"

	// AuditType is the audit-type of the event every issue writes.
	AuditType = "agent-token.issue"

	// The life of a token: DefaultTTL when the request names none, never
	// below MinTTL or above MaxTTL.
	DefaultTTL = 1800 * time.Second
	MinTTL     = 60 * time.Second
	MaxTTL     = 3600 * time.Second

	// role is the claim role of every token: the value sessions carry.
	role = "openova-user"
	// maxBody is the largest request body read.
	maxBody = 1 << 16
	// wildcard in an entry's organizations or agents admits every value.
	wildcard = "*"
)

var (
	// agentName is the form of an agent name.
	agentName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)
	// orgSlug is the form of an Organization slug: a DNS label, because an
	// Organization's Applications live in the namespace of that name.
	orgSlug = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// tierRank orders the two tiers an agent token may carry.
	tierRank = map[string]int{"viewer": 1, "developer": 2}
	// callerKeys are the keys of an entry of CallersEnv, all required.
	callerKeys = []string{"agents", "caller", "maxTier", "organizations"}
)

// Caller is one entry of CallersEnv.
type Caller struct {
	// Caller is the TokenReview user name of the workload,
	// system:serviceaccount:<namespace>:<name>.
	Caller string `json:"caller"`
	// Organizations it may ask for: slugs, or ["*"].
	Organizations []string `json:"organizations"`
	// MaxTier is the highest tier it may ask for: viewer or developer.
	MaxTier string `json:"maxTier"`
	// Agents it may ask for: names, or ["*"].
	Agents []string `json:"agents"`
}

// ParseCallers reads the value of CallersEnv. Every entry has exactly the
// four keys of Caller, a caller that no other entry names, at least one
// Organization and one agent, each "*" or of the right form, and a maxTier
// of viewer or developer. Anything else is an error, and the issue route
// then refuses every request.
func ParseCallers(value string) ([]Caller, error) {
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return nil, fmt.Errorf("%s is not a JSON list of objects: %v", CallersEnv, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("%s is not a JSON list of objects", CallersEnv)
	}
	out := make([]Caller, 0, len(raw))
	seen := map[string]bool{}
	for i, fields := range raw {
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		if !sameKeys(keys, callerKeys) {
			return nil, fmt.Errorf("%s entry %d: the keys must be exactly %s", CallersEnv, i, strings.Join(callerKeys, ", "))
		}
		var c Caller
		data, _ := json.Marshal(fields)
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("%s entry %d: %v", CallersEnv, i, err)
		}
		switch {
		case strings.TrimSpace(c.Caller) == "" || c.Caller != strings.TrimSpace(c.Caller):
			return nil, fmt.Errorf("%s entry %d: caller is empty or has blanks", CallersEnv, i)
		case seen[c.Caller]:
			return nil, fmt.Errorf("%s entry %d: caller %s is named twice", CallersEnv, i, c.Caller)
		case tierRank[c.MaxTier] == 0:
			return nil, fmt.Errorf("%s entry %d: maxTier %q is not viewer or developer", CallersEnv, i, c.MaxTier)
		case !validList(c.Organizations, orgSlug):
			return nil, fmt.Errorf("%s entry %d: organizations must be [\"*\"] or a list of slugs", CallersEnv, i)
		case !validList(c.Agents, agentName):
			return nil, fmt.Errorf("%s entry %d: agents must be [\"*\"] or a list of agent names", CallersEnv, i)
		}
		seen[c.Caller] = true
		out = append(out, c)
	}
	return out, nil
}

func sameKeys(have, want []string) bool {
	if len(have) != len(want) {
		return false
	}
	for _, w := range want {
		found := false
		for _, h := range have {
			found = found || h == w
		}
		if !found {
			return false
		}
	}
	return true
}

func validList(list []string, form *regexp.Regexp) bool {
	if len(list) == 0 {
		return false
	}
	for _, v := range list {
		if v != wildcard && !form.MatchString(v) {
			return false
		}
	}
	return true
}

func listed(list []string, v string) bool {
	for _, x := range list {
		if x == wildcard || x == v {
			return true
		}
	}
	return false
}

// permits returns nil when the entry may ask for this token, and otherwise
// says why not.
func (c Caller) permits(org, agent, tier string) error {
	switch {
	case !listed(c.Organizations, org):
		return fmt.Errorf("the entry of %s does not name Organization %s", c.Caller, org)
	case !listed(c.Agents, agent):
		return fmt.Errorf("the entry of %s does not name agent %s", c.Caller, agent)
	case tierRank[tier] > tierRank[c.MaxTier]:
		return fmt.Errorf("the entry of %s allows tier %s at most", c.Caller, c.MaxTier)
	}
	return nil
}

// RealmAccess is the claim realm_access of a token.
type RealmAccess struct {
	Roles []string `json:"roles"`
}

// Claims is the claim set of an agent token, exactly section 2.1 of
// docs/contracts/model-contract.md: iss, sub, aud, exp, iat and jti from
// RegisteredClaims (NotBefore stays unset), and the ten below.
type Claims struct {
	jwt.RegisteredClaims
	Typ           string      `json:"typ"`
	Org           string      `json:"org"`
	OrgID         string      `json:"org_id"`
	Agent         string      `json:"agent"`
	Tier          string      `json:"tier"`
	RealmAccess   RealmAccess `json:"realm_access"`
	Role          string      `json:"role"`
	Email         string      `json:"email"`
	EmailVerified bool        `json:"email_verified"`
	Caller        string      `json:"caller"`
}

// Clients returns the cluster client that the TokenReview runs with.
type Clients func() (kubernetes.Interface, error)

// Tokens serves the issue route and the key set route.
type Tokens struct {
	signer  *handoverjwt.Signer
	clients Clients
	audit   *audit.Bus
	log     *slog.Logger
	now     func() time.Time
}

// New returns the routes of agent tokens. signer is the session signer of
// this pod; without it both routes answer 503 signer-unavailable. clients
// gives the client the TokenReview runs with. bus receives the audit event
// of every issue; nil writes none.
func New(signer *handoverjwt.Signer, clients Clients, bus *audit.Bus, log *slog.Logger) *Tokens {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Tokens{signer: signer, clients: clients, audit: bus, log: log, now: time.Now}
}

// issuer is the claim iss: the session issuer of this Sovereign, the rule
// of pinIssuer in internal/handler/auth.go (CATALYST_PIN_ISSUER, else
// handoverjwt.OwnIssuer). A pod without SOVEREIGN_FQDN is not a Sovereign
// and issues no agent token.
func issuer() (string, error) {
	if strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN")) == "" {
		return "", errors.New("SOVEREIGN_FQDN is empty: this pod is not a Sovereign")
	}
	if v := strings.TrimSpace(os.Getenv("CATALYST_PIN_ISSUER")); v != "" {
		return v, nil
	}
	return handoverjwt.OwnIssuer(), nil
}

// issueRequest is the body of the issue route.
type issueRequest struct {
	Organization string `json:"organization"`
	Agent        string `json:"agent"`
	Tier         string `json:"tier"`
	TTLSeconds   *int   `json:"ttlSeconds"`
}

// HandleIssue serves POST /api/v1/internal/agent-tokens.
//
//   - 503 agent-tokens-misconfigured: CallersEnv is unset or does not
//     parse, or SOVEREIGN_FQDN is empty. It never falls back to allowing.
//   - 503 signer-unavailable: this pod has no signer, or signing failed.
//   - 401 missing-bearer: no `Authorization: Bearer` header.
//   - 502 token-review-failed: the TokenReview did not accept the token.
//   - 403 unauthorized-sa: the caller is in no entry.
//   - 422 invalid-request: the body is not the request of the contract.
//   - 403 caller-not-allowed: the Organization, the agent or the tier is
//     outside the caller's entry.
//   - 200 {"token": "<jwt>", "expiresAt": "<RFC 3339 time>"}.
func (t *Tokens) HandleIssue(w http.ResponseWriter, r *http.Request) {
	value, set := os.LookupEnv(CallersEnv)
	callers, err := ParseCallers(value)
	if !set {
		err = errors.New(CallersEnv + " is not set")
	}
	iss, issErr := issuer()
	if err == nil {
		err = issErr
	}
	if err != nil {
		t.log.Warn("agent token: the issue route is misconfigured", "err", err)
		writeError(w, http.StatusServiceUnavailable, "agent-tokens-misconfigured", err.Error())
		return
	}
	if t.signer == nil {
		writeError(w, http.StatusServiceUnavailable, "signer-unavailable", "this pod has no session signer")
		return
	}
	bearer := bearerOf(r)
	if bearer == "" {
		writeError(w, http.StatusUnauthorized, "missing-bearer", "Authorization: Bearer <serviceaccount-token> header is required")
		return
	}
	var core kubernetes.Interface
	if t.clients != nil {
		if c, cErr := t.clients(); cErr == nil {
			core = c
		}
	}
	user, err := sacaller.Validate(r.Context(), core, bearer)
	if err != nil {
		writeError(w, http.StatusBadGateway, "token-review-failed", err.Error())
		return
	}
	var entry *Caller
	for i := range callers {
		if callers[i].Caller == user {
			entry = &callers[i]
		}
	}
	if entry == nil {
		t.log.Info("agent token: refused a caller", "caller", user)
		writeError(w, http.StatusForbidden, "unauthorized-sa", fmt.Sprintf("token resolved to %q, which no entry of %s names", user, CallersEnv))
		return
	}
	req, ttl, err := readRequest(w, r)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid-request", err.Error())
		return
	}
	if err := entry.permits(req.Organization, req.Agent, req.Tier); err != nil {
		t.log.Info("agent token: refused a request", "caller", user, "why", err.Error())
		writeError(w, http.StatusForbidden, "caller-not-allowed", err.Error())
		return
	}
	token, claims, err := t.mint(iss, user, req.Organization, req.Agent, req.Tier, ttl)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "signer-unavailable", err.Error())
		return
	}
	if t.audit != nil {
		t.audit.Publish(r.Context(), audit.Event{
			AuditType:  AuditType,
			Actor:      user,
			TargetUser: claims.Subject,
			Tier:       req.Tier,
			Detail:     fmt.Sprintf("caller=%s organization=%s agent=%s tier=%s jti=%s", user, req.Organization, req.Agent, req.Tier, claims.ID),
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{
		"token":     token,
		"expiresAt": claims.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// readRequest decodes and checks the body of the issue route.
func readRequest(w http.ResponseWriter, r *http.Request) (issueRequest, time.Duration, error) {
	var req issueRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, 0, fmt.Errorf("the body is not the JSON object of the issue route: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return req, 0, errors.New("the body holds more than one JSON object")
	}
	ttl := DefaultTTL
	if req.TTLSeconds != nil {
		ttl = time.Duration(*req.TTLSeconds) * time.Second
	}
	switch {
	case !orgSlug.MatchString(req.Organization):
		return req, 0, errors.New("organization must be an Organization slug")
	case !agentName.MatchString(req.Agent):
		return req, 0, errors.New("agent must match ^[a-z][a-z0-9-]{1,62}$")
	case tierRank[req.Tier] == 0:
		return req, 0, errors.New("tier must be viewer or developer")
	case ttl < MinTTL || ttl > MaxTTL:
		return req, 0, errors.New("ttlSeconds must be an integer from 60 to 3600")
	}
	return req, ttl, nil
}

// mint signs the token of section 2.1 of the contract.
func (t *Tokens) mint(iss, caller, org, agent, tier string, ttl time.Duration) (string, Claims, error) {
	jti, err := uuid.NewRandom()
	if err != nil {
		return "", Claims{}, fmt.Errorf("generate jti: %w", err)
	}
	now := t.now().UTC().Truncate(time.Second)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    iss,
			Subject:   "agent:" + org + "/" + agent,
			Audience:  jwt.ClaimStrings{AudienceGateway, AudienceTools, AudienceAPI},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			ID:        jti.String(),
		},
		Typ:           Type,
		Org:           org,
		OrgID:         org,
		Agent:         agent,
		Tier:          tier,
		RealmAccess:   RealmAccess{Roles: []string{"catalyst-" + tier}},
		Role:          role,
		Email:         agent + "@" + org + ".agents.invalid",
		EmailVerified: false,
		Caller:        caller,
	}
	token, err := t.signer.SignCustomClaimsWithKeyID(claims)
	if err != nil {
		return "", Claims{}, err
	}
	return token, claims, nil
}

// HandleJWKS serves GET /api/v1/auth/signers.jwks: one key, the public key
// of the signer of this pod with its key identifier. Public material only.
func (t *Tokens) HandleJWKS(w http.ResponseWriter, _ *http.Request) {
	if t.signer == nil {
		writeError(w, http.StatusServiceUnavailable, "signer-unavailable", "this pod has no session signer")
		return
	}
	jwk, err := t.signer.PublicJWKWithKeyID()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "signer-unavailable", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "max-age=300")
	writeJSON(w, http.StatusOK, map[string][]json.RawMessage{"keys": {jwk}})
}

// bearerOf returns the token of an `Authorization: Bearer` header, or "".
func bearerOf(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, map[string]string{"error": code, "detail": detail})
}
