// post_handover_adoption_apply.go — the OpenTofu→Crossplane adoption
// WRITE-SEAM that delivers the real cloud-resource ids onto the Sovereign
// cluster at Phase-1 hand-off (#4212, Path B of #4018; un-gates #4002).
//
// THE BREAK THIS CLOSES. The provisioner's adoption generator
// (provisioner/adoption.go GenerateAdoptionClaims) already turns the
// post-`tofu apply` terraform.tfstate into one Observe-first CloudAdoption
// per real cloud resource (ELB / server / network / eip …), each carrying
// the live resource id as crossplane.io/external-name. But it only WROTE
// that YAML into the mothership deploy workdir — and the Sovereign's Flux
// reconciles the STATIC clusters/_template/infrastructure/base/adoption-
// claims.yaml from the public OpenOva repo, which ships only the
// `PENDING-GENERATION` placeholder. Nothing carried the generated real-id
// claims from the mothership workdir onto the cluster. So on every
// Sovereign `kubectl get cloudadoption -A` showed (at best) the placeholder
// and Crossplane observed zero real infra — the #4002/#4018 gap.
//
// This hook is that carrier. After Phase-1 reaches OutcomeReady, it:
//   1. reads the deploy workdir's terraform.tfstate,
//   2. regenerates the real-id CloudAdoption CRs (same pure generator),
//   3. SERVER-SIDE APPLIES them straight onto the Sovereign cluster via the
//      kubeconfig the cloud-init posted back, and
//   4. deletes the placeholder once ≥1 real claim landed.
//
// Observe-first by construction: every generated CloudAdoption is
// manage:false → the composed Workspace gets managementPolicies:[Observe]
// and can NEVER re-provision or delete the live ELB/nodes. Applying these
// is therefore safe against the RUNNING platform (ADR-0011).
//
// Idempotent + retrying: the CloudAdoption CRD ships via bp-crossplane-
// claims (bootstrap-kit slot 14) and the provider-opentofu via the
// infrastructure-config Flux Kustomization — both may still be installing
// when Phase-1 first goes Ready, so the apply retries until the CRD
// registers (bounded). Re-running on an already-adopted Sovereign is a
// no-op server-side-apply merge. Failures log + emit an SSE warn but never
// fail the handover (adoption is a day-2 observability surface, not on the
// bootstrap critical path).
//
// Run on a background goroutine from runPhase1Watch's OutcomeReady terminal
// block (phase1_watch.go), alongside runPostHandoverPolicyEnforceFlip.
package handler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// cloudAdoptionGVR — the CloudAdoption claim GVR (compose.openova.io,
// the claimNames in platform/crossplane-claims xrds/cloudadoption.yaml).
// Namespaced to crossplane-system (where the generator stamps them).
var cloudAdoptionGVR = schema.GroupVersionResource{
	Group:    "compose.openova.io",
	Version:  "v1alpha1",
	Resource: "cloudadoptions",
}

const (
	adoptionPlaceholderName = "adoption-placeholder"
	adoptionNamespace       = "crossplane-system"
	// adoptionApplyFieldManager — distinct field manager so a re-run
	// server-side-apply cleanly owns + merges the same fields.
	adoptionApplyFieldManager = "catalyst-api/crossplane-adoption-apply"
	// adoptionApplyMaxWait — total budget waiting for the CloudAdoption
	// CRD to register (bp-crossplane-claims + infrastructure-config Flux
	// reconciles). Generous: this is fully off the bootstrap critical path.
	adoptionApplyMaxWait = 15 * time.Minute
	adoptionApplyPoll    = 30 * time.Second
)

