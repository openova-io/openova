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
	"regexp"
	"strings"

	"github.com/openova-io/openova/core/controllers/application/admission"
	appv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/application/v1alpha1"
)

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
		switch r.Placement.VCluster {
		case "", "host", "mgmt", "dmz", "rtz":
		default:
			return &ShapeError{Code: "placement-vcluster-invalid",
				Message: fmt.Sprintf("placement.vcluster %q not in {host, mgmt, dmz, rtz}", r.Placement.VCluster)}
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
