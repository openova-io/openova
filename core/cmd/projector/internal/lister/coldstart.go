// Package lister performs a cold-start full LIST against the K8s
// apiserver (one cluster) for every kind in the supplied registry,
// then projects each result into Valkey via the projector.Apply
// surface. The cold-start runs once at startup BEFORE the NATS
// consumer is hooked up — newer events on the stream will overwrite
// stale cold-start projections deterministically (last-write-wins on
// the same key).
//
// Why cold-start at all?
//
//   1. The catalyst.events stream has a finite retention (24h
//      default). On a fresh projector replica, replaying the last
//      24h is necessary BUT NOT SUFFICIENT — resources older than
//      24h that haven't changed since are not in the stream.
//   2. A LIST captures the cluster's full current state regardless
//      of how long the resource has lived. Combined with NATS
//      replay catching every event in the retention window, the
//      projector reaches eventual consistency in O(LIST + 24h-replay)
//      time.
//
// The cold-start is bounded: it lists at most one cluster's worth of
// namespaced+cluster-scoped resources, then exits. The continuous
// catch-up is the consumer's job.
package lister

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	pvalkey "github.com/openova-io/openova/core/cmd/projector/internal/valkey"
)

// Kind describes one watched K8s resource type. Matches the structure
// of internal/k8scache/kinds.go's Kind so the cold-start sees the same
// view of "what is canonical".
type Kind struct {
	Name       string
	GVR        schema.GroupVersionResource
	Namespaced bool
}

// ColdStarter performs the full LIST + project sequence. Construct
// with the dynamic client + projector + cluster id; call Run.
type ColdStarter struct {
	Cluster   string
	Dyn       dynamic.Interface
	Projector *pvalkey.Projector
	Logger    *slog.Logger

	// Now is injectable for tests.
	Now func() time.Time
}

// Run performs the cold-start LIST for every kind in `kinds`. Returns
// the count of successfully projected objects + any non-fatal LIST
// errors (unsupported GVR, transient apiserver glitch). Errors are
// logged WARN; the cold-start does NOT halt on a single bad kind.
func (c *ColdStarter) Run(ctx context.Context, kinds []Kind) (int, error) {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Cluster == "" {
		return 0, fmt.Errorf("ColdStarter.Cluster is required")
	}
	if c.Dyn == nil {
		return 0, fmt.Errorf("ColdStarter.Dyn is required")
	}
	if c.Projector == nil {
		return 0, fmt.Errorf("ColdStarter.Projector is required")
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}

	total := 0
	for _, k := range kinds {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}

		var ri dynamic.ResourceInterface
		if k.Namespaced {
			ri = c.Dyn.Resource(k.GVR).Namespace(metav1.NamespaceAll)
		} else {
			ri = c.Dyn.Resource(k.GVR)
		}

		list, err := ri.List(ctx, metav1.ListOptions{})
		if err != nil {
			c.Logger.Warn("cold-start: LIST failed; skipping kind",
				"cluster", c.Cluster, "kind", k.Name, "err", err)
			continue
		}

		for i := range list.Items {
			item := &list.Items[i]
			if err := c.projectOne(ctx, k.Name, item); err != nil {
				c.Logger.Warn("cold-start: project failed",
					"cluster", c.Cluster, "kind", k.Name,
					"name", item.GetName(), "err", err)
				continue
			}
			total++
		}
		c.Logger.Info("cold-start: kind complete",
			"cluster", c.Cluster, "kind", k.Name, "items", len(list.Items))
	}
	return total, nil
}

// projectOne marshals a single unstructured object into the canonical
// projector.Event wire shape, then calls Apply.
func (c *ColdStarter) projectOne(ctx context.Context, kind string, u *unstructured.Unstructured) error {
	envelope := map[string]any{
		"cluster": c.Cluster,
		"kind":    kind,
		"type":    string(pvalkey.EventAdded),
		"object":  u.Object,
		"at":      c.Now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	ev := pvalkey.Event{
		Cluster:  c.Cluster,
		Kind:     kind,
		Type:     pvalkey.EventAdded,
		RawBytes: raw,
		At:       c.Now(),
	}
	ev.Object.Metadata.Name = u.GetName()
	ev.Object.Metadata.Namespace = u.GetNamespace()
	_, err = c.Projector.Apply(ctx, ev)
	return err
}

// DefaultKinds is the projector's bootstrap kind set. Mirrors a
// reasonable subset of internal/k8scache/kinds.go's DefaultKinds —
// the operator overrides via env if a Sovereign needs additional
// kinds (e.g. flux helmreleases). Production wires from the
// catalyst-api ConfigMap.
var DefaultKinds = []Kind{
	{Name: "pod", GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}, Namespaced: true},
	{Name: "service", GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}, Namespaced: true},
	{Name: "deployment", GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Namespaced: true},
	{Name: "statefulset", GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, Namespaced: true},
	{Name: "configmap", GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}, Namespaced: true},
	{Name: "secret", GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}, Namespaced: true},
	{Name: "node", GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "nodes"}, Namespaced: false},
	{Name: "namespace", GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}, Namespaced: false},
}
