// Package instances — locked admission-gate adapter for the
// `POST /catalyst/v1/apps/instances` endpoint declared in
// docs/api/catalyst-api-openapi.yaml.
//
// G117 W2.C2 brief specifies a `core/services/catalyst-api/internal/instances`
// package; the actual catalyst-api service lives at
// `products/catalyst/bootstrap/api/` per the existing repo layout, so
// this package lives next to the existing handler suite and is
// imported from the route handler.
//
// Responsibilities:
//
//  1. Decode + sanitise the wire body (CreateInstanceRequest per OpenAPI).
//  2. Bridge to the locked admission gates at
//     core/controllers/application/internal/admission via a typed
//     `Decision`.
//  3. Build the Application CR's spec.instanceId, spec.isolationLevel,
//     spec.namingTemplate per the W2.C2 brief (defaults applied
//     pure-function so the admission tests + the handler tests share
//     the same defaulting code).
//  4. Map admission Decisions to OpenAPI Error responses
//     (status code + Error.code + Error.message).
//
// No K8s client lives here — the handler owns the dynamic.Interface
// and supplies the existing-Application list as an
// `admission.ExistingApplication[]` slice. This keeps the package
// pure-Go-testable.
package instances

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/openova-io/openova/core/controllers/application/admission"
	appv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/application/v1alpha1"
)

// ── #5616 — a placement TIER is only real when the Sovereign installs it ──
//
// `placement.vcluster` carries a TIER KEY, not a namespace. The
// application-controller turns that key into a HOST NAMESPACE through its
// VClusterPlacements map (`VCLUSTER_PLACEMENT_<TIER>_NS`, defaulting to
// the tier name) and addresses the per-cluster HelmRelease there. So the
// key is usable only when a vCluster for that tier actually exists.
//
// Until now this validator accepted {host, mgmt, dmz, rtz}
// UNCONDITIONALLY, while `clusters/_template/bootstrap-kit/` installs NO
// mgmt / dmz / rtz vCluster and creates no such namespace (verified on
// hw292 2026-08-04: 53 namespaces, none of mgmt/dmz/rtz). Three of the
// four accepted values therefore resolved to a namespace that exists on
// no Sovereign: the API answered 201 and the Application then sat
// Degraded forever behind a raw Kubernetes error —
// `upsert per-cluster HelmRelease rtz/uatco-agenity-rtz-a: namespaces
// "rtz" not found` (#5616). PR #5622 stopped OFFERING those options in
// ONE of the four front-end trees; every other door into this endpoint
// (direct API call, Git edit, the MCP `create_application` tool, the
// catalyst-console tree) still walked straight into the same wall,
// because the CONTRACT never changed.
//
// The accepted set is now DERIVED from what the Sovereign installs
// rather than hardcoded: "host" (namespace-independent — the controller
// normalises it to the Organization's own namespace) plus whatever
// CATALYST_PLACEMENT_VCLUSTER_TIERS names. Default = host only, which
// matches every Sovereign this repo can provision today. An operator who
// installs the mgmt vCluster sets CATALYST_PLACEMENT_VCLUSTER_TIERS=mgmt
// and the tier is selectable again — one knob, no code change, and the
// knob is the SAME fact the controller's VCLUSTER_PLACEMENT_MGMT_NS
// already encodes.

// TierEnvVar is the env var an operator sets to declare which non-host
// vCluster tiers this Sovereign actually installs (comma-separated).
const TierEnvVar = "CATALYST_PLACEMENT_VCLUSTER_TIERS"

// knownVClusterTiers is the whole `placement.vcluster` VOCABULARY —
// the values the CRD + the application-controller can parse at all.
// Mirrors bpv1alpha1.IsKnownVCluster. A value outside this set is a
// malformed request (400 placement-vcluster-invalid); a value inside it
// but not installed here is a well-formed request this Sovereign cannot
// honour (400 placement-vcluster-unavailable).
var knownVClusterTiers = []string{"host", "mgmt", "dmz", "rtz"}

// availableNonHostTiers holds the non-host tiers this Sovereign can
// actually place into. Package-level so the handler stays untouched and
// tests can drive it explicitly; seeded once from the environment.
var availableNonHostTiers = parseAvailableTiers(os.Getenv(TierEnvVar))

// parseAvailableTiers turns the comma-separated env value into the set
// of non-host tiers to accept. Unknown or empty entries are dropped —
// an operator typo must never widen the accepted set. "host" is always
// accepted and is not stored here.
func parseAvailableTiers(csv string) map[string]bool {
	out := map[string]bool{}
	for _, raw := range strings.Split(csv, ",") {
		t := strings.ToLower(strings.TrimSpace(raw))
		if t == "" || t == "host" {
			continue
		}
		for _, known := range knownVClusterTiers {
			if t == known {
				out[t] = true
				break
			}
		}
	}
	return out
}

