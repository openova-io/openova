// mechanism_d_reflection_test.go — locks the #4286 mechanism-D wiring:
// cross-namespace reflection of the canonical Gitea-auth secret into every
// env-controller source-bearing namespace.
//
// THE EPIC (#4286)
// ================
// On a Sovereign, bp-gitea sets service.REQUIRE_SIGNIN_VIEW=true
// (platform/gitea/chart/values.yaml) → an anonymous git clone of the
// in-cluster Gitea returns HTTP 401 for EVERY repo. Therefore every Flux
// source targeting the Sovereign-local Gitea MUST carry a basic-auth
// spec.secretRef AND the referenced secret MUST be PRESENT in the source's
// namespace. The EPIC ships four layers:
//
//	A = shared fluxsource factory   (#4288)
//	B = Kyverno mutate admission    (#4301)
//	C = non-empty config default    (#4288)
//	D = THIS — reflect the canonical secret into every source-bearing ns
//
// THE GAP D CLOSES
// ================
// The env-controller (core/controllers/environment/internal/gitops/render.go)
// renders a per-Environment GitRepository whose spec.secretRef.name is
// openova-org-tenants-git-auth (mechanism C, env-controller-deployment.yaml).
// But that canonical secret is MINTED by gitea-flux-auth-secrets-sync-job ONLY
// in flux-system. An env-controller source that lands in a DIFFERENT namespace
// (a per-env / per-vCluster flux namespace) would resolve the secretRef as
// "secret not found" → 401. Mechanism D mirrors the secret into every
// source-bearing namespace via emberstack/reflector — the same cross-namespace
// seam org-services-secrets.yaml / cnpg-cluster.yaml already use.
//
// THE INVARIANT THIS TEST LOCKS
// =============================
// BOTH creators of openova-org-tenants-git-auth must carry the 4 emberstack
// reflector annotation keys, driven by the operator-overridable allow-list
// orgTenants.gitRepository.secretRef.reflectNamespaces:
//
//  1. templates/org-services/org-repo-gitrepo-auth-secret.yaml — the
//     lookup-template fallback creator.
//  2. templates/catalog-sovereign-flux/gitea-flux-auth-secrets-sync-job.yaml —
//     the reliable runtime creator (annotates the ORG secret it kubectl-applies).
//
// And the env-controller deployment's GITEA_SECRET_REF must point at that SAME
// canonical secret (so the secret that is reflected is the secret the source
// references) — closing the loop.
//
// A future commit that drops the reflection annotations (re-opening the env-
// controller 401 "secret not found" gap) lands HERE as a test failure, not as a
// live non-converging Environment on a fresh Sovereign.
package provisioner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readRepoFile loads a repo-root-relative file as a single string. The repo
// root is resolved the same way cilium_values_parity_test.go does: the
// provisioner package sits 6 segments below the repo root.
func readRepoFile(t *testing.T, relPath ...string) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", "..", "..", "..", "..", ".."))
	p := filepath.Join(append([]string{repoRoot}, relPath...)...)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(raw)
}

// the 4 emberstack-reflector annotation keys mechanism D depends on.
var reflectorAnnotationKeys = []string{
	"reflector.v1.k8s.emberstack.com/reflection-allowed",
	"reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces",
	"reflector.v1.k8s.emberstack.com/reflection-auto-enabled",
	"reflector.v1.k8s.emberstack.com/reflection-auto-namespaces",
}

// TestMechanismD_ValuesDefinesReflectNamespaces locks the operator-overridable
// allow-list knob (#4286 D). Without it the two secret creators have nothing to
// reflect into and the env-controller source 401s with "secret not found".
func TestMechanismD_ValuesDefinesReflectNamespaces(t *testing.T) {
	values := readRepoFile(t, "products", "catalyst", "chart", "values.yaml")
	if !strings.Contains(values, "reflectNamespaces:") {
		t.Fatalf("values.yaml must define orgTenants.gitRepository.secretRef.reflectNamespaces " +
			"(the #4286 mechanism-D reflector allow-list) — the env-controller in-vCluster " +
			"GitRepository secretRef 401s 'secret not found' without it")
	}
	// The default must cover the env-controller source namespace families the
	// keystone vcluster EPIC lands sources into. We assert the canonical
	// default substrings rather than exact equality so an operator widening the
	// list (Inviolable Principle #4) does not break this lock.
	for _, want := range []string{"flux-system", "tenant-", "org-"} {
		if !strings.Contains(values, want) {
			t.Errorf("reflectNamespaces default should cover %q (the env-controller "+
				"source-bearing namespace families)", want)
		}
	}
}

