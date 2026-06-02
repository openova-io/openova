// Unit tests for the catalyst-api multi-instance create adapter.
package instances

import (
	"strings"
	"testing"

	"github.com/openova-io/openova/core/controllers/application/admission"
	appv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/application/v1alpha1"
)

func TestCreateInstanceRequest_Sanitise(t *testing.T) {
	r := CreateInstanceRequest{
		Blueprint:      "  grafana  ",
		Org:            " acme ",
		Name:           " obs-1 ",
		IsolationLevel: " namespace ",
		Topology:       "  singleton  ",
	}
	r.Sanitise()
	if r.Blueprint != "grafana" || r.Org != "acme" || r.Name != "obs-1" ||
		r.IsolationLevel != "namespace" || r.Topology != "singleton" {
		t.Fatalf("Sanitise mishandled whitespace: %+v", r)
	}
}

func TestCreateInstanceRequest_ValidateShape(t *testing.T) {
	cases := []struct {
		name    string
		r       CreateInstanceRequest
		wantErr bool
		wantCode string
	}{
		{"happy", CreateInstanceRequest{Blueprint: "grafana", Org: "acme", Name: "obs-1"}, false, ""},
		{"missing blueprint", CreateInstanceRequest{Org: "acme", Name: "obs-1"}, true, "missing-required"},
		{"missing org", CreateInstanceRequest{Blueprint: "grafana", Name: "obs-1"}, true, "missing-required"},
		{"missing name", CreateInstanceRequest{Blueprint: "grafana", Org: "acme"}, true, "missing-required"},
		{"name has uppercase", CreateInstanceRequest{Blueprint: "grafana", Org: "acme", Name: "Obs1"}, true, "invalid-name"},
		{"name has trailing dash", CreateInstanceRequest{Blueprint: "grafana", Org: "acme", Name: "obs-"}, true, "invalid-name"},
		{"name has leading dash", CreateInstanceRequest{Blueprint: "grafana", Org: "acme", Name: "-obs"}, true, "invalid-name"},
		{"name too short (1 char)", CreateInstanceRequest{Blueprint: "grafana", Org: "acme", Name: "a"}, true, "invalid-name"},
		{"name 2 chars ok (a-z digits)", CreateInstanceRequest{Blueprint: "grafana", Org: "acme", Name: "ab"}, false, ""},
		{"name very long ok up to 42 chars", CreateInstanceRequest{Blueprint: "grafana", Org: "acme", Name: "abcdefghijklmnopqrstuvwxyz1234567890123456"}, false, ""},
		{"name 43 chars (over pattern)", CreateInstanceRequest{Blueprint: "grafana", Org: "acme", Name: "abcdefghijklmnopqrstuvwxyz12345678901234567"}, true, "invalid-name"},
		{"isolation level invalid", CreateInstanceRequest{Blueprint: "grafana", Org: "acme", Name: "obs-1", IsolationLevel: "host"}, true, "isolation-level-invalid"},
		{"isolation namespace ok", CreateInstanceRequest{Blueprint: "grafana", Org: "acme", Name: "obs-1", IsolationLevel: "namespace"}, false, ""},
		{"isolation vcluster ok", CreateInstanceRequest{Blueprint: "grafana", Org: "acme", Name: "obs-1", IsolationLevel: "vcluster"}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.r.ValidateShape()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr && err.Code != tc.wantCode {
				t.Fatalf("Code = %q, want %q", err.Code, tc.wantCode)
			}
		})
	}
}

func TestCreateInstanceRequest_Build(t *testing.T) {
	r := CreateInstanceRequest{Blueprint: "grafana", Org: "acme", Name: "obs-1"}
	seed, err := r.Build("singleton")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if seed.Name != "obs-1" || seed.Namespace != "acme" {
		t.Fatalf("Name/Namespace = %q/%q", seed.Name, seed.Namespace)
	}
	if seed.Blueprint != "bp-grafana" {
		t.Fatalf("Blueprint = %q, want bp-prefix", seed.Blueprint)
	}
	if seed.Topology != "singleton" {
		t.Fatalf("Topology = %q", seed.Topology)
	}
	if seed.InstanceID == "" || len(seed.InstanceID) != 8 {
		t.Fatalf("InstanceID = %q, want 8-char hex", seed.InstanceID)
	}
	// Hex-only
	for _, c := range seed.InstanceID {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			t.Fatalf("InstanceID %q is not lowercase hex", seed.InstanceID)
		}
	}
	if seed.IsolationLevel != appv1alpha1.IsolationNamespace {
		t.Fatalf("IsolationLevel = %q, want namespace (default)", seed.IsolationLevel)
	}
	if seed.NamingTemplate != "{{.AppName}}-{{.InstanceID}}" {
		t.Fatalf("NamingTemplate = %q", seed.NamingTemplate)
	}
	// Labels populated
	if seed.Labels["catalyst.openova.io/blueprint"] != "grafana" {
		t.Fatalf("blueprint label = %q", seed.Labels["catalyst.openova.io/blueprint"])
	}
	if seed.Labels["catalyst.openova.io/instance"] != seed.InstanceID {
		t.Fatalf("instance label = %q, expected %q", seed.Labels["catalyst.openova.io/instance"], seed.InstanceID)
	}
}

