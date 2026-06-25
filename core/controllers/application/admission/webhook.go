package admission

// webhook.go — #3969: the validating ADMISSION WEBHOOK that rejects an
// invalid desired-state placement at the apiserver, not silently at reconcile.
//
// Before this, ValidatePlacement (placement_target.go) ran ONLY at reconcile
// time — a multi-primary placement against a primary+standby Blueprint landed
// as a persisted Application that the controller later marked Ready=False. The
// operator had no synchronous signal at `kubectl apply` / `POST /apps`; the
// CR was accepted, then quietly Degraded. This handler closes that gap: it is
// a `ValidatingWebhookConfiguration` target that decodes the incoming
// AdmissionReview, projects the Application's `spec.placement` + the referenced
// Blueprint's `placementCapability`, runs EvaluatePlacement, and returns an
// allow/deny AdmissionResponse. An invalid placement is rejected SYNCHRONOUSLY
// at admission.
//
// The handler is deliberately thin + pure-ish: the capability lookup is
// injected (CapabilityResolver) so the package builds with NO K8s client
// dependency and the decode→evaluate→respond path is unit-testable against a
// raw AdmissionReview body. The cmd entrypoint wires a real resolver (a dynamic
// Blueprint GET) + TLS serving.
//
// Ref #3969 §7.3, "validating webhook for ValidatePlacement".

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// CapabilityResolver resolves the placementCapability of the Blueprint an
// Application references. The webhook injects this so the decode→evaluate path
// stays free of a K8s client; the cmd entrypoint wires a dynamic Blueprint GET.
// An error (Blueprint not found / API error) is surfaced by the handler as a
// deny with a clear message — a placement can't be validated against an
// unknown capability, and silently allowing it would defeat the gate.
type CapabilityResolver interface {
	// CapabilityFor returns the placementCapability for the named Blueprint
	// (with or without the `bp-` prefix; the resolver normalises). It returns
	// the safe primary+standby default (never multi-primary) when the
	// Blueprint omits the field — that folding lives in the resolver impl, but
	// the webhook also re-normalises defensively via NormalizeCapability.
	CapabilityFor(ctx context.Context, blueprintRef string) (bpv1alpha1.PlacementCapability, error)
}

// CapabilityResolverFunc adapts a plain function to CapabilityResolver.
type CapabilityResolverFunc func(ctx context.Context, blueprintRef string) (bpv1alpha1.PlacementCapability, error)

// CapabilityFor implements CapabilityResolver.
func (f CapabilityResolverFunc) CapabilityFor(ctx context.Context, blueprintRef string) (bpv1alpha1.PlacementCapability, error) {
	return f(ctx, blueprintRef)
}

// PlacementWebhook is the http.Handler for the placement ValidatingWebhook.
// It serves the `/validate-placement` path the ValidatingWebhookConfiguration
// targets.
type PlacementWebhook struct {
	// Resolver resolves the referenced Blueprint's placementCapability. When
	// nil, the webhook folds every Application to the safe primary+standby
	// default (single-Primary) — so the multi-primary gate still fires, but a
	// genuinely multi-primary Blueprint would be wrongly rejected. Production
	// MUST wire a real resolver; the nil case is a unit-test convenience.
	Resolver CapabilityResolver
}

// applicationPlacementShape is the minimal projection the webhook decodes out
// of the AdmissionReview's `object.raw` (an Application CR). Only the fields
// EvaluatePlacement needs are pulled — the desired-state placement + the
// Blueprint reference (to resolve the capability gate).
type applicationPlacementShape struct {
	Spec struct {
		BlueprintRef struct {
			Name string `json:"name"`
		} `json:"blueprintRef"`
		// BlueprintName is the legacy flat form some CRs carry; either
		// blueprintRef.name or blueprintName resolves the capability.
		BlueprintName string                `json:"blueprintName"`
		Placement     placementWireFragment `json:"placement"`
	} `json:"spec"`
}

// placementWireFragment decodes ONLY the targets[] + ownedDependencies[] of
// spec.placement. spec.placement may also be a bare legacy STRING (the posture
// mode) — UnmarshalJSON tolerates that, decoding it as an empty placement (no
// targets ⇒ EvaluatePlacement is a no-op, the legacy path is never gated).
type placementWireFragment struct {
	Targets           []bpv1alpha1.PlacementTarget         `json:"targets"`
	OwnedDependencies []bpv1alpha1.OwnedDependencyOverride `json:"ownedDependencies"`
}

