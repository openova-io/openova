// Package orglisteners holds the catalyst-api's read-modify-write of an
// Organization's listeners on one region's console Gateway (NET-195): its TLS
// passthrough listeners, merged as the organization-controller merges them
// (core/controllers/pkg/orglistener), and the removal of every listener of an
// Organization that is gone. The pair itself is server-side applied by
// handler/org_console_tls.go. These writes use Update because server-side
// apply cannot prune list entries another field manager owns, and the
// organization-controller writes its listeners by Update.
package orglisteners

import (
	"context"
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/core/controllers/pkg/orglistener"
)

var gatewayGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}

// A passthrough listener rides the port of the Organization's HTTPS listener,
// which is the port of the apex HTTPS listener (handler
// consoleApexListenerHTTPSName, fallback consoleListenerHTTPSPortFallback).
const (
	apexHTTPS     = "console-https"
	httpsFallback = int64(8443)
)

// Gateway is one region's console Gateway.
type Gateway struct {
	Dyn             dynamic.Interface
	Namespace, Name string
}

// EnsurePassthrough merges the passthrough listeners of the Organization CR org
// (spec.tenantPublic.passthroughHosts; hosts <label>.<zone>) into each Gateway.
// A host list that cannot be read writes nothing.
func EnsurePassthrough(ctx context.Context, org *unstructured.Unstructured, slug, zone, consoleRouteNamespace string, gws []Gateway) error {
	labels, _, err := unstructured.NestedStringSlice(org.Object, "spec", "tenantPublic", "passthroughHosts")
	if err != nil {
		return err
	}
	var errs []error
	for _, g := range gws {
		_, err := g.update(ctx, func(ls []any) ([]any, bool) {
			return orglistener.MergePassthrough(ls, slug,
				orglistener.PassthroughListeners(slug, zone, labels, apexHTTPSPort(ls), consoleRouteNamespace))
		})
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// RemoveOrg strips console-https-<slug>, console-http-<slug> and every
// passthrough listener of slug, and keeps every other listener as it is.
// Absent-as-success.
func (g Gateway) RemoveOrg(ctx context.Context, slug string) (bool, error) {
	return g.update(ctx, func(ls []any) ([]any, bool) {
		kept, removed := make([]any, 0, len(ls)), false
		for _, l := range ls {
			if m, ok := l.(map[string]any); ok && (m["name"] == "console-https-"+slug || m["name"] == "console-http-"+slug) {
				removed = true
				continue
			}
			kept = append(kept, l)
		}
		kept, passthrough := orglistener.MergePassthrough(kept, slug, nil)
		return kept, removed || passthrough
	})
}

func apexHTTPSPort(ls []any) int64 {
	for _, l := range ls {
		if m, ok := l.(map[string]any); ok && m["name"] == apexHTTPS {
			if p, ok := m["port"].(int64); ok {
				return p
			}
		}
	}
	return httpsFallback
}

// update reads the Gateway, merges its listeners and writes it back only when
// they changed. A missing Gateway is no change.
func (g Gateway) update(ctx context.Context, merge func([]any) ([]any, bool)) (bool, error) {
	gw, err := g.Dyn.Resource(gatewayGVR).Namespace(g.Namespace).Get(ctx, g.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ls, found, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if err != nil || !found {
		return false, err
	}
	out, changed := merge(ls)
	if !changed {
		return false, nil
	}
	if err := unstructured.SetNestedSlice(gw.Object, out, "spec", "listeners"); err != nil {
		return false, err
	}
	if _, err := g.Dyn.Resource(gatewayGVR).Namespace(g.Namespace).Update(ctx, gw, metav1.UpdateOptions{}); err != nil {
		return false, err
	}
	return true, nil
}
