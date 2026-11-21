// plan_quota_included_test.go — plan-quota holds what every Organization is
// given in the customer scope, and leaves the plan's part as sold (the
// smallest plan could not take one small application on hw311, 2026-11-21).
//
// Four properties:
//
//  1. LIST — includedStack covers exactly the applications the funnel installs
//     for every Organization (organization_funnel_apps.go
//     funnelApplicationFiles) that the platform scope does not cover.
//  2. PIN — every figure is the one its source sets: the funnel's overlay
//     values (organization_gitops.go) with the purchase sizing
//     (organization_purchase_sizing.go) substituted for its template markers,
//     the chart's container inventory, unsized containers, strategy and
//     inline hook sizes.
//  3. FIGURES — for every plan the rendered plan-quota is the plan as sold +
//     coredns + the given workloads (their pods, the largest hook pod and one
//     surge replica), pinned by value and re-derived from the table.
//  4. THE CHECK — the application of acceptance check NET-G07 step 9 fits the
//     customer's part of the plan the check gives its Organization A, and
//     that plan is the smallest one whose part holds it.
//
// The reads leave this module; run under -count=1 like the other pin tests.
package gitops

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

// wantIncludedCredit is what plan-quota holds for the given applications, per
// plan: pods (WordPress, its database, Stalwart, webmail) + the largest hook
// pod (a WordPress hook, 50m / 128Mi, 250m / 256Mi) + one surge replica (one
// WordPress pod). It grows on L and XL because the unsized init containers of
// WordPress and webmail take the plan's LimitRange defaults (headline / 8).
var wantIncludedCredit = map[string]map[string]string{
	"s":  {"requests.cpu": "407m", "requests.memory": "1069Mi", "limits.cpu": "2375m", "limits.memory": "3072Mi"},
	"m":  {"requests.cpu": "428m", "requests.memory": "1154Mi", "limits.cpu": "2500m", "limits.memory": "3328Mi"},
	"l":  {"requests.cpu": "470m", "requests.memory": "1667Mi", "limits.cpu": "2750m", "limits.memory": "4864Mi"},
	"xl": {"requests.cpu": "719m", "requests.memory": "2690Mi", "limits.cpu": "4250m", "limits.memory": "7936Mi"},
}

const (
	wlWordPress   = "<release>-bp-wordpress-tenant-<hash>"
	wlWordPressDB = "wordpress-tenant-db-<id>-1"
	wlStalwart    = "<release>-bp-stalwart-tenant-0"
	wlWebmail     = "<release>-bp-stalwart-tenant-webmail-<hash>"
)

var funnelFileRe = regexp.MustCompile(`(?m)^\s*"(bp-[a-z0-9-]+)\.yaml":\s*true,`)

// ─── 1. LIST ────────────────────────────────────────────────────────────────

