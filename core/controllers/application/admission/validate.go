// Package admission — multi-instance admission gates for the
// Application CRD shipped at products/catalyst/chart/crds/application.yaml.
//
// Per the G117 W2.C2 brief (parent EPIC #2737, tracking issue #2741),
// the Application CR moves from singleton-per-(blueprint,org) to N
// instances per Blueprint per Environment when the Blueprint declares
// `spec.multiInstance.enabled=true`. The admission package centralises
// the four invariants every entrypoint into the Application CR must
// enforce:
//
//  1. Multi-instance disabled gate — when the Blueprint's
//     `MultiInstance.Enabled` is false, reject creation if any
//     Application already exists with the same (blueprint, org) pair.
//
//  2. MaxPerOrg gate — when MultiInstance.Enabled is true AND
//     MultiInstance.MaxPerOrg > 0, reject creation when the existing
//     count is >= MaxPerOrg.
//
//  3. Name-collision gate — reject when the requested Application name
//     collides with an existing instance in the same Org for the same
//     Blueprint. This applies REGARDLESS of the multi-instance flag —
//     two Applications can never share a name within an Org's
//     namespace (CRD-level constraint).
//
//  4. Immutability invariant — once an Application carries a non-empty
//     spec.instanceId, the value MUST NOT change on any subsequent
//     update. The CRD's x-kubernetes-validations rule enforces this at
//     the API server; this package mirrors it so library callers
//     (catalyst-api, application-controller) get the same answer
//     before the apiserver round-trip.
//
// The package is intentionally pure: every gate takes the existing CR
// list + the candidate request and returns a typed Decision. There are
// no K8s client calls here — callers build the list however they
// want (catalyst-api uses dynamic.Interface; the controller uses
// controller-runtime's cache).
//
// Wire-error contract: each Decision.Code maps 1:1 to the OpenAPI
// `Error.code` enum at docs/api/catalyst-api-openapi.yaml. Adding a new
// failure mode requires updating both files in lockstep.
package admission

import (
	"errors"
	"fmt"
	"strings"

	appv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/application/v1alpha1"
)

// DecisionCode is the wire-stable identifier returned to clients on
// rejection. Mirrors the OpenAPI Error.code values exactly.
type DecisionCode string

const (
	// CodeMultiInstanceDisabled — Blueprint.MultiInstance.Enabled is
	// false AND an Application with same (blueprint, org) already
	// exists.
	CodeMultiInstanceDisabled DecisionCode = "multi-instance-disabled"

	// CodeMaxPerOrgExceeded — Blueprint.MultiInstance.Enabled is true,
	// MultiInstance.MaxPerOrg > 0, and the existing count is at-or-
	// above the cap.
	CodeMaxPerOrgExceeded DecisionCode = "max-per-org-exceeded"

	// CodeNameCollision — requested Application name collides with an
	// existing instance in the same Org for the same Blueprint.
	CodeNameCollision DecisionCode = "name-collision"

	// CodeInstanceIDImmutable — an Update mutated spec.instanceId.
	CodeInstanceIDImmutable DecisionCode = "instance-id-immutable"

	// CodeIsolationLevelInvalid — spec.isolationLevel is set to a
	// value not in {namespace, vcluster}.
	CodeIsolationLevelInvalid DecisionCode = "isolation-level-invalid"
)

// Decision is the verdict of an admission gate. Allowed=true means the
// caller may proceed. Code + Message are populated only on rejection
// (and are the values catalyst-api emits in the 409 / 422 response).
type Decision struct {
	Allowed bool
	Code    DecisionCode
	Message string
}

// AllowedDecision is the zero-allocation success value.
var AllowedDecision = Decision{Allowed: true}

// String renders the Decision for logging.
func (d Decision) String() string {
	if d.Allowed {
		return "allowed"
	}
	return fmt.Sprintf("denied(%s): %s", d.Code, d.Message)
}

// ExistingApplication is the minimal projection of an existing
// Application CR the admission gates inspect. Decoupled from
// unstructured.Unstructured so the package builds without a K8s
// dependency.
type ExistingApplication struct {
	// Name — metadata.name (Application name within the Org's namespace).
	Name string

	// InstanceID — spec.instanceId (may be empty for legacy CRs;
	// migration-script backfills before this package's gates run).
	InstanceID string

	// Blueprint — blueprintRef.name (with or without the `bp-` prefix;
	// the gates normalise on read).
	Blueprint string
}

// BlueprintMultiInstance is the locked projection of
// Blueprint.spec.multiInstance the gates inspect. Mirrors the JSON
// keys at platform/_schemas/blueprint-topology.json so the test
// fixtures match the wire shape exactly.
type BlueprintMultiInstance struct {
	Enabled   bool
	MaxPerOrg int
}

// CreateRequest is the locked projection of an admission request. The
// admission webhook + catalyst-api both build a CreateRequest from the
// incoming wire body before calling EvaluateCreate. Org is the
// Application's namespace.
type CreateRequest struct {
	Blueprint      string
	Org            string
	Name           string
	IsolationLevel string
}

// UpdateRequest carries the prior + new spec.instanceId / isolation
// state for the immutability gate. Both fields are required to allow
// the gate to differentiate "newly set" (allowed) from "mutated"
// (rejected).
type UpdateRequest struct {
	PriorInstanceID string
	NewInstanceID   string

	PriorIsolationLevel string
	NewIsolationLevel   string
}