func TestCreateInstanceRequest_Build_BPPrefixIdempotent(t *testing.T) {
	r := CreateInstanceRequest{Blueprint: "bp-grafana", Org: "acme", Name: "obs-1"}
	seed, err := r.Build("singleton")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if seed.Blueprint != "bp-grafana" {
		t.Fatalf("Blueprint = %q, must NOT double-prefix", seed.Blueprint)
	}
}

func TestCreateInstanceRequest_Build_VClusterTemplateDefault(t *testing.T) {
	r := CreateInstanceRequest{Blueprint: "grafana", Org: "acme", Name: "obs-1", IsolationLevel: "vcluster"}
	seed, err := r.Build("singleton")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if seed.IsolationLevel != appv1alpha1.IsolationVCluster {
		t.Fatalf("IsolationLevel = %q, want vcluster", seed.IsolationLevel)
	}
	if seed.NamingTemplate != "{{.AppName}}" {
		t.Fatalf("NamingTemplate = %q, want {{.AppName}} for vcluster", seed.NamingTemplate)
	}
}

func TestCreateInstanceRequest_Build_RejectInvalidShape(t *testing.T) {
	r := CreateInstanceRequest{Blueprint: "grafana", Org: "acme", Name: "BAD"}
	if _, err := r.Build("singleton"); err == nil {
		t.Fatal("expected invalid-name error")
	}
}

func TestNewInstanceID_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 256; i++ {
		id, err := newInstanceID()
		if err != nil {
			t.Fatalf("mint: %v", err)
		}
		if seen[id] {
			t.Fatalf("collision after %d iters on id %q (32 bits should not collide in 256 draws often; possibly RNG broken)", i, id)
		}
		seen[id] = true
		if len(id) != 8 {
			t.Fatalf("len = %d, want 8", len(id))
		}
	}
}

func TestMapDecision(t *testing.T) {
	cases := []struct {
		name        string
		d           admission.Decision
		wantNil     bool
		wantStatus  int
		wantCode    admission.DecisionCode
	}{
		{"allowed → nil", admission.AllowedDecision, true, 0, ""},
		{"multi-instance-disabled → 409", admission.Decision{Allowed: false, Code: admission.CodeMultiInstanceDisabled, Message: "x"}, false, 409, admission.CodeMultiInstanceDisabled},
		{"max-per-org-exceeded → 409", admission.Decision{Allowed: false, Code: admission.CodeMaxPerOrgExceeded, Message: "x"}, false, 409, admission.CodeMaxPerOrgExceeded},
		{"name-collision → 409", admission.Decision{Allowed: false, Code: admission.CodeNameCollision, Message: "x"}, false, 409, admission.CodeNameCollision},
		{"isolation-level-invalid → 422", admission.Decision{Allowed: false, Code: admission.CodeIsolationLevelInvalid, Message: "x"}, false, 422, admission.CodeIsolationLevelInvalid},
		{"instance-id-immutable → 422", admission.Decision{Allowed: false, Code: admission.CodeInstanceIDImmutable, Message: "x"}, false, 422, admission.CodeInstanceIDImmutable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MapDecision(tc.d)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil response")
			}
			if got.StatusCode != tc.wantStatus {
				t.Fatalf("StatusCode = %d, want %d", got.StatusCode, tc.wantStatus)
			}
			if got.Code != tc.wantCode {
				t.Fatalf("Code = %q, want %q", got.Code, tc.wantCode)
			}
		})
	}
}

func TestShapeError_Error(t *testing.T) {
	e := &ShapeError{Code: "x", Message: "y"}
	if !strings.Contains(e.Error(), "x: y") {
		t.Fatalf("Error string = %q", e.Error())
	}
}
