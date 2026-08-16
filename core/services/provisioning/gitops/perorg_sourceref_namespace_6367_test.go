package gitops

import (
	"strings"
	"testing"
)

// #6367 — the per-Org HelmRelease could never resolve its own HelmRepository.
//
// MEASURED ON hw299 (Org `mailwalk`, minted through a real funnel checkout with
// Stalwart Mail in the cart). The customer paid for mail and got a 404:
//
//	mailwalk/bp-stalwart-tenant  Ready=False  reason=SourceNotReady
//	  HelmChart 'flux-system/mailwalk-bp-stalwart-tenant' is not ready:
//	  failed to get source: HelmRepository.source.toolkit.fluxcd.io
//	  "bp-stalwart-tenant" not found
//
// The producer was NOT missing. helmRepoBlock() shipped the HelmRepository and
// the Kustomization's own inventory proved it landed:
//
//	mailwalk_bp-stalwart-tenant_source.toolkit.fluxcd.io_HelmRepository
//
// It landed in namespace `mailwalk`, because the per-Org Kustomization carries
// `targetNamespace: mailwalk`, and targetNamespace REWRITES metadata.namespace
// on every object in the tree. The generator's `namespace: flux-system` was
// therefore never in effect.
//
// But sourceRef.namespace is a SPEC FIELD, not object metadata, so Flux does
// not rewrite it. The HelmRelease kept pointing at flux-system while its
// HelmRepository sat in mailwalk. Nothing reconciled, forever.
//
// THE CONTROL that proves it was namespacing and not a broken source: the
// sibling `mailwalk/vcluster` HelmRelease in the SAME namespace was
// Ready=True/InstallSucceeded. The per-Org GitOps path works; only the
// cross-namespace reference was wrong.
//
// THE FIX is co-location: neither object pins a namespace, so both land in
// whatever targetNamespace is, and an omitted sourceRef.namespace defaults to
// the HelmRelease's own namespace. That holds for ANY Org namespace.
//
// WHY THIS WAS NOT ALREADY CAUGHT: the existing render test asserts the
// HelmRepository is present by name. That assertion cannot fail on this bug —
// the object WAS emitted, with the right name. Presence was never the question;
// REACHABILITY was.

var hrShapedAppFiles = map[string]string{
	"openclaw":      "app-openclaw.yaml",
	"newapi":        "app-newapi.yaml",
	"stalwart-mail": "app-stalwart-mail.yaml",
	"agenity":       "app-agenity.yaml",
}

// sourceRefBlocks returns each `sourceRef:` block body. A block ends at the
// first line indented no deeper than `sourceRef:` itself, which keeps sibling
// keys (install:, upgrade:) out of the extraction.
func sourceRefBlocks(body string) []string {
	var out []string
	lines := strings.Split(body, "\n")
	for i, ln := range lines {
		if strings.TrimSpace(ln) != "sourceRef:" {
			continue
		}
		indent := len(ln) - len(strings.TrimLeft(ln, " "))
		var b []string
		for _, nxt := range lines[i+1:] {
			if strings.TrimSpace(nxt) == "" {
				b = append(b, nxt)
				continue
			}
			if len(nxt)-len(strings.TrimLeft(nxt, " ")) <= indent {
				break
			}
			b = append(b, nxt)
		}
		out = append(out, strings.Join(b, "\n"))
	}
	return out
}

func TestPerOrgSourceRefResolvesInOrgNamespace(t *testing.T) {
	apps := []string{"wordpress", "openclaw", "newapi", "stalwart-mail", "agenity"}

	for _, plan := range []string{"s", "m"} {
		t.Run("plan="+plan, func(t *testing.T) {
			out := cartOrgFor(t, "acme", plan, apps)

			checked := 0
			for app, file := range hrShapedAppFiles {
				body, ok := out[testBasePath+"/acme/"+file]
				if !ok {
					continue
				}
				checked++

				blocks := sourceRefBlocks(body)
				if len(blocks) == 0 {
					t.Errorf("%s: rendered no sourceRef at all — the HelmRelease cannot resolve a chart:\n%s", app, body)
					continue
				}
				for _, b := range blocks {
					if strings.Contains(b, "namespace:") {
						t.Errorf("%s: sourceRef PINS a namespace (#6367).\n"+
							"The per-Org Kustomization sets targetNamespace, which rewrites the\n"+
							"HelmRepository's metadata.namespace but NOT this spec field, so a\n"+
							"pinned namespace points where the repository is not and the release\n"+
							"sits at SourceNotReady forever. Omit it so it defaults to the\n"+
							"HelmRelease's own namespace.\nsourceRef block:\n%s", app, b)
					}
				}

				if strings.Contains(body, "kind: HelmRepository") &&
					strings.Contains(body, "namespace: flux-system") {
					t.Errorf("%s: HelmRepository still declares `namespace: flux-system`, which "+
						"targetNamespace overrides — decoration that misleads (#6367):\n%s", app, body)
				}
			}

			// VACUITY GUARD: a run that resolved no files would pass every
			// assertion above while measuring nothing.
			if checked == 0 {
				t.Fatalf("checked 0 HR-shaped app files — the test measured nothing (keys: %v)", keys(out))
			}
		})
	}
}

// TestSourceRefBlocks_DetectsAPinnedNamespace is the CONTROL for the detector.
// Without it, sourceRefBlocks() could silently return nothing and the real test
// would pass on every input, including the broken one.
func TestSourceRefBlocks_DetectsAPinnedNamespace(t *testing.T) {
	broken := `  chart:
    spec:
      chart: bp-stalwart-tenant
      version: "*"
      sourceRef:
        kind: HelmRepository
        name: bp-stalwart-tenant
        namespace: flux-system
  install:
    timeout: 15m`

	blocks := sourceRefBlocks(broken)
	if len(blocks) != 1 {
		t.Fatalf("detector found %d sourceRef blocks in the known-bad doc, want 1 — it cannot see the defect it exists to catch", len(blocks))
	}
	if !strings.Contains(blocks[0], "namespace: flux-system") {
		t.Errorf("detector extracted a block but dropped the offending namespace line:\n%s", blocks[0])
	}
	if strings.Contains(blocks[0], "install:") {
		t.Errorf("detector over-read past the sourceRef block into sibling keys:\n%s", blocks[0])
	}

	fixed := `      sourceRef:
        kind: HelmRepository
        name: bp-stalwart-tenant
  install:
    timeout: 15m`
	fb := sourceRefBlocks(fixed)
	if len(fb) != 1 {
		t.Fatalf("detector found %d blocks in the FIXED doc, want 1", len(fb))
	}
	if strings.Contains(fb[0], "namespace:") {
		t.Errorf("detector reports a namespace in the fixed doc — it would fail a correct render:\n%s", fb[0])
	}
}
