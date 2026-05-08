package controller

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/useraccess/internal/labels"
)

func mkUA(spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(UserAccessGVK())
	u.SetName("test")
	_ = unstructured.SetNestedMap(u.Object, spec, "spec")
	return u
}

func TestParseSpec_HappyPath(t *testing.T) {
	u := mkUA(map[string]any{
		"user": map[string]any{
			"keycloakSubject": "abc-123",
			"keycloakGroups":  []any{"/acme/admins"},
		},
		"sovereignRef": "omantel",
		"applications": []any{
			map[string]any{
				"app":        "wp",
				"role":       "editor",
				"namespaces": []any{"acme-prod"},
			},
		},
	})
	spec, msg := ParseSpec(u)
	if msg != "" {
		t.Fatalf("ParseSpec returned err: %q", msg)
	}
	if len(spec.Subjects) != 2 {
		t.Fatalf("expected 2 subjects, got %d", len(spec.Subjects))
	}
	if spec.Subjects[0].Kind != SubjectKindUser || spec.Subjects[0].Name != "oidc:abc-123" {
		t.Fatalf("user subject: %+v", spec.Subjects[0])
	}
	if spec.Subjects[1].Kind != SubjectKindGroup || spec.Subjects[1].Name != "oidc:/acme/admins" {
		t.Fatalf("group subject: %+v", spec.Subjects[1])
	}
}

func TestParseSpec_Validation(t *testing.T) {
	cases := []struct {
		name    string
		spec    map[string]any
		wantErr string
	}{
		{
			"missing identity",
			map[string]any{
				"user":         map[string]any{},
				"sovereignRef": "omantel",
				"applications": []any{
					map[string]any{"app": "wp", "role": "viewer", "namespaces": []any{"x"}},
				},
			},
			"keycloakSubject",
		},
		{
			"missing sovereignRef",
			map[string]any{
				"user":         map[string]any{"keycloakSubject": "abc"},
				"sovereignRef": "",
				"applications": []any{
					map[string]any{"app": "wp", "role": "viewer", "namespaces": []any{"x"}},
				},
			},
			"sovereignRef",
		},
		{
			"empty applications",
			map[string]any{
				"user":         map[string]any{"keycloakSubject": "abc"},
				"sovereignRef": "omantel",
				"applications": []any{},
			},
			"applications",
		},
		{
			"bad role",
			map[string]any{
				"user":         map[string]any{"keycloakSubject": "abc"},
				"sovereignRef": "omantel",
				"applications": []any{
					map[string]any{"app": "wp", "role": "superuser", "namespaces": []any{"x"}},
				},
			},
			"admin, editor, viewer",
		},
		{
			"missing namespaces+vClusters",
			map[string]any{
				"user":         map[string]any{"keycloakSubject": "abc"},
				"sovereignRef": "omantel",
				"applications": []any{
					map[string]any{"app": "wp", "role": "viewer"},
				},
			},
			"namespaces[] or vClusters[]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, msg := ParseSpec(mkUA(tc.spec))
			if msg == "" {
				t.Fatal("expected error msg, got empty")
			}
			if !contains(msg, tc.wantErr) {
				t.Fatalf("msg=%q want substring %q", msg, tc.wantErr)
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestAppGrant_VClustersExpand(t *testing.T) {
	g := AppGrant{
		App:       "wp",
		Role:      "viewer",
		VClusters: []string{"acme", "globex"},
	}
	got := g.MaterializedNamespaces()
	want := map[string]bool{"vcluster-acme": true, "vcluster-globex": true}
	if len(got) != 2 {
		t.Fatalf("expected 2 namespaces, got %d", len(got))
	}
	for _, ns := range got {
		if !want[ns] {
			t.Fatalf("unexpected ns %q", ns)
		}
	}
}

func TestAppGrant_IsClusterWide(t *testing.T) {
	if !(AppGrant{Namespaces: []string{"*"}}).IsClusterWide() {
		t.Fatal("[*] should be cluster-wide")
	}
	if !(AppGrant{Namespaces: []string{"foo", "*"}}).IsClusterWide() {
		t.Fatal("any wildcard wins")
	}
	if (AppGrant{Namespaces: []string{"foo", "bar"}}).IsClusterWide() {
		t.Fatal("explicit ns should NOT be cluster-wide")
	}
}

func TestEffectiveScopes_DeveloperGetsEnvType(t *testing.T) {
	spec := UserAccessSpec{
		Tier: "developer",
		Scopes: []labels.Scope{
			{Key: "openova.io/application", Value: "wordpress"},
		},
	}
	got := spec.EffectiveScopes()
	if len(got) != 2 {
		t.Fatalf("expected 2 scopes (1 user-declared + 1 enforced), got %d", len(got))
	}
	// First is the user-declared scope, second is the enforced one.
	if got[1].Key != "openova.io/env-type" || got[1].Value != "dev" {
		t.Fatalf("enforced scope wrong: %+v", got[1])
	}
}

func TestEffectiveScopes_NonDeveloperUnchanged(t *testing.T) {
	spec := UserAccessSpec{
		Tier: "admin",
		Scopes: []labels.Scope{
			{Key: "openova.io/organization", Value: "acme"},
		},
	}
	got := spec.EffectiveScopes()
	if len(got) != 1 {
		t.Fatalf("admin tier must not auto-inject; got %d scopes", len(got))
	}
}
