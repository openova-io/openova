// Package handler — short_form_vocab_test.go: unit coverage for the
// canonical UAT matrix's short-form vocabulary aliases added in
// iter-7 fix #35 (qa-loop). One file covers every endpoint that
// widened to accept short-form bodies so the matrix's vocabulary
// becomes a regression-tested contract.
//
// Per `feedback_no_mvp_no_workarounds.md` the matrix is the contract;
// these tests assert the handler conforms (no `unknown field` 400s on
// the matrix's body shapes).
package handler

import (
	"testing"
)

// ── policy_mode.go: short-form `mode` (+ optional `policy`) ──

func TestExpandShortFormMode_Bare(t *testing.T) {
	body := policyModeRequest{Mode: "Audit"}
	out := expandShortFormMode(body)
	if out.Modes == nil {
		t.Fatalf("Modes should be populated")
	}
	if v, ok := out.Modes[policyModeBulkSentinel]; !ok || v != "Audit" {
		t.Fatalf("expected sentinel %q to map to Audit, got %v", policyModeBulkSentinel, out.Modes)
	}
}

func TestExpandShortFormMode_WithPolicy(t *testing.T) {
	body := policyModeRequest{Mode: "Enforce", Policy: "runasnonroot"}
	out := expandShortFormMode(body)
	if v, ok := out.Modes["runasnonroot"]; !ok || v != "Enforce" {
		t.Fatalf("expected runasnonroot=Enforce, got %v", out.Modes)
	}
	if _, ok := out.Modes[policyModeBulkSentinel]; ok {
		t.Fatalf("sentinel should NOT be set when policy is supplied")
	}
}

func TestExpandShortFormMode_LongFormPreserved(t *testing.T) {
	body := policyModeRequest{
		Modes: map[string]string{"alreadyHere": "permissive"},
		Mode:  "Enforce",
	}
	out := expandShortFormMode(body)
	if out.Modes["alreadyHere"] != "permissive" {
		t.Fatalf("long-form modes entry must be preserved")
	}
	if out.Modes[policyModeBulkSentinel] != "Enforce" {
		t.Fatalf("short-form sentinel must coexist with long-form entries")
	}
}

func TestExpandShortFormMode_NoMode_Noop(t *testing.T) {
	body := policyModeRequest{Modes: map[string]string{"x": "permissive"}}
	out := expandShortFormMode(body)
	if len(out.Modes) != 1 || out.Modes["x"] != "permissive" {
		t.Fatalf("expected unchanged Modes map, got %v", out.Modes)
	}
}

// ── applications.go: install short form ──

func TestApplicationInstallRequestNormalize_ShortForm(t *testing.T) {
	body := applicationInstallRequest{
		BlueprintShort: "bp-wordpress",
		VersionShort:   "latest",
		NamespaceShort: "qa-omantel",
		Name:           "qa-wp",
		ValuesShort:    map[string]interface{}{"siteTitle": "QA WP"},
	}
	out := applicationInstallRequestNormalize(body)
	if out.BlueprintRef.Name != "bp-wordpress" {
		t.Errorf("BlueprintRef.Name=%q, want bp-wordpress", out.BlueprintRef.Name)
	}
	if out.BlueprintRef.Version != "latest" {
		t.Errorf("BlueprintRef.Version=%q, want latest", out.BlueprintRef.Version)
	}
	if out.OrganizationRef != "qa-omantel" {
		t.Errorf("OrganizationRef=%q, want qa-omantel", out.OrganizationRef)
	}
	if out.EnvironmentRef != "qa-omantel-prod" {
		t.Errorf("EnvironmentRef=%q, want qa-omantel-prod (default)", out.EnvironmentRef)
	}
	if out.Parameters["siteTitle"] != "QA WP" {
		t.Errorf("Parameters not collapsed from values; got %v", out.Parameters)
	}
	if out.Placement.Mode != "single-region" {
		t.Errorf("Placement.Mode=%q, want single-region default", out.Placement.Mode)
	}
}