// SetAvailableVClusterTiers re-reads the available-tier set from a
// comma-separated list. Exported so tests — and any future explicit
// wiring from the handler's config — can set it without touching the
// process environment.
func SetAvailableVClusterTiers(csv string) { availableNonHostTiers = parseAvailableTiers(csv) }

// IsKnownVClusterTier reports whether v is in the vocabulary at all.
// The empty string ("inherit the Blueprint default") is always known.
// Exported so every door into the Application-create/update API shares
// ONE vocabulary instead of re-declaring the tuple (#5616).
func IsKnownVClusterTier(v string) bool {
	if v == "" {
		return true
	}
	for _, known := range knownVClusterTiers {
		if v == known {
			return true
		}
	}
	return false
}

// VClusterTierAvailable reports whether this Sovereign can actually
// place an Application into tier v. "" (inherit) and "host" always can;
// every other tier needs its vCluster to be installed and declared.
// Exported so the sovereign-scoped install + placement-update handlers
// enforce the SAME availability fact as create-instance (#5616).
func VClusterTierAvailable(v string) bool {
	if v == "" || v == "host" {
		return true
	}
	return availableNonHostTiers[v]
}

// KnownVClusterTiersCSV renders the vocabulary for an error message.
func KnownVClusterTiersCSV() string { return strings.Join(knownVClusterTiers, ", ") }

// UnavailableTierMessage is the ONE operator-facing explanation for a
// well-formed tier this Sovereign cannot honour. Shared by every door
// into the Application API so the remedy never diverges (#5616).
func UnavailableTierMessage(tier string) string {
	return fmt.Sprintf(
		"placement.vcluster %q is not available on this Sovereign: no vCluster is installed for that tier, "+
			"so the Application would be addressed into a namespace that does not exist. "+
			"Leave placement.vcluster blank to inherit the Blueprint default, or choose %q. "+
			"(A sovereign-admin who has installed the tier's vCluster enables it with %s.)",
		tier, "host", TierEnvVar)
}

// CreateInstanceRequest mirrors the OpenAPI schema
// `components/schemas/CreateInstanceRequest`. Unexported in the
// handler package; exported here so per-cluster reconciler code can
// reuse the same wire-shape for future imports.
type CreateInstanceRequest struct {
	Blueprint      string                 `json:"blueprint"`
	Org            string                 `json:"org"`
	Name           string                 `json:"name"`
	Topology       string                 `json:"topology,omitempty"`
	IsolationLevel string                 `json:"isolationLevel,omitempty"`
	Values         map[string]interface{} `json:"values,omitempty"`

	// Placement — #3373 instance-level placement from the
	// provisioning journey's GENERIC advanced view (rendered from the
	// Blueprint's defaultPlacement + allowedPlacements declaration —
	// never per-app UI). Nil = the user silently accepted the
	// Blueprint defaults ("he doesn't even need to know the vcluster
	// detail"); the application-controller derives the rest.
	Placement *InstancePlacementRequest `json:"placement,omitempty"`

	// Backing — #3370 provisioning journey. One entry per required
	// backing service of the blueprint being created, carrying the
	// operator's generic selector choice: Create new (default) or
	// Reuse existing. On create the backing service becomes its OWN
	// instance-application (own card) with a Context for this
	// consumer; on reuse NO new backing application is created — the
	// flow appends a Context to the named existing instance's IaC and
	// wires this Application's spec.dependsOn to it.
	Backing []BackingSelection `json:"backing,omitempty"`
}

// InstancePlacementRequest mirrors the OBJECT form of
// `Application.spec.placement` (#3373): WHERE the instance runs.
type InstancePlacementRequest struct {
	VCluster string   `json:"vcluster,omitempty"`
	Regions  []string `json:"regions,omitempty"`
	Clusters []string `json:"clusters,omitempty"`
}

// BackingSelection is one backing-service choice (#3370).
type BackingSelection struct {
	// Blueprint — the backing Blueprint id (bp- prefix optional).
	Blueprint string `json:"blueprint"`
	// Mode — "create" (default when empty) | "reuse".
	Mode string `json:"mode,omitempty"`
	// Instance — required when Mode=reuse: the existing instance
	// (Application name) to occupy a Context on.
	Instance string `json:"instance,omitempty"`
}