// EvaluateCreate runs the multi-instance + name-collision + isolation
// validity gates against a CreateRequest. The caller supplies the list
// of existing Applications in the Org for the same Blueprint.
//
// Order of evaluation (most-specific rejections first so the operator
// sees the actionable error):
//
//  1. isolation-level-invalid (validity)
//  2. name-collision (per-Org uniqueness — always applies)
//  3. multi-instance-disabled (singleton gate)
//  4. max-per-org-exceeded (cap gate)
//
// Returns AllowedDecision on success.
func EvaluateCreate(req CreateRequest, mi BlueprintMultiInstance, existing []ExistingApplication) Decision {
	// 0. Quick sanity check — the catalyst-api handler validates these
	//    earlier, but the controller's admission path may call us
	//    without that pre-check.
	if strings.TrimSpace(req.Blueprint) == "" || strings.TrimSpace(req.Org) == "" || strings.TrimSpace(req.Name) == "" {
		return Decision{
			Allowed: false,
			Code:    CodeNameCollision, // best-fit existing code; "missing-required" is handler-only
			Message: "blueprint, org, and name are required",
		}
	}

	// 1. isolationLevel validity — closed enum.
	if req.IsolationLevel != "" && !appv1alpha1.IsValidIsolationLevel(req.IsolationLevel) {
		return Decision{
			Allowed: false,
			Code:    CodeIsolationLevelInvalid,
			Message: fmt.Sprintf("isolationLevel %q not in {namespace, vcluster}", req.IsolationLevel),
		}
	}

	// 2. Name-collision — always evaluated, regardless of multiInstance.
	wantedName := strings.TrimSpace(req.Name)
	wantedBP := normaliseBlueprint(req.Blueprint)
	for _, ex := range existing {
		if normaliseBlueprint(ex.Blueprint) == wantedBP && ex.Name == wantedName {
			return Decision{
				Allowed: false,
				Code:    CodeNameCollision,
				Message: fmt.Sprintf("Application %q already exists in Org %s for Blueprint %s", wantedName, req.Org, wantedBP),
			}
		}
	}

	// 3. Multi-instance gate — when disabled, any existing instance
	//    in the Org for this Blueprint blocks the create.
	matching := countMatching(existing, wantedBP)
	if !mi.Enabled && matching > 0 {
		return Decision{
			Allowed: false,
			Code:    CodeMultiInstanceDisabled,
			Message: fmt.Sprintf("Blueprint %s does not permit multiple Applications per Organization (existing: %d)", wantedBP, matching),
		}
	}

	// 4. maxPerOrg cap.
	if mi.Enabled && mi.MaxPerOrg > 0 && matching >= mi.MaxPerOrg {
		return Decision{
			Allowed: false,
			Code:    CodeMaxPerOrgExceeded,
			Message: fmt.Sprintf("Blueprint %s allows at most %d Applications per Organization (existing: %d)", wantedBP, mi.MaxPerOrg, matching),
		}
	}

	return AllowedDecision
}

// EvaluateUpdate runs the immutability invariant on spec.instanceId
// and the isolation-level validity gate. The application-controller
// + admission webhook call this on every Update.
//
//  1. instance-id-immutable — once a non-empty instanceId has been
//     persisted, subsequent updates MUST leave it unchanged.
//  2. isolation-level-invalid — closed enum {namespace, vcluster}.
//
// Note: changing the isolation level (namespace → vcluster) is
// permitted at the admission layer because the application-controller
// (G117 W2.C1) can fan-out resources to a different target on next
// reconcile. The CR-level invariant is wire validity, not semantic
// stability — semantic stability is the application-controller's
// problem.
func EvaluateUpdate(req UpdateRequest) Decision {
	if req.PriorInstanceID != "" && req.NewInstanceID != req.PriorInstanceID {
		return Decision{
			Allowed: false,
			Code:    CodeInstanceIDImmutable,
			Message: fmt.Sprintf("spec.instanceId is immutable: prior=%q, new=%q", req.PriorInstanceID, req.NewInstanceID),
		}
	}
	if req.NewIsolationLevel != "" && !appv1alpha1.IsValidIsolationLevel(req.NewIsolationLevel) {
		return Decision{
			Allowed: false,
			Code:    CodeIsolationLevelInvalid,
			Message: fmt.Sprintf("isolationLevel %q not in {namespace, vcluster}", req.NewIsolationLevel),
		}
	}
	return AllowedDecision
}

// AsError wraps a denied Decision as a Go error suitable for
// controller-runtime's reconcile loop. Returns nil on allowed.
func (d Decision) AsError() error {
	if d.Allowed {
		return nil
	}
	return errors.New(d.String())
}

// normaliseBlueprint strips the leading `bp-` so callers can pass
// either form (`bp-grafana` or `grafana`) without changing semantics.
func normaliseBlueprint(name string) string {
	return strings.TrimPrefix(strings.TrimSpace(name), "bp-")
}

// countMatching returns the number of existing Applications whose
// Blueprint matches `wantedBP` (post-normalisation).
func countMatching(existing []ExistingApplication, wantedBP string) int {
	n := 0
	for _, ex := range existing {
		if normaliseBlueprint(ex.Blueprint) == wantedBP {
			n++
		}
	}
	return n
}