func TestApplicationInstallRequestNormalize_LongFormWins(t *testing.T) {
	body := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-x", Version: "1.0"},
		OrganizationRef: "real-org",
		EnvironmentRef:  "real-org-stg",
		Parameters:      map[string]interface{}{"a": 1},
		Placement:       applicationPlacement{Mode: "active-active", Regions: []string{"r1", "r2"}},
		BlueprintShort:  "bp-y",
		VersionShort:    "2.0",
		NamespaceShort:  "ns-y",
	}
	out := applicationInstallRequestNormalize(body)
	if out.BlueprintRef.Name != "bp-x" {
		t.Errorf("long-form BlueprintRef.Name should win; got %q", out.BlueprintRef.Name)
	}
	if out.OrganizationRef != "real-org" {
		t.Errorf("long-form OrganizationRef should win; got %q", out.OrganizationRef)
	}
	if out.EnvironmentRef != "real-org-stg" {
		t.Errorf("long-form EnvironmentRef should win; got %q", out.EnvironmentRef)
	}
	if out.Placement.Mode != "active-active" {
		t.Errorf("Placement preserved; got %q", out.Placement.Mode)
	}
}

// ── applications_preview.go: preview short form ──

func TestApplicationPreviewRequestNormalize_ShortForm(t *testing.T) {
	body := applicationPreviewRequest{
		BlueprintShort: "bp-wordpress",
		VersionShort:   "latest",
		NamespaceShort: "qa-omantel",
		ValuesShort:    map[string]interface{}{"siteTitle": "QA"},
	}
	out := applicationPreviewRequestNormalize(body)
	if out.BlueprintRef.Name != "bp-wordpress" || out.BlueprintRef.Version != "latest" {
		t.Errorf("Blueprint not collapsed: %+v", out.BlueprintRef)
	}
	if out.OrganizationRef != "qa-omantel" {
		t.Errorf("OrganizationRef=%q, want qa-omantel", out.OrganizationRef)
	}
	if out.Parameters["siteTitle"] != "QA" {
		t.Errorf("values→parameters: got %v", out.Parameters)
	}
}

// ── applications_update.go: update short form ──

func TestApplicationUpdateRequestNormalize_Values(t *testing.T) {
	body := applicationUpdateRequest{
		ValuesShort: map[string]interface{}{"siteTitle": "QA Updated"},
	}
	out := applicationUpdateRequestNormalize(body)
	if v, ok := out.Parameters["siteTitle"]; !ok || v != "QA Updated" {
		t.Errorf("values not promoted to parameters; got %v", out.Parameters)
	}
}

func TestApplicationUpdateRequestNormalize_ToVersion(t *testing.T) {
	body := applicationUpdateRequest{ToVersionShort: "5.6.1"}
	out := applicationUpdateRequestNormalize(body)
	if out.BlueprintRef == nil || out.BlueprintRef.Version != "5.6.1" {
		t.Errorf("toVersion not promoted to BlueprintRef.Version; got %+v", out.BlueprintRef)
	}
}

func TestApplicationUpdateRequestNormalize_Version(t *testing.T) {
	body := applicationUpdateRequest{VersionShort: "5.7.0"}
	out := applicationUpdateRequestNormalize(body)
	if out.BlueprintRef == nil || out.BlueprintRef.Version != "5.7.0" {
		t.Errorf("version not promoted to BlueprintRef.Version; got %+v", out.BlueprintRef)
	}
}

func TestApplicationUpdateRequestNormalize_LongFormWins(t *testing.T) {
	body := applicationUpdateRequest{
		Parameters:     map[string]interface{}{"long": "form"},
		ValuesShort:    map[string]interface{}{"short": "form"},
		ToVersionShort: "5.6.1",
		BlueprintRef:   &applicationBlueprintRef{Version: "5.0.0"},
	}
	out := applicationUpdateRequestNormalize(body)
	if out.Parameters["long"] != "form" || out.Parameters["short"] != nil {
		t.Errorf("long-form parameters should win; got %v", out.Parameters)
	}
	if out.BlueprintRef.Version != "5.0.0" {
		t.Errorf("long-form version should win; got %q", out.BlueprintRef.Version)
	}
}

// ── applications_update.go: change-preview (upgrade/topology) short form ──

func TestApplicationChangePreviewRequestNormalize_ToVersion(t *testing.T) {
	body := applicationChangePreviewRequest{ToVersionShort: "5.7.0"}
	out := applicationChangePreviewRequestNormalize(body)
	if out.BlueprintRef == nil || out.BlueprintRef.Version != "5.7.0" {
		t.Errorf("toVersion not promoted; got %+v", out.BlueprintRef)
	}
}

