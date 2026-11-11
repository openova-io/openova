package controller

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/core/controllers/organization/internal/gitops"
)

// The classless platform credit (#7067), driven with the LIVE hw307
// acmewalk307 namespace as it stood after #7078 (read-only kubectl,
// 2026-11-08): every platform pod admitted before the Policy, so classless —
// requests 3805m / 6736Mi — plus the customer's purchase sized before #7078 and
// a synced bookstack pod — 1400m / 3072Mi — for 5205m / 9808Mi used, against a
// transitional unscoped plan-quota of 5479m / 9286Mi (memory already over),
// with the keycloak-config-cli hook refused.

type acmePod struct {
	name                           string
	labels                         map[string]string
	reqCPU, reqMem, limCPU, limMem string
	class                          string
	phase                          corev1.PodPhase
}

func (p acmePod) obj() *corev1.Pod {
	phase := p.phase
	if phase == "" {
		phase = corev1.PodRunning
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: p.name, Namespace: "acme", Labels: p.labels},
		Spec: corev1.PodSpec{PriorityClassName: p.class, Containers: []corev1.Container{{Name: "c", Image: "x",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(p.reqCPU), corev1.ResourceMemory: resource.MustParse(p.reqMem)},
				Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(p.limCPU), corev1.ResourceMemory: resource.MustParse(p.limMem)},
			}}}},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func lbl(kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

// acmewalk307Platform / acmewalk307Customer are the live pod set.
func acmewalk307Platform() []acmePod {
	return []acmePod{
		{"vcluster-0", lbl("app", "vcluster"), "500m", "1Gi", "500m", "1Gi", "", ""},
		{"bp-keycloak-0", lbl("app.kubernetes.io/instance", "bp-keycloak", "app.kubernetes.io/name", "keycloak"), "1", "2Gi", "1", "2Gi", "", ""},
		{"bp-keycloak-postgresql-0", lbl("app.kubernetes.io/instance", "bp-keycloak", "app.kubernetes.io/name", "postgresql"), "500m", "512Mi", "500m", "512Mi", "", ""},
		{"bp-newapi-newapi-pg-1", lbl("cnpg.io/cluster", "bp-newapi-newapi-pg"), "500m", "512Mi", "500m", "512Mi", "", ""},
		{"bp-openclaw-controller-x", lbl("app.kubernetes.io/instance", "bp-openclaw", "app.kubernetes.io/name", "bp-openclaw"), "250m", "512Mi", "250m", "512Mi", "", ""},
		{"bp-agenity-0", lbl("app.kubernetes.io/instance", "bp-agenity", "app.kubernetes.io/name", "bp-agenity"), "1005m", "2064Mi", "1050m", "2112Mi", "", corev1.PodPending},
		{"oidc-gate-agenity-x", lbl("app.kubernetes.io/name", "bp-oidc-gate"), "50m", "64Mi", "50m", "64Mi", "", ""},
	}
}

func acmewalk307Customer() []acmePod {
	return []acmePod{
		{"bookstack-x-acmewalk307-x-vcluster", lbl("app", "bookstack", "vcluster.loft.sh/managed-by", "vcluster"), "100m", "256Mi", "100m", "256Mi", "", ""},
		{"bp-stalwart-tenant-0", lbl("app.kubernetes.io/instance", "bp-stalwart-tenant"), "500m", "1Gi", "500m", "1Gi", "", ""},
		{"bp-stalwart-tenant-webmail-x", lbl("app.kubernetes.io/instance", "bp-stalwart-tenant"), "500m", "1Gi", "500m", "1Gi", "", ""},
		{"bp-wordpress-tenant-x", lbl("app.kubernetes.io/instance", "bp-wordpress-tenant"), "100m", "256Mi", "500m", "512Mi", "", ""},
		{"wordpress-db-1", lbl("cnpg.io/cluster", "wordpress-db"), "100m", "256Mi", "500m", "512Mi", "", ""},
		{"wordpress-db-replica-1-pgbasebackup-x", lbl("cnpg.io/cluster", "wordpress-db-replica"), "100m", "256Mi", "250m", "512Mi", "", corev1.PodPending},
		{"bp-stalwart-tenant-setup-x", lbl("app.kubernetes.io/instance", "bp-stalwart-tenant"), "100m", "64Mi", "100m", "64Mi", "", corev1.PodSucceeded},
	}
}

// liveTransitionalPlanQuota is the plan-quota #7078 left on acmewalk307.
func liveTransitionalPlanQuota() *corev1.ResourceQuota {
	return &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: gitops.BoundaryResourceQuotaName, Namespace: "acme"},
		Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{
			"requests.cpu": resource.MustParse("5479m"), "requests.memory": resource.MustParse("9286Mi"),
			"limits.cpu": resource.MustParse("9500m"), "limits.memory": resource.MustParse("14122Mi"),
		}},
	}
}

