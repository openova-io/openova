package controller

import (
	"testing"

	"github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

func TestTenantDNSParent(t *testing.T) {
	cases := []struct {
		name               string
		org                orgapi.Organization
		wantParent, wantZn string
	}{
		{"customer pool", orgapi.Organization{Spec: orgapi.OrganizationSpec{Kind: "customer", SovereignRef: "hw307.omani.works", TenantPublic: orgapi.OrganizationTenantPublic{ParentDomain: "omani.rest"}}}, "omani.rest", "omani.rest"},
		{"internal, no pool", orgapi.Organization{Spec: orgapi.OrganizationSpec{Kind: "internal", SovereignRef: "hw307.omani.works"}}, "hw307.omani.works", "omani.works"},
		{"customer, no pool", orgapi.Organization{Spec: orgapi.OrganizationSpec{Kind: "customer", SovereignRef: "hw307.omani.works"}}, "", ""},
		{"internal, bad sovereign", orgapi.Organization{Spec: orgapi.OrganizationSpec{Kind: "internal", SovereignRef: "localhost"}}, "", ""},
	}
	for _, c := range cases {
		p, z := tenantDNSParent(&c.org)
		if p != c.wantParent || z != c.wantZn {
			t.Errorf("%s: parent=%q zone=%q want %q %q", c.name, p, z, c.wantParent, c.wantZn)
		}
	}
}
