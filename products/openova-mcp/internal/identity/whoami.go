package identity

import (
	"errors"
	"fmt"
	"strings"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
)

// WhoamiIdentity is catalyst-api's authoritative identity verdict for a
// session token, reduced to the fields the MCP scopes tools on. It is the
// plain (no HTTP-client import) input to (*Resolver).FromWhoami so the
// identity package stays free of a dependency on internal/catalystapi.
type WhoamiIdentity struct {
	Email         string
	Mode          string // "sovereign" | "organization"
	Tier          string // "owner" | "admin" | "viewer" | …
	Org           string // Organization slug (Org context)
	OrgScoped     bool
	DeploymentID  string
	SovereignFQDN string
}

// FromWhoami builds a verified *Identity from catalyst-api's /whoami verdict
// instead of a locally-verified token signature. This is the #5175 path: the
// MCP is configured with the mothership HANDOVER public key, but callers
// present post-handover SESSION tokens signed with a deployment-local key
// whose public half the MCP does not hold. Rather than ship that key to every
// MCP, the facade delegates the verdict to catalyst-api — the session-token
// authority — and maps its answer here.
//
// The SAME instance-level trust boundaries that fromClaims enforces apply on
// this path: the per-Org context pin and the org pin are re-applied so a
// whoami verdict can NEVER widen a per-Org instance's surface beyond its
// pinned Org (#3988 §4.3). rawBearer is retained so the thin-facade tool
// handlers forward the caller's token verbatim to catalyst-api (which accepts
// it — it minted it).
//
// Callers only reach this after a signature-verify miss AND a catalyst-api
// 2xx /whoami (a bad/expired/tampered token 401s upstream and never gets
// here), so the result is fail-closed: no catalyst-api acceptance, no
// identity.
func (r *Resolver) FromWhoami(w WhoamiIdentity, rawBearer string) (*Identity, error) {
	raw := strings.TrimSpace(strings.TrimPrefix(rawBearer, "Bearer "))
	if raw == "" {
		return nil, errors.New("identity: whoami fallback requires a bearer")
	}

	// Context: catalyst-api's `mode` (or the orgScoped flag) is authoritative.
	ctx := ContextSovereign
	if strings.EqualFold(w.Mode, string(ContextOrganization)) || w.OrgScoped {
		ctx = ContextOrganization
	}

	id := &Identity{
		// bearerOf() short-circuits to "" on a nil Claims, so a non-nil
		// Claims is required for the facade to forward the token. Populate
		// it faithfully from the verdict so the whoami tool echoes what the
		// server actually resolved.
		Claims: &sharedauth.Claims{
			Email:         w.Email,
			OrgID:         w.Org,
			Tier:          w.Tier,
			SovereignFQDN: w.SovereignFQDN,
			DeploymentID:  w.DeploymentID,
		},
		Context:       ctx,
		Tier:          tierFromLabel(w.Tier),
		OrgID:         w.Org,
		DeploymentID:  w.DeploymentID,
		SovereignFQDN: w.SovereignFQDN,
		Email:         w.Email,
		RawBearer:     raw,
	}

	// Instance-level pins — identical invariants to fromClaims so the whoami
	// path cannot bypass the per-Org trust boundary.
	if r.pinnedCtx != "" {
		if r.pinnedCtx == ContextOrganization && id.OrgID == "" {
			return nil, errors.New("identity: per-Org MCP requires an org-scoped session (no org in whoami)")
		}
		id.Context = r.pinnedCtx
	}
	if id.Context == ContextOrganization && id.OrgID == "" {
		return nil, errors.New("identity: organization context requires an org scope")
	}
	if r.orgPin != "" && id.OrgID != r.orgPin {
		return nil, fmt.Errorf("identity: session org scope %q does not match this instance's pinned org %q", id.OrgID, r.orgPin)
	}
	return id, nil
}
