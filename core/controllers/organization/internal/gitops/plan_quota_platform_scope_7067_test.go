// plan_quota_platform_scope_7067_test.go — the per-Organization namespace is
// admitted in TWO scopes (Refs #7067, platform_scope.go): platform-quota for
// the pods OpenOva delivers (PriorityClass catalyst-org-platform, stamped by
// the Kyverno Policy org-platform-scope), plan-quota for every other pod.
//
// Five properties:
//
//  1. OBJECTS — the PriorityClass, the Policy and both quotas render together,
//     are listed in the boundary kustomization, and carry the shape that makes
//     them safe on a live cluster (never-pruned class, never-preempting class,
//     force-recreate on the immutable scopeSelector, autogen off).
//  2. SELECTORS — the Policy's match clauses are exactly platformScope, and the
//     predicate classifies every pod the Organization namespace actually holds:
//     the 16 pods of hw307 acmewalk307 (2026-11-08, read-only kubectl) and the
//     hook / CronJob / per-user pods `helm template` renders from the stack
//     charts with the BSS door's values — including the keycloak-config-cli
//     hook that wedged hw307.
//  3. TRANSIENTS — every hook / CronJob figure in platformTransients equals
//     its Source, and the Surge flag matches each workload's controller kind.
//  4. FIGURES — platform-quota pinned by value.
//  5. INSTALL LIST — every HelmRelease the org-controller renders into the
//     namespace is the vCluster (the control plane modelled in
//     vclusterControlPlane); the BSS door's are covered by
//     TestPlatformStack_CoversTheBSSDoorInstallList (stack ∪ purchase).
//
// Reads leave this module; run under -count=1 (scripts/check-go-test-count1.sh).
package gitops

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

// ─── 1. OBJECTS ──────────────────────────────────────────────────────────────

