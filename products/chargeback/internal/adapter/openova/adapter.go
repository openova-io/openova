// Package openova is the OpenOva adapter of the chargeback application
// (ADR-0014 D2, case 1 of D3): built into the same binary, active only on
// the `sovereign` profile with in-cluster Kubernetes access. It contributes
// three things the standalone engine works without:
//
//   - Organization → Customer sync (orgsync.go): list+watch Organization CRs
//     and upsert one app-internal Customer per Organization, including its
//     GitOps-declared spec.costSources[].
//   - The platform collector (collector.go): informers on pods and PVCs in
//     Organization-labelled namespaces, event-driven with an hourly
//     reconciliation pass (ADR-0014 D3a), emitting k8s.* usage records.
//   - The billing hook (billinghook.go): the D6 seam — an issued statement
//     for a real-billing Organization becomes a credit debit via
//     POST /billing/metering/record.
//
// The `operator-central` profile never runs any of this (ADR-0014 D10):
// the engine's collectors, ledger, rating and statements have no Catalyst
// dependency (D5 invariant).
package openova

import "strings"

// Decide reports whether the adapter should run, and why. The default is
// on for the sovereign profile when in-cluster Kubernetes configuration is
// available; ADAPTER_ENABLED overrides in either direction (but nothing can
// run without a cluster to watch).
func Decide(profile, override string, inCluster bool) (bool, string) {
	switch strings.ToLower(strings.TrimSpace(override)) {
	case "false", "0", "no", "off":
		return false, "ADAPTER_ENABLED=false"
	case "true", "1", "yes", "on":
		if !inCluster {
			return false, "ADAPTER_ENABLED=true but no in-cluster Kubernetes configuration is available"
		}
		return true, "ADAPTER_ENABLED=true"
	}
	if profile != "sovereign" {
		return false, "profile is " + profile + ", not sovereign"
	}
	if !inCluster {
		return false, "no in-cluster Kubernetes configuration"
	}
	return true, "profile sovereign with in-cluster configuration"
}
