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
//     An entry of the own-Organization form admits one ServiceAccount name
//     in every namespace of an Organization, for that Organization only.
//     A request may name an acting person: the token then carries the
//     claim acting_person, written only when the person holds tier admin
//     in the Organization at the time of issue, a tier read from the
//     Organization object and never from the request.
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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/sacaller"
)

const (
	// CallersEnv names the workloads that may ask for a token: a JSON list
	// of Caller. The chart sets it from the value agentTokens.callers.
	CallersEnv = "CATALYST_AGENT_TOKEN_CALLERS"

	// OwnOrganization stands for the Organization of the namespace the
	// caller runs in. An entry whose caller is
	// system:serviceaccount:{organization}:<name> and whose organizations
	// are exactly ["{organization}"] admits the ServiceAccount <name> of
	// every namespace of an Organization, for that Organization only.
	OwnOrganization = "{organization}"

	// OrganizationLabel marks a namespace of an Organization; its value is
	// the slug. The organization controller writes it on the namespace of
	// each Organization.
	OrganizationLabel = "openova.io/organization"

	// Type is the claim typ of an agent token.
	Type = "agent"

	// The three audiences of every agent token.
	AudienceGateway = "model-gateway"
	AudienceTools   = "platform-tools"
	AudienceAPI     = "catalyst-api"

	// AuditType is the audit-type of the event every issue writes.
	AuditType = "agent-token.issue"

	// ActingTier is the tier of the claim acting_person: a person of tier
	// admin or owner in the Organization is the person of an agent token
	// acting at tier admin.
	ActingTier = "admin"

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
	// accountPrefix starts the TokenReview user name of a ServiceAccount.
	accountPrefix = "system:serviceaccount:"
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
	// personEmail is the form of the email address of an acting person.
	personEmail = regexp.MustCompile(`^[^@\s]{1,64}@[^@\s]{1,189}$`)
	// actingRoles are the roles of an entry of spec.owners of an
	// Organization that hold tier admin there.
	actingRoles = map[string]bool{"admin": true, "owner": true}
	// organizationResource is the cluster-scoped Organization object; its
	// name is the slug, and spec.owners lists its people and their roles.
	organizationResource = schema.GroupVersionResource{Group: "orgs.openova.io", Version: "v1", Resource: "organizations"}
)

// Caller is one entry of CallersEnv.
type Caller struct {
	// Caller is the TokenReview user name of the workload,
	// system:serviceaccount:<namespace>:<name>, or of the own-Organization
	// form system:serviceaccount:{organization}:<name>.
	Caller string `json:"caller"`
	// Organizations it may ask for: slugs, or ["*"]; ["{organization}"]
	// for the own-Organization form.
	Organizations []string `json:"organizations"`
	// MaxTier is the highest tier it may ask for: viewer or developer.
	MaxTier string `json:"maxTier"`
	// Agents it may ask for: names, or ["*"].
	Agents []string `json:"agents"`
	// account is the ServiceAccount name of an entry of the
	// own-Organization form, and "" for an entry that names its caller.
	account string
}

// ParseCallers reads the value of CallersEnv. Every entry has exactly the
// four keys of Caller, a caller that no other entry names, at least one
// Organization and one agent, each "*" or of the right form, and a maxTier
// of viewer or developer. OwnOrganization appears only in an entry of the
// own-Organization form (ownAccount). Anything else is an error, and the
// issue route then refuses every request.
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
		account, ownErr := ownAccount(c)
		switch {
		case strings.TrimSpace(c.Caller) == "" || c.Caller != strings.TrimSpace(c.Caller):
			return nil, fmt.Errorf("%s entry %d: caller is empty or has blanks", CallersEnv, i)
		case seen[c.Caller]:
			return nil, fmt.Errorf("%s entry %d: caller %s is named twice", CallersEnv, i, c.Caller)
		case tierRank[c.MaxTier] == 0:
			return nil, fmt.Errorf("%s entry %d: maxTier %q is not viewer or developer", CallersEnv, i, c.MaxTier)
		case ownErr != nil:
			return nil, fmt.Errorf("%s entry %d: %v", CallersEnv, i, ownErr)
		case account == "" && !validList(c.Organizations, orgSlug):
			return nil, fmt.Errorf("%s entry %d: organizations must be [\"*\"] or a list of slugs", CallersEnv, i)
		case !validList(c.Agents, agentName):
			return nil, fmt.Errorf("%s entry %d: agents must be [\"*\"] or a list of agent names", CallersEnv, i)
		}
		seen[c.Caller] = true
		c.account = account
		out = append(out, c)
	}
	return out, nil
}

