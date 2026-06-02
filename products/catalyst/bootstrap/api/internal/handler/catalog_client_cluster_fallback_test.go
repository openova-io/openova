// Package handler — tests for the chainedCatalogClient fallback
// pattern. Pins the contract change shipped in PR #2880 (#2879):
// fallback fires on ANY upstream error, not just ErrBlueprintNotFound,
// so chart-seeded Blueprint CRs remain reachable when gitea catalog-
// sovereign Org is missing / unreachable / returning 401-502.

package handler

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// stubCatalogClient — minimal upstream CatalogClient mock. getErr
// drives the Get/GetVersion paths; List unused in these tests.
type stubCatalogClient struct {
	getErr error
}

func (s *stubCatalogClient) List(context.Context, string, string) ([]CatalogBlueprint, error) {
	return nil, nil
}

func (s *stubCatalogClient) Get(_ context.Context, _, _ string) (*CatalogBlueprint, error) {
	return nil, s.getErr
}

func (s *stubCatalogClient) GetVersion(_ context.Context, _, _, _ string) (*CatalogBlueprint, error) {
	return nil, s.getErr
}

// fakeBlueprintDynClient returns a dynamic.Interface seeded with one
// Blueprint CR named `name` so the cluster-fallback path can resolve it.
func fakeBlueprintDynClient(t *testing.T, name string) dynamic.Interface {
	t.Helper()
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		blueprintCRGVR():       "BlueprintList",
		blueprintCRGVRAlpha1(): "BlueprintList",
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "catalyst.openova.io", Version: "v1", Kind: "Blueprint",
	})
	obj.SetName(name)
	if err := unstructured.SetNestedField(obj.Object, "1.0.0", "spec", "version"); err != nil {
		t.Fatalf("seed spec.version: %v", err)
	}
	if err := unstructured.SetNestedField(obj.Object, "listed", "spec", "visibility"); err != nil {
		t.Fatalf("seed spec.visibility: %v", err)
	}
	// Minimal card so unstructuredToBlueprint doesn't choke.
	card := map[string]interface{}{"title": name}
	if err := unstructured.SetNestedField(obj.Object, card, "spec", "card"); err != nil {
		t.Fatalf("seed spec.card: %v", err)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, obj)
}

// emptyBlueprintDynClient — fake dynamic with no Blueprint CRs.
// The cluster lookup returns ErrBlueprintNotFound, exercising the
// upstream-error preservation branch.
func emptyBlueprintDynClient() dynamic.Interface {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		blueprintCRGVR():       "BlueprintList",
		blueprintCRGVRAlpha1(): "BlueprintList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
}

func TestChainedCatalogClient_Get_FallbackOnUpstreamNotFound(t *testing.T) {
	// Pre-#2880 behaviour preserved: ErrBlueprintNotFound on upstream
	// triggers the cluster fallback. Cluster has the CR → returned.
	c := &chainedCatalogClient{
		upstream: &stubCatalogClient{getErr: ErrBlueprintNotFound},
		dyn:      fakeBlueprintDynClient(t, "grafana"),
	}
	bp, err := c.Get(context.Background(), "grafana", "")
	if err != nil {
		t.Fatalf("expected fallback to find grafana, got err: %v", err)
	}
	if bp == nil || bp.Name != "grafana" {
		t.Fatalf("expected bp.Name=grafana, got %+v", bp)
	}
}

func TestChainedCatalogClient_Get_FallbackOnUpstreamNon404(t *testing.T) {
	// NEW POST-#2880 BEHAVIOUR: ANY upstream error (not just 404) must
	// trigger the cluster fallback. Pre-fix, the upstream HTTP 502 from
	// gitea catalog-sovereign Org missing on hw86 bypassed the fallback
	// and surfaced as 503 in /catalyst/v1/apps/{uid}/launch-url — the
	// LaunchButton silently fell through to fallbackURL with no
	// prompt=none&kc_idp_hint=catalyst-pin appended.
	upstreamHTTPErr := errors.New("catalog get: upstream 502 user does not exist [uid: 0, name: ]")
	c := &chainedCatalogClient{
		upstream: &stubCatalogClient{getErr: upstreamHTTPErr},
		dyn:      fakeBlueprintDynClient(t, "grafana"),
	}
	bp, err := c.Get(context.Background(), "grafana", "")
	if err != nil {
		t.Fatalf("expected fallback to find grafana despite upstream 502, got err: %v", err)
	}
	if bp == nil || bp.Name != "grafana" {
		t.Fatalf("expected bp.Name=grafana, got %+v", bp)
	}
}

func TestChainedCatalogClient_Get_SurfacesUpstreamWhenClusterAlsoMisses(t *testing.T) {
	// Debuggability check: if upstream had a non-404 error AND cluster
	// also returns ErrBlueprintNotFound, surface the upstream error so
	// the underlying gitea/Org/Network problem stays visible instead of
	// getting masked as a generic "blueprint not found".
	upstreamHTTPErr := errors.New("catalog get: upstream 502 gitea unreachable")
	c := &chainedCatalogClient{
		upstream: &stubCatalogClient{getErr: upstreamHTTPErr},
		dyn:      emptyBlueprintDynClient(),
	}
	_, err := c.Get(context.Background(), "missing-bp", "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, upstreamHTTPErr) && err.Error() != upstreamHTTPErr.Error() {
		t.Fatalf("expected upstream error surfaced when cluster also misses, got: %v", err)
	}
}

func TestChainedCatalogClient_Get_ClusterNotFoundWhenUpstream404(t *testing.T) {
	// When upstream returned ErrBlueprintNotFound AND cluster also has
	// no CR, return ErrBlueprintNotFound (NOT the upstream error) so
	// the install handler's 404 -> 404 mapping stays intact.
	c := &chainedCatalogClient{
		upstream: &stubCatalogClient{getErr: ErrBlueprintNotFound},
		dyn:      emptyBlueprintDynClient(),
	}
	_, err := c.Get(context.Background(), "missing-bp", "")
	if !errors.Is(err, ErrBlueprintNotFound) {
		t.Fatalf("expected ErrBlueprintNotFound when both miss, got: %v", err)
	}
}

func TestChainedCatalogClient_GetVersion_FallbackOnUpstreamNon404(t *testing.T) {
	// Mirror of Get's broadened-fallback behavior on GetVersion.
	upstreamHTTPErr := errors.New("catalog getversion: upstream 502")
	c := &chainedCatalogClient{
		upstream: &stubCatalogClient{getErr: upstreamHTTPErr},
		dyn:      fakeBlueprintDynClient(t, "grafana"),
	}
	bp, err := c.GetVersion(context.Background(), "grafana", "1.0.0", "")
	if err != nil {
		t.Fatalf("expected GetVersion fallback to find grafana, got err: %v", err)
	}
	if bp == nil || bp.Name != "grafana" {
		t.Fatalf("expected bp.Name=grafana, got %+v", bp)
	}
}

// (List path is unchanged by this fix — pre-#2880 listing already
// falls back fully on any upstream failure; see line 109-110 of
// catalog_client_cluster_fallback.go.)

var _ = metav1.ObjectMeta{} // import-pin (unused but documents the dep)
