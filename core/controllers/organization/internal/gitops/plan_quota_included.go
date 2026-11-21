package gitops

import (
	"k8s.io/apimachinery/pkg/api/resource"
)

// ─── What every Organization is given, in the customer's scope ──────────────
//
// plan-quota (the customer scope: every pod WITHOUT PriorityClass
// catalyst-org-platform) is
//
//	what the platform runs there for the Organization
//	+ what EVERY Organization is given without the customer choosing it
//	+ the plan's part as sold, untouched, for what the customer adds.
//
// What the platform runs there: the vCluster's synced coredns
// (corednsOverhead) and the classless platform credit
// (Inputs.ClasslessPlatformCredit): on a new Organization that is the
// identity provider bp-keycloak (1 CPU) and its PostgreSQL (500m), whose
// release the funnel applies before the boundary tree that carries the
// Kyverno Policy, so they start without the class (hw311, 2026-11-21:
// bp-keycloak-0 created 04:08:29, the boundary's vcluster HelmRelease
// 04:09:49; both keycloak pods ran with no priorityClassName).
//
// What every Organization is given: the funnel installs five applications
// for every new Organization (products/catalyst/bootstrap/api/internal/
// handler/organization_funnel_apps.go funnelApplicationFiles, all rendered
// by renderOrganizationOverlay, none chosen by the customer). Three of them
// (bp-newapi, bp-openclaw, bp-agenity) are platform scope (platformScope).
// The other two are not, so their pods are charged to plan-quota:
// bp-wordpress-tenant (the website, with its CNPG database) and
// bp-stalwart-tenant (the mail server, with its webmail). They are the
// includedStack below. The entitlement matrix lists the mail server and SSO
// as features every package includes BESIDE its capacity (core/services/
// catalog/handlers/seed.go packageIncludedFeatures; products/chargeback/
// internal/synth/packages.go Features, the floor; core/marketplace/fixtures/
// public-packages.json, where a package `includes` only vcpu, memory_gb,
// storage_gb and bandwidth_mbps), and lists no website at all: neither is the
// capacity the plan's 1 vCPU / 167m pays for, exactly as the identity
// provider is not.
//
// Measured on hw311 (release candidate rc/20261121T000707Z-9a080ac, check
// NET-G07 step 9, 2026-11-21): plan S's plan-quota hard requests.cpu 1687m =
// plan 167m + coredns 20m + the 1500m classless credit; the funnel's
// WordPress and Stalwart sat in the same 167m, about 62m was left, and the
// check's application, wordpress-tenant, stalwart-tenant and newapi-mgmt
// stood at InstallFailed "context deadline exceeded" behind "exceeded
// quota: plan-quota".
//
// Each figure is the one the source that creates the pod sets — the funnel's
// purchase sizing (organization_purchase_sizing.go purchasedWorkloads: the
// chart's limit kept, request = limit / the plan overcommit, wired into the
// overlay by organization_gitops.go orgTenantBPWordPress / orgTenantBPStalwart
// as `index .Purchase "<key>"`), the chart's own inline hook sizes, and the
// LimitRange defaults of the plan for a container the chart leaves unsized
// (a zero-value shape, as in platformStack). The pin test
// (plan_quota_included_test.go) re-reads every one and fails on drift.
//
// Room for the pods that exist only during a Helm operation follows the
// platform scope's rule (transientHeadroomOf): the largest hook pod plus one
// surge replica of the largest Deployment on the RollingUpdate strategy. Of
// the given workloads two surge: the WordPress Deployment (RollingUpdate,
// because media.store defaults to s3 and wp-content is then an emptyDir —
// templates/deployment.yaml `{{- if $onPvc }}` Recreate, else RollingUpdate;
// maxSurge 25% of one replica rounds up to one pod) and the webmail
// Deployment (no strategy, the RollingUpdate default). Stalwart is a
// StatefulSet and the WordPress database a single-instance CNPG cluster:
// both replace their pod in place. Per resource the WordPress pod is never
// smaller than the webmail pod on any plan, so the surge term is one
// WordPress pod: 84m / 171Mi requests, 500m / 512Mi limits on S, and on XL
// its unsized inits' LimitRange defaults, 167m / 683Mi and 1 / 2Gi.
//
// An Organization that does not run the bundle (none is created that way
// today: the funnel installs it for every Organization unless
// TENANT_GITOPS_PER_ORG=true, which no chart sets) gets this room as unused
// headroom; a static credit is needed because a quota cannot reserve room for
// a pod that does not exist yet (the first install, a rollout's surge).

