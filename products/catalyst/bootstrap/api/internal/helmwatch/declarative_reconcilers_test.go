// Tests for the non-Flux declarative-reconciler reader (#3958).
//
// The contract these pin: ListDeclarativeReconcilers surfaces cert-manager
// Certificates, CNPG Clusters (status.phase-driven), External-Secrets
// ExternalSecrets, and the Catalyst control-plane CRs with the correct human
// Kind label + Obs* lifecycle status, and is best-effort per GVR (a CRD
// absent on the cluster never drops the other kinds).
package helmwatch

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// declarativeListScheme registers the *List GVKs the dynamic fake client
// needs so List(...) resolves for every declarative GVR the reader
// enumerates. Without a registered List kind the fake returns "no kind
// registered" and the pass is silently skipped.
func declarativeListScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	add := func(g, v, listKind string) {
		scheme.AddKnownTypeWithName(
			schema.GroupVersionKind{Group: g, Version: v, Kind: listKind},
			&unstructured.UnstructuredList{})
	}
	add("cert-manager.io", "v1", "CertificateList")
	add("postgresql.cnpg.io", "v1", "ClusterList")
	add("external-secrets.io", "v1", "ExternalSecretList")
	add("external-secrets.io", "v1beta1", "ExternalSecretList")
	add("apps.openova.io", "v1", "ApplicationList")
	add("catalyst.openova.io", "v1", "EnvironmentList")
	add("orgs.openova.io", "v1", "OrganizationList")
	add("dr.openova.io", "v1", "ContinuumList")
	return scheme
}

// declarativeFakeClient builds a dynamic fake client with every declarative
// reconciler GVR's List kind registered, seeded with the supplied objects.
func declarativeFakeClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		declarativeListScheme(),
		map[schema.GroupVersionResource]string{
			CertificateGVR:        "CertificateList",
			CNPGClusterGVR:        "ClusterList",
			ExternalSecretGVR:     "ExternalSecretList",
			ExternalSecretGVRBeta: "ExternalSecretList",
			ApplicationGVR:        "ApplicationList",
			EnvironmentGVR:        "EnvironmentList",
			OrganizationGVR:       "OrganizationList",
			ContinuumGVR:          "ContinuumList",
		},
		objs...,
	)
}

// readyConditionObj builds an unstructured CR with a single Ready condition.
func readyConditionObj(apiVersion, kind, ns, name, readyStatus, reason string) *unstructured.Unstructured {
	cond := map[string]any{
		"type":   "Ready",
		"status": readyStatus,
	}
	if reason != "" {
		cond["reason"] = reason
	}
	cond["message"] = kind + " " + name + " ready=" + readyStatus
	meta := map[string]any{"name": name}
	if ns != "" {
		meta["namespace"] = ns
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   meta,
		"status":     map[string]any{"conditions": []any{cond}},
	}}
}

// cnpgClusterObj builds an unstructured CNPG Cluster with a status.phase.
func cnpgClusterObj(ns, name, phase string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"status":     map[string]any{"phase": phase},
	}}
}

