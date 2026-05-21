package gitops

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	envv1 "github.com/openova-io/openova/core/controllers/environment/api/v1"
)

// Verify the env_type→branch mapping table, the load-bearing rule from
// NAMING-CONVENTION.md §11.2 item 1.
func TestBranchForEnvType(t *testing.T) {
	cases := map[string]string{
		"dev":     "develop",
		"stg":     "staging",
		"prod":    "main",
		"uat":     "uat",
		"poc":     "poc",
		"unknown": "unknown",
	}
	for envType, expected := range cases {
		assert.Equal(t, expected, BranchForEnvType(envType), "envType=%q", envType)
	}
}

// Verify the JetStream subject-prefix derivation per NAMING §11.2 item 4.
func TestJetStreamSubjectPrefix(t *testing.T) {
	assert.Equal(t, "ws.acme-prod.>", JetStreamSubjectPrefix("acme", "prod"))
	assert.Equal(t, "ws.acmebank-uat.>", JetStreamSubjectPrefix("acmebank", "uat"))
}

// Verify the host-cluster name derivation per NAMING §4.1 with explicit
// override taking precedence.
func TestHostClusterName(t *testing.T) {
	derived := HostClusterName(envv1.EnvironmentRegion{
		Provider:      "hetzner",
		Region:        "fsn",
		BuildingBlock: "rtz",
	}, "prod")
	assert.Equal(t, "hetzner-fsn-rtz-prod", derived)

	overridden := HostClusterName(envv1.EnvironmentRegion{
		Provider:      "huawei",
		Region:        "muc",
		BuildingBlock: "rtz",
		HostCluster:   "hw-muc-custom",
	}, "prod")
	assert.Equal(t, "hw-muc-custom", overridden, "explicit hostCluster wins")
}

// Verify the canonical Gitea path layout.
func TestGitRepositoryPath(t *testing.T) {
	assert.Equal(t,
		"clusters/hetzner-fsn-rtz-prod/environments/acme-prod/gitrepository.yaml",
		GitRepositoryPath("hetzner-fsn-rtz-prod", "acme-prod"),
	)
}

// Verify the rendered manifest contains all required fields and is
// deterministic across runs (idempotent comparison hinges on this).
func TestRenderGitRepository_DeterministicAndComplete(t *testing.T) {
	in := RenderInputs{
		EnvName:         "acme-prod",
		Namespace:       "flux-system",
		RepoURL:         "https://gitea.hfmp.acme.openova.io/acme/acme-environment.git",
		Branch:          "main",
		IntervalSeconds: 60,
		SecretRef:       "gitea-flux-token",
		OwnerEnvUID:     "abc-123",
		OwnerEnvGen:     5,
	}
	out1, err := RenderGitRepository(in)
	require.NoError(t, err)
	out2, err := RenderGitRepository(in)
	require.NoError(t, err)
	assert.Equal(t, string(out1), string(out2), "render must be deterministic for idempotent compare")

	body := string(out1)
	assert.Contains(t, body, "apiVersion: source.toolkit.fluxcd.io/v1")
	assert.Contains(t, body, "kind: GitRepository")
	assert.Contains(t, body, "name: environment")
	assert.Contains(t, body, "namespace: flux-system")
	assert.Contains(t, body, "interval: 60s")
	assert.Contains(t, body, "url: https://gitea.hfmp.acme.openova.io/acme/acme-environment.git")
	assert.Contains(t, body, "branch: main")
	assert.Contains(t, body, "secretRef:")
	assert.Contains(t, body, "name: gitea-flux-token")
	assert.Contains(t, body, "catalyst.openova.io/environment: acme-prod")
	assert.Contains(t, body, "catalyst.openova.io/environment-uid: \"abc-123\"")
	assert.Contains(t, body, "catalyst.openova.io/environment-generation: \"5\"")
	assert.Contains(t, body, "managed-by: environment-controller")
}

// Verify SecretRef omission renders an anonymous source.
func TestRenderGitRepository_AnonymousWhenNoSecret(t *testing.T) {
	body, err := RenderGitRepository(RenderInputs{
		EnvName:         "acme-dev",
		Namespace:       "flux-system",
		RepoURL:         "https://gitea.hfmp.acme.openova.io/acme/acme-environment.git",
		Branch:          "develop",
		IntervalSeconds: 60,
	})
	require.NoError(t, err)
	assert.NotContains(t, string(body), "secretRef")
}

// Verify default interval is applied when unspecified.
func TestRenderGitRepository_DefaultInterval(t *testing.T) {
	body, err := RenderGitRepository(RenderInputs{
		EnvName:   "acme-prod",
		Namespace: "flux-system",
		RepoURL:   "https://example.com/repo.git",
		Branch:    "main",
		// IntervalSeconds intentionally 0
	})
	require.NoError(t, err)
	assert.Contains(t, string(body), "interval: 60s", "0 should default to 60")
}

// Verify required-field validation.
func TestRenderGitRepository_RequiredFields(t *testing.T) {
	cases := []struct {
		name    string
		in      RenderInputs
		missing string
	}{
		{"missing EnvName", RenderInputs{Namespace: "flux-system", RepoURL: "u", Branch: "main"}, "EnvName"},
		{"missing Namespace", RenderInputs{EnvName: "acme-prod", RepoURL: "u", Branch: "main"}, "Namespace"},
		{"missing RepoURL", RenderInputs{EnvName: "acme-prod", Namespace: "flux-system", Branch: "main"}, "RepoURL"},
		{"missing Branch", RenderInputs{EnvName: "acme-prod", Namespace: "flux-system", RepoURL: "u"}, "Branch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RenderGitRepository(tc.in)
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), tc.missing) || strings.Contains(err.Error(), "required"),
				"expected mention of %q in %q", tc.missing, err.Error())
		})
	}
}

// Verify EnvironmentName composition.
func TestEnvironmentName(t *testing.T) {
	assert.Equal(t, "acme-prod", EnvironmentName("acme", "prod"))
	assert.Equal(t, "acmebank-uat", EnvironmentName("acmebank", "uat"))
}