// TestMechanismD_LookupTemplateSecretReflects locks the reflector annotations on
// the lookup-template creator of openova-org-tenants-git-auth.
func TestMechanismD_LookupTemplateSecretReflects(t *testing.T) {
	tpl := readRepoFile(t, "products", "catalyst", "chart", "templates",
		"org-services", "org-repo-gitrepo-auth-secret.yaml")

	// Must reference the canonical secret name (the thing env-controller's
	// secretRef points at and what we reflect).
	if !strings.Contains(tpl, "openova-org-tenants-git-auth") {
		t.Fatalf("org-repo-gitrepo-auth-secret.yaml must create openova-org-tenants-git-auth")
	}
	// Driven by the reflectNamespaces knob (gated so empty disables it).
	if !strings.Contains(tpl, ".reflectNamespaces") && !strings.Contains(tpl, "reflectNamespaces") {
		t.Errorf("org-repo-gitrepo-auth-secret.yaml must wire the reflectNamespaces allow-list")
	}
	for _, key := range reflectorAnnotationKeys {
		if !strings.Contains(tpl, key) {
			t.Errorf("org-repo-gitrepo-auth-secret.yaml missing reflector annotation %q "+
				"(#4286 mechanism D — the secret must mirror into env-controller "+
				"source namespaces)", key)
		}
	}
}

// TestMechanismD_SyncJobReflectsOrgSecret locks the reflector annotations on the
// reliable runtime creator (the post-install/upgrade sync Job). This is the
// creator that actually fires on every Sovereign (the lookup-template never
// emits at fresh-install render time — the whole reason the Job exists, #3668),
// so its reflection wiring is the load-bearing one.
func TestMechanismD_SyncJobReflectsOrgSecret(t *testing.T) {
	tpl := readRepoFile(t, "products", "catalyst", "chart", "templates",
		"catalog-sovereign-flux", "gitea-flux-auth-secrets-sync-job.yaml")

	// The Job must thread the reflect-namespaces allow-list to its container as
	// an env var and pass it to the ORG secret's apply_secret call.
	if !strings.Contains(tpl, "ORG_REFLECT_NAMESPACES") {
		t.Fatalf("gitea-flux-auth-secrets-sync-job.yaml must expose ORG_REFLECT_NAMESPACES " +
			"(#4286 mechanism D) so the runtime creator annotates the org secret for reflection")
	}
	if !strings.Contains(tpl, "reflectNamespaces") {
		t.Errorf("sync Job must source ORG_REFLECT_NAMESPACES from orgTenants...secretRef.reflectNamespaces")
	}
	for _, key := range reflectorAnnotationKeys {
		if !strings.Contains(tpl, key) {
			t.Errorf("gitea-flux-auth-secrets-sync-job.yaml missing reflector annotation %q "+
				"(#4286 mechanism D — the runtime creator must mark the org secret mirror-eligible)", key)
		}
	}
	// The annotations must be applied to the ORG secret (the env-controller's
	// secretRef target), driven by the 4th positional arg of apply_secret. The
	// CATALOG secret (flux-system-only consumer) must NOT be reflected.
	if !strings.Contains(tpl, `apply_secret "$ORG_SECRET_NS" "$ORG_SECRET_NAME" "$ORG_USER" "${ORG_REFLECT_NAMESPACES:-}"`) {
		t.Errorf("apply_secret for the ORG secret must pass ORG_REFLECT_NAMESPACES (the reflect allow-list)")
	}
}

// TestMechanismD_EnvControllerReferencesReflectedSecret closes the loop: the
// env-controller deployment's GITEA_SECRET_REF must point at the SAME canonical
// secret that mechanism D reflects. If these diverge, D mirrors a secret the
// env-controller source never references — the 401 returns.
func TestMechanismD_EnvControllerReferencesReflectedSecret(t *testing.T) {
	dep := readRepoFile(t, "products", "catalyst", "chart", "templates",
		"controllers", "environment-controller-deployment.yaml")

	if !strings.Contains(dep, "GITEA_SECRET_REF") {
		t.Fatalf("environment-controller-deployment.yaml must set GITEA_SECRET_REF")
	}
	// The default the deployment falls back to must be the canonical secret D
	// reflects (mechanism C flipped the phantom gitea-flux-token to this).
	if !strings.Contains(dep, "openova-org-tenants-git-auth") {
		t.Errorf("env-controller GITEA_SECRET_REF must reference openova-org-tenants-git-auth " +
			"(the canonical secret mechanism D reflects); the phantom gitea-flux-token must not return")
	}
	// The phantom secret the #4285/#4286 class eliminated must NOT reappear as a
	// VALUE (a quoted default the deployment falls back to). It may still appear
	// in an explanatory comment documenting that it was eliminated — only a
	// live `"gitea-flux-token"` default re-opens the defect.
	if strings.Contains(dep, `"gitea-flux-token"`) || strings.Contains(dep, `default "gitea-flux-token"`) {
		t.Errorf("env-controller must NOT default GITEA_SECRET_REF to the phantom " +
			"gitea-flux-token secret (no Job ever minted it — the original leg-D defect)")
	}
}
