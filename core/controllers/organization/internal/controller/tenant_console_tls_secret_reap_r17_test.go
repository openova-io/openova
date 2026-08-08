package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// UAT R17 — the TLS Secret behind the per-Org wildcard Certificate does NOT
// follow the Certificate on delete, so teardown has to remove it explicitly.
//
// tenant_networking_teardown.go's header asserted the Secret was
// "cert-manager-GC'd with it". That holds only when cert-manager runs with
// `--enable-certificate-owner-ref`, which stamps an ownerRef from Secret to
// Certificate. The flag is OFF by default and absent from the cert-manager
// Deployment args on hw292, so the Secret is left with no owner and nothing
// collects it.
//
// Measured on hw292 2026-08-08, Org r17probe deleted 4d21h earlier:
//
//	Certificate org-wildcard-tls-r17probe-omani-homes -> 0  (teardown worked)
//	Secret      org-wildcard-tls-r17probe-omani-homes -> 1  (survived)
//
// with a live-Org control still showing both, so the orphan was real and not an
// empty-filter artefact.
//
// Why it is worse than litter: the Secret name is DERIVED from slug+parent, so a
// future Org taking that subdomain finds a Secret of exactly the expected name
// holding the PREVIOUS Org's certificate — a cross-Org identity leak.
func TestDeleteOrgWildcardTLSSecret_ReapsTheOrphan_R17(t *testing.T) {
	const ns, name = "kube-system", "org-wildcard-tls-r17probe-omani-homes"

	// A second Secret that must SURVIVE. Without it this test would pass against
	// an implementation that deletes indiscriminately, which is the failure mode
	// that matters most for a teardown path shared by every Org.
	other := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: "org-wildcard-tls-uatco-omani-homes",
	}}
	target := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}

	cl := fake.NewClientBuilder().WithObjects(target, other).Build()
	r := &Reconciler{Client: cl}

	// PRE-CONDITION control: the target must exist before the reap, otherwise a
	// "not found afterwards" assertion proves nothing at all.
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &corev1.Secret{}); err != nil {
		t.Fatalf("precondition: target Secret should exist before teardown, got %v", err)
	}

	changed, err := r.deleteOrgWildcardTLSSecret(context.Background(), orgConsoleTLSNames{CertName: name})
	if err != nil {
		t.Fatalf("deleteOrgWildcardTLSSecret: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when a Secret was actually deleted — the caller ORs this into " +
			"its return, and a false here would report a clean teardown that removed nothing")
	}

	err = cl.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &corev1.Secret{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("orphaned TLS Secret survived teardown (this is the R17 leak): err=%v", err)
	}

	// The other Org's Secret is untouched.
	if err := cl.Get(context.Background(), types.NamespacedName{
		Namespace: ns, Name: "org-wildcard-tls-uatco-omani-homes",
	}, &corev1.Secret{}); err != nil {
		t.Errorf("teardown deleted ANOTHER Org's TLS Secret — blast radius bug, far worse than the "+
			"orphan it fixes: %v", err)
	}
}

// Absent-as-success: every helper in this teardown chain is idempotent, so a
// re-reconcile of an already-clean Org must be a silent no-op rather than an
// error that requeues forever.
func TestDeleteOrgWildcardTLSSecret_AbsentIsSuccess_R17(t *testing.T) {
	cl := fake.NewClientBuilder().Build()
	r := &Reconciler{Client: cl}

	changed, err := r.deleteOrgWildcardTLSSecret(context.Background(),
		orgConsoleTLSNames{CertName: "org-wildcard-tls-gone-omani-homes"})
	if err != nil {
		t.Fatalf("absent Secret must be success, got %v", err)
	}
	if changed {
		t.Error("expected changed=false when nothing was deleted — a true here would make every " +
			"re-reconcile of a clean Org report a mutation it did not make")
	}
}