func TestApplicationChangePreviewRequestNormalize_BlueprintAndValues(t *testing.T) {
	body := applicationChangePreviewRequest{
		BlueprintShort: "bp-x",
		ValuesShort:    map[string]interface{}{"k": "v"},
	}
	out := applicationChangePreviewRequestNormalize(body)
	if out.BlueprintRef == nil || out.BlueprintRef.Name != "bp-x" {
		t.Errorf("blueprint not promoted; got %+v", out.BlueprintRef)
	}
	if out.Parameters["k"] != "v" {
		t.Errorf("values not promoted to parameters; got %v", out.Parameters)
	}
}

// ── rbac_assign.go: short-form email/scopeType/scopeName + super-admin ──

func TestRBACAssignRequestNormalize_ShortForm(t *testing.T) {
	body := rbacAssignRequest{
		EmailShort:     "qa-user1@openova.io",
		Tier:           "developer",
		ScopeTypeShort: "application",
		ScopeNameShort: "qa-wp",
	}
	out := rbacAssignRequestNormalize(body)
	if out.User.Email != "qa-user1@openova.io" {
		t.Errorf("Email not promoted; got %q", out.User.Email)
	}
	if len(out.Scope) != 1 {
		t.Fatalf("Scope length=%d, want 1", len(out.Scope))
	}
	if out.Scope[0].Key != scopeKeyApplication {
		t.Errorf("Scope[0].Key=%q, want %q", out.Scope[0].Key, scopeKeyApplication)
	}
	if out.Scope[0].Value != "qa-wp" {
		t.Errorf("Scope[0].Value=%q, want qa-wp", out.Scope[0].Value)
	}
}

func TestRBACAssignRequestNormalize_OrgScope(t *testing.T) {
	body := rbacAssignRequest{
		EmailShort:     "crossOrg@x.io",
		Tier:           "admin",
		ScopeTypeShort: "organization",
		ScopeNameShort: "org-B",
	}
	out := rbacAssignRequestNormalize(body)
	if out.Scope[0].Key != scopeKeyOrg {
		t.Errorf("Scope[0].Key=%q, want %q", out.Scope[0].Key, scopeKeyOrg)
	}
	if out.Scope[0].Value != "org-B" {
		t.Errorf("Scope[0].Value=%q, want org-B", out.Scope[0].Value)
	}
}

func TestRBACAssignRequestNormalize_Global(t *testing.T) {
	body := rbacAssignRequest{
		EmailShort: "qa@openova.io",
		Tier:       "super-admin",
	}
	out := rbacAssignRequestNormalize(body)
	if len(out.Scope) != 0 {
		t.Errorf("Scope must be empty for global super-admin grant; got %v", out.Scope)
	}
	if out.User.Email != "qa@openova.io" {
		t.Errorf("Email not promoted; got %q", out.User.Email)
	}
}

func TestRBACAssignAllowedTiers_SuperAdmin(t *testing.T) {
	if _, ok := rbacAssignAllowedTiers["super-admin"]; !ok {
		t.Errorf("super-admin must be in rbacAssignAllowedTiers")
	}
}

func TestRBACAssignTierResolved_SuperAdminToOwner(t *testing.T) {
	if got := rbacAssignTierResolved("super-admin"); got != "owner" {
		t.Errorf("super-admin should resolve to owner; got %q", got)
	}
	if got := rbacAssignTierResolved("admin"); got != "admin" {
		t.Errorf("admin should resolve to admin; got %q", got)
	}
	if got := rbacAssignTierResolved("DEVELOPER"); got != "developer" {
		t.Errorf("case-insensitive; got %q", got)
	}
}

func TestRBACAssignScopeKeyForType_Mappings(t *testing.T) {
	cases := map[string]string{
		"application":  scopeKeyApplication,
		"app":          scopeKeyApplication,
		"organization": scopeKeyOrg,
		"org":          scopeKeyOrg,
		"env-type":     scopeKeyEnvType,
		"envType":      scopeKeyEnvType,
		"custom":       "custom", // pass-through
	}
	for in, want := range cases {
		if got := rbacAssignScopeKeyForType(in); got != want {
			t.Errorf("%q→%q, want %q", in, got, want)
		}
	}
}