func TestPlatformScope_ObjectsRenderTogetherAndSafely(t *testing.T) {
	t.Parallel()
	for slug := range planQuotaTable {
		out := renderPlan(t, slug)
		for _, f := range []string{"resourcequota.yaml", platformQuotaDoc, priorityClassDoc, platformScopePolicyDoc} {
			if _, ok := out["vcluster/"+f]; !ok {
				t.Fatalf("plan %q: vcluster/%s not rendered — the two scopes render together or not at all", slug, f)
			}
		}
		var k struct {
			Resources []string `json:"resources"`
		}
		if err := yaml.Unmarshal(out["vcluster/kustomization.yaml"], &k); err != nil {
			t.Fatal(err)
		}
		listed := map[string]bool{}
		for _, r := range k.Resources {
			listed[r] = true
		}
		for _, f := range []string{"resourcequota.yaml", platformQuotaDoc, priorityClassDoc, platformScopePolicyDoc} {
			if !listed[f] {
				t.Errorf("plan %q: boundary kustomization does not list %s — Flux would never apply it", slug, f)
			}
		}
	}

	out := renderPlan(t, "m")
	var pc struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name        string            `json:"name"`
			Namespace   string            `json:"namespace"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Value            int64  `json:"value"`
		GlobalDefault    bool   `json:"globalDefault"`
		PreemptionPolicy string `json:"preemptionPolicy"`
	}
	if err := yaml.Unmarshal(out["vcluster/"+priorityClassDoc], &pc); err != nil {
		t.Fatal(err)
	}
	if pc.Kind != "PriorityClass" || pc.Metadata.Name != OrgPlatformPriorityClassName || pc.Metadata.Namespace != "" {
		t.Errorf("PriorityClass = %s %q ns=%q, want cluster-scoped PriorityClass %q", pc.Kind, pc.Metadata.Name, pc.Metadata.Namespace, OrgPlatformPriorityClassName)
	}
	if pc.Metadata.Annotations["kustomize.toolkit.fluxcd.io/prune"] != "disabled" {
		t.Errorf("PriorityClass must never be pruned — every Organization renders it, and tearing one down must not remove it from the others")
	}
	if pc.GlobalDefault || pc.PreemptionPolicy != "Never" || pc.Value != orgPlatformPriorityValue {
		t.Errorf("PriorityClass globalDefault=%v preemptionPolicy=%q value=%d, want false / Never / %d", pc.GlobalDefault, pc.PreemptionPolicy, pc.Value, orgPlatformPriorityValue)
	}
	if pc.Value >= 100000000 || pc.Value <= 0 {
		t.Errorf("PriorityClass value %d must sit above default-0 pods and below catalyst-control-plane (100000000)", pc.Value)
	}
	// Same bytes for every Organization: it is ONE cluster object applied by
	// N Kustomizations; a per-Org difference would make them fight.
	other, err := Render(Inputs{Slug: "other", DisplayName: "Other", Tier: "org", PlanSlug: "xl",
		SovereignFQDN: "y.example", HostCluster: "hz2", VClusterChartVersion: "0.33.*", OverageMode: OverageModeGrow})
	if err != nil {
		t.Fatal(err)
	}
	if string(other["vcluster/"+priorityClassDoc]) != string(out["vcluster/"+priorityClassDoc]) {
		t.Errorf("PriorityClass renders differently for two Organizations — N Kustomizations would fight over one cluster object")
	}

	pol := parsePolicy(t, out["vcluster/"+platformScopePolicyDoc])
	if pol.Kind != "Policy" || pol.Metadata.Namespace != "acme" || pol.Metadata.Name != PlatformScopePolicyName {
		t.Errorf("Policy = %s %s/%s, want namespaced Policy acme/%s", pol.Kind, pol.Metadata.Namespace, pol.Metadata.Name, PlatformScopePolicyName)
	}
	if pol.Metadata.Annotations["pod-policies.kyverno.io/autogen-controllers"] != "none" {
		t.Errorf("autogen must be off: a rule autogenerated onto a Job's pod template would patch an immutable field")
	}
	if pol.Spec.Background {
		t.Errorf("background must be false for an admission mutation")
	}
	if len(pol.Spec.Rules) != 2 {
		t.Fatalf("Policy has %d rules, want stamp + synced-pod refusal", len(pol.Spec.Rules))
	}
	// The Priority admission plugin stamps spec.priority / preemptionPolicy
	// BEFORE the webhook and is re-invoked after it; a name-only patch is
	// refused ("the integer value of priority (0) must not be provided",
	// proven on the hw307-A apiserver). The patch must set the name and
	// REMOVE both computed fields, exactly these three ops in this order.
	var ops []struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value string `json:"value"`
	}
	if err := yaml.Unmarshal([]byte(pol.Spec.Rules[0].Mutate.PatchesJSON6902), &ops); err != nil {
		t.Fatalf("patchesJson6902 does not parse: %v", err)
	}
	want := []string{"add /spec/priorityClassName " + OrgPlatformPriorityClassName, "remove /spec/priority ", "remove /spec/preemptionPolicy "}
	got := []string{}
	for _, o := range ops {
		got = append(got, o.Op+" "+o.Path+" "+o.Value)
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("mutation ops = %v, want %v", got, want)
	}
	if pol.Spec.Rules[0].Mutate.PatchStrategicMerge != nil {
		t.Errorf("the stamp rule must not carry a patchStrategicMerge (a name-only or figure-setting merge is what the apiserver refuses)")
	}
	for _, c := range pol.Spec.Rules[0].Match.Any {
		if strings.Join(c.Resources.Kinds, ",") != "Pod" || strings.Join(c.Resources.Operations, ",") != "CREATE" {
			t.Errorf("match clause %+v must be Pod CREATE only", c.Resources)
		}
	}
	deny := pol.Spec.Rules[1]
	if deny.Validate.FailureAction != "Enforce" || len(deny.Validate.Deny.Conditions.Any) != 1 ||
		deny.Validate.Deny.Conditions.Any[0].Value != OrgPlatformPriorityClassName {
		t.Errorf("synced-pod rule = %+v, want an Enforce deny on a synced pod carrying %s", deny.Validate, OrgPlatformPriorityClassName)
	}
	if !strings.Contains(string(out["vcluster/"+platformScopePolicyDoc]), `key: "{{ request.object.spec.priorityClassName || '' }}"`) {
		t.Errorf("the deny condition must reach Kyverno as a literal JMESPath {{ }} expression, not be eaten by text/template:\n%s", out["vcluster/"+platformScopePolicyDoc])
	}
}

type kyvernoPolicy struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Background bool `json:"background"`
		Rules      []struct {
			Name  string `json:"name"`
			Match struct {
				Any []kyvernoClause `json:"any"`
			} `json:"match"`
			Exclude struct {
				Any []kyvernoClause `json:"any"`
			} `json:"exclude"`
			Mutate struct {
				PatchesJSON6902     string         `json:"patchesJson6902"`
				PatchStrategicMerge map[string]any `json:"patchStrategicMerge"`
			} `json:"mutate"`
			Validate struct {
				FailureAction string `json:"failureAction"`
				Deny          struct {
					Conditions struct {
						Any []struct {
							Key      string `json:"key"`
							Operator string `json:"operator"`
							Value    string `json:"value"`
						} `json:"any"`
					} `json:"conditions"`
				} `json:"deny"`
			} `json:"validate"`
		} `json:"rules"`
	} `json:"spec"`
}

type kyvernoClause struct {
	Resources struct {
		Kinds      []string `json:"kinds"`
		Operations []string `json:"operations"`
		Selector   struct {
			MatchLabels      map[string]string `json:"matchLabels"`
			MatchExpressions []struct {
				Key      string   `json:"key"`
				Operator string   `json:"operator"`
				Values   []string `json:"values"`
			} `json:"matchExpressions"`
		} `json:"selector"`
	} `json:"resources"`
}

func parsePolicy(t *testing.T, b []byte) kyvernoPolicy {
	t.Helper()
	var p kyvernoPolicy
	if err := yaml.Unmarshal(b, &p); err != nil {
		t.Fatalf("platform-scope Policy did not parse: %v\n%s", err, b)
	}
	return p
}

// ─── 2. SELECTORS ────────────────────────────────────────────────────────────

func TestPlatformScope_PolicyMatchesTable(t *testing.T) {
	t.Parallel()
	pol := parsePolicy(t, renderPlan(t, "s")["vcluster/"+platformScopePolicyDoc])
	got := map[string][]string{}
	for _, c := range pol.Spec.Rules[0].Match.Any {
		for _, e := range c.Resources.Selector.MatchExpressions {
			if e.Operator != "In" {
				t.Errorf("match expression %s uses %s, want In", e.Key, e.Operator)
			}
			got[e.Key] = append(got[e.Key], e.Values...)
		}
		for k, v := range c.Resources.Selector.MatchLabels {
			got[k] = append(got[k], v)
		}
	}
	want := map[string][]string{
		"app.kubernetes.io/instance":    platformScope.InstanceReleases,
		"app.kubernetes.io/name":        platformScope.ChartNames,
		"catalyst.openova.io/blueprint": platformScope.Blueprints,
		"cnpg.io/cluster":               {"*" + platformScope.CNPGClusterSuffix},
		"app":                           {platformScope.ControlPlaneApp},
	}
	if len(got) != len(want) {
		t.Errorf("Policy match keys %v, want %v", keys(got), keys(want))
	}
	for k, w := range want {
		if strings.Join(sorted(got[k]), ",") != strings.Join(sorted(w), ",") {
			t.Errorf("Policy %s = %v, platformScope says %v", k, got[k], w)
		}
	}
	ex := map[string][]string{}
	for _, c := range pol.Spec.Rules[0].Exclude.Any {
		for _, e := range c.Resources.Selector.MatchExpressions {
			ex[e.Key+" "+e.Operator] = e.Values
		}
	}
	if _, ok := ex["vcluster.loft.sh/managed-by Exists"]; !ok {
		t.Errorf("Policy does not exclude synced pods (vcluster.loft.sh/managed-by Exists): %v", ex)
	}
	if strings.Join(ex["app.kubernetes.io/component In"], ",") != strings.Join(platformScope.ExcludeComponents, ",") {
		t.Errorf("Policy component exclusion = %v, platformScope says %v", ex["app.kubernetes.io/component In"], platformScope.ExcludeComponents)
	}
}

// platformScopeFixture is one pod's labels and the scope it must land in.
type platformScopeFixture struct {
	name     string
	labels   map[string]string
	platform bool
}

// hw307Pods are the label sets of the 16 pods in acmewalk307 on hw307 region
// A, 2026-11-08 (kubectl get pods -o json, read-only), with the scope each one
// belongs in. The live Kyverno CLI run in the PR applies the rendered Policy
// to these same objects with the same result.
var hw307Pods = []platformScopeFixture{
	{"vcluster-0", map[string]string{"app": "vcluster", "release": "vcluster"}, true},
	{"bp-keycloak-0", map[string]string{"app.kubernetes.io/instance": "bp-keycloak", "app.kubernetes.io/name": "keycloak", "app.kubernetes.io/component": "keycloak"}, true},
	{"bp-keycloak-postgresql-0", map[string]string{"app.kubernetes.io/instance": "bp-keycloak", "app.kubernetes.io/name": "postgresql", "app.kubernetes.io/component": "primary"}, true},
	{"bp-newapi-newapi-pg-1", map[string]string{"app.kubernetes.io/instance": "bp-newapi-newapi-pg", "app.kubernetes.io/name": "postgresql", "cnpg.io/cluster": "bp-newapi-newapi-pg"}, true},
	{"bp-openclaw-controller", map[string]string{"app.kubernetes.io/instance": "bp-openclaw", "app.kubernetes.io/name": "bp-openclaw"}, true},
	{"bp-agenity-0", map[string]string{"app.kubernetes.io/instance": "bp-agenity", "app.kubernetes.io/name": "bp-agenity"}, true},
	{"oidc-gate-agenity-acmewalk307", map[string]string{"app.kubernetes.io/name": "bp-oidc-gate"}, true},
	{"bp-stalwart-tenant-0", map[string]string{"app.kubernetes.io/instance": "bp-stalwart-tenant", "app.kubernetes.io/name": "bp-stalwart-tenant"}, false},
	{"bp-stalwart-tenant-webmail", map[string]string{"app.kubernetes.io/instance": "bp-stalwart-tenant", "app.kubernetes.io/name": "bp-stalwart-tenant", "app.kubernetes.io/component": "webmail"}, false},
	{"bp-stalwart-tenant-setup", map[string]string{"app.kubernetes.io/instance": "bp-stalwart-tenant", "app.kubernetes.io/name": "bp-stalwart-tenant"}, false},
	{"bp-wordpress-tenant", map[string]string{"app.kubernetes.io/instance": "bp-wordpress-tenant", "app.kubernetes.io/name": "bp-wordpress-tenant"}, false},
	{"wordpress-db-1", map[string]string{"app.kubernetes.io/instance": "wordpress-db", "app.kubernetes.io/name": "postgresql", "cnpg.io/cluster": "wordpress-db"}, false},
	{"wordpress-db-replica-1-pgbasebackup", map[string]string{"app.kubernetes.io/instance": "wordpress-db-replica", "app.kubernetes.io/name": "postgresql", "cnpg.io/cluster": "wordpress-db-replica"}, false},
	{"bookstack (synced)", map[string]string{"app": "bookstack", "vcluster.loft.sh/managed-by": "vcluster", "vcluster.loft.sh/namespace": "acmewalk307"}, false},
}

// renderedHookPods are pods `helm template` renders from the stack and
// purchase charts with the BSS door's values (2026-11-08), as their
// controllers create them — the ones no live snapshot shows because they
// exist only during a Helm operation.
var renderedHookPods = []platformScopeFixture{
	// The hook that wedged hw307: bitnami keycloak 25.2.0 keycloak-config-cli-job.yaml.
	{"bp-keycloak-keycloak-config-cli (hook)", map[string]string{"app.kubernetes.io/instance": "bp-keycloak", "app.kubernetes.io/name": "keycloak", "app.kubernetes.io/component": "keycloak-config-cli"}, true},
	{"bp-newapi-admin-seed (hook)", map[string]string{"app.kubernetes.io/instance": "bp-newapi", "app.kubernetes.io/name": "newapi-admin-seed"}, true},
	{"bp-newapi-db-dsn-sync (hook)", map[string]string{"app.kubernetes.io/name": "newapi-db-dsn-sync"}, true},
	{"bp-newapi-admin-promote (CronJob)", map[string]string{"app.kubernetes.io/instance": "bp-newapi", "app.kubernetes.io/name": "newapi-admin-promote"}, true},
	{"newapi-eswebhook-gate (pre hook)", map[string]string{"catalyst.openova.io/blueprint": "bp-newapi", "catalyst.openova.io/component": "external-secrets-webhook-gate"}, true},
	// ADR-0015 D5: an Application-rendered HelmRelease is <app>-<cluster>,
	// so the instance label is not bp-newapi any more; the chart name is.
	{"newapi-hz-a deployment (Application-rendered release)", map[string]string{"app.kubernetes.io/instance": "newapi-hz-a", "app.kubernetes.io/name": "bp-newapi"}, true},
	{"newapi-hz-a db-dsn-sync (Application-rendered release)", map[string]string{"app.kubernetes.io/name": "newapi-db-dsn-sync"}, true},
	{"newapi-hz-a CNPG", map[string]string{"cnpg.io/cluster": "newapi-hz-a-newapi-pg"}, true},
	// The customer's own usage inside a platform chart.
	{"openclaw per-user workspace pod", map[string]string{"app.kubernetes.io/name": "bp-openclaw", "app.kubernetes.io/component": "per-user-pod", "catalyst.openova.io/blueprint": "bp-openclaw"}, false},
	// The purchase's hooks.
	{"bp-wordpress-tenant-admin-user (hook)", map[string]string{"app.kubernetes.io/name": "wordpress-admin-user"}, false},
	{"bp-wordpress-tenant-oidc-config (hook)", map[string]string{"app.kubernetes.io/name": "wordpress-oidc-config"}, false},
	// A customer forging platform labels inside its vCluster stays customer.
	{"synced pod forging bp-keycloak labels", map[string]string{"app.kubernetes.io/instance": "bp-keycloak", "vcluster.loft.sh/managed-by": "vcluster"}, false},
	{"synced vcluster coredns", map[string]string{"k8s-app": "kube-dns", "vcluster.loft.sh/managed-by": "vcluster", "vcluster.loft.sh/namespace": "kube-system"}, false},
}

func TestPlatformScope_ClassifiesEveryPod(t *testing.T) {
	t.Parallel()
	nPlat, nCust := 0, 0
	for _, f := range append(append([]platformScopeFixture(nil), hw307Pods...), renderedHookPods...) {
		if got := InPlatformScope(f.labels); got != f.platform {
			t.Errorf("%s: InPlatformScope = %v, want %v (labels %v)", f.name, got, f.platform, f.labels)
		}
		if f.platform {
			nPlat++
		} else {
			nCust++
		}
	}
	if nPlat < 10 || nCust < 10 {
		t.Fatalf("vacuity: %d platform / %d customer fixtures", nPlat, nCust)
	}
}

// TestPlatformScope_ChartLabelsAreInTheTable reads the app.kubernetes.io/name
// literals the OpenOva stack charts stamp on their pod templates, so a new
// hook Job with a new name label cannot land outside the platform scope.
func TestPlatformScope_ChartLabelsAreInTheTable(t *testing.T) {
	t.Parallel()
	names := map[string]bool{}
	for _, n := range platformScope.ChartNames {
		names[n] = true
	}
	lit := regexp.MustCompile(`(?m)^\s+app\.kubernetes\.io/name:\s*([a-z0-9-]+)\s*$`)
	seen := 0
	for _, f := range [][]string{
		{"platform", "newapi", "chart", "templates", "admin-sso-seed-job.yaml"},
		{"platform", "newapi", "chart", "templates", "database-secret-sync-job.yaml"},
		{"platform", "newapi", "chart", "templates", "channel-seed-job.yaml"},
		{"products", "agenity", "chart", "templates", "oidc-gate.yaml"},
	} {
		for _, m := range lit.FindAllStringSubmatch(readRepo(t, f...), -1) {
			seen++
			if !names[m[1]] {
				t.Errorf("%s stamps app.kubernetes.io/name %s, which platformScope.ChartNames does not carry — that pod would be admitted against the customer's plan-quota", strings.Join(f, "/"), m[1])
			}
		}
	}
	if seen < 5 {
		t.Fatalf("vacuity: found only %d name literals — the scanner missed", seen)
	}
	// The chart-derived names come from each chart's name helper = Chart.yaml name.
	for _, c := range [][]string{
		{"platform", "newapi", "chart", "Chart.yaml"},
		{"platform", "openclaw", "chart", "Chart.yaml"},
		{"products", "agenity", "chart", "Chart.yaml"},
	} {
		m := regexp.MustCompile(`(?m)^name:\s*(\S+)`).FindStringSubmatch(readRepo(t, c...))
		if m == nil || !names[m[1]] {
			t.Errorf("%s chart name %v is not in platformScope.ChartNames", strings.Join(c, "/"), m)
		}
	}
	// The eswebhook gate hook carries only the blueprint label.
	if !strings.Contains(readRepo(t, "platform", "newapi", "chart", "templates", "000-external-secrets-webhook-readiness-job.yaml"),
		"catalyst.openova.io/blueprint: bp-newapi") {
		t.Errorf("newapi eswebhook gate no longer carries catalyst.openova.io/blueprint: bp-newapi — re-point platformScope.Blueprints")
	}
	// The per-user pod's component label is what keeps it in the customer scope.
	if !strings.Contains(readRepo(t, "platform", "openclaw", "chart", "templates", "per-user-pod-template.yaml"),
		"app.kubernetes.io/component: per-user-pod") {
		t.Errorf("openclaw per-user pods no longer carry app.kubernetes.io/component: per-user-pod — they would be stamped platform and escape the customer's quota")
	}
}

// ─── 3. TRANSIENTS ───────────────────────────────────────────────────────────

func TestPlatformTransients_PinnedToSources(t *testing.T) {
	t.Parallel()
	by := map[string]platformTransient{}
	for _, tr := range platformTransients {
		by[tr.Name] = tr
	}
	bss := readRepo(t, "products", "catalyst", "bootstrap", "api", "internal", "handler", "organization_gitops.go")
	kc := helmReleaseValues(t, goRawString(t, bss, "const orgTenantBPKeycloak = "))
	sameShape(t, "keycloak-config-cli", by["bp-keycloak-keycloak-config-cli"].Containers[0], shapeAt(t, kc, "keycloak", "keycloakConfigCli", "resources"))

	seed := readRepo(t, "platform", "newapi", "chart", "templates", "admin-sso-seed-job.yaml")
	def := regexp.MustCompile(`\$seed\.resources \| default \(dict "requests" \(dict "cpu" "(\S+)" "memory" "(\S+)"\) "limits" \(dict "cpu" "(\S+)" "memory" "(\S+)"\)\)`).FindStringSubmatch(seed)
	if def == nil {
		t.Fatal("admin-seed default resources literal not found — re-point the pin")
	}
	sameShape(t, "newapi admin-seed", by["bp-newapi-admin-seed"].Containers[0], containerShape{RequestsCPU: def[1], RequestsMemory: def[2], LimitsCPU: def[3], LimitsMemory: def[4]})
	promote := seed[strings.Index(seed, "- name: admin-promote"):]
	sameShape(t, "newapi admin-promote", by["bp-newapi-admin-promote"].Containers[0], firstInlineShape(t, "admin-promote", promote))
	sync := readRepo(t, "platform", "newapi", "chart", "templates", "database-secret-sync-job.yaml")
	sameShape(t, "newapi db-dsn-sync", by["bp-newapi-db-dsn-sync"].Containers[0], firstInlineShape(t, "db-dsn-sync", sync[strings.Index(sync, "- name: sync"):]))

	if len(platformTransients) != 4 {
		t.Errorf("platformTransients has %d rows, this test pins 4 — add the new row's source pin here", len(platformTransients))
	}
	// The largest hook is the one that wedged hw307; if a bigger one lands
	// the headroom grows with it (TestPlatformQuota_PinnedFigures).
	th := transientHeadroomFor(planQuota("m"))
	if th.RequestsCPU.Cmp(mustQ(t, "", "250m")) <= 0 {
		t.Errorf("transient headroom requests.cpu %s does not exceed the keycloak-config-cli hook alone (250m)", th.RequestsCPU.String())
	}
}

func firstInlineShape(t *testing.T, what, block string) containerShape {
	t.Helper()
	m := inlineResourcesRe.FindStringSubmatch(block)
	if m == nil {
		t.Fatalf("%s: no literal requests/limits block", what)
	}
	return containerShape{RequestsCPU: m[1], RequestsMemory: m[2], LimitsCPU: m[3], LimitsMemory: m[4]}
}

// TestPlatformStack_SurgeMatchesController: a Deployment on RollingUpdate
// surges one pod; a StatefulSet or the CNPG instance does not.
func TestPlatformStack_SurgeMatchesController(t *testing.T) {
	t.Parallel()
	deployment := map[string][]string{
		wlNewAPI:   {"platform", "newapi", "chart", "templates", "deployment.yaml"},
		wlOpenClaw: {"platform", "openclaw", "chart", "templates", "controller-deployment.yaml"},
		wlOIDCGate: {"products", "agenity", "chart", "templates", "oidc-gate.yaml"},
	}
	surging := 0
	for _, w := range platformStack {
		f, isDeploy := deployment[w.Name]
		if w.Surge != isDeploy {
			t.Errorf("%s: Surge=%v, want %v (Deployments surge, StatefulSets/CNPG do not)", w.Name, w.Surge, isDeploy)
		}
		if !isDeploy {
			continue
		}
		surging++
		src := readRepo(t, f...)
		if !strings.Contains(src, "kind: Deployment") {
			t.Errorf("%s: %s no longer renders a Deployment", w.Name, strings.Join(f, "/"))
		}
		if strings.Contains(src, "type: Recreate") {
			t.Errorf("%s: %s now uses Recreate — it no longer surges; drop the Surge flag", w.Name, strings.Join(f, "/"))
		}
	}
	if surging != 3 {
		t.Errorf("vacuity: %d surging workloads, want 3", surging)
	}
}

// ─── 4. FIGURES ──────────────────────────────────────────────────────────────

// wantPlatformQuota: vcluster-0 500m/1Gi + stack (3840m/6064Mi, 4550m/7Gi) +
// transient headroom = largest hook (keycloak-config-cli 250m/256Mi) + one
// surge replica per resource (requests: newapi pod 535m, openclaw 512Mi;
// limits: newapi pod 1200m/1408Mi) = 785m/768Mi requests, 1450m/1664Mi limits.
// Identical on every plan for the reason TestPlatformStackOverhead_PinnedFigures
// gives.
var wantPlatformQuota = map[string]string{
	"requests.cpu": "5125m", "requests.memory": "7856Mi", "limits.cpu": "6500m", "limits.memory": "9856Mi",
}

func TestPlatformQuota_PinnedFigures(t *testing.T) {
	t.Parallel()
	th := transientHeadroomFor(planQuota("m"))
	for res, w := range map[string]struct {
		got  resource.Quantity
		want string
	}{
		"requests.cpu": {th.RequestsCPU, "785m"}, "requests.memory": {th.RequestsMemory, "768Mi"},
		"limits.cpu": {th.LimitsCPU, "1450m"}, "limits.memory": {th.LimitsMemory, "1664Mi"},
	} {
		if w.got.Cmp(mustQ(t, res, w.want)) != 0 {
			t.Errorf("transient headroom %s = %s, pinned %s", res, w.got.String(), w.want)
		}
	}
	for slug := range planQuotaTable {
		var d struct {
			Spec struct {
				Hard map[string]string `json:"hard"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal(renderPlan(t, slug)["vcluster/"+platformQuotaDoc], &d); err != nil {
			t.Fatal(err)
		}
		for res, w := range wantPlatformQuota {
			if d.Spec.Hard[res] != w {
				t.Errorf("plan %q: platform-quota %s = %s, pinned %s — re-pin here AND in docs/ARCHITECTURE.md", slug, res, d.Spec.Hard[res], w)
			}
		}
	}
	// The live hw307 platform pods (requests) fit with the headroom free for
	// the hook that was refused: 3805m running + newapi 535m + 250m hook.
	if c := mustQ(t, "", wantPlatformQuota["requests.cpu"]); c.Cmp(mustQ(t, "", "4590m")) < 0 {
		t.Errorf("platform-quota requests.cpu %s cannot hold the hw307 stack + newapi + the keycloak hook (4590m)", c.String())
	}
}

