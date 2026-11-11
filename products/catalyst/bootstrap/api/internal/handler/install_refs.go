// install_refs.go — ADR-0015 D4: references, never fabrication.
//
// Every Application-create door (POST /catalyst/v1/apps/instances, POST
// /api/v1/sovereigns/{id}/applications, POST /api/v1/org/applications and the
// signup funnel, D5) resolves the request's Organization, Environment,
// Blueprint and regions through validateInstallRefs BEFORE anything is
// written. A reference that names no object is a 422 naming the field; an
// Organization whose org-controller-owned namespace does not exist yet is a
// 409 organization-not-ready. Nothing here creates a namespace, appends
// "-prod", substitutes "sovereign" or writes a "primary" region sentinel.
//
// Two omissions are resolved from objects, never invented:
//
//   - environment omitted → the Organization's ONE Environment whose
//     spec.envType equals Organization.spec.defaultEnvironmentType. Zero or
//     several candidates → 422 environment-required.
//   - regions omitted     → the registry's declared primary region
//     (regions.Primary) for a singleton; every registry region (primary
//     first) for a multi-region mode. Empty registry → 422
//     region-registry-empty.
package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/core/controllers/pkg/regionlocal"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/regions"
)

// refError is a reference-resolution failure. Status is 422 for a reference
// that names nothing, 409 for an Organization not ready, 503 when the
// apiserver could not answer.
type refError struct {
	Status  int
	Code    string
	Field   string
	Message string
}

func (e *refError) Error() string { return e.Code + ": " + e.Message }

// body is the JSON envelope every door writes for a refError.
func (e *refError) body() map[string]any {
	return map[string]any{
		"code":       e.Code,
		"error":      e.Code,
		"field":      e.Field,
		"message":    e.Message,
		"detail":     e.Message,
		"status":     fmt.Sprintf("%d", e.Status),
		"httpStatus": fmt.Sprintf("%d", e.Status),
		"applied":    false,
	}
}

func unprocessable(code, field, format string, args ...any) *refError {
	return &refError{Status: http.StatusUnprocessableEntity, Code: code, Field: field, Message: fmt.Sprintf(format, args...)}
}

func refLookupFailed(kind, name string, err error) *refError {
	return &refError{Status: http.StatusServiceUnavailable, Code: "reference-lookup-failed", Field: "",
		Message: fmt.Sprintf("could not read %s %q: %v", kind, name, err)}
}

// installRefs is what a create request names.
type installRefs struct {
	Org         string   // Organization CR name
	Environment string   // Environment CR name ("" → resolved, see file doc)
	Blueprint   string   // Blueprint CR name, exactly as stored (bp-…)
	Mode        string   // placement mode ("" → not checked here)
	Regions     []string // canonical region ids ("" → registry primary)
}

// resolvedRefs is the validated result — every field names a live object.
type resolvedRefs struct {
	Org              string
	Namespace        string
	Environment      string
	Blueprint        string
	BlueprintVersion string
	Regions          []string
}