func TestValidateRBACAssignRequest_SuperAdmin(t *testing.T) {
	body := rbacAssignRequest{
		User: rbacAssignUserBody{Email: "qa@openova.io"},
		Tier: "super-admin",
	}
	if msg, ok := validateRBACAssignRequest(body); !ok {
		t.Errorf("super-admin should validate; got %q", msg)
	}
}

// ── user_access.go: short-form email/tier ──

func TestUserAccessRequestNormalize_PostShortForm(t *testing.T) {
	body := userAccessRequest{
		EmailShort: "qa-user2@openova.io",
		TierShort:  "viewer",
	}
	out := userAccessRequestNormalize(body, "sovereign-omantel.biz", "")
	if out.Name == "" {
		t.Errorf("Name should be derived from email")
	}
	if out.Spec.User.KeycloakSubject != "qa-user2@openova.io" {
		t.Errorf("KeycloakSubject not derived from email; got %q", out.Spec.User.KeycloakSubject)
	}
	if out.Spec.SovereignRef != "sovereign-omantel.biz" {
		t.Errorf("SovereignRef not derived from depID; got %q", out.Spec.SovereignRef)
	}
	if len(out.Spec.Applications) != 1 {
		t.Fatalf("Applications length=%d, want 1", len(out.Spec.Applications))
	}
	if out.Spec.Applications[0].App != "*" {
		t.Errorf("App=%q, want *", out.Spec.Applications[0].App)
	}
	if out.Spec.Applications[0].Role != "viewer" {
		t.Errorf("Role=%q, want viewer", out.Spec.Applications[0].Role)
	}
}

func TestUserAccessRequestNormalize_PutShortForm(t *testing.T) {
	body := userAccessRequest{TierShort: "developer"}
	out := userAccessRequestNormalize(body, "sov", "qa-user2")
	if out.Name != "qa-user2" {
		t.Errorf("Name should come from urlName; got %q", out.Name)
	}
	if len(out.Spec.Applications) != 1 || out.Spec.Applications[0].Role != "editor" {
		t.Errorf("developer should map to editor; got %+v", out.Spec.Applications)
	}
}

func TestUserAccessTierToRole(t *testing.T) {
	cases := map[string]string{
		"viewer":      "viewer",
		"developer":   "editor",
		"operator":    "editor",
		"admin":       "admin",
		"owner":       "admin",
		"super-admin": "admin",
		"DEVELOPER":   "editor",
		"unknown":     "unknown", // pass-through
	}
	for in, want := range cases {
		if got := userAccessTierToRole(in); got != want {
			t.Errorf("%q→%q, want %q", in, got, want)
		}
	}
}

func TestIsUserAccessShortFormPut(t *testing.T) {
	cases := []struct {
		name string
		body userAccessRequest
		want bool
	}{
		{"bare-tier", userAccessRequest{TierShort: "developer"}, true},
		{"bare-email", userAccessRequest{EmailShort: "x@y.io"}, true},
		{"long-form-no-aliases", userAccessRequest{Spec: userAccessSpecBody{Applications: []userAccessAppGrantBody{{App: "wp", Role: "editor"}}}}, false},
		{"mixed-with-real-app", userAccessRequest{TierShort: "x", Spec: userAccessSpecBody{Applications: []userAccessAppGrantBody{{App: "wp", Role: "editor"}}}}, false},
		{"groups-set", userAccessRequest{TierShort: "x", Spec: userAccessSpecBody{User: userAccessUserBody{KeycloakGroups: []string{"g1"}}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUserAccessShortFormPut(tc.body); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// ── continuum.go: short-form `target` alias ──
//
// The handler-level promotion (Target → TargetRegion) lives inline in
// HandleContinuumSwitchoverRequest; this assertion guards the struct
// shape so a future refactor can't silently drop the alias field.

func TestContinuumSwitchoverRequest_TargetAlias(t *testing.T) {
	body := continuumSwitchoverRequest{Target: "hz-hel-rtz-prod"}
	if body.Target != "hz-hel-rtz-prod" {
		t.Errorf("Target field must be present and round-trip; got %q", body.Target)
	}
	if body.TargetRegion != "" {
		t.Errorf("TargetRegion not auto-populated by struct (handler does it); got %q", body.TargetRegion)
	}
}
