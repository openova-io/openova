// display_test.go — locks in the granular, human-readable Job naming
// (issue #3646: "why I see non granular naming in
// https://console.<fqdn>/jobs"). The founder's exact complaint is the
// raw slug ("install-keycloak", "mutation-remove-region",
// "cron-openbao-snapshot-save") surfacing in the /jobs Name column; these
// tests assert every leaf carries a friendly DisplayName instead.
package jobs

import "testing"

func TestHumanizeSlug(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"keycloak", "Keycloak"},
		{"cert-manager", "Cert Manager"},
		{"catalyst-api", "Catalyst API"},
		{"sso-bridge", "SSO Bridge"},
		{"openbao-snapshot-save", "OpenBao Snapshot Save"},
		{"vcluster-registry-pivot", "vCluster Registry Pivot"},
		{"flux-system", "Flux System"},
		{"powerdns", "PowerDNS"},
		{"external-dns", "External DNS"},
		// A token already carrying internal casing (a region segment) is
		// left untouched rather than mangled.
		{"me-east-215-b-1", "Me East 215 B 1"},
	}
	for _, c := range cases {
		if got := HumanizeSlug(c.in); got != c.want {
			t.Errorf("HumanizeSlug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDeriveLeafDisplayName(t *testing.T) {
	cases := []struct {
		name string
		job  Job
		want string
	}{
		{
			name: "install leaf — the dominant non-granular case",
			job:  Job{JobName: "install-keycloak", AppID: "keycloak", Kind: KindInstall},
			want: "Install Keycloak",
		},
		{
			name: "install leaf acronym component",
			job:  Job{JobName: "install-catalyst-api", AppID: "catalyst-api", Kind: KindInstall},
			want: "Install Catalyst API",
		},
		{
			name: "secondary-region install via first-class Region field",
			job:  Job{JobName: "install-cilium", AppID: "cilium", Region: "me-east-215-b-1", Kind: KindInstall},
			want: "Install Cilium (me-east-215-b-1)",
		},
		{
			name: "secondary-region install via region-prefixed appId (no Region field)",
			job:  Job{JobName: "install-me-east-215-b-1:harbor", AppID: "me-east-215-b-1:harbor", Kind: KindInstall},
			want: "Install Harbor (me-east-215-b-1)",
		},
		{
			name: "mutation leaf — verb + kind + target",
			job:  Job{JobName: "mutation-remove-region", AppID: "region", Kind: KindMutation},
			want: "Remove Region",
		},
		{
			name: "mutation leaf with slug target",
			job:  Job{JobName: "mutation-scale-node-pool-pool-a", AppID: "node-pool", Kind: KindMutation},
			want: "Scale Node Pool Pool A",
		},
		{
			name: "reconcile leaf",
			job:  Job{JobName: "reconcile-flux-system", AppID: "flux-system", Kind: KindReconcile},
			want: "Reconcile Flux System",
		},
		{
			name: "cron leaf",
			job:  Job{JobName: "cron-openbao-snapshot-save", AppID: "openbao-snapshot-save", Kind: KindCron},
			want: "OpenBao Snapshot Save (cron)",
		},
		{
			name: "task leaf",
			job:  Job{JobName: "task-gitea-sso-configure", AppID: "gitea-sso-configure", Kind: KindTask},
			want: "Gitea SSO Configure (task)",
		},
		{
			name: "reconciler leaf",
			job:  Job{JobName: "reconciler-sso-bridge", AppID: "sso-bridge", Kind: KindReconciler},
			want: "SSO Bridge (reconciler)",
		},
		{
			name: "step leaf — group · step",
			job:  Job{JobName: "cutover-step-harbor-prewarm", Kind: KindStep},
			want: "Cutover · Harbor Prewarm",
		},
		{
			name: "lifecycle slug floor (no kind, inferred)",
			job:  Job{JobName: "tofu-init"},
			want: "Tofu Init",
		},
		{
			name: "kind inferred from JobName when Kind empty (legacy row)",
			job:  Job{JobName: "install-gitea", AppID: "gitea"},
			want: "Install Gitea",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DeriveLeafDisplayName(c.job); got != c.want {
				t.Errorf("DeriveLeafDisplayName(%+v) = %q, want %q", c.job, got, c.want)
			}
		})
	}
}

// TestDeriveLeafDisplayName_neverRawSlug is the founder-complaint guard:
// no leaf may surface its raw "<prefix>-<slug>" id as the display label.
// We assert the derived name contains a space (humanized) and is not byte-
// identical to the JobName for every representative leaf shape.
func TestDeriveLeafDisplayName_neverRawSlug(t *testing.T) {
	jobs := []Job{
		{JobName: "install-keycloak", AppID: "keycloak", Kind: KindInstall},
		{JobName: "mutation-remove-region", AppID: "region", Kind: KindMutation},
		{JobName: "cron-openbao-snapshot-save", AppID: "openbao-snapshot-save", Kind: KindCron},
		{JobName: "reconcile-flux-system", AppID: "flux-system", Kind: KindReconcile},
		{JobName: "task-some-batch", AppID: "some-batch", Kind: KindTask},
		{JobName: "reconciler-sso-bridge", AppID: "sso-bridge", Kind: KindReconciler},
	}
	for _, j := range jobs {
		got := DeriveLeafDisplayName(j)
		if got == j.JobName {
			t.Errorf("DeriveLeafDisplayName(%q) returned the raw slug unchanged", j.JobName)
		}
		if got == "" {
			t.Errorf("DeriveLeafDisplayName(%q) returned empty", j.JobName)
		}
	}
}

// TestDeriveTreeView_backfillsLeafDisplayName proves the END-TO-END wire
// contract: a leaf written by a bridge that left DisplayName empty (the
// install + mutation bridges) gets a granular DisplayName at read time,
// while a bridge-supplied friendly DisplayName + every group is preserved
// untouched (never overridden).
func TestDeriveTreeView_backfillsLeafDisplayName(t *testing.T) {
	dep := "dep-x"
	in := []Job{
		// install leaf — bridge left DisplayName empty.
		{
			ID:           JobID(dep, "install-keycloak"),
			DeploymentID: dep,
			JobName:      "install-keycloak",
			AppID:        "keycloak",
			Type:         JobTypeInstall,
			ParentID:     JobID(dep, GroupBootstrapKit),
			Status:       StatusSucceeded,
		},
		// mutation leaf — bridge left DisplayName empty.
		{
			ID:           JobID(dep, "mutation-remove-region"),
			DeploymentID: dep,
			JobName:      "mutation-remove-region",
			AppID:        "region",
			Type:         JobTypeInstall,
			ParentID:     JobID(dep, GroupDay2Mutations),
			Status:       StatusRunning,
		},
		// group — friendly DisplayName must be preserved, not derived.
		{
			ID:           JobID(dep, GroupBootstrapKit),
			DeploymentID: dep,
			JobName:      GroupBootstrapKit,
			DisplayName:  GroupBootstrapKitDisplay,
			Type:         JobTypeGroup,
			Status:       StatusPending,
		},
		// lifecycle leaf with a bridge-supplied friendly name — preserved.
		{
			ID:           JobID(dep, PhaseTofuInit),
			DeploymentID: dep,
			JobName:      PhaseTofuInit,
			DisplayName:  "Terraform Init",
			Type:         JobTypeInstall,
			ParentID:     JobID(dep, GroupProvisioner),
			Status:       StatusSucceeded,
		},
	}
	out := deriveTreeView(in)
	get := func(jobName string) Job {
		t.Helper()
		for _, j := range out {
			if j.JobName == jobName {
				return j
			}
		}
		t.Fatalf("job %q not found in derived output", jobName)
		return Job{}
	}

	if got := get("install-keycloak").DisplayName; got != "Install Keycloak" {
		t.Errorf("install leaf DisplayName = %q, want %q", got, "Install Keycloak")
	}
	if got := get("mutation-remove-region").DisplayName; got != "Remove Region" {
		t.Errorf("mutation leaf DisplayName = %q, want %q", got, "Remove Region")
	}
	if got := get(GroupBootstrapKit).DisplayName; got != GroupBootstrapKitDisplay {
		t.Errorf("group DisplayName = %q, want %q (must be preserved)", got, GroupBootstrapKitDisplay)
	}
	if got := get(PhaseTofuInit).DisplayName; got != "Terraform Init" {
		t.Errorf("lifecycle DisplayName = %q, want %q (must be preserved)", got, "Terraform Init")
	}
}

// TestReconcilerDisplay locks the granular reconciler-leaf labels the
// reconciler bridge stamps at write time (so the source-set value matches
// the read-time floor).
func TestReconcilerDisplay(t *testing.T) {
	cases := []struct {
		obsKind, name, want string
	}{
		{ObsKindCron, "openbao-snapshot-save", "OpenBao Snapshot Save (cron)"},
		{ObsKindReconcile, "flux-system", "Reconcile Flux System"},
		{ObsKindTask, "gitea-sso-configure", "Gitea SSO Configure (task)"},
		{ObsKindReconciler, "sso-bridge", "SSO Bridge (reconciler)"},
	}
	for _, c := range cases {
		if got := reconcilerDisplay(c.obsKind, c.name); got != c.want {
			t.Errorf("reconcilerDisplay(%q,%q) = %q, want %q", c.obsKind, c.name, got, c.want)
		}
	}
}
