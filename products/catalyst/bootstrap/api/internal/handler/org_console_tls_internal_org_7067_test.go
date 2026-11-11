package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// An internal Organization (no tenantPublic) gets its per-Org wildcard
// listener under the Sovereign domain; a customer without a pool parent is
// still skipped (Refs #7067).
func TestOrgConsoleTLSRecordFromOrgCR_InternalOrgUsesSovereignDomain(t *testing.T) {
	internal := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "orgs.openova.io/v1alpha1", "kind": "Organization",
		"metadata": map[string]interface{}{"name": "hw307-omani-works"},
		"spec":     map[string]interface{}{"slug": "hw307-omani-works", "kind": "internal"},
	}}
	rec, ok := orgConsoleTLSRecordFromOrgCR(internal, "hw307.omani.works")
	if !ok {
		t.Fatalf("internal Organization skipped; want a listener under the Sovereign domain")
	}
	if rec.ParentDomain != "hw307.omani.works" || rec.Subdomain != "hw307-omani-works" {
		t.Fatalf("record = parent %q sub %q", rec.ParentDomain, rec.Subdomain)
	}
	names, ok := resolveOrgConsoleTLSNames(rec)
	if !ok || names.WildcardHost != "*.hw307-omani-works.hw307.omani.works" {
		t.Fatalf("wildcard = %q ok=%v", names.WildcardHost, ok)
	}
	customer := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "orgs.openova.io/v1alpha1", "kind": "Organization",
		"metadata": map[string]interface{}{"name": "acme"},
		"spec":     map[string]interface{}{"slug": "acme", "kind": "customer"},
	}}
	if _, ok := orgConsoleTLSRecordFromOrgCR(customer, "hw307.omani.works"); ok {
		t.Fatalf("a customer without a pool parent must still be skipped")
	}
}
