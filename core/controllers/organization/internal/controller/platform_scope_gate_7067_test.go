package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openova-io/openova/core/controllers/organization/internal/gitops"
)

// The #7067 rollout gate end to end through Reconcile: the committed
// plan-quota is held UNSCOPED while a platform pod in the Org namespace still
// lacks the platform PriorityClass, and scoped once none does. A customer pod
// without the class never holds the gate; a finished classless hook pod does
// not either.
func TestReconcile_7067_PlanQuotaScopedOnlyOnceEveryPlatformPodCarriesTheClass(t *testing.T) {
	t.Parallel()
	const rqKey = "acme/catalyst-tenant/vcluster/resourcequota.yaml"
	pod := func(name string, labels map[string]string, class string, phase corev1.PodPhase) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "acme", Labels: labels},
			Spec:       corev1.PodSpec{PriorityClassName: class, Containers: []corev1.Container{{Name: "c", Image: "x"}}},
			Status:     corev1.PodStatus{Phase: phase},
		}
	}
	keycloak := map[string]string{"app.kubernetes.io/instance": "bp-keycloak", "app.kubernetes.io/name": "keycloak"}
	stalwart := map[string]string{"app.kubernetes.io/instance": "bp-stalwart-tenant"}
	hook := map[string]string{"app.kubernetes.io/name": "newapi-db-dsn-sync"}
	cls := gitops.OrgPlatformPriorityClassName

	for _, c := range []struct {
		name    string
		pods    []client.Object
		pending bool
	}{
		{"fresh namespace, no pods", nil, false},
		{"classless keycloak admitted before the Policy", []client.Object{pod("bp-keycloak-0", keycloak, "", corev1.PodRunning)}, true},
		{"keycloak restarted with the class", []client.Object{pod("bp-keycloak-0", keycloak, cls, corev1.PodRunning)}, false},
		{"classless customer pod is the customer's", []client.Object{pod("bp-stalwart-tenant-0", stalwart, "", corev1.PodRunning), pod("bp-keycloak-0", keycloak, cls, corev1.PodRunning)}, false},
		{"finished classless hook pod is charged to nothing", []client.Object{pod("dsn-sync-x", hook, "", corev1.PodSucceeded), pod("bp-keycloak-0", keycloak, cls, corev1.PodRunning)}, false},
	} {
		objs := append([]client.Object{sampleOrg()}, c.pods...)
		r, gs, _ := makeReconciler(t, objs...)
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "acme"}}); err != nil {
			t.Fatalf("%s: reconcile: %v", c.name, err)
		}
		f, ok := gs.files[rqKey]
		if !ok {
			t.Fatalf("%s: controller did not commit %s", c.name, rqKey)
		}
		rq := string(f.content)
		scoped := strings.Contains(rq, "scopeSelector:")
		if scoped == c.pending {
			t.Errorf("%s: plan-quota scoped=%v, want scoped=%v:\n%s", c.name, scoped, !c.pending, rq)
		}
		if c.pending && !strings.Contains(rq, "openova.io/quota-scope: transitional") {
			t.Errorf("%s: pending plan-quota not labelled transitional", c.name)
		}
	}
}
