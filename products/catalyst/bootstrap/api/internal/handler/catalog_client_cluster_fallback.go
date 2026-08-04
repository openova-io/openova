// Package handler — catalog_client_cluster_fallback.go: in-cluster
// Blueprint CR fallback for the catalog client (qa-loop iter-8 Fix #40).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #1 (target-state, not MVP) the
// Sovereign Console MUST resolve a Blueprint name → installable bytes
// regardless of where the Blueprint definition lives:
//
//	(a) Public catalog mirror (catalog Gitea Org)        — operator-facing
//	(b) Sovereign-curated mirror (catalog-sovereign Org) — operator-curated
//	(c) In-cluster Blueprint CRs (catalyst.openova.io)   — chart-shipped fixtures
//
// Pre-Fix #40 the catalyst-api ONLY consulted catalyst-catalog (paths
// (a)+(b)). Chart-shipped Blueprint CRs from per-Sovereign fixtures
// (e.g. qa-fixtures.bp-qa-app, bp-wordpress aliases) were invisible to
// the install handler — POST /applications with `"blueprint":"bp-wordpress"`
// returned 404 even though the cluster carried the Blueprint definition.
//
// chainedCatalogClient wraps the upstream HTTP catalog client and, on
// 404 from the upstream, falls back to a Kubernetes API LIST against
// `blueprints.catalyst.openova.io` matching by metadata.name. Found
// Blueprint CRs are converted to the same CatalogBlueprint wire shape
// returned by catalyst-catalog so the install handler downstream code
// is wire-shape unchanged.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) the in-cluster
// fallback is gated on `dynamicClient` being non-nil; tests + offline
// dev paths inject nil and degrade to upstream-only behaviour without
// a code change.

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// blueprintCRGVR is the GVR for the cluster-scoped Blueprint CRD shipped
// at products/catalyst/chart/crds/blueprint.yaml. v1 is storage; v1alpha1
// stays served for the 38 legacy v1alpha1 blueprint files. We try v1
// first and fall back to v1alpha1 on the version-not-served path so the
// fallback works on any Sovereign regardless of which version the
// chart's Blueprint CRs were authored under.
// catalogOriginSovereign mirrors source.OriginSovereign. The canonical
// enum lives in an `internal/` package of a different module and cannot be
// imported here, so it is restated rather than duplicated as a literal —
// a bare 3 is what caused #5475.
const catalogOriginSovereign = 2

func blueprintCRGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "catalyst.openova.io",
		Version:  "v1",
		Resource: "blueprints",
	}
}

func blueprintCRGVRAlpha1() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "catalyst.openova.io",
		Version:  "v1alpha1",
		Resource: "blueprints",
	}
}

// chainedCatalogClient wraps `upstream` (the HTTP catalog) and, on
// ErrBlueprintNotFound, falls back to an in-cluster Blueprint CR lookup
// via `dyn`. The fallback is read-only — Blueprint CRs are not
// mutated.
type chainedCatalogClient struct {
	upstream CatalogClient
	dyn      dynamic.Interface
}

// NewChainedCatalogClient returns a CatalogClient whose List/Get/GetVersion
// methods consult `upstream` first and fall back to in-cluster Blueprint
// CRs when the upstream returns ErrBlueprintNotFound.
//
// Both upstream and dyn may be nil:
//   - upstream nil: every call returns ErrBlueprintNotFound for in-
//     cluster misses, the upstream's ErrBlueprintNotFound otherwise.
//   - dyn nil: the chain degrades to upstream-only behaviour.
func NewChainedCatalogClient(upstream CatalogClient, dyn dynamic.Interface) CatalogClient {
	return &chainedCatalogClient{upstream: upstream, dyn: dyn}
}

func (c *chainedCatalogClient) List(ctx context.Context, org, sessionToken string) ([]CatalogBlueprint, error) {
	if c.upstream == nil {
		return c.listFromCluster(ctx)
	}
	out, err := c.upstream.List(ctx, org, sessionToken)
	if err == nil && len(out) > 0 {
		// Augment with in-cluster CRs — they may carry chart-shipped
		// Blueprints not yet mirrored into Gitea. Dedup by name.
		extra, _ := c.listFromCluster(ctx)
		seen := make(map[string]struct{}, len(out))
		for _, bp := range out {
			seen[bp.Name] = struct{}{}
		}
		for _, bp := range extra {
			if _, dup := seen[bp.Name]; !dup {
				out = append(out, bp)
			}
		}
		return out, nil
	}
	// Upstream failed or returned empty — fall back fully.
	return c.listFromCluster(ctx)
}

