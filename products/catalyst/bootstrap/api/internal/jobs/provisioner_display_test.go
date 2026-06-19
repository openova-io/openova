// provisioner_display_test.go — the Phase-0 lifecycle parent group must
// render "Provision <Provider>" for the deployment's actual cloud provider
// (issue #3895). A Huawei prov must never masquerade as Hetzner.
package jobs

import "testing"

func TestProvisionerDisplay(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		want     string
	}{
		{"empty falls back to hetzner default", "", "Provision Hetzner"},
		{"whitespace falls back to default", "   ", "Provision Hetzner"},
		{"hetzner", "hetzner", "Provision Hetzner"},
		{"huawei", "huawei", "Provision Huawei"},
		{"huawei mixed case", "Huawei", "Provision Huawei"},
		{"huawei trimmed", "  huawei  ", "Provision Huawei"},
		{"aws upper label", "aws", "Provision AWS"},
		{"gcp", "gcp", "Provision GCP"},
		{"azure", "azure", "Provision Azure"},
		{"unmapped provider title-cases", "scaleway", "Provision Scaleway"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProvisionerDisplay(tc.provider); got != tc.want {
				t.Fatalf("ProvisionerDisplay(%q) = %q, want %q", tc.provider, got, tc.want)
			}
		})
	}
}

// TestSeedProvisionerJobsProviderLabel asserts the end-to-end path: a
// bridge stamped with a provider seeds the lifecycle parent group with the
// provider-specific display name, and an un-stamped bridge keeps the
// Hetzner default (back-compat for legacy deployment records).
func TestSeedProvisionerJobsProviderLabel(t *testing.T) {
	t.Run("huawei provider labels the group Provision Huawei", func(t *testing.T) {
		st, br, depID := newBridgeFixture(t)
		br.SetProvider("huawei")
		if err := br.SeedProvisionerJobs(); err != nil {
			t.Fatalf("SeedProvisionerJobs: %v", err)
		}
		group := mustGetGroup(t, st, depID, GroupProvisioner)
		if group.DisplayName != "Provision Huawei" {
			t.Fatalf("provisioner group DisplayName = %q, want %q",
				group.DisplayName, "Provision Huawei")
		}
	})

	t.Run("unset provider keeps the Hetzner default", func(t *testing.T) {
		st, br, depID := newBridgeFixture(t)
		if err := br.SeedProvisionerJobs(); err != nil {
			t.Fatalf("SeedProvisionerJobs: %v", err)
		}
		group := mustGetGroup(t, st, depID, GroupProvisioner)
		if group.DisplayName != GroupProvisionerDisplay {
			t.Fatalf("provisioner group DisplayName = %q, want default %q",
				group.DisplayName, GroupProvisionerDisplay)
		}
	})

	t.Run("blank SetProvider never clobbers a real provider", func(t *testing.T) {
		st, br, depID := newBridgeFixture(t)
		br.SetProvider("huawei")
		br.SetProvider("") // must be ignored
		if err := br.SeedProvisionerJobs(); err != nil {
			t.Fatalf("SeedProvisionerJobs: %v", err)
		}
		group := mustGetGroup(t, st, depID, GroupProvisioner)
		if group.DisplayName != "Provision Huawei" {
			t.Fatalf("provisioner group DisplayName = %q, want %q (blank must not clobber)",
				group.DisplayName, "Provision Huawei")
		}
	})
}

func mustGetGroup(t *testing.T, st *Store, depID, slug string) Job {
	t.Helper()
	job, _, err := st.GetJob(depID, JobID(depID, slug))
	if err != nil {
		t.Fatalf("GetJob(%s): %v", slug, err)
	}
	return job
}
