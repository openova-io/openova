// Unit tests for the multi-instance + name-collision + immutability
// gates. Table-driven coverage of every Decision.Code the wire
// contract exposes (docs/api/catalyst-api-openapi.yaml Error.code).
package admission

import "testing"

func TestEvaluateCreate(t *testing.T) {
	cases := []struct {
		name     string
		req      CreateRequest
		mi       BlueprintMultiInstance
		existing []ExistingApplication
		want     Decision
	}{
		{
			name: "happy path — singleton blueprint, empty Org",
			req:  CreateRequest{Blueprint: "wordpress", Org: "acme", Name: "marketing"},
			mi:   BlueprintMultiInstance{Enabled: false},
			want: AllowedDecision,
		},
		{
			name: "happy path — multi-instance blueprint, 2 of 5 cap",
			req:  CreateRequest{Blueprint: "grafana", Org: "acme", Name: "obs-3"},
			mi:   BlueprintMultiInstance{Enabled: true, MaxPerOrg: 5},
			existing: []ExistingApplication{
				{Name: "obs-1", InstanceID: "a1", Blueprint: "grafana"},
				{Name: "obs-2", InstanceID: "a2", Blueprint: "grafana"},
			},
			want: AllowedDecision,
		},
		{
			name: "multi-instance-disabled — singleton blueprint with existing instance",
			req:  CreateRequest{Blueprint: "wordpress", Org: "acme", Name: "marketing-2"},
			mi:   BlueprintMultiInstance{Enabled: false},
			existing: []ExistingApplication{
				{Name: "marketing-1", InstanceID: "a1", Blueprint: "wordpress"},
			},
			want: Decision{Allowed: false, Code: CodeMultiInstanceDisabled},
		},
		{
			name: "multi-instance-disabled — bp- prefix normalisation",
			req:  CreateRequest{Blueprint: "bp-wordpress", Org: "acme", Name: "marketing-2"},
			mi:   BlueprintMultiInstance{Enabled: false},
			existing: []ExistingApplication{
				// Stored without prefix — must still match.
				{Name: "marketing-1", InstanceID: "a1", Blueprint: "wordpress"},
			},
			want: Decision{Allowed: false, Code: CodeMultiInstanceDisabled},
		},
		{
			name: "max-per-org-exceeded — at cap",
			req:  CreateRequest{Blueprint: "grafana", Org: "acme", Name: "obs-4"},
			mi:   BlueprintMultiInstance{Enabled: true, MaxPerOrg: 3},
			existing: []ExistingApplication{
				{Name: "obs-1", InstanceID: "a1", Blueprint: "grafana"},
				{Name: "obs-2", InstanceID: "a2", Blueprint: "grafana"},
				{Name: "obs-3", InstanceID: "a3", Blueprint: "grafana"},
			},
			want: Decision{Allowed: false, Code: CodeMaxPerOrgExceeded},
		},
		{
			name: "max-per-org-exceeded — over cap (defensive)",
			req:  CreateRequest{Blueprint: "grafana", Org: "acme", Name: "obs-5"},
			mi:   BlueprintMultiInstance{Enabled: true, MaxPerOrg: 2},
			existing: []ExistingApplication{
				{Name: "obs-1", InstanceID: "a1", Blueprint: "grafana"},
				{Name: "obs-2", InstanceID: "a2", Blueprint: "grafana"},
				{Name: "obs-3", InstanceID: "a3", Blueprint: "grafana"},
			},
			want: Decision{Allowed: false, Code: CodeMaxPerOrgExceeded},
		},
		{
			name: "maxPerOrg=0 means unlimited",
			req:  CreateRequest{Blueprint: "grafana", Org: "acme", Name: "obs-999"},
			mi:   BlueprintMultiInstance{Enabled: true, MaxPerOrg: 0},
			existing: func() []ExistingApplication {
				out := make([]ExistingApplication, 50)
				for i := range out {
					out[i] = ExistingApplication{Name: nthName(i), InstanceID: "x", Blueprint: "grafana"}
				}
				return out
			}(),
			want: AllowedDecision,
		},
		{
			name: "name-collision — duplicate Application name in same Org for same Blueprint, multi-instance enabled",
			req:  CreateRequest{Blueprint: "grafana", Org: "acme", Name: "obs-1"},
			mi:   BlueprintMultiInstance{Enabled: true, MaxPerOrg: 5},
			existing: []ExistingApplication{
				{Name: "obs-1", InstanceID: "a1", Blueprint: "grafana"},
			},
			want: Decision{Allowed: false, Code: CodeNameCollision},
		},
		{
			name: "name-collision — duplicate Application name even with singleton (CRD invariant)",
			req:  CreateRequest{Blueprint: "wordpress", Org: "acme", Name: "marketing-1"},
			mi:   BlueprintMultiInstance{Enabled: false},
			existing: []ExistingApplication{
				{Name: "marketing-1", InstanceID: "a1", Blueprint: "wordpress"},
			},
			// name-collision wins over multi-instance-disabled per
			// evaluation order — actionable for the caller.
			want: Decision{Allowed: false, Code: CodeNameCollision},
		},
		{
			name: "different blueprint same name in same Org — allowed (Apps are blueprint-scoped within Org)",
			req:  CreateRequest{Blueprint: "grafana", Org: "acme", Name: "obs"},
			mi:   BlueprintMultiInstance{Enabled: true, MaxPerOrg: 5},
			existing: []ExistingApplication{
				{Name: "obs", InstanceID: "a1", Blueprint: "wordpress"},
			},
			want: AllowedDecision,
		},
		{
			name: "isolation-level-invalid — unknown value rejected",
			req:  CreateRequest{Blueprint: "grafana", Org: "acme", Name: "obs", IsolationLevel: "host-per-instance"},
			mi:   BlueprintMultiInstance{Enabled: true, MaxPerOrg: 5},
			want: Decision{Allowed: false, Code: CodeIsolationLevelInvalid},
		},
		{
			name: "isolation-level — empty is valid (admission defaults)",
			req:  CreateRequest{Blueprint: "grafana", Org: "acme", Name: "obs", IsolationLevel: ""},
			mi:   BlueprintMultiInstance{Enabled: true, MaxPerOrg: 5},
			want: AllowedDecision,
		},
		{
			name: "isolation-level — namespace explicit valid",
			req:  CreateRequest{Blueprint: "grafana", Org: "acme", Name: "obs", IsolationLevel: "namespace"},
			mi:   BlueprintMultiInstance{Enabled: true, MaxPerOrg: 5},
			want: AllowedDecision,
		},
		{
			name: "isolation-level — vcluster explicit valid",
			req:  CreateRequest{Blueprint: "grafana", Org: "acme", Name: "obs", IsolationLevel: "vcluster"},
			mi:   BlueprintMultiInstance{Enabled: true, MaxPerOrg: 5},
			want: AllowedDecision,
		},
		{
			name: "missing required — empty name",
			req:  CreateRequest{Blueprint: "grafana", Org: "acme", Name: ""},
			mi:   BlueprintMultiInstance{Enabled: true},
			want: Decision{Allowed: false, Code: CodeNameCollision}, // best-fit (no missing-required code yet)
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateCreate(tc.req, tc.mi, tc.existing)
			if got.Allowed != tc.want.Allowed {
				t.Fatalf("Allowed = %v, want %v (got=%s)", got.Allowed, tc.want.Allowed, got)
			}
			if !tc.want.Allowed && got.Code != tc.want.Code {
				t.Fatalf("Code = %q, want %q (got=%s)", got.Code, tc.want.Code, got)
			}
			if tc.want.Allowed && got.Code != "" {
				t.Fatalf("Allowed result must have empty Code, got %q", got.Code)
			}
		})
	}
}

