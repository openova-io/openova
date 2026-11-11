package endpoints

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestOrgDomain(t *testing.T) {
	cust := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{
		"slug": "acme", "tenantPublic": map[string]interface{}{"parentDomain": "omani.rest"}}}}
	internal := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{"slug": "hw307-omani-works", "kind": "internal"}}}
	if got := OrgDomain(cust, "acme", "hw307.omani.works"); got != "acme.omani.rest" {
		t.Errorf("customer: %q", got)
	}
	if got := OrgDomain(internal, "", "hw307.omani.works"); got != "hw307-omani-works.hw307.omani.works" {
		t.Errorf("internal: %q", got)
	}
	if got := OrgDomain(nil, "", ""); got != "" {
		t.Errorf("unknown: %q", got)
	}
}

func TestFromBlueprint(t *testing.T) {
	bp := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{"endpoints": []interface{}{
		map[string]interface{}{"name": "ui", "hostnameTemplate": "{{.AppName}}.{{.OrgDomain}}", "port": int64(443), "tls": true, "launchDefault": true, "visibility": "public"},
		map[string]interface{}{"name": "admin", "hostnameTemplate": "admin.{{.OrgDomain}}", "visibility": "internal"},
		map[string]interface{}{"name": "bad", "hostnameTemplate": "{{.Nope}}.x"},
	}}}}
	eps, errs := FromBlueprint(bp, Vars{AppName: "blog", OrgDomain: "acme.omani.rest"})
	if len(eps) != 1 || eps[0].Name != "ui" || eps[0].Hostname != "blog.acme.omani.rest" || eps[0].Port != 443 || !eps[0].TLS || !eps[0].LaunchDefault {
		t.Fatalf("endpoints = %#v", eps)
	}
	if len(errs) != 1 {
		t.Fatalf("want 1 unresolved error, got %v", errs)
	}
}