// ownAccount returns the ServiceAccount name of an entry of the
// own-Organization form, "" for an entry that names its caller exactly, and
// an error when the entry uses OwnOrganization in any other way: anywhere
// but the whole namespace of its caller, or with organizations other than
// exactly [OwnOrganization].
func ownAccount(c Caller) (string, error) {
	used := strings.Contains(c.Caller, OwnOrganization)
	for _, o := range c.Organizations {
		used = used || o == OwnOrganization
	}
	if !used {
		return "", nil
	}
	name := strings.TrimPrefix(c.Caller, accountPrefix+OwnOrganization+":")
	switch {
	case name == c.Caller || !orgSlug.MatchString(name):
		return "", fmt.Errorf("an entry with %s has the caller %s%s:<ServiceAccount name>", OwnOrganization, accountPrefix, OwnOrganization)
	case len(c.Organizations) != 1 || c.Organizations[0] != OwnOrganization:
		return "", fmt.Errorf("an entry with the caller %s has the organizations [%q] and no other", c.Caller, OwnOrganization)
	}
	return name, nil
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
// says why not. own is the Organization of the caller's namespace, read for
// an entry of the own-Organization form.
func (c Caller) permits(own, org, agent, tier string) error {
	switch {
	case c.account != "" && org != own:
		return fmt.Errorf("the entry of %s admits only the Organization of the caller's namespace, %s, not %s", c.Caller, own, org)
	case c.account == "" && !listed(c.Organizations, org):
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
// RegisteredClaims (NotBefore stays unset), the ten below, and
// acting_person only on a token issued for an acting person.
type Claims struct {
	jwt.RegisteredClaims
	Typ           string             `json:"typ"`
	Org           string             `json:"org"`
	OrgID         string             `json:"org_id"`
	Agent         string             `json:"agent"`
	Tier          string             `json:"tier"`
	RealmAccess   RealmAccess        `json:"realm_access"`
	Role          string             `json:"role"`
	Email         string             `json:"email"`
	EmailVerified bool               `json:"email_verified"`
	Caller        string             `json:"caller"`
	ActingPerson  *auth.ActingPerson `json:"acting_person,omitempty"`
}

// Clients returns the cluster client that the TokenReview runs with.
type Clients func() (kubernetes.Interface, error)

// Organizations returns the client that reads Organization objects, where
// the tier of an acting person is read.
type Organizations func() (dynamic.Interface, error)

// Tokens serves the issue route and the key set route.
type Tokens struct {
	signer  *handoverjwt.Signer
	clients Clients
	orgs    Organizations
	audit   *audit.Bus
	log     *slog.Logger
	now     func() time.Time
}

// New returns the routes of agent tokens. signer is the session signer of
// this pod; without it both routes answer 503 signer-unavailable. clients
// gives the client the TokenReview runs with. bus receives the audit event
// of every issue; nil writes none. Organization objects are read with a
// client of the cluster this pod runs in.
func New(signer *handoverjwt.Signer, clients Clients, bus *audit.Bus, log *slog.Logger) *Tokens {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Tokens{signer: signer, clients: clients, orgs: inClusterOrganizations, audit: bus, log: log, now: time.Now}
}

var (
	inClusterOrgsMu sync.Mutex
	inClusterOrgs   dynamic.Interface
)

// inClusterOrganizations is the Organizations of the cluster this pod runs
// in. It builds the client on first use and keeps it once it is built.
func inClusterOrganizations() (dynamic.Interface, error) {
	inClusterOrgsMu.Lock()
	defer inClusterOrgsMu.Unlock()
	if inClusterOrgs != nil {
		return inClusterOrgs, nil
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	inClusterOrgs = dyn
	return dyn, nil
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
	// ActingPerson is the email address of the person the agent acts for;
	// optional. It names the person and never the person's tier.
	ActingPerson string `json:"actingPerson"`
}

// HandleIssue serves POST /api/v1/internal/agent-tokens.
//
//   - 503 agent-tokens-misconfigured: CallersEnv is unset or does not
//     parse, or SOVEREIGN_FQDN is empty. It never falls back to allowing.
//   - 503 signer-unavailable: this pod has no signer, or signing failed.
//   - 401 missing-bearer: no `Authorization: Bearer` header.
//   - 502 token-review-failed: the TokenReview did not accept the token.
//   - 403 unauthorized-sa: the caller is in no entry, or its entry is of
//     the own-Organization form and its namespace is not a namespace of an
//     Organization.
//   - 422 invalid-request: the body is not the request of the contract.
//   - 403 caller-not-allowed: the Organization, the agent or the tier is
//     outside the caller's entry; for the own-Organization form, any
//     Organization but that of the caller's namespace.
//   - 403 acting-person-not-admin: the request names an acting person who
//     holds neither admin nor owner in spec.owners of the Organization,
//     or the Organization does not exist.
//   - 502 organization-read-failed: the Organization could not be read.
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
	entry, own, why := entryOf(r.Context(), core, callers, user)
	if entry == nil {
		t.log.Info("agent token: refused a caller", "caller", user, "why", why)
		writeError(w, http.StatusForbidden, "unauthorized-sa", fmt.Sprintf("token resolved to %q, which no entry of %s names%s", user, CallersEnv, why))
		return
	}
	req, ttl, err := readRequest(w, r)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid-request", err.Error())
		return
	}
	if err := entry.permits(own, req.Organization, req.Agent, req.Tier); err != nil {
		t.log.Info("agent token: refused a request", "caller", user, "why", err.Error())
		writeError(w, http.StatusForbidden, "caller-not-allowed", err.Error())
		return
	}
	var acting *auth.ActingPerson
	if req.ActingPerson != "" {
		role, err := t.roleIn(r.Context(), req.Organization, req.ActingPerson)
		if err != nil {
			writeError(w, http.StatusBadGateway, "organization-read-failed", err.Error())
			return
		}
		if !actingRoles[role] {
			t.log.Info("agent token: refused an acting person", "caller", user, "organization", req.Organization, "role", role)
			writeError(w, http.StatusForbidden, "acting-person-not-admin", fmt.Sprintf("the acting person holds %q in Organization %s, not admin or owner", role, req.Organization))
			return
		}
		acting = &auth.ActingPerson{Email: req.ActingPerson, Tier: ActingTier}
	}
	token, claims, err := t.mint(iss, user, req.Organization, req.Agent, req.Tier, ttl, acting)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "signer-unavailable", err.Error())
		return
	}
	if t.audit != nil {
		detail := fmt.Sprintf("caller=%s organization=%s agent=%s tier=%s jti=%s", user, req.Organization, req.Agent, req.Tier, claims.ID)
		if acting != nil {
			detail += " acting_person=" + acting.Email
		}
		t.audit.Publish(r.Context(), audit.Event{
			AuditType:  AuditType,
			Actor:      user,
			TargetUser: claims.Subject,
			Tier:       req.Tier,
			Detail:     detail,
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{
		"token":     token,
		"expiresAt": claims.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// entryOf returns the entry that admits user and, for an entry of the
// own-Organization form, the Organization of the caller's namespace. An
// entry that names user exactly comes first. Otherwise user must be
// system:serviceaccount:<namespace>:<name>, an entry of the own-Organization
// form must name <name>, and <namespace> must be a namespace of an
// Organization. why says, for the refusal message, why that last test
// failed.
func entryOf(ctx context.Context, core kubernetes.Interface, callers []Caller, user string) (entry *Caller, own, why string) {
	for i := range callers {
		if callers[i].account == "" && callers[i].Caller == user {
			return &callers[i], "", ""
		}
	}
	rest, isAccount := strings.CutPrefix(user, accountPrefix)
	ns, name, ok := strings.Cut(rest, ":")
	if !isAccount || !ok {
		return nil, "", ""
	}
	for i := range callers {
		if callers[i].account != "" && callers[i].account == name {
			entry = &callers[i]
		}
	}
	if entry == nil {
		return nil, "", ""
	}
	own, err := organizationOf(ctx, core, ns)
	if err != nil {
		return nil, "", "; " + err.Error()
	}
	return entry, own, ""
}

// organizationOf returns the Organization whose namespace ns is: the slug
// that OrganizationLabel carries on the Namespace ns. core is the client the
// TokenReview ran with, so it is never nil here.
func organizationOf(ctx context.Context, core kubernetes.Interface, ns string) (string, error) {
	obj, err := core.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("namespace %s could not be read: %v", ns, err)
	}
	org := obj.Labels[OrganizationLabel]
	if !orgSlug.MatchString(org) {
		return "", fmt.Errorf("namespace %s is not a namespace of an Organization: its label %s is %q", ns, OrganizationLabel, org)
	}
	return org, nil
}

// roleIn returns the role the person with email holds in the Organization
// org at this moment: the role of their entry in spec.owners of the
// Organization object named org, in lower case, or "" when the Organization
// does not exist or does not list the person. An error means the
// Organization could not be read.
func (t *Tokens) roleIn(ctx context.Context, org, email string) (string, error) {
	if t.orgs == nil {
		return "", errors.New("no client reads Organization objects")
	}
	dyn, err := t.orgs()
	if err != nil {
		return "", fmt.Errorf("the client of Organization objects: %v", err)
	}
	obj, err := dyn.Resource(organizationResource).Get(ctx, org, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("the Organization %s could not be read: %v", org, err)
	}
	owners, _, err := unstructured.NestedSlice(obj.Object, "spec", "owners")
	if err != nil {
		return "", fmt.Errorf("spec.owners of Organization %s: %v", org, err)
	}
	for _, o := range owners {
		entry, _ := o.(map[string]any)
		e, _ := entry["email"].(string)
		role, _ := entry["role"].(string)
		if strings.EqualFold(strings.TrimSpace(e), email) {
			return strings.ToLower(strings.TrimSpace(role)), nil
		}
	}
	return "", nil
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
	req.ActingPerson = strings.ToLower(req.ActingPerson)
	switch {
	case req.ActingPerson != "" && !personEmail.MatchString(req.ActingPerson):
		return req, 0, errors.New("actingPerson must be an email address")
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

// mint signs the token of section 2.1 of the contract. acting is the claim
// acting_person, nil for a token that acts for no person.
func (t *Tokens) mint(iss, caller, org, agent, tier string, ttl time.Duration, acting *auth.ActingPerson) (string, Claims, error) {
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
		ActingPerson:  acting,
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