// ─── 5. INSTALL LIST ─────────────────────────────────────────────────────────

// TestPlatformScope_OrgControllerHelmReleasesAreModelled: the org-controller's
// own tree renders exactly one HelmRelease into the namespace — the vCluster,
// whose pods are the control plane (vcluster-0 in platform-quota, coredns in
// plan-quota). A new HelmRelease here must be added to platformStack or named
// as the customer's purchase.
func TestPlatformScope_OrgControllerHelmReleasesAreModelled(t *testing.T) {
	t.Parallel()
	hrs := []string{}
	for path, b := range renderPlan(t, "m") {
		for _, doc := range strings.Split(string(b), "\n---") {
			var o struct {
				Kind     string `json:"kind"`
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			}
			if yaml.Unmarshal([]byte(doc), &o) == nil && o.Kind == "HelmRelease" {
				hrs = append(hrs, path+":"+o.Metadata.Name)
			}
		}
	}
	sort.Strings(hrs)
	if strings.Join(hrs, ",") != "vcluster/vcluster.yaml:vcluster" {
		t.Errorf("org-controller renders HelmReleases %v; only the vCluster is modelled (vclusterControlPlane) — add the new one to platformStack or to the purchase list", hrs)
	}
}

func keys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// ─── 6. CLASSLESS PLATFORM CREDIT ────────────────────────────────────────────

