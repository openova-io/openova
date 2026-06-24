// Package gitops renders the per-vCluster Flux GitRepository manifest
// the environment-controller writes into the per-Org Gitea repo.
//
// Per docs/EPICS-1-6-unified-design.md §3.3 + docs/NAMING-CONVENTION.md
// §11.2 item 3, every vCluster realizing an Environment runs ONE Flux
// instance that watches ONE branch (per env_type) across all of the
// Org's Application repos via N GitRepository sources.
//
// This file authors a single canonical GitRepository CR pointing at
// the Org's Gitea repo on the env_type-mapped branch
// (`develop`/`staging`/`main` ↔ `dev`/`stg`/`prod`). Per-Application
// repos and per-Application GitRepository CRs are slice C4
// (application-controller)'s job; here we only seed the Org-wide
// GitRepository so Flux on the host cluster can boot the rest.
//
// Per Inviolable Principle #4 (never hardcode), every value in the
// rendered manifest is derived from Environment.spec or runtime
// configuration. There are NO hardcoded URLs, branches, or intervals.
package gitops

import (
	"bytes"
	"fmt"
	"text/template"

	envv1 "github.com/openova-io/openova/core/controllers/environment/api/v1"
	"github.com/openova-io/openova/core/controllers/pkg/fluxsource"
)

// BranchForEnvType maps the canonical env_type values to Gitea branch
// names per NAMING §11.2 item 1. Values outside the documented enum
// fall back to the env_type string itself; the controller's
// validation surface treats this as a Condition, not a panic.
//
// `prod` → `main` is the load-bearing rule (NAMING §11.2 item 1):
// `develop` → `dev`, `staging` → `stg`, `main` → `prod`.
// `uat` and `poc` are not in §11.2's three-branch mapping; they each
// get their own branch named after the env_type.
func BranchForEnvType(envType string) string {
	switch envType {
	case "dev":
		return "develop"
	case "stg":
		return "staging"
	case "prod":
		return "main"
	case "uat", "poc":
		return envType
	default:
		return envType
	}
}

// JetStreamSubjectPrefix returns the canonical JetStream subject
// prefix for an Environment per NAMING §11.2 item 4 + ARCHITECTURE.md
// §5: `ws.{org}-{envType}.>`.
func JetStreamSubjectPrefix(org, envType string) string {
	return fmt.Sprintf("ws.%s-%s.>", org, envType)
}

// HostClusterName derives the canonical host-cluster name for an
// Environment region per NAMING §4.1: `{provider}-{region}-{bb}-{envType}`.
// When the operator overrode it on the CR (`spec.regions[i].hostCluster`
// non-empty), that wins.
func HostClusterName(r envv1.EnvironmentRegion, envType string) string {
	if r.HostCluster != "" {
		return r.HostCluster
	}
	return fmt.Sprintf("%s-%s-%s-%s", r.Provider, r.Region, r.BuildingBlock, envType)
}

// EnvironmentName composes the canonical Environment name per NAMING
// §11.1: `{org}-{envType}`.
func EnvironmentName(org, envType string) string {
	return fmt.Sprintf("%s-%s", org, envType)
}

// GitRepositoryPath returns the in-Gitea-repo path where the per-vCluster
// Flux GitRepository manifest is committed:
//
//	clusters/<host-cluster>/environments/<env-name>/gitrepository.yaml
//
// Flux on the host cluster reconciles this path tree into the per-Org
// vCluster (per slice C2 brief item 3).
func GitRepositoryPath(hostCluster, envName string) string {
	return fmt.Sprintf("clusters/%s/environments/%s/gitrepository.yaml", hostCluster, envName)
}

