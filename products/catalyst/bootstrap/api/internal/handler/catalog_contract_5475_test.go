// Tests for #5475 — two catalog API contract defects found on hw291.
//
// 1. chartRef carried a DOUBLE `bp-` prefix on all 80 blueprints, because
//    the registry const ended in `bp-` and was concatenated with a name
//    that already began with `bp-`:
//        got  ghcr.io/openova-io/bp-bp-alloy:1.0.2   (404 - no such package)
//        want ghcr.io/openova-io/bp-alloy:1.0.2      (11 published tags)
//
//    It had no consumers, so nothing broke - but it did produce a wrong
//    inference during an R21 walk, where `chartRef -> 404` was read as
//    proof that specific blueprints would hollow-install. The doubling
//    was universal, including for the 75 whose charts DO exist, so that
//    reasoning was unsound.
//
// 2. The cluster-fallback path stamped Origin: 3 with a comment asserting
//    org-private was 0. The canonical enum (core/services/catalyst-catalog/
//    internal/source.Origin) is public=1, sovereign=2, org-private=3 - so 3
//    mislabelled every platform blueprint as org-private. Latent only
//    because no sovereign surface renders origin yet.

package handler

import "testing"

func TestPopulateVersionsAlias_ChartRefHasExactlyOneBPPrefix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		bpName  string
		version string
		want    string
	}{
		{"already-qualified name (the real catalog shape)", "bp-alloy", "1.0.2", "ghcr.io/openova-io/bp-alloy:1.0.2"},
		{"another qualified name", "bp-wordpress", "0.4.22", "ghcr.io/openova-io/bp-wordpress:0.4.22"},
		{"bare name is tolerated, not doubled", "alloy", "1.0.2", "ghcr.io/openova-io/bp-alloy:1.0.2"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := &CatalogBlueprint{Name: tc.bpName, Version: tc.version}
			b.PopulateVersionsAlias()
			if b.ChartRef != tc.want {
				t.Errorf("ChartRef = %q want %q", b.ChartRef, tc.want)
			}
		})
	}
}

// An explicitly-supplied chartRef must survive untouched — the function
// only fills a blank.
func TestPopulateVersionsAlias_DoesNotOverwriteSuppliedChartRef(t *testing.T) {
	t.Parallel()
	b := &CatalogBlueprint{Name: "bp-alloy", Version: "1.0.2", ChartRef: "registry.example/custom:9.9.9"}
	b.PopulateVersionsAlias()
	if b.ChartRef != "registry.example/custom:9.9.9" {
		t.Errorf("an upstream-supplied ChartRef was overwritten: %q", b.ChartRef)
	}
}

// The cluster-fallback origin must not collide with org-private.
//
// Anti-theater: this asserts the VALUE, not merely that a constant exists.
// Restoring the literal 3 makes it fail, which is the whole defect.
func TestCatalogOriginSovereign_DoesNotCollideWithOrgPrivate(t *testing.T) {
	t.Parallel()
	const (
		canonicalPublic     = 1
		canonicalSovereign  = 2
		canonicalOrgPrivate = 3
	)
	if catalogOriginSovereign == canonicalOrgPrivate {
		t.Fatalf("cluster-fallback origin %d collides with org-private — every platform "+
			"blueprint would be labelled as belonging to the caller's Org", catalogOriginSovereign)
	}
	if catalogOriginSovereign != canonicalSovereign {
		t.Errorf("cluster-fallback origin = %d want %d (sovereign-curated); public=%d org-private=%d",
			catalogOriginSovereign, canonicalSovereign, canonicalPublic, canonicalOrgPrivate)
	}
}
