package gitops

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// #5450 — the render-time half of the image-tag-pinned contract.
//
// On hw290 the Sovereign's own Kyverno ClusterPolicy `image-tag-pinned`
// (Enforce) rejected the platform's own rendered app manifests at admission:
// listmonk and vaultwarden pods were refused with 'image-pinned-pod: One or
// more containers reference an unpinned image' and simply never existed. The
// Application read INSTALLED, Flux inventory was non-zero — only the vcluster
// syncer's event carried the truth. #5397 pinned the KnownApps SOURCE images
// and apps_image_pinned_5397_test.go guards that map, but the map is not what
// admission sees: admission sees the RENDER, which also carries DB sidecars,
// initContainers and any image a generator hardcodes (gitops.go renders
// initContainers and two ghcr.io images into a StatefulSet on its own).
//
// This guard therefore validates the SHIPPED render path — GeneratePerOrgAppsTree,
// the tree the vcluster syncer applies to the host (#4384) — against the SAME
// semantics the policy enforces, for every KnownApps entry, so a manifest the
// platform's own Kyverno would reject fails HERE, in CI, instead of silently in
// a customer's namespace after purchase.
//
// The pin semantics are mirrored VERBATIM from
// platform/kyverno-policies/chart/templates/baseline/12-image-tag-pinned.yaml:
// an image is pinned iff it carries a @sha256:<64-hex> digest OR a `:<tag>` in
// the FINAL path segment (so a registry-port colon like host:5000/img does NOT
// count), and `:latest` is always rejected. Note `:postgresql-latest` is a
// distinct immutable tag and is ADMITTED — the policy anchors on `:latest$`,
// and umami proved it live (admitted on the same walk that refused listmonk).
// If the policy's expressions change, change these four regexes with them.
var (
	pinnedDigest    = regexp.MustCompile(`^.*@sha256:[a-fA-F0-9]{64}$`)
	pinnedMultiSeg  = regexp.MustCompile(`^[^@]*/[^/:]+:[^/:]+$`)
	pinnedSingleSeg = regexp.MustCompile(`^[^@/]+:[^/:]+$`)
	latestTag       = regexp.MustCompile(`^.*:latest$`)
)

func admissionRejects(image string) bool {
	pinned := pinnedDigest.MatchString(image) ||
		pinnedMultiSeg.MatchString(image) ||
		pinnedSingleSeg.MatchString(image)
	return !pinned || latestTag.MatchString(image)
}

// imageLine matches a container image assignment in rendered YAML, list-item or
// mapping form. Values inside ConfigMap data that happen to say `image:` are
// swept too — an embedded image reference feeds a workload eventually, and
// strict-here beats silent-at-admission.
var imageLine = regexp.MustCompile(`^\s*-?\s*image:\s*["']?([^"'\s]+)["']?\s*$`)

func renderedImages(body string) []string {
	var out []string
	for _, ln := range strings.Split(body, "\n") {
		if m := imageLine.FindStringSubmatch(ln); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

func sortedKnownAppSlugs(t *testing.T) []string {
	t.Helper()
	if len(KnownApps) == 0 {
		t.Fatal("KnownApps is empty — the guard would vacuously pass")
	}
	slugs := make([]string, 0, len(KnownApps))
	for slug := range KnownApps {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	return slugs
}

func assertTreeAdmissionClean(t *testing.T, label string, files map[string]string) {
	t.Helper()
	found := 0
	for _, path := range sortedKeys(files) {
		for _, image := range renderedImages(files[path]) {
			found++
			if admissionRejects(image) {
				t.Errorf("%s: %s renders image %q — the Sovereign's own image-tag-pinned "+
					"policy (Enforce) rejects it at admission and the pod is never created "+
					"(the #5450 failure mode: INSTALLED badge, non-zero inventory, no pod)",
					label, path, image)
			}
		}
	}
	if found == 0 {
		t.Fatalf("%s: render produced ZERO image lines — the sweep is vacuous, "+
			"check the render entry point or the image-line matcher", label)
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Every KnownApps entry, rendered alone through the shipped per-Org tree, must
// be admission-clean in every file it produces (app Deployment, DB sidecars,
// initContainers — everything).
func TestPerOrgAppsTree_EveryKnownApp_ImagePinned_5450(t *testing.T) {
	for _, slug := range sortedKnownAppSlugs(t) {
		t.Run(slug, func(t *testing.T) {
			g := NewManifestGenerator("clusters/sov/org-tenants")
			g.ParentDomain = "omani.homes"
			files, _ := g.GeneratePerOrgAppsTree("guard5450", "s", []string{slug}, "pw123")
			assertTreeAdmissionClean(t, slug, files)
		})
	}
}

// The full cart in one Org — cross-app renders (shared DB tiers, dedup paths)
// must be clean too, not only the single-app case.
func TestPerOrgAppsTree_FullCart_ImagePinned_5450(t *testing.T) {
	g := NewManifestGenerator("clusters/sov/org-tenants")
	g.ParentDomain = "omani.homes"
	files, _ := g.GeneratePerOrgAppsTree("guard5450", "m", sortedKnownAppSlugs(t), "pw123")
	assertTreeAdmissionClean(t, "full-cart", files)
}

// The mirror itself must agree with the policy on the known edge cases — a
// drifted mirror is worse than none, it certifies manifests the cluster rejects.
func TestPolicyMirror_AgreesWithKyvernoEdgeCases_5450(t *testing.T) {
	rejected := []string{
		"listmonk/listmonk:latest",   // the live hw290 refusal
		"vaultwarden/server",         // tagless → K8s resolves :latest
		"nginx",                      // single-segment tagless
		"host:5000/img",              // registry-port colon is NOT a tag (final-segment rule)
		"ghcr.io/x/y:latest",         // explicit :latest, multi-segment
	}
	admitted := []string{
		"listmonk/listmonk:v6.2.0",
		"ghcr.io/umami-software/umami:postgresql-latest", // distinct immutable tag, proven admitted live
		"nginx:1.31.3-alpine",
		"host:5000/img:1.2.3", // tag in the final segment alongside a registry port
		"ghcr.io/x/y@sha256:" + strings.Repeat("a", 64),
	}
	for _, img := range rejected {
		if !admissionRejects(img) {
			t.Errorf("mirror ADMITS %q but the Kyverno policy rejects it — the guard has drifted from 12-image-tag-pinned.yaml", img)
		}
	}
	for _, img := range admitted {
		if admissionRejects(img) {
			t.Errorf("mirror REJECTS %q but the Kyverno policy admits it — the guard is stricter than admission and will block a working image", img)
		}
	}
}