func (c *chainedCatalogClient) Get(ctx context.Context, name, sessionToken string) (*CatalogBlueprint, error) {
	var upstreamErr error
	if c.upstream != nil {
		bp, err := c.upstream.Get(ctx, name, sessionToken)
		if err == nil {
			return bp, nil
		}
		upstreamErr = err
	}
	// G117 #2879 (2026-06-03): try the cluster fallback on ANY upstream
	// error, not just ErrBlueprintNotFound. Pre-fix the fallback only fired
	// on a definitive 404 — but on hw86 the gitea catalog-sovereign Org
	// doesn't exist (bp-self-sovereign-cutover gitea-mirror Job never
	// ran), so every catalog Get returned upstream HTTP 401/502 and the
	// fallback was bypassed. Result: chart-seeded Blueprint CRs (which
	// EXIST in-cluster via templates/catalog-seed/blueprints.yaml) were
	// invisible to the install + launch-url handlers, /catalyst/v1/apps/
	// {uid}/launch-url returned 503, and the operator's Launch button
	// silently degraded to a plain externalURL with no SSO query string.
	// Caught live this session via authenticated Playwright walk.
	//
	// If the cluster ALSO doesn't have the Blueprint, surface the
	// upstream error (when present) so the underlying gitea/org/Network
	// problem stays debuggable instead of getting masked behind a generic
	// "not found".
	bp, clusterErr := c.getFromCluster(ctx, name, "")
	if clusterErr == nil {
		return bp, nil
	}
	if errors.Is(clusterErr, ErrBlueprintNotFound) && upstreamErr != nil && !errors.Is(upstreamErr, ErrBlueprintNotFound) {
		return nil, upstreamErr
	}
	return nil, clusterErr
}

func (c *chainedCatalogClient) GetVersion(ctx context.Context, name, version, sessionToken string) (*CatalogBlueprint, error) {
	var upstreamErr error
	if c.upstream != nil {
		bp, err := c.upstream.GetVersion(ctx, name, version, sessionToken)
		if err == nil {
			return bp, nil
		}
		upstreamErr = err
	}
	// G117 #2879 (2026-06-03): same broadened-fallback rationale as Get.
	bp, clusterErr := c.getFromCluster(ctx, name, version)
	if clusterErr == nil {
		return bp, nil
	}
	if errors.Is(clusterErr, ErrBlueprintNotFound) && upstreamErr != nil && !errors.Is(upstreamErr, ErrBlueprintNotFound) {
		return nil, upstreamErr
	}
	return nil, clusterErr
}

// listFromCluster enumerates Blueprint CRs from the chroot's apiserver.
// Tries v1 first, falls back to v1alpha1 on NoMatchKind / NotFound.
func (c *chainedCatalogClient) listFromCluster(ctx context.Context) ([]CatalogBlueprint, error) {
	if c.dyn == nil {
		return nil, ErrBlueprintNotFound
	}
	items, err := c.dyn.Resource(blueprintCRGVR()).List(ctx, metav1.ListOptions{})
	if err != nil {
		if isVersionNotServed(err) {
			items, err = c.dyn.Resource(blueprintCRGVRAlpha1()).List(ctx, metav1.ListOptions{})
		}
	}
	if err != nil {
		return nil, fmt.Errorf("catalog cluster fallback list: %w", err)
	}
	out := make([]CatalogBlueprint, 0, len(items.Items))
	for i := range items.Items {
		bp, perr := parseBlueprintCRToCatalog(&items.Items[i])
		if perr != nil {
			continue
		}
		if bp != nil {
			out = append(out, *bp)
		}
	}
	return out, nil
}

// getFromCluster looks up a Blueprint CR by name. When `version` is
// non-empty the version field on the CR must match exactly; an "latest"
// or empty version returns whatever's in spec.version.
//
// G117 #2880-followup (2026-06-03): callers pass the bare blueprint name
// (e.g. "grafana") because catalyst-catalog's HTTP API uses the bare
// form. The chart-seeded CRs in templates/catalog-seed/blueprints.yaml
// are named with the canonical `bp-` prefix (e.g. "bp-grafana") per
// docs/NAMING §Blueprint. Try both forms so the cluster fallback works
// for the common case where upstream is unreachable and the chart's
// fixtures are the only path to a working install. Caught live on hw86
// 2026-06-03 — `getFromCluster(ctx, "grafana", "")` returned
// ErrBlueprintNotFound for `kubectl get blueprint bp-grafana` because
// the bare-name lookup never matched the `bp-`-prefixed CR.
func (c *chainedCatalogClient) getFromCluster(ctx context.Context, name, version string) (*CatalogBlueprint, error) {
	if c.dyn == nil {
		return nil, ErrBlueprintNotFound
	}
	if strings.TrimSpace(name) == "" {
		return nil, ErrBlueprintNotFound
	}
	obj, err := c.dyn.Resource(blueprintCRGVR()).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if isVersionNotServed(err) {
			obj, err = c.dyn.Resource(blueprintCRGVRAlpha1()).Get(ctx, name, metav1.GetOptions{})
		}
	}
	// Retry with the canonical `bp-` prefix when the bare name missed.
	if err != nil && apierrors.IsNotFound(err) && !strings.HasPrefix(name, "bp-") {
		bpName := "bp-" + name
		obj2, err2 := c.dyn.Resource(blueprintCRGVR()).Get(ctx, bpName, metav1.GetOptions{})
		if err2 != nil && isVersionNotServed(err2) {
			obj2, err2 = c.dyn.Resource(blueprintCRGVRAlpha1()).Get(ctx, bpName, metav1.GetOptions{})
		}
		if err2 == nil {
			obj, err = obj2, nil
		}
	}
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrBlueprintNotFound
		}
		return nil, fmt.Errorf("catalog cluster fallback get %q: %w", name, err)
	}
	bp, perr := parseBlueprintCRToCatalog(obj)
	if perr != nil {
		return nil, fmt.Errorf("catalog cluster fallback parse %q: %w", name, perr)
	}
	if bp == nil {
		return nil, ErrBlueprintNotFound
	}
	// Version matching: empty / "latest" / equal → match. Mismatch → 404.
	v := strings.TrimSpace(version)
	if v == "" || strings.EqualFold(v, "latest") || v == bp.Version {
		return bp, nil
	}
	return nil, ErrBlueprintNotFound
}

