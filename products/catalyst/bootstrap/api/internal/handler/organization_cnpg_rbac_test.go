package handler

import (
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// TestPerOrgBPCNPGOperatorNotEmitted is the generator-side guard for the #4920
// fix (issue Option B): the Organization tenant overlay must NOT deploy a
// per-Org bp-cnpg operator anymore.
//
// History: the per-Org operator ran webhook-LESS + namespace-scoped and, since
// G65 (#4322), with its cluster-scoped webhookconfigurations RBAC stripped so
// it could not hijack the shared cnpg webhook singletons. But at the pinned
// operator image (CNPG 1.29.0) the mandatory startup `ensurePKI` UNCONDITIONALLY
// get+patches those singletons — the `MANAGE_WEBHOOK_CONFIGURATIONS=false`
// opt-out does not exist until a later CNPG release, so it is a silent no-op on
// 1.29.0. Without the RBAC the operator CrashLoopBackOff'd on ensurePKI → the
// bp-cnpg HelmRelease never went Ready → the Flux `dependsOn: bp-cnpg` gate
// blocked bp-wordpress-tenant / bp-newapi from installing (hw233 walk, #4920).
// Re-granting the RBAC would fix the crash but re-introduce the exact #4322
// cluster-wide caBundle hijack.
//
// FIX: drop the per-Org operator entirely. The platform cnpg-system operator is
// cluster-wide (upstream subchart default `config.clusterWide: true`, no
// WATCH_NAMESPACE) and already reconciles postgresql.cnpg.io/v1.Cluster CRs in
// EVERY namespace — including the host org-tenant namespace the per-Org apps
// install into — AND owns the singleton webhook that admits them. So no per-Org
// operator is needed. This test locks that in: no bp-cnpg.yaml overlay file, and
// no per-Org app HelmRelease `dependsOn: bp-cnpg`.
func TestPerOrgBPCNPGOperatorNotEmitted(t *testing.T) {
	rec := store.OrganizationProvisionRecord{
		OrganizationID:  "t-acme",
		Subdomain:       "acme",
		DomainMode:      store.OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		CompanyName:     "Acme Corp",
		OTECHFQDN:       "otech.example",
		ParentDomain:    "otech.example",
		VClusterName:    "vc-acme",
		TenantNamespace: "org-t-acme",
	}
	files, err := renderOrganizationOverlay(rec, OrganizationChartVersions{CNPG: "1.0.14"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// 1. No per-Org bp-cnpg operator HelmRelease overlay file.
	if _, ok := files["bp-cnpg.yaml"]; ok {
		t.Errorf("#4920 regression: bp-cnpg.yaml must NOT be emitted — the per-Org operator crashloops on ensurePKI (no webhookconfigurations RBAC) and re-granting it re-introduces the #4322 hijack; the cluster-wide platform operator reconciles the Org's Cluster CRs")
	}

	// 2. It must not appear in the kustomization resource list either.
	if k := files["kustomization.yaml"]; strings.Contains(k, "bp-cnpg.yaml") {
		t.Errorf("#4920 regression: kustomization.yaml still lists bp-cnpg.yaml:\n%s", k)
	}

	// 3. No per-Org app HelmRelease may `dependsOn` a per-Org bp-cnpg — the HR
	//    no longer exists, so any such reference wedges the app on
	//    `dependency bp-cnpg not found`. Postgres readiness is owned by the
	//    always-present platform cnpg-system operator (bootstrap-kit slot 16).
	for name, body := range files {
		if strings.Contains(body, "name: bp-cnpg") {
			t.Errorf("#4920 regression: %s still references a per-Org bp-cnpg HelmRelease (dependsOn/sourceRef):\n%s", name, body)
		}
	}
}
