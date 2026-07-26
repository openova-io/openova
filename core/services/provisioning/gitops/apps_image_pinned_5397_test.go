package gitops

import (
	"strings"
	"testing"
)

// #5397 — every KnownApps image MUST carry a real tag.
//
// Nine catalog apps shipped pinned to `:latest` (cal-com, chatwoot,
// invoiceshelf, vaultwarden, bookstack, nocodb, listmonk, rocket-chat,
// formbricks). Every Sovereign runs a Kyverno ClusterPolicy
// `image-tag-pinned` with validationFailureAction=Enforce, and tenant
// namespaces are not in its exclude anchor — so those Pods were rejected at
// admission and NEVER CREATED. The failure signature is an absence, not a
// crash: `kubectl get pods` simply has no row for the app, which is why it
// survived so long. Live proof on hw290 Org theta-corp: a 4-app cart
// committed cleanly and produced pods for umami and uptime-kuma but none at
// all for listmonk or vaultwarden.
//
// The images live in a hardcoded Go map, so no seed or database change can
// work around it — the fix has to be here, and so does the guard.
//
// Deliberately NOT flagged: a tag that merely CONTAINS "latest", such as
// umami's `ghcr.io/umami-software/umami:postgresql-latest`. That is a
// distinct, immutable tag name, and it is admitted in practice — umami's
// Pod was created on the same walk where listmonk's was refused. Matching
// on a suffix here would fail a working image and push someone to "fix" it
// into something worse.
func TestKnownApps_NoUnpinnedImages_5397(t *testing.T) {
	if len(KnownApps) == 0 {
		t.Fatal("KnownApps is empty — the guard would vacuously pass")
	}

	for slug, spec := range KnownApps {
		image := strings.TrimSpace(spec.Image)
		if image == "" {
			t.Errorf("%s: empty Image", slug)
			continue
		}

		// Split off the tag: only a ':' AFTER the final '/' is a tag
		// separator, so a registry host:port (host:5000/repo) is not
		// mistaken for one.
		lastSlash := strings.LastIndexByte(image, '/')
		colon := strings.LastIndexByte(image, ':')

		if colon <= lastSlash {
			t.Errorf("%s: image %q carries no tag — Kyverno image-tag-pinned (Enforce) rejects it at admission and the Pod is never created", slug, image)
			continue
		}

		switch tag := image[colon+1:]; tag {
		case "":
			t.Errorf("%s: image %q has an empty tag", slug, image)
		case "latest":
			t.Errorf("%s: image %q is pinned to :latest — Kyverno image-tag-pinned (Enforce) rejects it at admission and the Pod is never created. Pin a real version.", slug, image)
		}
	}
}