func TestListDeclarativeReconcilers_MapsKindAndStatus(t *testing.T) {
	dyn := declarativeFakeClient(
		// cert-manager Certificate, Ready=True → Succeeded.
		readyConditionObj("cert-manager.io/v1", "Certificate", "flux-system", "wildcard-tls", "True", "Ready"),
		// ExternalSecret, Ready=False non-Stalled → Running (still retrying).
		readyConditionObj("external-secrets.io/v1", "ExternalSecret", "catalyst", "smtp-creds", "False", "SecretSyncedError"),
		// CNPG Cluster, healthy phase → Succeeded.
		cnpgClusterObj("shared-data", "shared-pg", "Cluster in healthy state"),
		// Catalyst Application, Ready=True → Succeeded.
		readyConditionObj("apps.openova.io/v1", "Application", "acme", "marketing-site", "True", "Ready"),
	)

	got, err := ListDeclarativeReconcilers(context.Background(), dyn)
	if err != nil {
		t.Fatalf("ListDeclarativeReconcilers: %v", err)
	}

	type key struct{ kind, name string }
	byKey := map[key]DeclarativeReconciler{}
	for _, d := range got {
		byKey[key{d.Kind, d.Name}] = d
	}

	cases := []struct {
		kind, name, wantStatus string
	}{
		{"Certificate", "wildcard-tls", ObsStatusSucceeded},
		{"ExternalSecret", "smtp-creds", ObsStatusRunning},
		{"Cluster", "shared-pg", ObsStatusSucceeded},
		{"Application", "marketing-site", ObsStatusSucceeded},
	}
	for _, c := range cases {
		d, ok := byKey[key{c.kind, c.name}]
		if !ok {
			t.Fatalf("missing observation for %s/%s (got %d total)", c.kind, c.name, len(got))
		}
		if d.Status != c.wantStatus {
			t.Fatalf("%s/%s status = %q, want %q", c.kind, c.name, d.Status, c.wantStatus)
		}
		if d.Message == "" {
			t.Fatalf("%s/%s message must be non-empty", c.kind, c.name)
		}
	}

	// The CNPG message carries the verbatim phase text.
	if d := byKey[key{"Cluster", "shared-pg"}]; d.Message != "Cluster in healthy state" {
		t.Fatalf("CNPG message = %q, want verbatim phase", d.Message)
	}
}

// CNPG phase mapping: healthy → Succeeded, Failed/Unrecoverable → Failed,
// empty/other → Running.
func TestCNPGStatusFromPhase(t *testing.T) {
	cases := []struct {
		phase, wantStatus string
	}{
		{"Cluster in healthy state", ObsStatusSucceeded},
		{"Failed", ObsStatusFailed},
		{"Cluster is in an unrecoverable state", ObsStatusFailed},
		{"Setting up primary", ObsStatusRunning},
		{"", ObsStatusRunning},
	}
	for _, c := range cases {
		u := cnpgClusterObj("ns", "pg", c.phase)
		got, _ := cnpgStatusFromPhase(u)
		if got != c.wantStatus {
			t.Fatalf("cnpgStatusFromPhase(%q) = %q, want %q", c.phase, got, c.wantStatus)
		}
	}
}

// Best-effort: nil client returns an empty (never nil) slice; a missing CRD
// (no objects of a kind) simply yields no rows for that kind without error.
func TestListDeclarativeReconcilers_NilClientEmpty(t *testing.T) {
	got, err := ListDeclarativeReconcilers(context.Background(), nil)
	if err != nil {
		t.Fatalf("nil client should not error: %v", err)
	}
	if got == nil {
		t.Fatal("must return empty slice, never nil")
	}
	if len(got) != 0 {
		t.Fatalf("nil client must return 0 observations, got %d", len(got))
	}
}

// A cluster-scoped Organization (no namespace) is surfaced with an empty
// Namespace, so the handler can form the "organization//<name>" node ID.
func TestListDeclarativeReconcilers_ClusterScopedOrganization(t *testing.T) {
	dyn := declarativeFakeClient(
		readyConditionObj("orgs.openova.io/v1", "Organization", "", "acme", "True", "Ready"),
	)
	got, err := ListDeclarativeReconcilers(context.Background(), dyn)
	if err != nil {
		t.Fatalf("ListDeclarativeReconcilers: %v", err)
	}
	var found bool
	for _, d := range got {
		if d.Kind == "Organization" && d.Name == "acme" {
			found = true
			if d.Namespace != "" {
				t.Fatalf("cluster-scoped Organization namespace = %q, want empty", d.Namespace)
			}
			if d.Status != ObsStatusSucceeded {
				t.Fatalf("Organization status = %q, want Succeeded", d.Status)
			}
		}
	}
	if !found {
		t.Fatalf("Organization observation missing (got %d)", len(got))
	}
}