// includedStack are the long-running pods of the applications every
// Organization is given, in the customer scope. Release is the name of the
// Application the funnel creates (its overlay HelmRelease name without "bp-",
// funnelApplicationsFromOverlay); the application controller names the
// HelmRelease <application>-<cluster>, so that name is the stable identity.
var includedStack = []platformStackWorkload{
	{
		Name: "<release>-bp-wordpress-tenant-<hash>", Release: "wordpress-tenant",
		Source: "wordpress: organization_gitops.go orgTenantBPWordPress values.wordpress.resources = organization_purchase_sizing.go purchasedWorkloads \"wordpress\" (chart limit, request = limit / the overcommit); " +
			"wp-fix-perms, wp-content-bootstrap, wp-plugin-install: unsized in platform/wordpress-tenant/chart/templates/deployment.yaml, the LimitRange defaults of the plan; " +
			"RollingUpdate under media.store s3 (platform/wordpress-tenant/chart/values.yaml)",
		Surge:      true,
		Containers: []containerShape{{RequestsCPU: "84m", RequestsMemory: "171Mi", LimitsCPU: "500m", LimitsMemory: "512Mi"}}, // wordpress
		Inits:      []containerShape{{}, {}, {}},                                                                              // wp-fix-perms, wp-content-bootstrap, wp-plugin-install
	},
	{
		Name: "wordpress-tenant-db-<id>-1", Release: "wordpress-tenant",
		Source: "organization_gitops.go orgTenantBPWordPress values.database.cluster.resources = purchasedWorkloads \"wordpress-db\" × database.cluster.instances (1, platform/wordpress-tenant/chart/values.yaml); " +
			"the CNPG bootstrap-controller init and the initdb Job inherit the same block",
		Containers: []containerShape{{RequestsCPU: "84m", RequestsMemory: "171Mi", LimitsCPU: "500m", LimitsMemory: "512Mi"}},
		Inits:      []containerShape{{RequestsCPU: "84m", RequestsMemory: "171Mi", LimitsCPU: "500m", LimitsMemory: "512Mi"}},
	},
	{
		Name: "<release>-bp-stalwart-tenant-0", Release: "stalwart-tenant",
		Source:     "organization_gitops.go orgTenantBPStalwart values.stalwart.resources = purchasedWorkloads \"stalwart\"; a StatefulSet (platform/stalwart-tenant/chart/templates/deployment.yaml)",
		Containers: []containerShape{{RequestsCPU: "84m", RequestsMemory: "342Mi", LimitsCPU: "500m", LimitsMemory: "1Gi"}},
	},
	{
		Name: "<release>-bp-stalwart-tenant-webmail-<hash>", Release: "stalwart-tenant",
		Source: "webmail: organization_gitops.go orgTenantBPStalwart values.webmail.resources = purchasedWorkloads \"webmail\"; " +
			"seed-domain: unsized in platform/stalwart-tenant/chart/templates/webmail-deployment.yaml (webmail.domainSeed.enabled), the LimitRange defaults of the plan; no strategy, so RollingUpdate",
		Surge:      true,
		Containers: []containerShape{{RequestsCPU: "17m", RequestsMemory: "43Mi", LimitsCPU: "100m", LimitsMemory: "128Mi"}}, // webmail
		Inits:      []containerShape{{}},                                                                                     // seed-domain
	},
}