// UnmarshalJSON tolerates the dual-form spec.placement: the #3373 object form
// (which carries targets[]) OR the legacy bare string posture. A string decodes
// to the zero fragment (no targets) so the legacy posture is never rejected.
func (p *placementWireFragment) UnmarshalJSON(data []byte) error {
	// Legacy string form — decode to empty (un-gated).
	if len(data) > 0 && data[0] == '"' {
		*p = placementWireFragment{}
		return nil
	}
	type alias placementWireFragment
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*p = placementWireFragment(a)
	return nil
}

// ServeHTTP decodes the AdmissionReview, evaluates the placement gate, and
// writes the AdmissionReview response. It always responds with HTTP 200 + an
// AdmissionReview body (the apiserver contract) — a deny is carried in
// response.allowed=false, never as an HTTP error.
func (w *PlacementWebhook) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(rw, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	review := &admissionv1.AdmissionReview{}
	if err := json.Unmarshal(body, review); err != nil {
		http.Error(rw, "decode AdmissionReview: "+err.Error(), http.StatusBadRequest)
		return
	}
	resp := w.Review(req.Context(), review)

	out := &admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "admission.k8s.io/v1",
			Kind:       "AdmissionReview",
		},
		Response: resp,
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(rw).Encode(out)
}

// Review is the pure decode→evaluate→respond core, factored out of ServeHTTP
// so tests exercise it without an HTTP round-trip. It NEVER returns nil: a
// malformed request or a capability-resolution failure becomes a deny with a
// clear message (fail-closed — an un-validatable placement is rejected).
func (w *PlacementWebhook) Review(ctx context.Context, review *admissionv1.AdmissionReview) *admissionv1.AdmissionResponse {
	if review == nil || review.Request == nil {
		return denyResponse("", "empty AdmissionReview request")
	}
	uid := review.Request.UID

	// DELETE carries no object — always allow (nothing to validate).
	if review.Request.Operation == admissionv1.Delete {
		return allowResponse(uid)
	}

	raw := review.Request.Object.Raw
	if len(raw) == 0 {
		return denyResponseUID(uid, "admission request carries no object")
	}
	var app applicationPlacementShape
	if err := json.Unmarshal(raw, &app); err != nil {
		return denyResponseUID(uid, "decode Application object: "+err.Error())
	}

	// No desired-state targets ⇒ legacy posture path, un-gated.
	if len(app.Spec.Placement.Targets) == 0 {
		return allowResponse(uid)
	}

	capability, err := w.resolveCapability(ctx, firstNonEmptyStr(app.Spec.BlueprintRef.Name, app.Spec.BlueprintName))
	if err != nil {
		return denyResponseUID(uid, "resolve Blueprint placementCapability: "+err.Error())
	}

	decision := EvaluatePlacement(PlacementRequest{
		Placement: bpv1alpha1.Placement{
			Targets:           app.Spec.Placement.Targets,
			OwnedDependencies: app.Spec.Placement.OwnedDependencies,
		},
		Capability: capability,
	})
	if !decision.Allowed {
		return denyResponseUID(uid, fmt.Sprintf("%s: %s", decision.Code, decision.Message))
	}
	return allowResponse(uid)
}

// resolveCapability calls the injected resolver, folding to the safe
// primary+standby default when no resolver is wired (unit-test convenience).
func (w *PlacementWebhook) resolveCapability(ctx context.Context, blueprintRef string) (bpv1alpha1.PlacementCapability, error) {
	if w.Resolver == nil {
		return bpv1alpha1.CapabilityPrimaryStandby, nil
	}
	c, err := w.Resolver.CapabilityFor(ctx, blueprintRef)
	if err != nil {
		return "", err
	}
	return bpv1alpha1.NormalizeCapability(c), nil
}

// allowResponse builds an allow AdmissionResponse for the given request UID.
func allowResponse(uid types.UID) *admissionv1.AdmissionResponse {
	return &admissionv1.AdmissionResponse{
		UID:     uid,
		Allowed: true,
	}
}

// denyResponseUID builds a deny AdmissionResponse carrying the message in the
// status so `kubectl apply` surfaces it to the operator.
func denyResponseUID(uid types.UID, msg string) *admissionv1.AdmissionResponse {
	return &admissionv1.AdmissionResponse{
		UID:     uid,
		Allowed: false,
		Result: &metav1.Status{
			Status:  metav1.StatusFailure,
			Message: msg,
			Reason:  metav1.StatusReasonInvalid,
			Code:    http.StatusUnprocessableEntity,
		},
	}
}

// denyResponse is denyResponseUID with an explicit (possibly empty) UID.
func denyResponse(uid types.UID, msg string) *admissionv1.AdmissionResponse {
	return denyResponseUID(uid, msg)
}

// firstNonEmptyStr returns the first non-empty argument.
func firstNonEmptyStr(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
