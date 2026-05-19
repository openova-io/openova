// applications_label_selector_test.go — coverage for installLabelSelectorForHR.
//
// Issue #1928 (t34 chart 1.4.197, 2026-05-19): the synth-from-HelmRelease
// path was returning `app.kubernetes.io/name=bp-<chart>` (bp-prefixed),
// but the upstream subchart labels its rendered resources with
// `app.kubernetes.io/name=<chart>` (prefix stripped). The Resources tab
// XHR therefore returned 174-byte empty `items: []` responses across
// every kind for every bootstrap-kit Application (harbor, alloy,
// cert-manager, ...).
//
// These tests pin the new contract: the install label selector keys off
// the Helm release name via `app.kubernetes.io/instance=<releaseName>`,
// which every Helm chart-helpers `labels` template stamps on every
// rendered resource — including Pods (via Deployment pod-template-spec).
package handler

import "testing"

func TestInstallLabelSelectorForHR_KeysOffReleaseName(t *testing.T) {
	cases := []struct {
		name        string
		releaseName string
		want        string
	}{
		{
			// Founder-reported t34 walk: HR `bp-harbor` in flux-system,
			// `spec.releaseName: harbor`. The selector must hit pods +
			// services + deployments + configmaps + secrets + PVCs +
			// ingresses in the harbor namespace.
			name:        "bp-harbor releaseName harbor → instance=harbor (issue #1928)",
			releaseName: "harbor",
			want:        "app.kubernetes.io/instance=harbor",
		},
		{
			// Bootstrap-kit alloy: HR `bp-alloy`, `spec.releaseName: alloy`.
			name:        "bp-alloy releaseName alloy → instance=alloy",
			releaseName: "alloy",
			want:        "app.kubernetes.io/instance=alloy",
		},
		{
			// Bootstrap-kit cert-manager: HR `bp-cert-manager`,
			// `spec.releaseName: cert-manager`.
			name:        "bp-cert-manager releaseName cert-manager → instance=cert-manager",
			releaseName: "cert-manager",
			want:        "app.kubernetes.io/instance=cert-manager",
		},
		{
			// Wizard-installed Application CR path also routes through
			// this helper via resp.ReleaseName = name → selector is
			// instance=<applicationName>, the catalyst standard.
			name:        "wizard app releaseName equals app name → instance=<app>",
			releaseName: "my-redis",
			want:        "app.kubernetes.io/instance=my-redis",
		},
		{
			// Empty releaseName: helper returns "" so the caller leaves
			// resp.InstallLabelSelector unset and the SPA falls back to
			// its UI-side default `instance=<applicationName>`.
			name:        "empty releaseName → empty selector (UI default)",
			releaseName: "",
			want:        "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := installLabelSelectorForHR(tc.releaseName)
			if got != tc.want {
				t.Fatalf("installLabelSelectorForHR(%q) = %q; want %q", tc.releaseName, got, tc.want)
			}
		})
	}
}

// TestInstallLabelSelectorForHR_NotBpPrefixed pins the regression for
// issue #1928: the selector MUST NOT be derived from the bp-prefixed
// `spec.chart.spec.chart` field (which yields e.g. `bp-harbor`). Caught
// on t34 chart 1.4.197 (agent aced939b, 2026-05-19 12:21Z) — 174-byte
// empty responses across all 7 resource kinds.
func TestInstallLabelSelectorForHR_NotBpPrefixed(t *testing.T) {
	got := installLabelSelectorForHR("harbor")
	if got == "app.kubernetes.io/name=bp-harbor" {
		t.Fatalf("regression: selector returned the bp-prefixed chart name (issue #1928)")
	}
	if got == "app.kubernetes.io/name=harbor" {
		t.Fatalf("regression: selector still keyed on app.kubernetes.io/name; must key on /instance per issue #1928")
	}
}
