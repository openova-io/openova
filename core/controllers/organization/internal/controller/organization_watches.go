package controller

// Dependency watches of the Organization reconciler (CTL-420).
//
// The reconciler used to learn that its vCluster HelmRelease turned Ready, or
// that the shared console Gateway lost or admitted a listener, only by asking
// again every 30 seconds. It now watches both kinds. A change that matters
// enqueues the Organization at once; the requeue in Reconcile stays only as a
// safety net of ten minutes.
//
//   - HelmRelease: every HelmRelease labelled openova.io/organization=<slug>
//     enqueues the Organization <slug>. The vCluster HelmRelease the
//     controller renders (gitops.vclusterTemplate) carries that label.
//   - Gateway: the console Gateway is shared by every Organization and carries
//     no Organization label, and a listener that was rewritten away is no
//     longer on it to name its owner. So a change of the console Gateway (the
//     name and namespace the reconciler is configured with) enqueues every
//     Organization that engages tenant networking (a pool parentDomain). Any
//     other Gateway enqueues nothing.
//
// Both watches pass an update only when the object's readiness changed: the
// status or the observedGeneration of its Ready or Programmed condition, or
// its status.observedGeneration. A create or a delete always passes.
//
// Only the cluster this controller runs in is watched. The console Gateways
// of the other regions are read through their own clients and are still
// covered by the ten-minute safety net.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// OrganizationLabel names the Organization an object was rendered for. Every
// object the controller renders for an Organization carries it.
const OrganizationLabel = "openova.io/organization"

// helmReleaseGVK is the Flux HelmRelease kind the reconciler reads back
// (vclusterReadiness) and watches.
var helmReleaseGVK = schema.GroupVersionKind{
	Group:   "helm.toolkit.fluxcd.io",
	Version: "v2",
	Kind:    "HelmRelease",
}

// watchedConditionTypes are the readiness conditions whose change passes the
// predicate: Ready on a HelmRelease, Programmed on a Gateway.
var watchedConditionTypes = []string{"Ready", "Programmed"}

// withDependencyWatches adds the HelmRelease and Gateway watches to the
// builder of the Organization controller. SetupWithManager calls it.
func (r *Reconciler) withDependencyWatches(b *builder.Builder) *builder.Builder {
	return b.
		Watches(watchObject(helmReleaseGVK),
			handler.EnqueueRequestsFromMapFunc(organizationForLabelledObject),
			builder.WithPredicates(readinessChanged())).
		Watches(watchObject(gatewayGVK),
			handler.EnqueueRequestsFromMapFunc(r.organizationsForConsoleGateway),
			builder.WithPredicates(readinessChanged()))
}

// watchObject is an empty unstructured object of the given kind, the form in
// which the controller reads both watched kinds.
func watchObject(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	return u
}

// organizationForLabelledObject maps an object labelled
// openova.io/organization=<slug> to a request for the Organization <slug>.
// An object without the label maps to nothing.
func organizationForLabelledObject(_ context.Context, obj client.Object) []reconcile.Request {
	slug := strings.TrimSpace(obj.GetLabels()[OrganizationLabel])
	if slug == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: slug}}}
}

// organizationsForConsoleGateway maps the console Gateway to every
// Organization that engages tenant networking, sorted by name. Any other
// Gateway maps to nothing. A failed list maps to nothing and is logged: the
// safety net requeue still reaches every Organization.
func (r *Reconciler) organizationsForConsoleGateway(ctx context.Context, obj client.Object) []reconcile.Request {
	if obj.GetNamespace() != r.consoleGatewayNamespace() || obj.GetName() != r.consoleGatewayName() {
		return nil
	}
	var orgs orgapi.OrganizationList
	if err := r.List(ctx, &orgs); err != nil {
		r.Log.Error(err, "list organizations for a console Gateway change")
		return nil
	}
	out := make([]reconcile.Request, 0, len(orgs.Items))
	for i := range orgs.Items {
		if orgEngagesTenantNetworking(&orgs.Items[i]) {
			out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Name: orgs.Items[i].Name}})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// readinessChanged passes every create and delete, and an update only when
// readinessSignature differs between the old and the new object.
func readinessChanged() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool { return true },
		DeleteFunc: func(event.DeleteEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return true
			}
			return readinessSignature(e.ObjectOld) != readinessSignature(e.ObjectNew)
		},
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// readinessSignature is the part of an object that the predicate compares:
// status.observedGeneration, then the status and the observedGeneration of
// each watched condition. An object that is not unstructured has none.
func readinessSignature(obj client.Object) string {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return ""
	}
	var b strings.Builder
	observed, _, _ := unstructured.NestedFieldNoCopy(u.Object, "status", "observedGeneration")
	b.WriteString(fieldText(observed))
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, want := range watchedConditionTypes {
		b.WriteString("|" + want + "=")
		for _, ci := range conds {
			c, ok := ci.(map[string]any)
			if !ok || c["type"] != want {
				continue
			}
			b.WriteString(fieldText(c["status"]) + "/" + fieldText(c["observedGeneration"]))
			break
		}
	}
	return b.String()
}

// fieldText renders a status field for readinessSignature; an absent field is
// the empty string.
func fieldText(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