// validateInstallRefs resolves every reference of a create request.
func validateInstallRefs(ctx context.Context, client dynamic.Interface, in installRefs) (resolvedRefs, *refError) {
	out := resolvedRefs{}
	org := strings.TrimSpace(in.Org)
	if org == "" {
		return out, unprocessable("organization-required", "org", "an Organization is required (Organization CR name)")
	}

	// 1. Organization — by CR name.
	orgObj, err := client.Resource(organizationGVR()).Get(ctx, org, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return out, unprocessable("organization-not-found", "org",
				"Organization %q does not exist on this Sovereign", org)
		}
		return out, refLookupFailed("Organization", org, err)
	}
	out.Org = orgObj.GetName()

	// 2. Environment — exists and belongs to the Organization.
	env := strings.TrimSpace(in.Environment)
	if env == "" {
		resolved, rerr := defaultEnvironmentFor(ctx, client, orgObj)
		if rerr != nil {
			return out, rerr
		}
		env = resolved
	}
	envObj, err := client.Resource(EnvironmentGVR()).Get(ctx, env, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return out, unprocessable("environment-not-found", "environment",
				"Environment %q does not exist on this Sovereign", env)
		}
		return out, refLookupFailed("Environment", env, err)
	}
	envOrg, _, _ := unstructured.NestedString(envObj.Object, "spec", "organizationRef")
	if envOrg != out.Org {
		return out, unprocessable("environment-organization-mismatch", "environment",
			"Environment %q belongs to Organization %q, not %q", env, envOrg, out.Org)
	}
	out.Environment = env

	// 3. Blueprint — exists; the placement mode is one its placementSchema allows.
	bpName := strings.TrimSpace(in.Blueprint)
	if bpName == "" {
		return out, unprocessable("blueprint-required", "blueprint", "a Blueprint is required (Blueprint CR name)")
	}
	bpObj, err := client.Resource(blueprintCRGVR()).Get(ctx, bpName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return out, unprocessable("blueprint-not-found", "blueprint",
				"Blueprint %q does not exist on this Sovereign", bpName)
		}
		return out, refLookupFailed("Blueprint", bpName, err)
	}
	out.Blueprint = bpObj.GetName()
	out.BlueprintVersion, _, _ = unstructured.NestedString(bpObj.Object, "spec", "version")
	if mode := canonicalizeTopology(in.Mode); mode != "" {
		allowed := blueprintAllowedModes(bpObj)
		if !refContains(allowed, mode) {
			return out, unprocessable("placement-mode-not-allowed", "placement.mode",
				"placement mode %q is not allowed by Blueprint %q (placementSchema.modes: %s)",
				mode, out.Blueprint, strings.Join(allowed, ", "))
		}
	}

	// 4. Regions — every entry is in the Sovereign's region registry.
	if len(in.Regions) == 0 {
		primary, ok := regions.Primary()
		if !ok {
			return out, unprocessable("region-registry-empty", "placement.regions",
				"this Sovereign declares no regions; name a region explicitly once the registry is configured")
		}
		out.Regions = []string{primary.ID}
		if m := canonicalizeTopology(in.Mode); m != "" && m != "singleton" {
			// A multi-region posture spans the registry (primary first).
			out.Regions = regions.IDs()
		}
	} else {
		known := regions.IDs()
		for i, r := range in.Regions {
			if !refContains(known, r) {
				return out, unprocessable("region-unknown", fmt.Sprintf("placement.regions[%d]", i),
					"region %q is not in this Sovereign's region registry (%s)", r, strings.Join(known, ", "))
			}
		}
		out.Regions = append([]string(nil), in.Regions...)
	}

	// 4b. ADR-0015 D6 — region-local volumes. A multi-region placement (any
	//     mode but singleton, or more than one region) is refused while the
	//     Blueprint declares a spec.regionLocalVolumes entry that nothing
	//     replicates: a primary move would leave that volume's data in the
	//     old region. The message names the volume. Same rule as the
	//     application-controller (package regionlocal), so a placement edited
	//     in Git is refused there too.
	if err := regionlocal.Refusal(out.Blueprint, bpObj.Object, canonicalizeTopology(in.Mode), len(out.Regions)); err != nil {
		return out, unprocessable("region-local-volume-not-replicated", "placement.mode", "%s", err.Error())
	}

	// 5. The Organization's namespace — owned by the org-controller. Never
	//    created here: an Org whose boundary has not been reconciled is not
	//    ready to receive Applications.
	ns := out.Org
	if _, err := client.Resource(namespacesGVR()).Get(ctx, ns, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return out, &refError{Status: http.StatusConflict, Code: "organization-not-ready", Field: "org",
				Message: fmt.Sprintf("Organization %q has no namespace yet — the organization controller has not reconciled it; retry when the Organization is Ready", out.Org)}
		}
		return out, refLookupFailed("Namespace", ns, err)
	}
	out.Namespace = ns
	return out, nil
}

// defaultEnvironmentFor resolves an omitted Environment from objects: the
// Organization's single Environment of its declared default envType.
func defaultEnvironmentFor(ctx context.Context, client dynamic.Interface, org *unstructured.Unstructured) (string, *refError) {
	wantType, _, _ := unstructured.NestedString(org.Object, "spec", "defaultEnvironmentType")
	list, err := client.Resource(EnvironmentGVR()).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", refLookupFailed("Environment list for Organization", org.GetName(), err)
	}
	var candidates []string
	for i := range list.Items {
		e := &list.Items[i]
		ref, _, _ := unstructured.NestedString(e.Object, "spec", "organizationRef")
		if ref != org.GetName() {
			continue
		}
		envType, _, _ := unstructured.NestedString(e.Object, "spec", "envType")
		if wantType != "" && envType != wantType {
			continue
		}
		candidates = append(candidates, e.GetName())
	}
	if len(candidates) != 1 {
		return "", unprocessable("environment-required", "environment",
			"name an Environment of Organization %q (found %d candidate(s) of default type %q: %s)",
			org.GetName(), len(candidates), wantType, strings.Join(candidates, ", "))
	}
	return candidates[0], nil
}

// blueprintAllowedModes reads spec.placementSchema.modes in the canonical
// vocabulary. A Blueprint that declares none allows singleton only.
func blueprintAllowedModes(bp *unstructured.Unstructured) []string {
	raw, _, _ := unstructured.NestedStringSlice(bp.Object, "spec", "placementSchema", "modes")
	out := make([]string, 0, len(raw))
	for _, m := range raw {
		if c := canonicalizeTopology(m); c != "" && !refContains(out, c) {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		out = []string{"singleton"}
	}
	return out
}

func refContains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
