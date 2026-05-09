package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("CATALYST_GITEA_URL", "http://gitea-http.gitea.svc.cluster.local:3000")
	t.Setenv("CATALYST_GITEA_TOKEN", "deadbeef")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ListenAddr != ":8080" {
		t.Errorf("ListenAddr default = %q, want :8080", c.ListenAddr)
	}
	if c.PublicCatalogOrg != "catalog" {
		t.Errorf("PublicCatalogOrg default = %q, want catalog", c.PublicCatalogOrg)
	}
	if c.SovereignCatalogOrg != "catalog-sovereign" {
		t.Errorf("SovereignCatalogOrg default = %q, want catalog-sovereign", c.SovereignCatalogOrg)
	}
	if c.OrgPrivateRepoSuffix != "shared-blueprints" {
		t.Errorf("OrgPrivateRepoSuffix default = %q, want shared-blueprints", c.OrgPrivateRepoSuffix)
	}
	if c.CacheTTL != 30*time.Second {
		t.Errorf("CacheTTL default = %v, want 30s", c.CacheTTL)
	}
	if c.CacheCapacity != 1024 {
		t.Errorf("CacheCapacity default = %d, want 1024", c.CacheCapacity)
	}
	if c.AnonymousReads {
		t.Error("AnonymousReads default = true, want false (closed by default per Inviolable Principle #1)")
	}
}

func TestLoad_RequiresGiteaURL(t *testing.T) {
	t.Setenv("CATALYST_GITEA_URL", "")
	t.Setenv("CATALYST_GITEA_TOKEN", "deadbeef")
	_, err := Load()
	if err == nil {
		t.Fatal("Load with empty CATALYST_GITEA_URL must fail")
	}
	if !strings.Contains(err.Error(), "CATALYST_GITEA_URL") {
		t.Errorf("expected error to mention CATALYST_GITEA_URL, got %v", err)
	}
}

func TestLoad_RequiresGiteaToken(t *testing.T) {
	t.Setenv("CATALYST_GITEA_URL", "http://gitea")
	t.Setenv("CATALYST_GITEA_TOKEN", "")
	_, err := Load()
	if err == nil {
		t.Fatal("Load with empty CATALYST_GITEA_TOKEN must fail")
	}
	if !strings.Contains(err.Error(), "CATALYST_GITEA_TOKEN") {
		t.Errorf("expected error to mention CATALYST_GITEA_TOKEN, got %v", err)
	}
}

func TestLoad_RejectsApiV1Suffix(t *testing.T) {
	t.Setenv("CATALYST_GITEA_URL", "http://gitea/api/v1")
	t.Setenv("CATALYST_GITEA_TOKEN", "deadbeef")
	_, err := Load()
	if err == nil {
		t.Fatal("Load with /api/v1 suffix must fail")
	}
	if !strings.Contains(err.Error(), "/api/v1") {
		t.Errorf("expected error to mention /api/v1, got %v", err)
	}
}

func TestLoad_AnonymousReadsToggle(t *testing.T) {
	t.Setenv("CATALYST_GITEA_URL", "http://gitea")
	t.Setenv("CATALYST_GITEA_TOKEN", "deadbeef")
	t.Setenv("CATALOG_ANONYMOUS_READS", "true")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.AnonymousReads {
		t.Error("CATALOG_ANONYMOUS_READS=true should set AnonymousReads=true")
	}
}

func TestLoad_CacheTTLOverride(t *testing.T) {
	t.Setenv("CATALYST_GITEA_URL", "http://gitea")
	t.Setenv("CATALYST_GITEA_TOKEN", "deadbeef")
	t.Setenv("CATALOG_CACHE_TTL_SECONDS", "120")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.CacheTTL != 120*time.Second {
		t.Errorf("CacheTTL = %v, want 120s", c.CacheTTL)
	}
}

func TestLoad_RejectsNegativeCacheTTL(t *testing.T) {
	t.Setenv("CATALYST_GITEA_URL", "http://gitea")
	t.Setenv("CATALYST_GITEA_TOKEN", "deadbeef")
	t.Setenv("CATALOG_CACHE_TTL_SECONDS", "-1")
	_, err := Load()
	if err == nil {
		t.Fatal("Load with negative cache TTL must fail")
	}
}

func TestLoad_RejectsZeroCapacity(t *testing.T) {
	t.Setenv("CATALYST_GITEA_URL", "http://gitea")
	t.Setenv("CATALYST_GITEA_TOKEN", "deadbeef")
	t.Setenv("CATALOG_CACHE_CAPACITY", "0")
	_, err := Load()
	if err == nil {
		t.Fatal("Load with zero capacity must fail")
	}
}