func renderCredit(t *testing.T, in Inputs) quotaDocFor7067 {
	t.Helper()
	in.Slug, in.DisplayName, in.Tier = "acme", "Acme", "org"
	in.SovereignFQDN, in.HostCluster, in.VClusterChartVersion = "x.example", "hz", "0.33.*"
	if in.PlanSlug == "" {
		in.PlanSlug = "m"
	}
	out, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	var d quotaDocFor7067
	if err := yaml.Unmarshal(out["vcluster/resourcequota.yaml"], &d); err != nil {
		t.Fatal(err)
	}
	return d
}

type quotaDocFor7067 struct {
	Metadata struct {
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		ScopeSelector *struct {
			MatchExpressions []struct {
				Operator string `json:"operator"`
			} `json:"matchExpressions"`
		} `json:"scopeSelector"`
		Hard map[string]string `json:"hard"`
	} `json:"spec"`
}

// TestPlatformScope_PlanQuotaAlwaysScopedPlusCredit: plan-quota is scoped
// NotIn the class in EVERY state — no unscoped transitional quota (the #7078
// design that wedged acmewalk307) — and its hard is plan + coredns + the
// classless platform credit.
func TestPlatformScope_PlanQuotaAlwaysScopedPlusCredit(t *testing.T) {
	t.Parallel()
	credit := PlatformCredit{
		RequestsCPU: mustQ(t, "", "3805m"), RequestsMemory: mustQ(t, "", "6736Mi"),
		LimitsCPU: mustQ(t, "", "3850m"), LimitsMemory: mustQ(t, "", "6784Mi"), Pods: 7,
	}
	for name, in := range map[string]Inputs{
		"no credit":      {},
		"credit":         {ClasslessPlatformCredit: credit},
		"credit unknown": {CreditUnknown: true, LivePlanQuotaHard: map[string]string{"requests.cpu": "5479m"}},
	} {
		d := renderCredit(t, in)
		if d.Spec.ScopeSelector == nil || len(d.Spec.ScopeSelector.MatchExpressions) != 1 || d.Spec.ScopeSelector.MatchExpressions[0].Operator != "NotIn" {
			t.Errorf("%s: plan-quota is not scoped NotIn the platform class: %+v", name, d.Spec.ScopeSelector)
		}
		if d.Metadata.Annotations["openova.io/classless-platform-credit"] == "" {
			t.Errorf("%s: plan-quota does not state its classless platform credit", name)
		}
	}
	// plan m = 334m / 1366Mi, 2 / 4Gi; coredns 20m / 64Mi, 1 / 170Mi.
	got := renderCredit(t, Inputs{ClasslessPlatformCredit: credit}).Spec.Hard
	for k, w := range map[string]string{"requests.cpu": "4159m", "requests.memory": "8166Mi", "limits.cpu": "6850m", "limits.memory": "11050Mi"} {
		if got[k] != w {
			t.Errorf("credit: plan-quota %s = %s, want %s (plan + coredns + credit)", k, got[k], w)
		}
	}
	zero := renderCredit(t, Inputs{}).Spec.Hard
	if zero["requests.cpu"] != "354m" || zero["requests.memory"] != "1430Mi" {
		t.Errorf("no credit: plan-quota = %v, want the plan + coredns (354m / 1430Mi)", zero)
	}
}