func TestIncludedStack_IsWhatTheFunnelGivesOutsideThePlatformScope(t *testing.T) {
	t.Parallel()
	src := readRepo(t, "products", "catalyst", "bootstrap", "api", "internal", "handler", "organization_funnel_apps.go")
	i := strings.Index(src, "var funnelApplicationFiles = map[string]bool{")
	if i < 0 {
		t.Fatal("funnelApplicationFiles not found in organization_funnel_apps.go — re-point this pin")
	}
	block := src[i : i+strings.Index(src[i:], "\n}")]
	funnel := funnelFileRe.FindAllStringSubmatch(block, -1)
	if len(funnel) < 4 {
		t.Fatalf("vacuity: %d funnel applications found (%v)", len(funnel), funnel)
	}
	// Each file's chart is the Blueprint the funnel installs (its HelmRelease
	// spec.chart.spec.chart): read it, do not assume the file name.
	bss := readRepo(t, "products", "catalyst", "bootstrap", "api", "internal", "handler", "organization_gitops.go")
	consts := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*"(bp-[a-z0-9-]+\.yaml)":\s*(orgTenantBP\w+),`).FindAllStringSubmatch(bss, -1) {
		consts[m[1]] = m[2]
	}
	platformCharts := map[string]bool{}
	for _, n := range platformScope.ChartNames {
		platformCharts[n] = true
	}
	given := map[string]bool{}
	for _, w := range includedStack {
		given[w.Release] = true
	}
	inPlatform := map[string]bool{}
	for _, w := range platformStack {
		inPlatform[w.Release] = true
	}
	// The funnel names each Application after its overlay HelmRelease, "bp-"
	// trimmed; includedStack rows carry that Application name.
	if !strings.Contains(src, `Name:       strings.TrimPrefix(hr.Metadata.Name, "bp-"),`) {
		t.Fatal("funnelApplicationsFromOverlay no longer names the Application after the HelmRelease without bp- — re-point includedStack's Release")
	}
	outside := map[string]bool{}
	for _, m := range funnel {
		c, ok := consts[m[1]+".yaml"]
		if !ok {
			t.Fatalf("orgTenantTemplates has no template for funnel application %s", m[1])
		}
		name, chart := helmReleaseIdentity(t, goRawString(t, bss, "const "+c+" = "))
		app := strings.TrimPrefix(name, "bp-")
		switch {
		case platformCharts[chart]:
			if !inPlatform[chart] {
				t.Errorf("funnel application %s is platform scope but platformStack does not carry it", chart)
			}
		default:
			outside[app] = true
			if !given[app] {
				t.Errorf("the funnel gives every Organization %s (chart %s), which the platform scope does not cover, and includedStack does not carry it — its pods eat the plan's part", app, chart)
			}
		}
	}
	for r := range given {
		if !outside[r] {
			t.Errorf("includedStack carries %s, which the funnel does not install outside the platform scope — plan-quota is overstated", r)
		}
	}
	if len(outside) != 2 {
		t.Errorf("vacuity: %d given applications outside the platform scope (%v), want the website and the mail server", len(outside), outside)
	}
}

// helmReleaseIdentity returns metadata.name and spec.chart.spec.chart of the
// HelmRelease in a template.
func helmReleaseIdentity(t *testing.T, doc string) (name, chart string) {
	t.Helper()
	neutral := fmtVerbRe.ReplaceAllString(tmplMarkerRe.ReplaceAllString(doc, "x"), "x")
	var obj struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Chart struct {
				Spec struct {
					Chart string `json:"chart"`
				} `json:"spec"`
			} `json:"chart"`
		} `json:"spec"`
	}
	for _, d := range strings.Split(neutral, "\n---\n") {
		if strings.Contains(d, "kind: HelmRelease") {
			if err := yaml.Unmarshal([]byte(d), &obj); err != nil {
				t.Fatalf("HelmRelease did not parse: %v", err)
			}
		}
	}
	if obj.Metadata.Name == "" || obj.Spec.Chart.Spec.Chart == "" {
		t.Fatalf("vacuity: HelmRelease name %q, chart %q", obj.Metadata.Name, obj.Spec.Chart.Spec.Chart)
	}
	return obj.Metadata.Name, obj.Spec.Chart.Spec.Chart
}

// ─── 2. PIN ─────────────────────────────────────────────────────────────────

var (
	purchasedRe     = regexp.MustCompile(`\{Key: "([a-z0-9-]+)", Release: "(bp-[a-z0-9-]+)", ValuesPath: \[\]string\{[^}]*\}, LimitsCPU: "([^"]+)", LimitsMemory: "([^"]+)"\}`)
	purchaseMarkRe  = regexp.MustCompile(`\{\{-?\s*\(index \.Purchase "([a-z0-9-]+)"\)\.(RequestsCPU|RequestsMemory|LimitsCPU|LimitsMemory)\s*-?\}\}`)
	purchaseRatioRe = regexp.MustCompile(`(?m)^\s*purchaseOvercommit(CPU|Memory)\s*=\s*(\d+)`)
)

// purchaseFigures reads the funnel's purchase sizing: every purchasedWorkloads
// row with its request as the funnel renders it (limit / the overcommit,
// rounded up — the ratioSized rule, the same ceilDiv as the LimitRange).
func purchaseFigures(t *testing.T) map[string]containerShape {
	t.Helper()
	src := readRepo(t, "products", "catalyst", "bootstrap", "api", "internal", "handler", "organization_purchase_sizing.go")
	ratio := map[string]int64{}
	for _, m := range purchaseRatioRe.FindAllStringSubmatch(src, -1) {
		n, _ := strconv.ParseInt(m[2], 10, 64)
		ratio[m[1]] = n
	}
	if ratio["CPU"] != planOvercommitCPU || ratio["Memory"] != planOvercommitMemory {
		t.Fatalf("purchase overcommit %v, the plan's is %d / %d — the funnel no longer sizes by the plan's rule; re-derive includedStack", ratio, planOvercommitCPU, planOvercommitMemory)
	}
	if !strings.Contains(src, "ceil(c.MilliValue(), purchaseOvercommitCPU)") || !strings.Contains(src, "ceil(ceil(m.Value(), mi), purchaseOvercommitMemory)") {
		t.Fatal("ratioSized no longer rounds the request up from the limit — re-derive the rule here")
	}
	out := map[string]containerShape{}
	for _, m := range purchasedRe.FindAllStringSubmatch(src, -1) {
		lc, lm := mustQ(t, m[1], m[3]), mustQ(t, m[1], m[4])
		out[m[1]] = containerShape{
			RequestsCPU:    ceilDiv(lc.MilliValue(), planOvercommitCPU, "m"),
			RequestsMemory: ceilDiv((lm.Value()+mi-1)/mi, planOvercommitMemory, "Mi"),
			LimitsCPU:      m[3], LimitsMemory: m[4],
		}
	}
	if len(out) != 5 {
		t.Fatalf("vacuity: %d purchasedWorkloads rows parsed (%v), want 5 — re-point purchasedRe", len(out), out)
	}
	return out
}

// funnelValues renders the spec.values of one funnel overlay template with its
// purchase markers resolved, as the funnel renders them.
func funnelValues(t *testing.T, constName string, figures map[string]containerShape) map[string]any {
	t.Helper()
	bss := readRepo(t, "products", "catalyst", "bootstrap", "api", "internal", "handler", "organization_gitops.go")
	doc := goRawString(t, bss, "const "+constName+" = ")
	marks := 0
	doc = purchaseMarkRe.ReplaceAllStringFunc(doc, func(s string) string {
		m := purchaseMarkRe.FindStringSubmatch(s)
		f, ok := figures[m[1]]
		if !ok {
			t.Errorf("%s reads purchase %q, which purchasedWorkloads does not carry", constName, m[1])
			return "0"
		}
		marks++
		return map[string]string{"RequestsCPU": f.RequestsCPU, "RequestsMemory": f.RequestsMemory, "LimitsCPU": f.LimitsCPU, "LimitsMemory": f.LimitsMemory}[m[2]]
	})
	if marks < 8 {
		t.Fatalf("vacuity: %s carries %d purchase markers — the sizing moved; re-point this pin", constName, marks)
	}
	return helmReleaseValues(t, doc)
}

// pinShape compares a table figure with its source, as quantities.
func pinShape(t *testing.T, what string, got, want containerShape) {
	t.Helper()
	for _, f := range []struct{ name, g, w string }{
		{"requests.cpu", got.RequestsCPU, want.RequestsCPU},
		{"requests.memory", got.RequestsMemory, want.RequestsMemory},
		{"limits.cpu", got.LimitsCPU, want.LimitsCPU},
		{"limits.memory", got.LimitsMemory, want.LimitsMemory},
	} {
		g, w := mustQ(t, what+" table "+f.name, f.g), mustQ(t, what+" source "+f.name, f.w)
		if g.IsZero() || w.IsZero() {
			t.Errorf("vacuity: %s %s is zero (table %q, source %q)", what, f.name, f.g, f.w)
		}
		if g.Cmp(w) != 0 {
			t.Errorf("%s %s: the table carries %s but its source sets %s — move them together and re-pin wantIncludedCredit and the docs", what, f.name, g.String(), w.String())
		}
	}
}

// looseInlineShape reads a literal requests/limits block from a container's
// template text, comment lines removed (the WordPress hooks explain their
// limits inside the block).
func looseInlineShape(t *testing.T, what, block string) containerShape {
	t.Helper()
	var keep []string
	for _, l := range strings.Split(block, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			keep = append(keep, l)
		}
	}
	m := inlineResourcesRe.FindStringSubmatch(strings.Join(keep, "\n"))
	if m == nil {
		t.Fatalf("%s: no literal requests/limits block", what)
	}
	return containerShape{RequestsCPU: m[1], RequestsMemory: m[2], LimitsCPU: m[3], LimitsMemory: m[4]}
}

func includedByName(t *testing.T) (map[string]platformStackWorkload, map[string]platformTransient) {
	t.Helper()
	pods := map[string]platformStackWorkload{}
	for _, w := range includedStack {
		pods[w.Name] = w
	}
	hooks := map[string]platformTransient{}
	for _, h := range includedTransients {
		hooks[h.Name] = h
	}
	if len(pods) != 4 || len(includedStack) != 4 {
		t.Fatalf("includedStack has %d rows (%d names), this test pins 4 — add the new row's source pin here", len(includedStack), len(pods))
	}
	if len(hooks) != 4 || len(includedTransients) != 4 {
		t.Fatalf("includedTransients has %d rows (%d names), this test pins 4 — add the new row's source pin here", len(includedTransients), len(hooks))
	}
	return pods, hooks
}

func unsized(t *testing.T, what, tmpl string, w platformStackWorkload, names []string) {
	t.Helper()
	if len(w.Inits) != len(names) {
		t.Fatalf("%s: modelled with %d inits, the chart declares %v", what, len(w.Inits), names)
	}
	for i, n := range names {
		if w.Inits[i] != (containerShape{}) {
			t.Errorf("%s: %s is modelled as sized %+v, but the chart leaves it unsized — the LimitRange default is what admission charges", what, n, w.Inits[i])
		}
		if strings.Contains(containerBlock(t, tmpl, n), "resources:") {
			t.Errorf("%s: the chart now sizes %s — model its figures instead of the LimitRange default", what, n)
		}
	}
}

func TestIncludedStack_PinnedToSources(t *testing.T) {
	t.Parallel()
	pods, hooks := includedByName(t)
	fig := purchaseFigures(t)
	wp := funnelValues(t, "orgTenantBPWordPress", fig)
	st := funnelValues(t, "orgTenantBPStalwart", fig)
	wv := yamlFile(t, "platform", "wordpress-tenant", "chart", "values.yaml")
	sv := yamlFile(t, "platform", "stalwart-tenant", "chart", "values.yaml")
	limitsKept := func(what string, got containerShape, chart map[string]any, path ...string) {
		t.Helper()
		for _, f := range []struct{ key, got string }{{"cpu", got.LimitsCPU}, {"memory", got.LimitsMemory}} {
			c := scalarAt(t, chart, append(append([]string(nil), path...), "limits", f.key)...)
			a, b := mustQ(t, what, c), mustQ(t, what, f.got)
			if a.Cmp(b) != 0 {
				t.Errorf("%s: limits.%s %s, the chart sets %s — the purchase sizing keeps the chart's limit", what, f.key, f.got, c)
			}
		}
	}
	one := func(chart map[string]any, path ...string) {
		t.Helper()
		if v := scalarAt(t, chart, path...); v != "1" {
			t.Errorf("%s = %s — the table models one pod", strings.Join(path, "."), v)
		}
	}

	// WordPress: the funnel's sizing, three unsized inits, RollingUpdate under s3.
	dep := readRepo(t, "platform", "wordpress-tenant", "chart", "templates", "deployment.yaml")
	w := pods[wlWordPress]
	if len(w.Containers) != 1 {
		t.Fatalf("%s: %d containers, want wordpress", wlWordPress, len(w.Containers))
	}
	pinShape(t, wlWordPress+" wordpress", w.Containers[0], shapeAt(t, wp, "wordpress", "resources"))
	pinShape(t, wlWordPress+" wordpress (purchase)", w.Containers[0], fig["wordpress"])
	limitsKept(wlWordPress, w.Containers[0], wv, "wordpress", "resources")
	namesEqual(t, wlWordPress, podSpecContainerNames(t, dep, "bp-wordpress-tenant deployment.yaml"),
		[]string{"wp-fix-perms", "wp-content-bootstrap", "wp-plugin-install", "wordpress"})
	unsized(t, wlWordPress, dep, w, []string{"wp-fix-perms", "wp-content-bootstrap", "wp-plugin-install"})
	one(wv, "wordpress", "replicas")
	if v := scalarAt(t, wv, "media", "store"); v != "s3" {
		t.Errorf("media.store defaults to %q — under pvc the Deployment is Recreate and does not surge", v)
	}
	if _, set := dig(wp, "media", "store"); set {
		t.Errorf("the funnel now sets media.store — re-derive the WordPress strategy")
	}
	helpers := readRepo(t, "platform", "wordpress-tenant", "chart", "templates", "_helpers.tpl")
	if !strings.Contains(helpers, `{{- if and (eq (include "bp-wordpress-tenant.mediaStore" .) "pvc") .Values.persistence.wpContent.enabled -}}true{{- end -}}`) {
		t.Errorf("wpContentOnPvc no longer requires media.store pvc — re-derive the WordPress strategy")
	}
	if !strings.Contains(dep, "kind: Deployment") || !regexp.MustCompile(`(?s)\{\{- if \$onPvc \}\}.*type: Recreate.*\{\{- else \}\}.*type: RollingUpdate`).MatchString(dep) {
		t.Errorf("the WordPress Deployment no longer rolls (RollingUpdate) off the PVC — re-derive its Surge flag")
	}
	if !w.Surge {
		t.Errorf("%s rolls with RollingUpdate and must carry Surge", wlWordPress)
	}

	// Its database: the funnel's sizing, one CNPG instance.
	db := pods[wlWordPressDB]
	if len(db.Containers) != 1 || len(db.Inits) != 1 {
		t.Fatalf("%s: modelled as %d containers / %d inits, want the instance and the bootstrap-controller init", wlWordPressDB, len(db.Containers), len(db.Inits))
	}
	pinShape(t, wlWordPressDB+" instance", db.Containers[0], shapeAt(t, wp, "database", "cluster", "resources"))
	pinShape(t, wlWordPressDB+" instance (purchase)", db.Containers[0], fig["wordpress-db"])
	pinShape(t, wlWordPressDB+" bootstrap-controller init", db.Inits[0], shapeAt(t, wp, "database", "cluster", "resources"))
	one(wv, "database", "cluster", "instances")
	cnpg := readRepo(t, "platform", "wordpress-tenant", "chart", "templates", "cnpg-cluster.yaml")
	for _, s := range []string{"kind: Cluster", "instances: {{ .Values.database.cluster.instances }}", "{{- toYaml .Values.database.cluster.resources | nindent 4 }}"} {
		if !strings.Contains(cnpg, s) {
			t.Errorf("cnpg-cluster.yaml no longer renders %q — re-point the database pin", s)
		}
	}
	if db.Surge {
		t.Errorf("%s: a single-instance CNPG cluster replaces its pod in place and does not surge", wlWordPressDB)
	}

	// Stalwart: a StatefulSet, one container.
	sts := readRepo(t, "platform", "stalwart-tenant", "chart", "templates", "deployment.yaml")
	s := pods[wlStalwart]
	if len(s.Containers) != 1 || len(s.Inits) != 0 {
		t.Fatalf("%s: modelled as %d containers / %d inits, want one", wlStalwart, len(s.Containers), len(s.Inits))
	}
	pinShape(t, wlStalwart+" stalwart", s.Containers[0], shapeAt(t, st, "stalwart", "resources"))
	pinShape(t, wlStalwart+" stalwart (purchase)", s.Containers[0], fig["stalwart"])
	limitsKept(wlStalwart, s.Containers[0], sv, "stalwart", "resources")
	namesEqual(t, wlStalwart, podSpecContainerNames(t, sts, "bp-stalwart-tenant deployment.yaml"), []string{"stalwart"})
	one(sv, "stalwart", "replicas")
	if !strings.Contains(sts, "kind: StatefulSet") || s.Surge {
		t.Errorf("%s: a StatefulSet replaces its pod in place (Surge=%v)", wlStalwart, s.Surge)
	}

	// Webmail: the funnel's sizing, the unsized seed-domain init, default strategy.
	wmt := readRepo(t, "platform", "stalwart-tenant", "chart", "templates", "webmail-deployment.yaml")
	wm := pods[wlWebmail]
	if len(wm.Containers) != 1 {
		t.Fatalf("%s: %d containers, want webmail", wlWebmail, len(wm.Containers))
	}
	pinShape(t, wlWebmail+" webmail", wm.Containers[0], shapeAt(t, st, "webmail", "resources"))
	pinShape(t, wlWebmail+" webmail (purchase)", wm.Containers[0], fig["webmail"])
	limitsKept(wlWebmail, wm.Containers[0], sv, "webmail", "resources")
	namesEqual(t, wlWebmail, podSpecContainerNames(t, wmt, "bp-stalwart-tenant webmail-deployment.yaml"), []string{"seed-domain", "webmail"})
	unsized(t, wlWebmail, wmt, wm, []string{"seed-domain"})
	one(sv, "webmail", "replicas")
	for _, p := range [][]string{{"webmail", "enabled"}, {"webmail", "domainSeed", "enabled"}} {
		if v := scalarAt(t, sv, p...); v != "true" {
			t.Errorf("%s = %s — the table models the webmail pod and its seed-domain init as rendered", strings.Join(p, "."), v)
		}
	}
	if !strings.Contains(wmt, "kind: Deployment") || strings.Contains(wmt, "strategy:") || !wm.Surge {
		t.Errorf("%s: a Deployment on the default RollingUpdate surges one pod (Surge=%v); a strategy in the template must be re-derived", wlWebmail, wm.Surge)
	}

	// The hooks: inline sizes, the setup Job the funnel's sizing.
	for _, h := range []struct {
		name, file string
		inits      []string
		container  string
	}{
		{"<release>-bp-wordpress-tenant-oidc-config", "oidc-config-job.yaml", []string{"wp-artifacts"}, "oidc-config"},
		{"<release>-bp-wordpress-tenant-admin-user", "admin-user-job.yaml", nil, "admin-user"},
		{"<release>-bp-wordpress-tenant-media-migrate", "media-migrate-job.yaml", nil, "media-migrate"},
	} {
		tmpl := readRepo(t, "platform", "wordpress-tenant", "chart", "templates", h.file)
		row, ok := hooks[h.name]
		if !ok {
			t.Fatalf("includedTransients has no %s", h.name)
		}
		if !regexp.MustCompile(`"helm.sh/hook":\s*"post-install,post-upgrade"`).MatchString(tmpl) {
			t.Errorf("%s is no longer a post-install/post-upgrade hook — re-derive includedTransients", h.file)
		}
		namesEqual(t, h.name, podSpecContainerNames(t, tmpl, h.file), append(append([]string(nil), h.inits...), h.container))
		if len(row.Containers) != 1 || len(row.Inits) != len(h.inits) {
			t.Fatalf("%s: modelled as %d containers / %d inits", h.name, len(row.Containers), len(row.Inits))
		}
		pinShape(t, h.name, row.Containers[0], looseInlineShape(t, h.name, containerBlock(t, tmpl, h.container)))
		for i, n := range h.inits {
			pinShape(t, h.name+" "+n, row.Inits[i], looseInlineShape(t, h.name+" "+n, containerBlock(t, tmpl, n)))
		}
	}
	setup := hooks["<release>-bp-stalwart-tenant-setup"]
	mp := readRepo(t, "platform", "stalwart-tenant", "chart", "templates", "mailbox-provision-job.yaml")
	if !strings.Contains(mp, "{{- toYaml .Values.mailboxProvisioner.setupJob.resources | nindent 12 }}") || !regexp.MustCompile(`"helm.sh/hook":\s*post-install,post-upgrade`).MatchString(mp) {
		t.Errorf("mailbox-provision-job.yaml no longer sizes the setup hook from mailboxProvisioner.setupJob.resources — re-point the pin")
	}
	namesEqual(t, "stalwart setup", podSpecContainerNames(t, mp, "mailbox-provision-job.yaml"), []string{"setup"})
	if len(setup.Containers) != 1 || len(setup.Inits) != 0 {
		t.Fatalf("stalwart setup: modelled as %d containers / %d inits", len(setup.Containers), len(setup.Inits))
	}
	pinShape(t, "stalwart setup", setup.Containers[0], shapeAt(t, st, "mailboxProvisioner", "setupJob", "resources"))
	pinShape(t, "stalwart setup (purchase)", setup.Containers[0], fig["stalwart-setup"])

	for _, w := range includedStack {
		if w.Source == "" {
			t.Errorf("%s: no Source", w.Name)
		}
	}
}

// ─── 3. FIGURES ─────────────────────────────────────────────────────────────

type includedRQ struct {
	Metadata struct {
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Hard map[string]string `json:"hard"`
	} `json:"spec"`
}

func planQuotaOf(t *testing.T, plan string) includedRQ {
	t.Helper()
	var rq includedRQ
	if err := yaml.Unmarshal(renderPlan(t, plan)["vcluster/resourcequota.yaml"], &rq); err != nil {
		t.Fatalf("plan %s resourcequota.yaml: %v", plan, err)
	}
	if len(rq.Spec.Hard) != 4 {
		t.Fatalf("plan %s: plan-quota hard %v, want the four cpu/memory keys", plan, rq.Spec.Hard)
	}
	return rq
}

// givenCharge is what admission charges for the given applications on a plan,
// derived here from the tables: every pod under the pod-usage rule, plus the
// largest hook pod, plus one replica of the largest surging pod, per resource.
func givenCharge(t *testing.T, q PlanQuota) map[string]resource.Quantity {
	t.Helper()
	def := limitRangeDefaults(q)
	size := func(cs []containerShape) []containerShape {
		out := append([]containerShape(nil), cs...)
		for i := range out {
			if out[i] == (containerShape{}) {
				out[i] = def
			}
		}
		return out
	}
	keys := []string{"requests.cpu", "requests.memory", "limits.cpu", "limits.memory"}
	field := func(s containerShape, k string) resource.Quantity {
		return mustQ(t, k, map[string]string{"requests.cpu": s.RequestsCPU, "requests.memory": s.RequestsMemory, "limits.cpu": s.LimitsCPU, "limits.memory": s.LimitsMemory}[k])
	}
	sum := map[string]resource.Quantity{}
	hook := map[string]resource.Quantity{}
	surge := map[string]resource.Quantity{}
	for _, w := range includedStack {
		pod := podEffectiveShape(size(w.Containers), size(w.Inits))
		for _, k := range keys {
			v := sum[k]
			v.Add(field(pod, k))
			sum[k] = v
			if f := field(pod, k); w.Surge && f.Cmp(surge[k]) > 0 {
				surge[k] = f
			}
		}
	}
	for _, h := range includedTransients {
		pod := podEffectiveShape(size(h.Containers), size(h.Inits))
		for _, k := range keys {
			if f := field(pod, k); f.Cmp(hook[k]) > 0 {
				hook[k] = f
			}
		}
	}
	for _, k := range keys {
		v := sum[k]
		v.Add(hook[k])
		v.Add(surge[k])
		sum[k] = v
	}
	return sum
}

// TestPlanQuota_HoldsTheGivenWorkloadsAndThePlanAsSold: on every plan, plan-quota
// minus the plan as sold minus coredns is exactly what the given applications
// are charged — so with them running, the customer has the plan's part.
// Fails on a render that holds the plan + coredns only (main on 2026-11-21).
func TestPlanQuota_HoldsTheGivenWorkloadsAndThePlanAsSold(t *testing.T) {
	t.Parallel()
	if len(wantIncludedCredit) != len(planQuotaTable) {
		t.Fatalf("vacuity: %d plans pinned, %d in planQuotaTable", len(wantIncludedCredit), len(planQuotaTable))
	}
	for slug, q := range planQuotaTable {
		rq := planQuotaOf(t, slug)
		want := wantIncludedCredit[slug]
		charge := givenCharge(t, q)
		total := includedCreditFor(q).Total()
		model := map[string]resource.Quantity{"requests.cpu": total.RequestsCPU, "requests.memory": total.RequestsMemory, "limits.cpu": total.LimitsCPU, "limits.memory": total.LimitsMemory}
		for res, plan := range map[string]string{"requests.cpu": q.CPURequest, "requests.memory": q.MemRequest, "limits.cpu": q.CPULimit, "limits.memory": q.MemLimit} {
			dns := map[string]resource.Quantity{"requests.cpu": corednsOverhead.RequestsCPU, "requests.memory": corednsOverhead.RequestsMemory, "limits.cpu": corednsOverhead.LimitsCPU, "limits.memory": corednsOverhead.LimitsMemory}[res]
			held := mustQ(t, slug+" hard "+res, rq.Spec.Hard[res])
			held.Sub(mustQ(t, slug+" plan "+res, plan))
			held.Sub(dns)
			c := charge[res]
			if c.IsZero() {
				t.Fatalf("vacuity: plan %s: the given applications charge zero %s", slug, res)
			}
			if held.Cmp(c) < 0 {
				left := mustQ(t, "", plan)
				left.Add(held)
				left.Sub(c)
				t.Errorf("plan %s: plan-quota holds %s %s beyond the plan as sold + coredns, but the given applications are charged %s — the customer is left %s of the plan's %s", slug, held.String(), res, c.String(), left.String(), plan)
			}
			if mr := model[res]; held.Cmp(c) != 0 || mr.Cmp(c) != 0 {
				t.Errorf("plan %s %s: held %s, includedCreditFor %s, derived charge %s — all three must agree", slug, res, held.String(), mr.String(), c.String())
			}
			if w := mustQ(t, slug+" want "+res, want[res]); c.Cmp(w) != 0 {
				t.Errorf("plan %s: the given applications are charged %s %s, pinned %s — a source moved; re-pin wantIncludedCredit and docs/ARCHITECTURE.md + docs/SYSTEM-DESIGN.md", slug, c.String(), res, w.String())
			}
		}
		if got, w := rq.Metadata.Annotations["openova.io/included-applications-credit"], includedCreditFor(q).String(); got != w {
			t.Errorf("plan %s: annotation openova.io/included-applications-credit = %q, want %q", slug, got, w)
		}
	}
}

// TestPlanQuota_GivenTermIsTheHeadlinesInGrow: grow raises only the customer's
// limits; the given term is sized from the headline's LimitRange defaults.
func TestPlanQuota_GivenTermIsTheHeadlinesInGrow(t *testing.T) {
	t.Parallel()
	grow := parseRQ(t, renderOverage(t, "s", OverageModeGrow, "", ""))
	capped := parseRQ(t, renderOverage(t, "s", OverageModeCapped, "", ""))
	xl := planQuotaTable[growCeilingPlan]
	given := includedCreditFor(planQuota("s")).Total()
	for _, k := range []string{"requests.cpu", "requests.memory"} {
		if grow.Spec.Hard[k] != capped.Spec.Hard[k] {
			t.Errorf("grow s %s = %s, capped %s — the requests side does not move", k, grow.Spec.Hard[k], capped.Spec.Hard[k])
		}
	}
	for k, parts := range map[string][]resource.Quantity{
		"limits.cpu":    {mustQ(t, "", xl.CPULimit), corednsOverhead.LimitsCPU, given.LimitsCPU},
		"limits.memory": {mustQ(t, "", xl.MemLimit), corednsOverhead.LimitsMemory, given.LimitsMemory},
	} {
		var want resource.Quantity
		for _, p := range parts {
			want.Add(p)
		}
		if got := mustQ(t, k, grow.Spec.Hard[k]); got.Cmp(want) != 0 {
			t.Errorf("grow s %s = %s, want the XL ceiling + coredns + the given term at the S headline = %s", k, got.String(), want.String())
		}
	}
}

// ─── 4. THE CHECK ───────────────────────────────────────────────────────────

var (
	isoBlueprintRe = regexp.MustCompile(`(?m)^\s*isoBlueprint\s*=\s*"([^"]+)"`)
	isoPlanARe     = regexp.MustCompile(`(?m)^\s*isoPlanA\s*=\s*"([^"]+)"`)
)

