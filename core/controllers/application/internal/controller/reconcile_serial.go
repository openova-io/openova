package controller

import (
	"context"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// keyedLocks hands out one mutex per Application key.
type keyedLocks struct{ m sync.Map }

func (k *keyedLocks) lock(key string) func() {
	v, _ := k.m.LoadOrStore(key, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// reconcileLatest reconciles the Application as it is NOW, one reconcile per
// Application at a time.
//
// The watch loop and the periodic relist both drive Reconcile. The relist
// reconciles a List snapshot; when the Application was deleted after that
// List, the snapshot carries no deletionTimestamp and its reconcile
// re-committed the placement files the delete path had just removed. The
// Application then went away and its HelmReleases stayed declared in Git for
// good (hw307 acceptance runs 1794377153 and 1794386610: "remove … from every
// region" followed by "placement … (gen 1)" in the same second; Refs #7067).
// Holding the per-key lock and re-reading the object closes that window: a
// reconcile can only act on state at least as new as the last one's.
func (r *Reconciler) reconcileLatest(ctx context.Context, obj *unstructured.Unstructured) error {
	unlock := r.keyLocks.lock(obj.GetNamespace() + "/" + obj.GetName())
	defer unlock()
	cur, err := r.Dynamic.Resource(ApplicationGVR).Namespace(obj.GetNamespace()).
		Get(ctx, obj.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.Reconcile(ctx, cur)
}