func TestEvaluateUpdate(t *testing.T) {
	cases := []struct {
		name string
		req  UpdateRequest
		want Decision
	}{
		{
			name: "happy path — instanceId unchanged",
			req:  UpdateRequest{PriorInstanceID: "a3f7c91d", NewInstanceID: "a3f7c91d", PriorIsolationLevel: "namespace", NewIsolationLevel: "namespace"},
			want: AllowedDecision,
		},
		{
			name: "happy path — first-time set (prior empty)",
			req:  UpdateRequest{PriorInstanceID: "", NewInstanceID: "a3f7c91d"},
			want: AllowedDecision,
		},
		{
			name: "instance-id-immutable — mutation rejected",
			req:  UpdateRequest{PriorInstanceID: "a3f7c91d", NewInstanceID: "b8d2e1c5"},
			want: Decision{Allowed: false, Code: CodeInstanceIDImmutable},
		},
		{
			name: "instance-id-immutable — clearing rejected",
			req:  UpdateRequest{PriorInstanceID: "a3f7c91d", NewInstanceID: ""},
			want: Decision{Allowed: false, Code: CodeInstanceIDImmutable},
		},
		{
			name: "isolation-level-invalid on Update",
			req:  UpdateRequest{PriorInstanceID: "a", NewInstanceID: "a", NewIsolationLevel: "host"},
			want: Decision{Allowed: false, Code: CodeIsolationLevelInvalid},
		},
		{
			name: "isolation-level change namespace → vcluster permitted at admission",
			req:  UpdateRequest{PriorInstanceID: "a", NewInstanceID: "a", PriorIsolationLevel: "namespace", NewIsolationLevel: "vcluster"},
			want: AllowedDecision,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateUpdate(tc.req)
			if got.Allowed != tc.want.Allowed {
				t.Fatalf("Allowed = %v, want %v (got=%s)", got.Allowed, tc.want.Allowed, got)
			}
			if !tc.want.Allowed && got.Code != tc.want.Code {
				t.Fatalf("Code = %q, want %q (got=%s)", got.Code, tc.want.Code, got)
			}
		})
	}
}

func TestDecision_AsError(t *testing.T) {
	if err := AllowedDecision.AsError(); err != nil {
		t.Fatalf("Allowed → nil error, got %v", err)
	}
	denied := Decision{Allowed: false, Code: CodeMultiInstanceDisabled, Message: "nope"}
	err := denied.AsError()
	if err == nil {
		t.Fatal("denied → non-nil error")
	}
	if got := err.Error(); got != "denied(multi-instance-disabled): nope" {
		t.Fatalf("error string = %q", got)
	}
}

func TestDecision_String(t *testing.T) {
	if AllowedDecision.String() != "allowed" {
		t.Fatalf("Allowed → %q", AllowedDecision.String())
	}
	denied := Decision{Allowed: false, Code: CodeNameCollision, Message: "x"}
	if denied.String() != "denied(name-collision): x" {
		t.Fatalf("denied String = %q", denied.String())
	}
}

func TestNormaliseBlueprint(t *testing.T) {
	for in, want := range map[string]string{
		"grafana":      "grafana",
		"bp-grafana":   "grafana",
		"  grafana  ":  "grafana",
		"bp-bp-double": "bp-double", // only single prefix stripped
		"":             "",
	} {
		if got := normaliseBlueprint(in); got != want {
			t.Errorf("normaliseBlueprint(%q) = %q, want %q", in, got, want)
		}
	}
}

// nthName helper for the unlimited-cap test fixture.
func nthName(i int) string {
	return "obs-" + intToStr(i)
}

func intToStr(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
