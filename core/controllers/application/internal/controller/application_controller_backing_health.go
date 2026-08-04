// application_controller_backing_health.go — #5513 backing-workload health.
//
// A downstream Flux HelmRelease reporting Ready=True means only that Helm
// applied the chart's manifests — NOT that the workload the chart installs
// actually came up. For a CNPG-backed Application the load-bearing workload is
// the CNPG Cluster CR, and CNPG has a terminal `unrecoverable` phase ("Cluster
// is unrecoverable and needs manual intervention") in which the database has
// zero pods and cannot serve. Reporting the Application Ready over that state
// is the fabricated DR posture walked live on hw291 (#5513).
//
// This file adds a read-only, failure-tolerant observation the reconcile status
// rollup consults before it lets an Application settle on Ready=True. It never
// writes and never fabricates a downgrade: a missing CNPG CRD (no Postgres in
// this Sovereign) or a transient list error leaves the HR-derived phase intact.
package controller

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// CNPGClusterGVR is the CloudNativePG Cluster CR GVR (postgresql.cnpg.io/v1).
// Namespaced. Read via the dynamic client + Unstructured — the controller
// never depends on a generated CNPG client (mirrors the continuum-controller's
// ADR-0001 §2.7 convention).
var CNPGClusterGVR = schema.GroupVersionResource{
	Group:    "postgresql.cnpg.io",
	Version:  "v1",
	Resource: "clusters",
}

// InstanceLabel is the standard Helm/Kubernetes label a chart stamps on the
// resources it renders, carrying the Helm release name. The bp-postgres /
// cnpg-pair chart stamps it on each CNPG Cluster it creates, so it is the
// stable link from an Application's per-cluster HelmRelease (whose name is the
// release name) back to the CNPG Cluster CR that HelmRelease installed. Walked
// live on hw291: the collapsed CNPG carried
// `app.kubernetes.io/instance=uatwalk-ahs-07300830-rtz-a` (= the HR name).
const InstanceLabel = "app.kubernetes.io/instance"

// backingCNPGUnrecoverable reports whether any CNPG Cluster CR backing this
// Application is in CNPG's terminal `unrecoverable` phase. It returns the
// namespace + name of the first such Cluster and true; ("", "", false) when
// none is unrecoverable (including when there is no CNPG backing at all).
//
// Discovery: the backing CNPG Cluster carries `app.kubernetes.io/instance` set
// to the Helm release name of the HelmRelease that installed it — i.e. each
// per-cluster HR name (the fan-out path) or the bare Application name (the
// legacy single-HR host path). We search that label across the namespaces the
// HRs were authored in, plus the Application's own namespace, so a CNPG routed
// host-side (#4398) or landed in a vCluster host-ns is found either way.
//
// Read-only + failure-tolerant: a List error (CNPG CRD absent → the fake/real
// dynamic client returns a no-kind error; or a transient API error) is skipped,
// never surfaced as an unrecoverable verdict. The caller only downgrades on a
// POSITIVE match, so a read failure can never manufacture a false Degraded.
func (r *Reconciler) backingCNPGUnrecoverable(
	ctx context.Context,
	app *unstructured.Unstructured,
	perClusterStatus []map[string]interface{},
) (namespace, name string, unrecoverable bool) {
	if r == nil || r.Dynamic == nil || app == nil {
		return "", "", false
	}

	// Build the (namespace, instance-label) work list from the materialised
	// per-cluster HRs. Fall back to the bare Application identity when no
	// fan-out ran (legacy single-HR host path).
	type target struct{ ns, instance string }
	nsSet := map[string]struct{}{app.GetNamespace(): {}}
	var instances []string
	for _, pcs := range perClusterStatus {
		hrName, _ := pcs["hr"].(string)
		if hrName == "" {
			continue
		}
		instances = append(instances, hrName)
		if ns, _ := pcs["namespace"].(string); ns != "" {
			nsSet[ns] = struct{}{}
		}
	}
	if len(instances) == 0 {
		instances = append(instances, app.GetName())
	}

	// De-dup (namespace × instance) probes.
	seen := map[target]struct{}{}
	for ns := range nsSet {
		for _, inst := range instances {
			t := target{ns: ns, instance: inst}
			if _, ok := seen[t]; ok {
				continue
			}
			seen[t] = struct{}{}

			list, err := r.Dynamic.Resource(CNPGClusterGVR).Namespace(t.ns).List(ctx, metav1.ListOptions{
				LabelSelector: InstanceLabel + "=" + t.instance,
			})
			if err != nil {
				// CNPG CRD absent or transient list error — never fabricate
				// a downgrade on a read failure.
				continue
			}
			for i := range list.Items {
				if cnpgClusterUnrecoverable(&list.Items[i]) {
					return t.ns, list.Items[i].GetName(), true
				}
			}
		}
	}
	return "", "", false
}

// cnpgClusterUnrecoverable reports whether a CNPG Cluster CR is in CNPG's
// terminal `unrecoverable` phase. CNPG sets status.phase to a human string
// (e.g. "Cluster in unrecoverable state" / "... unrecoverable and needs manual
// intervention"); a case-insensitive substring match on "unrecoverable" is
// robust across CNPG phrasings while staying specific to the terminal,
// manual-intervention state (it does NOT trip on transient phases like
// "Setting up primary" or "Cluster in healthy state").
func cnpgClusterUnrecoverable(cr *unstructured.Unstructured) bool {
	if cr == nil {
		return false
	}
	phase, _, _ := unstructured.NestedString(cr.Object, "status", "phase")
	return strings.Contains(strings.ToLower(phase), "unrecoverable")
}