type renderedQuota struct {
	Spec struct {
		ScopeSelector *struct {
			MatchExpressions []struct {
				Operator string   `json:"operator"`
				Values   []string `json:"values"`
			} `json:"matchExpressions"`
		} `json:"scopeSelector"`
		Hard map[string]string `json:"hard"`
	} `json:"spec"`
}

// reconcileQuotas runs one Reconcile over the pod set and returns the
// committed plan-quota and platform-quota.
func reconcileQuotas(t *testing.T, pods []acmePod, reader client.Reader) (plan, platform renderedQuota) {
	t.Helper()
	objs := []client.Object{sampleOrg(), liveTransitionalPlanQuota()}
	for _, p := range pods {
		objs = append(objs, p.obj())
	}
	r, gs, _ := makeReconciler(t, objs...)
	if reader != nil {
		r.PodReader = reader
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "acme"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	for key, dst := range map[string]*renderedQuota{
		"acme/catalyst-tenant/vcluster/resourcequota.yaml": &plan,
		"acme/catalyst-tenant/vcluster/platformquota.yaml": &platform,
	} {
		f, ok := gs.files[key]
		if !ok {
			t.Fatalf("controller did not commit %s", key)
		}
		if err := yaml.Unmarshal(f.content, dst); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	return plan, platform
}

// charged is what a scoped quota counts from a pod set (non-terminal pods;
// In = the class only, NotIn = everything else) — the ResourceQuota rule.
func charged(pods []acmePod, platformScope bool) map[string]resource.Quantity {
	out := map[string]resource.Quantity{}
	for _, p := range pods {
		if p.phase == corev1.PodSucceeded || p.phase == corev1.PodFailed {
			continue
		}
		if (p.class == gitops.OrgPlatformPriorityClassName) != platformScope {
			continue
		}
		for k, v := range map[string]string{"requests.cpu": p.reqCPU, "requests.memory": p.reqMem, "limits.cpu": p.limCPU, "limits.memory": p.limMem} {
			q := out[k]
			q.Add(resource.MustParse(v))
			out[k] = q
		}
	}
	return out
}

// admits reports whether quota q (scope In or NotIn) admits pod p given the
// pods already charged to it — the ResourceQuota admission check.
func admits(t *testing.T, q renderedQuota, platformScope bool, existing []acmePod, p acmePod) (bool, string) {
	t.Helper()
	used := charged(existing, platformScope)
	for k, v := range map[string]string{"requests.cpu": p.reqCPU, "requests.memory": p.reqMem, "limits.cpu": p.limCPU, "limits.memory": p.limMem} {
		total := used[k]
		total.Add(resource.MustParse(v))
		hard := resource.MustParse(q.Spec.Hard[k])
		if total.Cmp(hard) > 0 {
			return false, k + " " + total.String() + " > " + hard.String()
		}
	}
	return true, ""
}

func scopedNotIn(q renderedQuota) bool {
	return q.Spec.ScopeSelector != nil && len(q.Spec.ScopeSelector.MatchExpressions) == 1 &&
		q.Spec.ScopeSelector.MatchExpressions[0].Operator == "NotIn" &&
		len(q.Spec.ScopeSelector.MatchExpressions[0].Values) == 1 &&
		q.Spec.ScopeSelector.MatchExpressions[0].Values[0] == gitops.OrgPlatformPriorityClassName
}

func TestReconcile_7067_ClasslessCredit_UnwedgesAcmewalk307(t *testing.T) {
	t.Parallel()
	cls := gitops.OrgPlatformPriorityClassName
	platform, customer := acmewalk307Platform(), acmewalk307Customer()
	all := append(append([]acmePod(nil), platform...), customer...)

	// Vacuity: the fixture IS the live namespace — 5205m / 9808Mi used.
	tot := charged(all, false)
	if c, m := tot["requests.cpu"], tot["requests.memory"]; c.Cmp(resource.MustParse("5205m")) != 0 || m.Cmp(resource.MustParse("9808Mi")) != 0 {
		t.Fatalf("fixture used = %s / %s, want the live 5205m / 9808Mi", c.String(), m.String())
	}

	plan, plat := reconcileQuotas(t, all, nil)
	if !scopedNotIn(plan) {
		t.Fatalf("plan-quota is not scoped NotIn %s on the first render: %+v", cls, plan.Spec.ScopeSelector)
	}
	// hard = plan m (334m/1366Mi, 2/4Gi) + coredns (20m/64Mi, 1/170Mi) +
	// credit (the 7 classless platform pods incl. the Pending agenity:
	// 3805m/6736Mi requests, 3850m/6784Mi limits).
	for k, w := range map[string]string{"requests.cpu": "4159m", "requests.memory": "8166Mi", "limits.cpu": "6850m", "limits.memory": "11050Mi"} {
		if got := resource.MustParse(plan.Spec.Hard[k]); got.Cmp(resource.MustParse(w)) != 0 {
			t.Errorf("first render: plan-quota %s = %s, want %s (plan + coredns + classless credit)", k, plan.Spec.Hard[k], w)
		}
	}

	// (a) The keycloak-config-cli hook is created with the class (Kyverno),
	// so it is charged to platform-quota, which charges no classless pod.
	hook := acmePod{"bp-keycloak-keycloak-config-cli-x", lbl("app.kubernetes.io/instance", "bp-keycloak"), "250m", "256Mi", "250m", "256Mi", cls, ""}
	if ok, why := admits(t, plat, true, all, hook); !ok {
		t.Errorf("(a) the hook is refused by platform-quota on the first render: %s", why)
	}

	// The customer is over its plan with pods sized before #7078: 1400m /
	// 3072Mi used against 354m / 1430Mi + 0 credit. Existing pods keep
	// running; a NEW customer pod is refused — correct, and only that.
	if ok, _ := admits(t, plan, false, all, acmePod{"new-customer", lbl("app", "x"), "10m", "16Mi", "10m", "16Mi", "", ""}); ok {
		t.Errorf("a new customer pod fits although the customer is over its plan — the credit is crediting customer pods")
	}

	// (b) + (c) Replace the classless platform pods one by one (each restart
	// is stamped by Kyverno and charged to platform-quota), reconciling after
	// each: every recreated pod is admitted, and the credit decays to zero.
	cur := append([]acmePod(nil), all...)
	prevCPU := resource.MustParse(plan.Spec.Hard["requests.cpu"])
	for i := range platform {
		p := platform[i]
		// delete the classless pod, then admit its stamped replacement
		next := []acmePod{}
		for _, x := range cur {
			if x.name != p.name {
				next = append(next, x)
			}
		}
		repl := p
		repl.class = cls
		repl.phase = ""
		if ok, why := admits(t, plat, true, next, repl); !ok {
			t.Fatalf("(b) restarting %s: the stamped replacement is refused by platform-quota: %s", p.name, why)
		}
		cur = append(next, repl)
		plan, plat = reconcileQuotas(t, cur, nil)
		if !scopedNotIn(plan) {
			t.Fatalf("after restarting %s plan-quota is not scoped", p.name)
		}
		got := resource.MustParse(plan.Spec.Hard["requests.cpu"])
		if got.Cmp(prevCPU) >= 0 {
			t.Errorf("(c) after restarting %s the credit did not shrink: requests.cpu %s (was %s)", p.name, got.String(), prevCPU.String())
		}
		prevCPU = got
	}
	for k, w := range map[string]string{"requests.cpu": "354m", "requests.memory": "1430Mi", "limits.cpu": "3", "limits.memory": "4266Mi"} {
		if got := resource.MustParse(plan.Spec.Hard[k]); got.Cmp(resource.MustParse(w)) != 0 {
			t.Errorf("(c) every platform pod stamped: plan-quota %s = %s, want the bare plan + coredns %s", k, plan.Spec.Hard[k], w)
		}
	}
	// The stamped stack plus the hook fits platform-quota.
	if ok, why := admits(t, plat, true, cur, hook); !ok {
		t.Errorf("after the switch the hook does not fit platform-quota: %s", why)
	}
}

// failingReader is a pod reader whose List is forbidden (the read Role has
// not landed yet — the first render after an upgrade).
type failingReader struct{ client.Reader }

func (f failingReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("RBAC: pods is forbidden"))
}

// (d) Pods not listable: never lower than the safe bound — per resource, the
// larger of the live hard and plan + coredns + the whole modelled stack.
func TestReconcile_7067_ClasslessCredit_RBACMissingNeverShrinks(t *testing.T) {
	t.Parallel()
	pods := append(acmewalk307Platform(), acmewalk307Customer()...)
	plan, _ := reconcileQuotas(t, pods, failingReader{})
	if !scopedNotIn(plan) {
		t.Fatalf("plan-quota is not scoped when the credit is unknown: %+v", plan.Spec.ScopeSelector)
	}
	live := liveTransitionalPlanQuota().Spec.Hard
	static := map[string]string{"requests.cpu": "4694m", "requests.memory": "8518Mi", "limits.cpu": "8050m", "limits.memory": "12458Mi"}
	for k, s := range static {
		got := resource.MustParse(plan.Spec.Hard[k])
		l := live[corev1.ResourceName(k)]
		if got.Cmp(resource.MustParse(s)) < 0 || got.Cmp(l) < 0 {
			t.Errorf("(d) unknown credit: plan-quota %s = %s, below the safe bound (static %s, live %s)", k, got.String(), s, l.String())
		}
	}
	// It is no looser than needed: the live hard wins exactly here.
	if plan.Spec.Hard["requests.memory"] != "9286Mi" {
		t.Errorf("(d) unknown credit: requests.memory %s, want the live 9286Mi kept", plan.Spec.Hard["requests.memory"])
	}
	// And the classless platform pods are still credited at least in full:
	// the static stack bound alone covers the 6736Mi they hold.
	if c := resource.MustParse(static["requests.memory"]); c.Cmp(resource.MustParse("1430Mi")) <= 0 {
		t.Fatal("vacuity")
	}
}

// Pod usage follows the ResourceQuota rule: native sidecars add, plain init
// containers compete (with the sidecars started before them), overhead adds.
func TestPodQuotaUsage_SidecarsInitsOverhead(t *testing.T) {
	t.Parallel()
	always := corev1.ContainerRestartPolicyAlways
	rr := func(cpu, mem string) corev1.ResourceRequirements {
		l := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)}
		return corev1.ResourceRequirements{Requests: l, Limits: l}
	}
	p := &corev1.Pod{Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{
			{Name: "bridge", RestartPolicy: &always, Resources: rr("10m", "32Mi")},
			{Name: "wait", Resources: rr("2", "16Mi")}, // big CPU init (+ bridge started before it)
		},
		Containers: []corev1.Container{{Name: "newapi", Resources: rr("500m", "256Mi")}, {Name: "meter", Resources: rr("25m", "64Mi")}},
		Overhead:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5m")},
	}}
	req, _ := podQuotaUsage(p)
	if c := req[corev1.ResourceCPU]; c.Cmp(resource.MustParse("2015m")) != 0 { // max(535m, 2+10m) + 5m
		t.Errorf("cpu = %s, want 2015m", c.String())
	}
	if m := req[corev1.ResourceMemory]; m.Cmp(resource.MustParse("352Mi")) != 0 { // max(352Mi, 48Mi)
		t.Errorf("memory = %s, want 352Mi", m.String())
	}
}