// runPostHandoverAdoptionApply applies the real-id Observe-first
// CloudAdoption CRs onto the Sovereign cluster. See file header.
func (h *Handler) runPostHandoverAdoptionApply(dep *Deployment) {
	defer func() {
		if r := recover(); r != nil {
			h.log.Error("adoption-apply: panic recovered", "id", dep.ID, "panic", r)
		}
	}()

	if h.kubeconfigsDir == "" {
		h.log.Warn("adoption-apply: kubeconfigsDir unset; skipping", "id", dep.ID)
		return
	}

	// 1. Read the deploy workdir's terraform.tfstate. The auto-handover
	//    path does NOT delete the workdir (only the synchronous POST
	//    /handover endpoint does), so the state is still present here.
	prov := provisioner.New()
	statePath := filepath.Join(prov.WorkDir, dep.ID, "terraform.tfstate")
	stateRaw, err := os.ReadFile(statePath)
	if err != nil {
		h.log.Warn("adoption-apply: read terraform.tfstate failed; skipping (Crossplane keeps the placeholder)",
			"id", dep.ID, "path", statePath, "err", err)
		return
	}

	// 2. Regenerate the real-id CloudAdoption CRs (same pure generator the
	//    provisioner already calls; re-running is deterministic).
	region := strings.TrimSpace(dep.Request.HuaweiRegion)
	if region == "" && len(dep.Request.Regions) > 0 {
		region = strings.TrimSpace(dep.Request.Regions[0].CloudRegion)
	}
	cloud := strings.TrimSpace(dep.Request.Provider)
	if cloud == "" {
		cloud = "huawei"
	}
	claimsYAML, err := provisioner.GenerateAdoptionClaims(stateRaw, dep.Request.SovereignFQDN, cloud, region)
	if err != nil {
		h.log.Error("adoption-apply: generate claims failed", "id", dep.ID, "err", err)
		return
	}

	objs, err := decodeCloudAdoptionDocs(claimsYAML)
	if err != nil {
		h.log.Error("adoption-apply: decode generated claims failed", "id", dep.ID, "err", err)
		return
	}
	if len(objs) == 0 {
		h.log.Warn("adoption-apply: no adoptable resources in state; nothing to apply (placeholder stays)", "id", dep.ID)
		return
	}

	// 3. Build the dynamic client from the posted-back kubeconfig.
	kcPath := filepath.Join(h.kubeconfigsDir, dep.ID+".yaml")
	kcRaw, err := os.ReadFile(kcPath)
	if err != nil {
		h.log.Warn("adoption-apply: read kubeconfig failed; skipping", "id", dep.ID, "path", kcPath, "err", err)
		return
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kcRaw)
	if err != nil {
		h.log.Warn("adoption-apply: parse kubeconfig failed", "id", dep.ID, "err", err)
		return
	}
	cfg.Timeout = 30 * time.Second
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		h.log.Warn("adoption-apply: build dynamic client failed", "id", dep.ID, "err", err)
		return
	}

	// 4. Wait for the CloudAdoption CRD to register, then server-side-apply
	//    every real claim. The CRD lands via bp-crossplane-claims (slot 14)
	//    which may still be reconciling at first OutcomeReady.
	deadline := time.Now().Add(adoptionApplyMaxWait)
	applied := 0
	for {
		applied = h.applyCloudAdoptions(dyn, dep, objs)
		if applied == len(objs) {
			break
		}
		if time.Now().After(deadline) {
			h.log.Warn("adoption-apply: budget exhausted; partial apply",
				"id", dep.ID, "applied", applied, "total", len(objs))
			break
		}
		time.Sleep(adoptionApplyPoll)
	}

	if applied == 0 {
		dep.recordEvent(provisioner.Event{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Phase:   "post-handover",
			Level:   "warn",
			Message: "Crossplane adoption: CloudAdoption CRD not yet registered after budget; the Observe-first real-id claims could not be applied. Crossplane keeps the placeholder until the next reconcile applies them.",
		})
		return
	}

	// 5. Best-effort delete the placeholder now that real claims landed.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	delErr := dyn.Resource(cloudAdoptionGVR).Namespace(adoptionNamespace).
		Delete(ctx, adoptionPlaceholderName, metav1.DeleteOptions{})
	if delErr != nil && !errors.IsNotFound(delErr) {
		h.log.Warn("adoption-apply: placeholder delete failed (non-fatal)", "id", dep.ID, "err", delErr)
	}

	h.log.Info("adoption-apply: applied Observe-first CloudAdoption claims onto Sovereign",
		"id", dep.ID, "applied", applied, "total", len(objs), "cloud", cloud)
	dep.recordEvent(provisioner.Event{
		Time:  time.Now().UTC().Format(time.RFC3339),
		Phase: "post-handover",
		Level: "info",
		Message: fmt.Sprintf(
			"Crossplane adoption: applied %d Observe-first CloudAdoption(s) for the real OpenTofu cloud resources (ELB/server/network/eip). Crossplane now OBSERVES the live infra by external-name without re-provisioning it (ADR-0011, #4002/#4018/#4212).",
			applied),
	})
}