// isoApplicationNeed is what NET-G07 step 9's Application needs on a plan: it
// is a bp-wordpress-tenant installed with no values (POST
// /catalyst/v1/apps/instances), so the chart's own defaults — the WordPress
// pod (its unsized inits at the plan's LimitRange defaults) + its database
// (no CPU limit in the chart: the LimitRange default) + its largest hook, all
// at once while the install's post-install hooks run.
func isoApplicationNeed(t *testing.T, q PlanQuota) map[string]resource.Quantity {
	t.Helper()
	def := limitRangeDefaults(q)
	wv := yamlFile(t, "platform", "wordpress-tenant", "chart", "values.yaml")
	app := shapeAt(t, wv, "wordpress", "resources")
	wpPod := podEffectiveShape([]containerShape{app}, []containerShape{def, def, def}) // its three unsized inits
	dbCPU := def.LimitsCPU
	if _, set := dig(wv, "database", "cluster", "resources", "limits", "cpu"); set {
		dbCPU = scalarAt(t, wv, "database", "cluster", "resources", "limits", "cpu")
	}
	db := containerShape{
		RequestsCPU:    scalarAt(t, wv, "database", "cluster", "resources", "requests", "cpu"),
		RequestsMemory: scalarAt(t, wv, "database", "cluster", "resources", "requests", "memory"),
		LimitsCPU:      dbCPU,
		LimitsMemory:   scalarAt(t, wv, "database", "cluster", "resources", "limits", "memory"),
	}
	dbPod := podEffectiveShape([]containerShape{db}, []containerShape{db})
	// The largest WordPress hook pod: podEffectiveShape of inits alone is
	// their per-resource maximum.
	var hookPods []containerShape
	for _, h := range includedTransients {
		if h.Release == "wordpress-tenant" {
			hookPods = append(hookPods, podEffectiveShape(h.Containers, h.Inits))
		}
	}
	if len(hookPods) == 0 {
		t.Fatal("vacuity: no WordPress hook in includedTransients")
	}
	hook := podEffectiveShape(nil, hookPods)
	out := map[string]resource.Quantity{}
	for k, vs := range map[string][]string{
		"requests.cpu":    {wpPod.RequestsCPU, dbPod.RequestsCPU, hook.RequestsCPU},
		"requests.memory": {wpPod.RequestsMemory, dbPod.RequestsMemory, hook.RequestsMemory},
		"limits.cpu":      {wpPod.LimitsCPU, dbPod.LimitsCPU, hook.LimitsCPU},
		"limits.memory":   {wpPod.LimitsMemory, dbPod.LimitsMemory, hook.LimitsMemory},
	} {
		var s resource.Quantity
		for _, v := range vs {
			s.Add(mustQ(t, k, v))
		}
		out[k] = s
	}
	return out
}