// RenderInputs holds the values feeding `gitRepositoryTemplate`.
type RenderInputs struct {
	// EnvName is the Environment CR's metadata.name (e.g. "acme-prod").
	EnvName string

	// Namespace is the K8s namespace inside the vCluster where Flux
	// expects its source CRs. flux-system is the canonical namespace
	// per upstream Flux convention; we don't hardcode it here so test
	// fixtures and per-Sovereign overlays can override.
	Namespace string

	// RepoURL is the Gitea HTTPS URL for the Org's repo
	// (e.g. "https://gitea.hfmp.acme.openova.io/acme/acme-environment.git").
	// Derived from the runtime-configurable Gitea base URL +
	// `{org}/{org}-environment`. The `-environment` suffix is the
	// per-Env scoped repo name; per-Application repos are separate
	// (slice C4 owns those).
	RepoURL string

	// Branch is BranchForEnvType(spec.envType) — the env-type-mapped
	// Gitea branch.
	Branch string

	// IntervalSeconds is the Flux poll interval. Per Inviolable
	// Principle #4 this is configurable, defaulting to 60s on the
	// reconciler side.
	IntervalSeconds int

	// SecretRef is the in-vCluster Kubernetes Secret that holds the
	// Gitea token Flux uses to clone. Empty means anonymous clone.
	SecretRef string

	// OwnerEnvUID/OwnerEnvGen are the parent Environment's UID and
	// generation, included as labels for traceability (per NAMING
	// §11.2 — every per-Env artifact is traceable back to the CR).
	OwnerEnvUID string
	OwnerEnvGen int64
}

// gitRepositoryTemplate renders the Flux v1 GitRepository CR.
//
// We render YAML directly via text/template rather than k8s.io/api
// imports because:
//
//  1. The Flux CRD types (`source.toolkit.fluxcd.io/v1`) are not in
//     the standard k8s.io/api package; pulling fluxcd/source-controller
//     as a dep doubles the binary size and broadens the supply-chain
//     attack surface (Inviolable Principle #2 — quality over
//     convenience).
//  2. The output is committed to a Gitea repo as bytes; a YAML string
//     is the natural shape.
//  3. Idempotent comparison in the Gitea client is byte-equality, so
//     deterministic field ordering (which `text/template` gives us
//     for free) matters more than type fidelity.
const gitRepositoryTemplate = `# This manifest was generated by environment-controller
# (slice C2 of EPIC-0 #1095). Do not edit by hand.
#
# Per docs/NAMING-CONVENTION.md §11.2 item 3, every vCluster realizing
# an Environment runs ONE Flux watching ONE branch (per env_type)
# across all of the Org's Application repos via N GitRepository
# sources. This is the Org-wide seed source; per-Application sources
# land in this same Gitea repo under a sibling path managed by
# application-controller (slice C4).
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: environment
  namespace: {{ .Namespace }}
  labels:
    app.kubernetes.io/managed-by: environment-controller
    catalyst.openova.io/environment: {{ .EnvName }}
{{- if .OwnerEnvUID }}
    catalyst.openova.io/environment-uid: "{{ .OwnerEnvUID }}"
{{- end }}
{{- if .OwnerEnvGen }}
    catalyst.openova.io/environment-generation: "{{ .OwnerEnvGen }}"
{{- end }}
spec:
  interval: {{ .IntervalSeconds }}s
  url: {{ .RepoURL }}
  ref:
    branch: {{ .Branch }}
{{- if .SecretRef }}
  secretRef:
    name: {{ .SecretRef }}
{{- end }}
`

// RenderGitRepository produces the canonical YAML bytes for the
// per-vCluster Flux GitRepository. Pure function; no I/O. The bytes
// are what UpsertFile commits.
func RenderGitRepository(in RenderInputs) ([]byte, error) {
	if in.EnvName == "" || in.Namespace == "" || in.RepoURL == "" || in.Branch == "" {
		return nil, fmt.Errorf("gitops: RenderGitRepository: EnvName, Namespace, RepoURL, Branch required (got %+v)", in)
	}
	// #4285 — enforce the shared "local-Gitea source MUST carry a secretRef"
	// law: bp-gitea REQUIRE_SIGNIN_VIEW=true makes anonymous clone 401, so a
	// Sovereign-local GitRepository with an empty SecretRef is a hard render
	// error (caught by the controller's "RenderError" markDegraded path), not
	// a silently-dead 401 source. This is the leg-D fix — the env-controller
	// previously defaulted SecretRef to the phantom `gitea-flux-token` that no
	// Job mints; the chart now defaults it to openova-org-tenants-git-auth.
	if err := fluxsource.ValidateGiteaSecretRef(in.RepoURL, in.SecretRef); err != nil {
		return nil, fmt.Errorf("gitops: RenderGitRepository: %w", err)
	}
	if in.IntervalSeconds <= 0 {
		in.IntervalSeconds = 60
	}
	tmpl, err := template.New("gitrepository").Parse(gitRepositoryTemplate)
	if err != nil {
		return nil, fmt.Errorf("gitops: parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, in); err != nil {
		return nil, fmt.Errorf("gitops: execute template: %w", err)
	}
	return buf.Bytes(), nil
}