// TestPlatformScope_CreditUnknownNeverShrinks: when the pods cannot be listed
// (the read RBAC lands in the same Flux apply as the first render) the hard cap
// is, per resource, the larger of the live hard and the static safe bound —
// never lower than either.
func TestPlatformScope_CreditUnknownNeverShrinks(t *testing.T) {
	t.Parallel()
	static := staticPlatformCredit(planQuota("m"))
	// plan + coredns + vcluster-0 + stack = 354m+500m+3840m / 1430Mi+1Gi+6064Mi …
	bound := map[string]string{"requests.cpu": "4694m", "requests.memory": "8518Mi", "limits.cpu": "8050m", "limits.memory": "12458Mi"}
	if static.Pods != len(platformStack)+1 {
		t.Errorf("static credit covers %d pods, want the stack + vcluster-0", static.Pods)
	}
	// The live acmewalk307 hard after #7078 (unscoped transitional): larger on
	// every side, so it is kept.
	live := map[string]string{"requests.cpu": "5479m", "requests.memory": "9286Mi", "limits.cpu": "9500m", "limits.memory": "14122Mi"}
	if got := renderCredit(t, Inputs{CreditUnknown: true, LivePlanQuotaHard: live}).Spec.Hard; !equalHard(got, live) {
		t.Errorf("unknown credit with a larger live hard: %v, want the live hard kept %v", got, live)
	}
	// A live hard below the bound (or none at all) never pulls it down.
	for name, l := range map[string]map[string]string{
		"smaller live": {"requests.cpu": "354m", "requests.memory": "1430Mi", "limits.cpu": "3", "limits.memory": "4266Mi"},
		"no live":      nil,
		"garbage live": {"requests.cpu": "lots"},
	} {
		if got := renderCredit(t, Inputs{CreditUnknown: true, LivePlanQuotaHard: l}).Spec.Hard; !equalHard(got, bound) {
			t.Errorf("unknown credit, %s: %v, want the static safe bound %v", name, got, bound)
		}
	}
	// Mixed: each resource takes its own maximum.
	mixed := map[string]string{"requests.cpu": "9000m", "requests.memory": "1Mi", "limits.cpu": "1", "limits.memory": "99Gi"}
	got := renderCredit(t, Inputs{CreditUnknown: true, LivePlanQuotaHard: mixed}).Spec.Hard
	want := map[string]string{"requests.cpu": "9", "requests.memory": "8518Mi", "limits.cpu": "8050m", "limits.memory": "99Gi"}
	if !equalHard(got, want) {
		t.Errorf("unknown credit, mixed live: %v, want per-resource max %v", got, want)
	}
}

