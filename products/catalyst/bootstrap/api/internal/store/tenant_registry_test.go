package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTenantRegistry_PutGetDelete(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewTenantRegistry(dir)
	if err != nil {
		t.Fatalf("NewTenantRegistry: %v", err)
	}

	otech := TenantRegistration{
		Host:             "console.otech-acme.example",
		TenantID:         "tenant-otech-acme",
		KeycloakRealmURL: "https://kc.otech-acme.example/realms/otech",
		KeycloakClientID: "catalyst-ui",
		TenantKind:       TenantKindOTECH,
	}
	if err := reg.Put(otech); err != nil {
		t.Fatalf("Put otech: %v", err)
	}

	sme := TenantRegistration{
		Host:                 "console.acme.otech.example",
		TenantID:             "tenant-org-acme",
		KeycloakRealmURL:     "https://kc.otech.example/realms/org-acme",
		KeycloakClientID:     "catalyst-ui",
		TenantKind:           TenantKindSME,
		OrganizationNamespace:   "org-acme",
		SMEKeycloakAdminURL:  "http://keycloak-org-acme.org-acme.svc.cluster.local:8080",
		OrgKeycloakRealmName: "org-acme",
	}
	if err := reg.Put(sme); err != nil {
		t.Fatalf("Put sme: %v", err)
	}

	// Case-insensitive lookup.
	got, ok := reg.Get("Console.Acme.Otech.Example")
	if !ok {
		t.Fatalf("Get: missing sme tenant")
	}
	if got.TenantKind != TenantKindSME {
		t.Errorf("TenantKind = %q, want %q", got.TenantKind, TenantKindSME)
	}
	if got.OrganizationNamespace != "org-acme" {
		t.Errorf("OrganizationNamespace = %q, want org-acme", got.OrganizationNamespace)
	}

	// File materialised on disk.
	if _, err := os.Stat(filepath.Join(dir, "-tenant-registry.json")); err != nil {
		t.Errorf("registry file not on disk: %v", err)
	}

	// Reload from disk → same data.
	reg2, err := NewTenantRegistry(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got2, ok := reg2.Get("console.otech-acme.example")
	if !ok || got2.TenantID != otech.TenantID {
		t.Errorf("reload lost otech: got=%+v ok=%v", got2, ok)
	}

	// Delete is idempotent.
	if err := reg.Delete("nonexistent.example"); err != nil {
		t.Errorf("Delete missing host: %v", err)
	}
	if err := reg.Delete("console.acme.otech.example"); err != nil {
		t.Errorf("Delete sme: %v", err)
	}
	if _, ok := reg.Get("console.acme.otech.example"); ok {
		t.Errorf("sme still present after delete")
	}
}

func TestTenantRegistry_ValidatesInput(t *testing.T) {
	reg, err := NewTenantRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	bad := []TenantRegistration{
		{Host: "", TenantID: "x", TenantKind: TenantKindSME},
		{Host: "x", TenantID: "", TenantKind: TenantKindSME},
		{Host: "x", TenantID: "x", TenantKind: "bogus"},
	}
	for i, b := range bad {
		if err := reg.Put(b); err == nil {
			t.Errorf("case %d: expected error, got nil", i)
		}
	}
}

func TestTenantRegistry_List(t *testing.T) {
	reg, err := NewTenantRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	hosts := []string{"console.b.example", "console.a.example", "console.c.example"}
	for _, h := range hosts {
		_ = reg.Put(TenantRegistration{Host: h, TenantID: "id-" + h, TenantKind: TenantKindOTECH})
	}
	got := reg.List()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Host != "console.a.example" {
		t.Errorf("not sorted, got[0] = %s", got[0].Host)
	}
}
