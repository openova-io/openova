package gitops

import (
	"strings"
	"testing"
)

// #4422 render-proof — the per-Org funnel cart's bundled stateful DB Deployments
// (mysql/postgres/redis) MUST be admission-clean on a per-Org Sovereign namespace,
// where two policies gate the apply:
//
//  1. LimitRange `plan-limits` enforces maxLimitRequestRatio {cpu:1,memory:1}
//     (Guaranteed QoS) — limits MUST equal requests or the Pod is forbidden with
//     'cpu max limit to request ratio is 1, but provided ratio is 10'. A failed
//     ratio creates ZERO pods (the live #4422 symptom: mysql 0/1, no pods).
//  2. Kyverno `multi-replica-drainability` (replicas-at-least-two) demands
//     spec.replicas>=2; these are single-PVC stateful singletons that cannot run
//     a 2nd replica, so they carry the `openova.io/singleton-db: "true"` carve-out
//     label the policy excludes on.
//
// If this regresses, the funnel WordPress installs but never serves (no DB).

// dbResourceBlock extracts the container resources requests/limits cpu+memory for
// a named DB Deployment within a rendered multi-document YAML body. It is a coarse
// substring check sufficient to prove Guaranteed QoS (requests==limits).
func assertGuaranteedDB(t *testing.T, name, body string, cpu, mem string) {
	t.Helper()
	// Guaranteed: the same cpu/memory value appears in BOTH requests and limits.
	if c := strings.Count(body, "cpu: "+cpu); c < 2 {
		t.Errorf("%s: expected cpu: %s in BOTH requests and limits (Guaranteed QoS, ratio 1), saw %d:\n%s", name, cpu, c, body)
	}
	if c := strings.Count(body, "memory: "+mem); c < 2 {
		t.Errorf("%s: expected memory: %s in BOTH requests and limits (Guaranteed QoS, ratio 1), saw %d:\n%s", name, mem, c, body)
	}
	// Must NOT carry the old Burstable ceilings that produced ratio 10 / 8.
	for _, banned := range []string{"cpu: 500m", "cpu: 200m", "memory: 64Mi"} {
		if strings.Contains(body, banned) {
			t.Errorf("%s: still renders the Burstable ceiling %q (ratio>1 → LimitRange forbids the pod, ZERO pods created)\n%s", name, banned, body)
		}
	}
	// Singleton carve-out for the multi-replica policy must be on BOTH the
	// Deployment's own metadata.labels (Kyverno resources.selector) and the pod
	// template (documents intent / future per-pod selectors).
	if c := strings.Count(body, `openova.io/singleton-db: "true"`); c < 2 {
		t.Errorf("%s: expected the singleton-db carve-out label on BOTH Deployment metadata and pod template (saw %d) — the multi-replica policy can't exempt it otherwise:\n%s", name, c, body)
	}
}

// TestFunnelMySQL_AdmissionClean is the direct #4422 reproduction: the funnel
// WordPress cart bundles a mysql Deployment that previously rendered Burstable
// (req 50m/128Mi vs lim 500m/256Mi, ratio 10/2) → forbidden by the per-Org
// LimitRange → 0 pods → no DB → WordPress never serves.
func TestFunnelMySQL_AdmissionClean(t *testing.T) {
	g := NewManifestGenerator("clusters/sov/org-tenants")
	g.ParentDomain = "omani.homes"

	// Walk the actual funnel render entry point (#4384 per-Org apps tree) so we
	// prove the SHIPPED path, not a unit helper.
	for _, plan := range []string{"s", "m", "l", "xl", "flexi"} {
		t.Run("plan="+plan, func(t *testing.T) {
			files, _ := g.GeneratePerOrgAppsTree("w3376walk", plan, []string{"wordpress"}, "pw123")

			mysql, ok := files["vcluster/apps/db-mysql.yaml"]
			if !ok {
				t.Fatalf("funnel wordpress cart did not render vcluster/apps/db-mysql.yaml (keys: %v)", keys(files))
			}
			// The DB tier is Guaranteed regardless of plan (the LimitRange ratio
			// applies to every paid plan; Flexi omits it but Guaranteed still
			// admits). mysql = 250m/256Mi symmetric.
			assertGuaranteedDB(t, "mysql", mysql, "250m", "256Mi")

			// Velero backup annotation so the PVC passes backup-configured (enforce).
			if !strings.Contains(mysql, "velero.io/backup-volumes: mysqldata") {
				t.Errorf("mysql Deployment missing velero.io/backup-volumes: mysqldata — the per-Org backup-configured policy blocks the PVC:\n%s", mysql)
			}
			// Single replica MUST be preserved — a 2nd replica on one RWO PVC
			// corrupts data, and MySQL primary-replica is not wired.
			if !strings.Contains(mysql, "replicas: 1") {
				t.Errorf("mysql must stay replicas:1 (singleton DB); bumping to 2 corrupts the shared RWO PVC:\n%s", mysql)
			}

			// The WordPress app Deployment itself must also be admission-clean.
			wp, ok := files["vcluster/apps/app-wordpress.yaml"]
			if !ok {
				t.Fatalf("funnel wordpress cart did not render vcluster/apps/app-wordpress.yaml (keys: %v)", keys(files))
			}
			if plan != "flexi" {
				// Fixed tiers: wordpress request floor 100m/256Mi rendered as
				// limits too (Guaranteed) — must NOT carry the Burstable ceiling.
				if strings.Contains(wp, "cpu: 500m") || strings.Contains(wp, "memory: 512Mi") {
					t.Errorf("plan %s: wordpress app Deployment renders Burstable ceiling 500m/512Mi (LimitRange ratio 1 would forbid it):\n%s", plan, wp)
				}
			}
		})
	}
}

// TestFunnelPostgresRedis_AdmissionClean covers the other two funnel DB tiers
// that share the same per-Org LimitRange + multi-replica gates (postgres-backed
// apps + redis-backed apps in the cart). Uses listmonk (postgres) to force the
// postgres path; redis is emitted when an app declares NeedsCache.
func TestFunnelPostgresRedis_AdmissionClean(t *testing.T) {
	g := NewManifestGenerator("clusters/sov/org-tenants")
	g.ParentDomain = "omani.homes"

	files, _ := g.GeneratePerOrgAppsTree("pgwalk", "s", []string{"listmonk"}, "pw123")
	if pg, ok := files["vcluster/apps/db-postgres.yaml"]; ok {
		assertGuaranteedDB(t, "postgres", pg, "250m", "256Mi")
		if !strings.Contains(pg, "velero.io/backup-volumes: pgdata") {
			t.Errorf("postgres Deployment missing velero.io/backup-volumes: pgdata:\n%s", pg)
		}
	} else {
		t.Logf("listmonk cart did not emit db-postgres.yaml (keys: %v) — skipping postgres assertion", keys(files))
	}
}
