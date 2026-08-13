// podresolve.go — turn the pod segment of an exec URL into the name of
// a Pod that exists RIGHT NOW.
//
// WHY THIS EXISTS (#5991 / UAT row 115). A Guacamole connection is a
// row in a database. Its parameters are written once and read on every
// click, and one of them — for guacd's `kubernetes` protocol — is a
// literal Pod name. Pod names for a Deployment or DaemonSet carry a
// generated suffix and change on every rollout, restart and eviction,
// so a connection that names a Pod literally is a connection that works
// until the next reconcile and then 404s forever. Seeding one of those
// would satisfy row 115's "the list is non-empty" clause while leaving
// the feature broken on click, which is the exact vacuity the row's own
// guard is written against.
//
// So the pod segment may also name a WORKLOAD, resolved at request time
// against a single label. It is OFF unless the operator sets
// POD_ALIAS_LABEL: with the label unset the resolver is nil and the pod
// segment is used verbatim, byte-identical to the pre-#5991 proxy.
//
// The order is literal-first and the fallback is NOT open:
//
//  1. GET the Pod by that exact name. Found ⇒ use it. This keeps every
//     existing caller (catalyst-api names a real Pod) on the same code
//     path it always had, and costs one cached apiserver read.
//  2. Only on NotFound, LIST Pods in the namespace with
//     <POD_ALIAS_LABEL>=<segment>. Zero matches ⇒ the request fails.
//     There is no "pick something reasonable" branch.
//  3. Among matches, prefer a Running Pod on THIS node (the proxy is a
//     DaemonSet and the Service in front of it is
//     internalTrafficPolicy: Local, so the node-local Pod is the one
//     whose exec stream never leaves the node), then fall back to any
//     Running Pod, choosing by name so two proxies on two nodes make
//     the same choice for the same input.
package proxy

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// PodResolver maps (namespace, segment) to a concrete Pod name.
// Interface rather than a concrete type so the handler test can pin the
// CALL SITE — that the handler actually consults the resolver and
// actually dials the name it returns — without a live apiserver.
type PodResolver interface {
	Resolve(ctx context.Context, namespace, segment string) (string, error)
}

// ErrNoPodForAlias is returned when the segment names neither an
// existing Pod nor any Pod carrying the alias label. It is a hard
// failure: the caller maps it to 404 rather than dialing a guess.
var ErrNoPodForAlias = fmt.Errorf("no pod matches")

// LabelPodResolver implements PodResolver against the apiserver.
type LabelPodResolver struct {
	Client kubernetes.Interface

	// AliasLabel is the single label key consulted when the literal
	// lookup misses. Empty means the alias leg is off — construct via
	// NewLabelPodResolver, which returns nil in that case so the
	// handler skips resolution entirely.
	AliasLabel string

	// NodeName is this proxy Pod's node, from the downward API. Empty
	// simply disables the node-locality preference; it never changes
	// whether a Pod is eligible.
	NodeName string
}

// NewLabelPodResolver returns nil when aliasLabel is empty — the
// nil-resolver case is the pre-#5991 behaviour and the handler treats
// it as "use the segment verbatim, make no apiserver call".
func NewLabelPodResolver(client kubernetes.Interface, aliasLabel, nodeName string) PodResolver {
	if aliasLabel == "" || client == nil {
		return nil
	}
	return &LabelPodResolver{Client: client, AliasLabel: aliasLabel, NodeName: nodeName}
}

// Resolve implements PodResolver.
func (r *LabelPodResolver) Resolve(ctx context.Context, namespace, segment string) (string, error) {
	if _, err := r.Client.CoreV1().Pods(namespace).Get(ctx, segment, metav1.GetOptions{}); err == nil {
		return segment, nil
	} else if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("get pod %s/%s: %w", namespace, segment, err)
	}

	list, err := r.Client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", r.AliasLabel, segment),
	})
	if err != nil {
		return "", fmt.Errorf("list pods %s by %s=%s: %w", namespace, r.AliasLabel, segment, err)
	}
	name := pickPod(list.Items, r.NodeName)
	if name == "" {
		return "", fmt.Errorf("%w: %s/%s is not a pod and no Running pod carries %s=%s",
			ErrNoPodForAlias, namespace, segment, r.AliasLabel, segment)
	}
	return name, nil
}

// pickPod applies the deterministic preference: Running only,
// node-local first, then lowest name. Split out so the ordering rule is
// testable without a fake clientset.
func pickPod(pods []corev1.Pod, nodeName string) string {
	var local, any []string
	for _, p := range pods {
		if p.Status.Phase != corev1.PodRunning || p.DeletionTimestamp != nil {
			continue
		}
		if nodeName != "" && p.Spec.NodeName == nodeName {
			local = append(local, p.Name)
			continue
		}
		any = append(any, p.Name)
	}
	if len(local) > 0 {
		sort.Strings(local)
		return local[0]
	}
	if len(any) > 0 {
		sort.Strings(any)
		return any[0]
	}
	return ""
}