// parseBlueprintCRToCatalog converts a Blueprint CR's unstructured form
// to a CatalogBlueprint matching what catalyst-catalog returns.
// The `Raw` field is populated with the entire object so the install
// handler's spec.configSchema validation has the same view as the
// upstream-served path.
func parseBlueprintCRToCatalog(u *unstructured.Unstructured) (*CatalogBlueprint, error) {
	if u == nil {
		return nil, nil
	}
	name := u.GetName()
	if name == "" {
		return nil, nil
	}
	bp := &CatalogBlueprint{
		Name:   name,
		Source: "in-cluster",
		// #5475: this was 3 with a comment claiming org-private was 0.
		// The canonical enum (core/services/catalyst-catalog/internal/
		// source.Origin) is public=1, sovereign=2, org-private=3, so 3
		// mislabelled every platform blueprint as org-private. A blueprint
		// read from the in-cluster CR is sovereign-curated.
		Origin: catalogOriginSovereign,
	}
	spec, ok, _ := unstructured.NestedMap(u.Object, "spec")
	if !ok {
		return nil, nil
	}
	if v, ok := spec["version"].(string); ok {
		bp.Version = v
	}
	if v, ok := spec["visibility"].(string); ok {
		bp.Visibility = v
	}
	if cardRaw, ok := spec["card"].(map[string]interface{}); ok {
		card := CatalogBlueprintCard{}
		if v, ok := cardRaw["title"].(string); ok {
			card.Title = v
		}
		if v, ok := cardRaw["summary"].(string); ok {
			card.Summary = v
		}
		if v, ok := cardRaw["description"].(string); ok {
			card.Description = v
		}
		if v, ok := cardRaw["tagline"].(string); ok {
			card.Tagline = v
		}
		if v, ok := cardRaw["icon"].(string); ok {
			card.Icon = v
		}
		// #3668 §3B(d) — the theme-aware icons MUST survive into the wire
		// shape from a CR/Gitea-sourced Blueprint, not only via the
		// commerce-store overlay. Without these reads an out-of-band
		// `card.iconLight` edit in Gitea (or a reconciled CR) would never
		// reach the console's icon resolver.
		if v, ok := cardRaw["iconLight"].(string); ok {
			card.IconLight = v
		}
		if v, ok := cardRaw["iconDark"].(string); ok {
			card.IconDark = v
		}
		if v, ok := cardRaw["category"].(string); ok {
			card.Category = v
		}
		if v, ok := cardRaw["family"].(string); ok {
			card.Family = v
		}
		if v, ok := cardRaw["license"].(string); ok {
			card.License = v
		}
		if v, ok := cardRaw["docs"].(string); ok {
			card.Docs = v
		} else if v, ok := cardRaw["documentation"].(string); ok {
			card.Docs = v
		}
		if rawTags, ok := cardRaw["tags"].([]interface{}); ok {
			for _, t := range rawTags {
				if s, ok := t.(string); ok {
					card.Tags = append(card.Tags, s)
				}
			}
		}
		bp.Card = card
	}
	// `Raw` carries the entire object so blueprintConfigSchema has spec
	// available downstream.
	rawBytes, err := json.Marshal(u.Object)
	if err == nil {
		var raw map[string]interface{}
		if err := json.Unmarshal(rawBytes, &raw); err == nil {
			bp.Raw = raw
		}
	}
	return bp, nil
}

// isVersionNotServed returns true when the error indicates the requested
// CRD version isn't served (NoMatchKind / NotFound on the group/version).
func isVersionNotServed(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsNotFound(err) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "no matches for kind") ||
		strings.Contains(msg, "the server could not find the requested resource") ||
		strings.Contains(msg, "could not find requested resource")
}