// includedTransients are the hook pods of the given applications: every
// post-install and post-upgrade hook their charts render.
var includedTransients = []platformTransient{
	{
		Name: "<release>-bp-wordpress-tenant-oidc-config", Release: "wordpress-tenant",
		Source:     "platform/wordpress-tenant/chart/templates/oidc-config-job.yaml oidc-config and its wp-artifacts init (inline, post-install/post-upgrade hook)",
		Containers: []containerShape{{RequestsCPU: "50m", RequestsMemory: "128Mi", LimitsCPU: "250m", LimitsMemory: "256Mi"}},
		Inits:      []containerShape{{RequestsCPU: "25m", RequestsMemory: "64Mi", LimitsCPU: "100m", LimitsMemory: "128Mi"}},
	},
	{
		Name: "<release>-bp-wordpress-tenant-admin-user", Release: "wordpress-tenant",
		Source:     "platform/wordpress-tenant/chart/templates/admin-user-job.yaml admin-user (inline, post-install/post-upgrade hook)",
		Containers: []containerShape{{RequestsCPU: "50m", RequestsMemory: "128Mi", LimitsCPU: "250m", LimitsMemory: "256Mi"}},
	},
	{
		Name: "<release>-bp-wordpress-tenant-media-migrate", Release: "wordpress-tenant",
		Source:     "platform/wordpress-tenant/chart/templates/media-migrate-job.yaml media-migrate (inline, post-install/post-upgrade hook)",
		Containers: []containerShape{{RequestsCPU: "50m", RequestsMemory: "128Mi", LimitsCPU: "250m", LimitsMemory: "256Mi"}},
	},
	{
		Name: "<release>-bp-stalwart-tenant-setup", Release: "stalwart-tenant",
		Source:     "organization_gitops.go orgTenantBPStalwart values.mailboxProvisioner.setupJob.resources = purchasedWorkloads \"stalwart-setup\" (platform/stalwart-tenant/chart/templates/mailbox-provision-job.yaml, post-install/post-upgrade hook)",
		Containers: []containerShape{{RequestsCPU: "17m", RequestsMemory: "22Mi", LimitsCPU: "100m", LimitsMemory: "64Mi"}},
	},
}

// IncludedCredit is what plan-quota holds for the given applications on one
// plan: their long-running pods plus the room of their Helm operations.
type IncludedCredit struct {
	Pods      PlatformStackOverhead
	Transient PlatformTransientHeadroom
}

// Total is the credit added to plan-quota's hard, per resource.
func (c IncludedCredit) Total() PlatformStackOverhead {
	sum := func(a, b resource.Quantity) resource.Quantity {
		x := a.DeepCopy()
		x.Add(b)
		return x
	}
	return PlatformStackOverhead{
		RequestsCPU:    sum(c.Pods.RequestsCPU, c.Transient.RequestsCPU),
		RequestsMemory: sum(c.Pods.RequestsMemory, c.Transient.RequestsMemory),
		LimitsCPU:      sum(c.Pods.LimitsCPU, c.Transient.LimitsCPU),
		LimitsMemory:   sum(c.Pods.LimitsMemory, c.Transient.LimitsMemory),
	}
}

// String renders the credit for the plan-quota annotation.
func (c IncludedCredit) String() string {
	return "pods " + c.Pods.String() + "; helm operations " + c.Transient.String()
}

// includedCreditFor is the credit for a plan: a chart-unsized container takes
// the plan's LimitRange defaults, exactly as in platformStackOverheadFor. The
// defaults are the HEADLINE's in both overage modes, so the caller passes the
// plan row, never a grow-ceiling copy of it.
func includedCreditFor(q PlanQuota) IncludedCredit {
	def := limitRangeDefaults(q)
	return IncludedCredit{
		Pods:      platformStackOverheadOf(includedStack, def),
		Transient: transientHeadroomOf(includedStack, includedTransients, def),
	}
}
