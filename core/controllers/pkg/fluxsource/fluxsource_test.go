package fluxsource

import (
	"errors"
	"testing"
)

func TestIsLocalSovereignSource(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"in-cluster gitea service", "http://gitea-http.gitea.svc.cluster.local:3000/omantel-biz/agenity.git", true},
		{"in-cluster gitea no scheme", "gitea-http.gitea.svc.cluster.local:3000/omantel-biz/agenity.git", true},
		{"per-sovereign gitea dns", "https://gitea.hfmp.omantel.biz/openova/openova", true},
		{"per-sovereign harbor", "https://harbor.hfmp.omantel.biz/proxy-ghcr/loft-sh/vcluster", true},
		{"in-cluster harbor-core service", "http://harbor-core.harbor.svc.cluster.local/x", true},
		// External sources MUST NOT be guarded:
		{"ghcr oci helmrepo", "oci://ghcr.io/openova-io/bp-wordpress", false},
		{"ghcr https", "https://ghcr.io/openova-io/openova", false},
		{"mothership proxy-cache harbor", "https://harbor.openova.io/proxy-ghcr/loft-sh/vcluster", false},
		{"upstream public chart repo", "https://charts.bitnami.com/bitnami", false},
		{"empty url", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsLocalSovereignSource(tc.url); got != tc.want {
				t.Fatalf("IsLocalSovereignSource(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

// TestBuildGitRepositorySpec_LocalGiteaEmptySecretErrors is the load-bearing
// CI guard for #4285: emitting a Gitea-targeted GitRepository spec with an
// empty secretRef MUST be a hard error so a future forgotten secret fails in
// CI, not live as a 401 "authentication required" source.
func TestBuildGitRepositorySpec_LocalGiteaEmptySecretErrors(t *testing.T) {
	_, err := BuildGitRepositorySpec(GitRepositorySpecInput{
		URL:             "http://gitea-http.gitea.svc.cluster.local:3000/omantel-biz/shared-pg-d.git",
		Branch:          "main",
		IntervalSeconds: 60,
		SecretRef:       "", // forgotten — the exact #4285 defect
	})
	if err == nil {
		t.Fatal("expected error for local-Gitea source with empty secretRef, got nil")
	}
	var target *ErrLocalSourceNeedsSecret
	if !errors.As(err, &target) {
		t.Fatalf("expected *ErrLocalSourceNeedsSecret, got %T: %v", err, err)
	}
}

func TestBuildGitRepositorySpec_LocalGiteaWithSecretStampsRef(t *testing.T) {
	spec, err := BuildGitRepositorySpec(GitRepositorySpecInput{
		URL:             "http://gitea-http.gitea.svc.cluster.local:3000/omantel-biz/shared-pg-d.git",
		Branch:          "main",
		IntervalSeconds: 30,
		SecretRef:       "openova-org-tenants-git-auth",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr, ok := spec["secretRef"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected secretRef map, got %#v", spec["secretRef"])
	}
	if sr["name"] != "openova-org-tenants-git-auth" {
		t.Fatalf("secretRef.name = %v, want openova-org-tenants-git-auth", sr["name"])
	}
	if spec["interval"] != "30s" {
		t.Fatalf("interval = %v, want 30s", spec["interval"])
	}
}

// TestBuildGitRepositorySpec_ExternalSourceNoSecretOK confirms the guard does
// NOT fire for external (ghcr / upstream) sources — an empty secretRef there
// is legitimate and no secretRef key is emitted.
func TestBuildGitRepositorySpec_ExternalSourceNoSecretOK(t *testing.T) {
	spec, err := BuildGitRepositorySpec(GitRepositorySpecInput{
		URL:             "https://ghcr.io/openova-io/openova",
		Branch:          "main",
		IntervalSeconds: 60,
		SecretRef:       "",
	})
	if err != nil {
		t.Fatalf("unexpected error for external source: %v", err)
	}
	if _, present := spec["secretRef"]; present {
		t.Fatalf("external source must not carry a secretRef, got %#v", spec["secretRef"])
	}
}

func TestValidateGiteaSecretRef(t *testing.T) {
	// Local Gitea + empty secret => error.
	if err := ValidateGiteaSecretRef("http://gitea-http.gitea.svc.cluster.local:3000/o/r.git", ""); err == nil {
		t.Fatal("expected error for local-Gitea url with empty secretRef")
	}
	// Local Gitea + secret => nil.
	if err := ValidateGiteaSecretRef("http://gitea-http.gitea.svc.cluster.local:3000/o/r.git", "openova-org-tenants-git-auth"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// External + empty secret => nil.
	if err := ValidateGiteaSecretRef("oci://ghcr.io/openova-io/bp-x", ""); err != nil {
		t.Fatalf("unexpected error for external source: %v", err)
	}
}