// customerPart is what the rendered plan-quota leaves the customer once the
// given applications and coredns are running (no classless credit).
func customerPart(t *testing.T, slug string) map[string]resource.Quantity {
	t.Helper()
	rq := planQuotaOf(t, slug)
	charge := givenCharge(t, planQuota(slug))
	dns := map[string]resource.Quantity{"requests.cpu": corednsOverhead.RequestsCPU, "requests.memory": corednsOverhead.RequestsMemory, "limits.cpu": corednsOverhead.LimitsCPU, "limits.memory": corednsOverhead.LimitsMemory}
	out := map[string]resource.Quantity{}
	for k, v := range rq.Spec.Hard {
		x := mustQ(t, slug+" "+k, v)
		x.Sub(dns[k])
		x.Sub(charge[k])
		out[k] = x
	}
	return out
}

// TestPlanQuota_AcceptanceApplicationFitsItsPlan: NET-G07 step 9 installs one
// bp-wordpress-tenant in Organization A. It needs 250m requests.cpu at its
// install on every plan (WordPress 100m + its database 100m + a hook 50m,
// platform/wordpress-tenant/chart/values.yaml) — more than plan S's 167m
// guaranteed share as sold, whatever else runs. So the check gives A the
// smallest plan whose customer part holds it, and this test holds both: it
// fits that plan, and no smaller plan.
func TestPlanQuota_AcceptanceApplicationFitsItsPlan(t *testing.T) {
	t.Parallel()
	check := readRepo(t, "tests", "acceptance", "sovereign", "internal", "suite", "check_org_isolation.go")
	if m := isoBlueprintRe.FindStringSubmatch(check); m == nil || m[1] != "bp-wordpress-tenant" {
		t.Fatalf("NET-G07's ISO-9 Blueprint is %v — isoApplicationNeed models bp-wordpress-tenant; re-model it", m)
	}
	m := isoPlanARe.FindStringSubmatch(check)
	if m == nil {
		t.Fatal("check_org_isolation.go names no isoPlanA — the plan of the Organization that installs ISO-9's Application must be one whose customer part holds it")
	}
	plan := m[1]
	if _, ok := planQuotaTable[plan]; !ok {
		t.Fatalf("isoPlanA %q is not a plan", plan)
	}
	fits := func(slug string) (bool, string) {
		need, part := isoApplicationNeed(t, planQuota(slug)), customerPart(t, slug)
		var short []string
		for _, k := range []string{"requests.cpu", "requests.memory", "limits.cpu", "limits.memory"} {
			n, p := need[k], part[k]
			if n.Cmp(p) > 0 {
				short = append(short, k+" "+n.String()+" > "+p.String())
			}
		}
		return len(short) == 0, strings.Join(short, ", ")
	}
	if ok, why := fits(plan); !ok {
		t.Errorf("plan %s: ISO-9's Application does not fit the customer's part: %s", plan, why)
	}
	slugs := make([]string, 0, len(planQuotaTable))
	for s := range planQuotaTable {
		slugs = append(slugs, s)
	}
	sort.Slice(slugs, func(i, j int) bool {
		a, b := mustQ(t, "", planQuotaTable[slugs[i]].CPURequest), mustQ(t, "", planQuotaTable[slugs[j]].CPURequest)
		return a.Cmp(b) < 0
	})
	for _, s := range slugs {
		if s == plan {
			break
		}
		if ok, _ := fits(s); ok {
			t.Errorf("ISO-9's Application fits plan %s, smaller than isoPlanA %s — give A the smallest plan that holds it", s, plan)
		}
	}
}