func equalHard(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range b {
		x, err1 := resource.ParseQuantity(a[k])
		y, err2 := resource.ParseQuantity(v)
		if err1 != nil || err2 != nil || x.Cmp(y) != 0 {
			return false
		}
	}
	return true
}

// TestPlatformScope_ReadRBACBindsTheController: the credit's pod read is a
// namespaced Role bound to the org-controller's ServiceAccount, delivered by
// the Org's own tree — no cluster-wide pod read, no umbrella ClusterRole change.
func TestPlatformScope_ReadRBACBindsTheController(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ sa, ns, wantSA, wantNS string }{
		{"", "", DefaultControllerServiceAccount, DefaultControllerNamespace},
		{"orgctl", "ops", "orgctl", "ops"},
	} {
		out, err := Render(Inputs{Slug: "acme", PlanSlug: "m", SovereignFQDN: "x", HostCluster: "hz", VClusterChartVersion: "0.33.*",
			ControllerServiceAccount: c.sa, ControllerNamespace: c.ns})
		if err != nil {
			t.Fatal(err)
		}
		docs := strings.Split(string(out["vcluster/"+platformScopeRBACDoc]), "\n---\n")
		if len(docs) != 2 {
			t.Fatalf("platform-scope-rbac.yaml has %d documents, want Role + RoleBinding", len(docs))
		}
		var role struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Rules []struct {
				Resources []string `json:"resources"`
				Verbs     []string `json:"verbs"`
			} `json:"rules"`
		}
		var rb struct {
			Kind     string `json:"kind"`
			Subjects []struct {
				Kind, Name, Namespace string
			} `json:"subjects"`
		}
		if yaml.Unmarshal([]byte(docs[0]), &role) != nil || yaml.Unmarshal([]byte(docs[1]), &rb) != nil {
			t.Fatal("rbac does not parse")
		}
		if role.Kind != "Role" || role.Metadata.Namespace != "acme" || len(role.Rules) != 1 ||
			strings.Join(role.Rules[0].Resources, ",") != "pods" || strings.Join(role.Rules[0].Verbs, ",") != "get,list" {
			t.Errorf("Role = %+v, want a namespaced read-only pods Role in acme", role)
		}
		if rb.Kind != "RoleBinding" || len(rb.Subjects) != 1 || rb.Subjects[0].Name != c.wantSA || rb.Subjects[0].Namespace != c.wantNS {
			t.Errorf("RoleBinding subjects = %+v, want %s/%s", rb.Subjects, c.wantNS, c.wantSA)
		}
	}
}
