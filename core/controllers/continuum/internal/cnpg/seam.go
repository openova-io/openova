// GitOps seam for the cnpg-pair's desired role — the per-region flux
// `bootstrap-kit` Kustomization's spec.postBuild.substitute.
//
// WHY NOT PATCH THE Cluster CR
// ----------------------------
// Both halves of a bp-cnpg-pair are Flux/Helm-owned. A live patch of a
// Cluster CR's spec is reverted by helm-controller drift correction
// (hw256 G12, #5125-D1: a live `replica.enabled=false` was undone at
// T0+2m07s mid-outage). The DR actors in the chart therefore move the
// pair's role through the bootstrap-kit Kustomization's
// postBuild.substitute — SOVEREIGN_CNPG_PAIR_DEMOTED (region A,
// dr-failback) and SOVEREIGN_CNPG_PAIR_PROMOTED (region B, dr-promoter)
// — each with its OWN field manager, which kustomize-controller's
// managed-fields cleanup does not reclaim (see the LATCH-DURABILITY
// header of platform/cnpg-pair/chart/templates/dr-failback.yaml).
// clusters/_template/bootstrap-kit/16b-bp-cnpg-pair.yaml renders the
// substitutes into the HelmRelease values, so the role becomes the
// SOURCE-rendered desired state and drift correction re-affirms it.
//
// The planned (Continuum-driven) cross-region switchover follows the
// same pattern with two more keys, written under the field manager
// FieldManagerSwitchover:
//
//	SOVEREIGN_CNPG_PAIR_ACTIVE_PRIMARY   -> cnpgPair.distributed.primary
//	SOVEREIGN_CNPG_PAIR_PROMOTION_TOKEN  -> cnpgPair.distributed.promotionToken
//
// The chart's distributed mode (cnpgPair.distributed.enabled) renders
// them as CNPG's native distributed-topology `spec.replica.{primary,
// self, source, promotionToken}` on both halves.
package cnpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// KustomizationGVR — flux kustomize.toolkit.fluxcd.io/v1 Kustomization.
var KustomizationGVR = schema.GroupVersionResource{
	Group:    "kustomize.toolkit.fluxcd.io",
	Version:  "v1",
	Resource: "kustomizations",
}

// RegionLabel is the canonical region label bp-cnpg-pair stamps on each
// Cluster CR half (the same label its node-affinity pins on). The
// cross-region switchover maps a Continuum region to a pair half by it.
const RegionLabel = "openova.io/region"

// Substitute keys of the cnpg-pair role seam.
const (
	// SubstituteActivePrimary names the Cluster the distributed
	// topology designates primary (`cnpgPair.distributed.primary`).
	// Empty/absent = the chart default (the pair-role primary half).
	SubstituteActivePrimary = "SOVEREIGN_CNPG_PAIR_ACTIVE_PRIMARY"
	// SubstitutePromotionToken carries the demotion token of the peer
	// into the promoting half (`cnpgPair.distributed.promotionToken`).
	SubstitutePromotionToken = "SOVEREIGN_CNPG_PAIR_PROMOTION_TOKEN"
	// SubstitutePromoted / SubstituteDemoted are the unplanned-DR seams
	// owned by the chart's dr-promoter / dr-failback actors. The
	// switchover engine only READS them (preflight): when either is
	// latched the pair is in a DR episode whose switchback is the
	// RUNBOOKS §6.1 path, not a planned switchover.
	SubstitutePromoted = "SOVEREIGN_CNPG_PAIR_PROMOTED"
	SubstituteDemoted  = "SOVEREIGN_CNPG_PAIR_DEMOTED"
)

// FieldManagerSwitchover is the server-side field manager every seam
// write of the planned switchover uses. Like `dr-promoter` /
// `dr-failback` it does not match kustomize-controller's `kubectl*`
// managed-fields cleanup, so the write persists across reconciles.
const FieldManagerSwitchover = "continuum-switchover"

// SeamRef locates the bootstrap-kit Kustomization of one region.
type SeamRef struct {
	Namespace string
	Name      string
}

// DefaultSeamRef is the canonical per-region bootstrap-kit
// Kustomization (the object the cnpg-pair DR actors patch).
var DefaultSeamRef = SeamRef{Namespace: "flux-system", Name: "bootstrap-kit"}

func (s SeamRef) orDefault() SeamRef {
	if s.Namespace == "" {
		s.Namespace = DefaultSeamRef.Namespace
	}
	if s.Name == "" {
		s.Name = DefaultSeamRef.Name
	}
	return s
}

// ReadSubstitutes returns the Kustomization's postBuild.substitute map.
func ReadSubstitutes(ctx context.Context, dyn dynamic.Interface, ref SeamRef) (map[string]string, error) {
	if dyn == nil {
		return nil, errors.New("cnpg seam: nil dynamic client")
	}
	ref = ref.orDefault()
	ks, err := dyn.Resource(KustomizationGVR).Namespace(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("cnpg seam: get kustomization %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	sub, _, err := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
	if err != nil {
		return nil, fmt.Errorf("cnpg seam: kustomization %s/%s postBuild.substitute: %w", ref.Namespace, ref.Name, err)
	}
	if sub == nil {
		sub = map[string]string{}
	}
	return sub, nil
}

// PatchSubstitutes merge-patches the given substitute keys onto the
// Kustomization under FieldManagerSwitchover and stamps the flux
// reconcile-request annotation so kustomize re-renders without waiting
// a full interval. A nil value DELETES the key (JSON merge-patch
// semantics) — used by rollback to restore an originally-absent key.
func PatchSubstitutes(ctx context.Context, dyn dynamic.Interface, ref SeamRef, kv map[string]*string, now time.Time) error {
	if dyn == nil {
		return errors.New("cnpg seam: nil dynamic client")
	}
	ref = ref.orDefault()
	sub := map[string]interface{}{}
	for k, v := range kv {
		if v == nil {
			sub[k] = nil
		} else {
			sub[k] = *v
		}
	}
	body := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"reconcile.fluxcd.io/requestedAt": strconv.FormatInt(now.Unix(), 10),
			},
		},
		"spec": map[string]interface{}{
			"postBuild": map[string]interface{}{
				"substitute": sub,
			},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("cnpg seam: marshal patch: %w", err)
	}
	if _, err := dyn.Resource(KustomizationGVR).Namespace(ref.Namespace).Patch(ctx, ref.Name, types.MergePatchType, raw, metav1.PatchOptions{FieldManager: FieldManagerSwitchover}); err != nil {
		return fmt.Errorf("cnpg seam: patch kustomization %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	return nil
}

// Snapshot captures the current values of keys so a rollback can
// restore them exactly (absent keys map to nil = delete on restore).
func Snapshot(sub map[string]string, keys ...string) map[string]*string {
	out := make(map[string]*string, len(keys))
	for _, k := range keys {
		if v, ok := sub[k]; ok {
			v := v
			out[k] = &v
		} else {
			out[k] = nil
		}
	}
	return out
}

// StrPtr is a tiny helper for PatchSubstitutes callers.
func StrPtr(s string) *string { return &s }