// NameRE is the OpenAPI-declared name pattern.
var NameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}[a-z0-9]$`)

// Sanitise trims whitespace + normalises blueprint name. Mutates
// in-place to keep the call site terse.
func (r *CreateInstanceRequest) Sanitise() {
	r.Blueprint = strings.TrimSpace(r.Blueprint)
	r.Org = strings.TrimSpace(r.Org)
	r.Name = strings.TrimSpace(r.Name)
	r.IsolationLevel = strings.TrimSpace(r.IsolationLevel)
	r.Topology = strings.TrimSpace(r.Topology)
	for i := range r.Backing {
		r.Backing[i].Blueprint = strings.TrimSpace(r.Backing[i].Blueprint)
		r.Backing[i].Mode = strings.ToLower(strings.TrimSpace(r.Backing[i].Mode))
		r.Backing[i].Instance = strings.TrimSpace(r.Backing[i].Instance)
	}
}

// ValidateShape applies the OpenAPI-declared invariants the admission
// package isn't responsible for (those are caller pre-checks; admission
// owns multi-instance + name-collision + immutability only).
//
// Returns a typed error mapped 1:1 to OpenAPI 400 Error.code values.
func (r CreateInstanceRequest) ValidateShape() *ShapeError {
	if r.Blueprint == "" || r.Org == "" || r.Name == "" {
		return &ShapeError{Code: "missing-required", Message: "blueprint, org, name are required"}
	}
	if !NameRE.MatchString(r.Name) {
		return &ShapeError{Code: "invalid-name", Message: fmt.Sprintf("name %q must match %s", r.Name, NameRE.String())}
	}
	if r.IsolationLevel != "" && !appv1alpha1.IsValidIsolationLevel(r.IsolationLevel) {
		return &ShapeError{Code: "isolation-level-invalid", Message: fmt.Sprintf("isolationLevel %q not in {namespace, vcluster}", r.IsolationLevel)}
	}
	// #3373 — instance placement WHERE fields.
	if r.Placement != nil {
		if !IsKnownVClusterTier(r.Placement.VCluster) {
			return &ShapeError{Code: "placement-vcluster-invalid",
				Message: fmt.Sprintf("placement.vcluster %q not in {%s}",
					r.Placement.VCluster, KnownVClusterTiersCSV())}
		}
		// #5616 — well-formed, but is it REAL here? A tier with no
		// vCluster installed resolves to a host namespace that does not
		// exist, and the Application would land Degraded on a raw
		// Kubernetes error minutes after a 201. Refuse it at the point
		// of choice instead, for every client of this endpoint.
		if !VClusterTierAvailable(r.Placement.VCluster) {
			return &ShapeError{Code: "placement-vcluster-unavailable",
				Message: UnavailableTierMessage(r.Placement.VCluster)}
		}
		for i, c := range r.Placement.Clusters {
			if strings.TrimSpace(c) == "" {
				return &ShapeError{Code: "placement-clusters-invalid",
					Message: fmt.Sprintf("placement.clusters[%d] is empty", i)}
			}
		}
		for i, rg := range r.Placement.Regions {
			if strings.TrimSpace(rg) == "" {
				return &ShapeError{Code: "placement-regions-invalid",
					Message: fmt.Sprintf("placement.regions[%d] is empty", i)}
			}
		}
	}
	for i := range r.Backing {
		b := &r.Backing[i]
		if b.Blueprint == "" {
			return &ShapeError{Code: "backing-blueprint-required", Message: "backing[].blueprint is required"}
		}
		switch b.Mode {
		case "", "create":
			// default — auto-create the backing instance.
		case "reuse":
			if b.Instance == "" {
				return &ShapeError{Code: "backing-instance-required", Message: "backing[].instance is required when mode=reuse"}
			}
		default:
			return &ShapeError{Code: "backing-mode-invalid", Message: fmt.Sprintf("backing[].mode %q not in {create, reuse}", b.Mode)}
		}
	}
	return nil
}

// ShapeError is the 400-class error returned by ValidateShape.
type ShapeError struct {
	Code    string
	Message string
}

func (e *ShapeError) Error() string { return e.Code + ": " + e.Message }

// ApplicationSeed is the locked projection the handler converts into
// an unstructured.Unstructured before calling
// `dynamic.Interface.Create`. Keeping it as a plain struct here makes
// the unit tests independent of the K8s client.
type ApplicationSeed struct {
	Name           string
	Namespace      string
	Blueprint      string // with `bp-` prefix
	Topology       string
	InstanceID     string
	IsolationLevel appv1alpha1.IsolationLevel
	NamingTemplate string
	Values         map[string]interface{}
	Labels         map[string]string

	// Placement — #3373 instance placement (nil = Blueprint
	// defaults; the controller derives). Passed through verbatim to
	// the OBJECT form of `spec.placement` on the Application CR.
	Placement *InstancePlacementRequest

	// SovereignFQDN — the Sovereign's own FQDN (e.g. "omantel.biz"), stamped
	// by the create-instance handler so the bp-agenity install gets
	// spec.parameters.sovereignFqdn (#4556 Item 2). Empty on the
	// mothership/Catalyst-Zero. Consumed by newApplicationCRFromSeed via
	// defaultedParameters; ignored for non-agenity Blueprints.
	SovereignFQDN string

	// OrgConsoleHost — the Org's public console host
	// (console.<slug>.<poolParentDomain>, e.g. console.nstar.omani.homes),
	// stamped by the create-instance handler from the tenant registry so the
	// bp-agenity install gets spec.parameters.openovaMCP.tenantHost (#4624):
	// the OPENOVA_MCP_TENANT_HOST the agent-side MCP forwards as
	// X-Tenant-Host MUST be the ORG console host — the Sovereign console
	// host (console.<sovereignFqdn>, where the catalyst-api URL correctly
	// points) is NOT a registered tenant, so without this every agent
	// create_application 404s `tenant-not-registered`. Empty on the
	// mothership / when the registry has no row for the Org (no stamp,
	// fail-closed). Consumed by newApplicationCRFromSeed via
	// defaultedParameters; ignored for non-agenity Blueprints.
	OrgConsoleHost string
}

// Build constructs the ApplicationSeed from a sanitised+validated
// CreateInstanceRequest plus the chosen topology. Applies multi-instance
// defaulting (InstanceID = freshly minted 8-char hex when absent;
// IsolationLevel = namespace; NamingTemplate per
// appv1alpha1.DefaultNamingTemplate).
func (r CreateInstanceRequest) Build(chosenTopology string) (ApplicationSeed, error) {
	if err := r.ValidateShape(); err != nil {
		return ApplicationSeed{}, err
	}

	bp := r.Blueprint
	if !strings.HasPrefix(bp, "bp-") {
		bp = "bp-" + bp
	}

	mi := appv1alpha1.MultiInstanceSpec{IsolationLevel: appv1alpha1.IsolationLevel(r.IsolationLevel)}
	mi.ApplyDefaults()

	id, err := newInstanceID()
	if err != nil {
		return ApplicationSeed{}, fmt.Errorf("mint instance-id: %w", err)
	}

	return ApplicationSeed{
		Name:           r.Name,
		Namespace:      r.Org,
		Blueprint:      bp,
		Topology:       chosenTopology,
		InstanceID:     id,
		IsolationLevel: mi.IsolationLevel,
		NamingTemplate: mi.NamingTemplate,
		Values:         r.Values,
		Placement:      r.Placement,
		Labels: map[string]string{
			"catalyst.openova.io/managed-by":   "catalyst-api",
			"catalyst.openova.io/organization": r.Org,
			"catalyst.openova.io/blueprint":    strings.TrimPrefix(bp, "bp-"),
			"catalyst.openova.io/topology":     chosenTopology,
			"catalyst.openova.io/instance":     id,
		},
	}, nil
}

// newInstanceID mints an 8-char hex InstanceID. Uses crypto/rand for
// collision resistance — 32 bits is ample given that admission's
// name-collision gate is the real uniqueness invariant.
func newInstanceID() (string, error) {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// AdmissionResponse maps an admission.Decision to the HTTP wire
// response. Status is the HTTP code; Body is the JSON body bytes the
// handler writes.
type AdmissionResponse struct {
	StatusCode int
	Code       admission.DecisionCode
	Message    string
}

// MapDecision converts an admission Decision into the OpenAPI 409 /
// 422 response. Allowed decisions return nil.
func MapDecision(d admission.Decision) *AdmissionResponse {
	if d.Allowed {
		return nil
	}
	status := 409 // Conflict — covers multi-instance-disabled, max-per-org-exceeded, name-collision
	if d.Code == admission.CodeIsolationLevelInvalid || d.Code == admission.CodeInstanceIDImmutable {
		status = 422 // Unprocessable Entity — semantic invariant violation
	}
	return &AdmissionResponse{
		StatusCode: status,
		Code:       d.Code,
		Message:    d.Message,
	}
}

// ErrNoBlueprint is returned by callers when the Blueprint metadata
// can't be resolved. The handler maps this to HTTP 503 with code
// "blueprint-unavailable".
var ErrNoBlueprint = errors.New("blueprint metadata unavailable")