// applyCloudAdoptions server-side-applies every CloudAdoption object,
// returning how many landed. On the CRD-not-registered error it returns
// early (0) so the caller retries; per-object errors are logged but do not
// abort the batch (one bad claim should not block the rest).
func (h *Handler) applyCloudAdoptions(dyn dynamic.Interface, dep *Deployment, objs []*unstructured.Unstructured) int {
	applied := 0
	for _, obj := range objs {
		name := obj.GetName()
		data, err := obj.MarshalJSON()
		if err != nil {
			h.log.Warn("adoption-apply: marshal object failed", "id", dep.ID, "name", name, "err", err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err = dyn.Resource(cloudAdoptionGVR).Namespace(adoptionNamespace).
			Patch(ctx, name, types.ApplyPatchType, data, metav1.PatchOptions{
				FieldManager: adoptionApplyFieldManager,
				Force:        boolPtr(true),
			})
		cancel()
		if err != nil {
			// CRD not yet registered → the WHOLE batch should retry; signal
			// by returning the count so far (caller re-polls).
			if isNoMatchOrCRDMissing(err) {
				h.log.Info("adoption-apply: CloudAdoption CRD not registered yet; will retry",
					"id", dep.ID, "applied", applied)
				return applied
			}
			h.log.Warn("adoption-apply: apply claim failed (continuing)", "id", dep.ID, "name", name, "err", err)
			continue
		}
		applied++
	}
	return applied
}

// decodeCloudAdoptionDocs splits the multi-document generated YAML into
// unstructured CloudAdoption objects, skipping comment-only / empty docs.
func decodeCloudAdoptionDocs(multiDoc []byte) ([]*unstructured.Unstructured, error) {
	var out []*unstructured.Unstructured
	for _, doc := range strings.Split(string(multiDoc), "\n---") {
		trimmed := strings.TrimSpace(doc)
		if trimmed == "" {
			continue
		}
		m := map[string]any{}
		if err := yaml.Unmarshal([]byte(trimmed), &m); err != nil {
			return nil, fmt.Errorf("unmarshal adoption doc: %w", err)
		}
		if len(m) == 0 {
			continue // comment-only header block
		}
		u := &unstructured.Unstructured{Object: m}
		if u.GetKind() != "CloudAdoption" {
			continue
		}
		// Force the canonical namespace (the generator sets it, but be
		// defensive so the namespaced Patch target is unambiguous).
		u.SetNamespace(adoptionNamespace)
		out = append(out, u)
	}
	return out, nil
}

// isNoMatchOrCRDMissing reports whether the error means the CloudAdoption
// CRD / REST mapping is not yet registered on the cluster — the retry
// signal. The dynamic client surfaces this as a NoMatch/NotFound on the
// resource type itself rather than the object.
func isNoMatchOrCRDMissing(err error) bool {
	if err == nil {
		return false
	}
	if errors.IsNotFound(err) {
		// A NotFound on a server-side-apply CREATE means the resource type
		// itself is unknown (apply creates if absent), i.e. CRD missing.
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "no matches for kind") ||
		strings.Contains(msg, "could not find the requested resource") ||
		strings.Contains(msg, "the server doesn't have a resource type")
}
