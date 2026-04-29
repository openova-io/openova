package dynadot

import (
	"os"
	"sort"
	"strings"
	"testing"
)

func TestIsManagedDomainEnvVar(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "openova.io, omani.works  acme.io")
	t.Setenv("DYNADOT_DOMAIN", "")
	ResetManagedDomains()

	for _, d := range []string{"openova.io", "omani.works", "acme.io"} {
		if !IsManagedDomain(d) {
			t.Errorf("IsManagedDomain(%q) = false, want true", d)
		}
	}
	if IsManagedDomain("not-managed.com") {
		t.Errorf("IsManagedDomain(not-managed.com) = true, want false")
	}
	got := ManagedDomains()
	sort.Strings(got)
	want := []string{"acme.io", "omani.works", "openova.io"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ManagedDomains() = %v, want %v", got, want)
	}
}

func TestIsManagedDomainLegacyFallback(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "")
	t.Setenv("DYNADOT_DOMAIN", "legacy.io")
	ResetManagedDomains()

	if !IsManagedDomain("legacy.io") {
		t.Errorf("IsManagedDomain(legacy.io) = false, want true")
	}
	if IsManagedDomain("openova.io") {
		t.Errorf("legacy fallback should not include built-in defaults")
	}
}

func TestIsManagedDomainBuiltInDefaults(t *testing.T) {
	os.Unsetenv("DYNADOT_MANAGED_DOMAINS")
	os.Unsetenv("DYNADOT_DOMAIN")
	ResetManagedDomains()

	for _, d := range []string{"openova.io", "omani.works"} {
		if !IsManagedDomain(d) {
			t.Errorf("built-in default missing %q", d)
		}
	}
}

func TestSplitDomainsList(t *testing.T) {
	got := splitDomainsList("Foo.com, BAR.IO\tbaz.io ,foo.com")
	want := []string{"foo.com", "bar.io", "baz.io"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("splitDomainsList = %v, want %v", got, want)
	}
}
