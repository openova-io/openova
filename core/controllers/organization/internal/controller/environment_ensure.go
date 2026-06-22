// Environment auto-ensure (issue #4077, Refs #3687).
//
// # Why this file exists
//
// An Application CR (apps.openova.io) hard-fails its reconcile with
// `Ready=False:EnvironmentMissing` until a parent Environment CR
// (catalyst.openova.io/v1, name `<org>-<envType>`) already exists — see
// core/controllers/application/internal/controller/application_controller.go
// (`fetchEnvironment` → `markPending(ReasonEnvironmentMissing)`). Before
// this file NOTHING on the Org-onboarding path created that Environment:
// neither catalyst-api signup/funnel nor the organization-controller
// emitted one, so `kubectl get environments.catalyst.openova.io` was empty
// on a fresh Sovereign and EVERY app install (e.g. the agenity agentic
// journey) wedged at EnvironmentMissing forever.
//
// # The seam
//
// The organization-controller is the natural producer: it already owns the
// Org's vCluster lifecycle (Keycloak group + Gitea Org + vCluster
// HelmRelease + per-Org Flux loop) and walks status Pending → Provisioning
// → Ready off a LIVE readback of the vCluster HR. We hook the Environment
// ensure right after that readback, GATED on the vCluster being Ready — so
// the Environment appears exactly when the Org is actually able to host
// Applications, never as a green badge over a vCluster that isn't up yet.
//
// # What the Environment carries
//
// The Application controller reads ONLY `spec.organizationRef`,
// `spec.envType`, `spec.placement`, and `len(spec.regions)` off the
// Environment (parseEnvSpec) — it never reads the per-region
// provider/region/hostCluster VALUES (the install pivots through the
// Blueprint topology + the controller's VClusterPlacements, not the
// Environment's region strings). The environment-controller, in turn,
// only uses the region fields to derive Gitea host-cluster paths. So the
// single region we emit needs a provider/region/buildingBlock triple that
// (a) satisfies the CRD schema (region must match `^[a-z]{3}[a-z0-9]?$`,
// provider/buildingBlock are enums) and (b) names the real host cluster
// via the explicit `hostCluster` override. Those come from
// `EnvRegionProvider/EnvRegionCode/EnvRegionBuildingBlock` config (env,
// per Inviolable Principle #4) with safe canonical defaults, and from
// `r.HostCluster` when it is already a `{provider}-{region}-{bb}-{envType}`
// name (Hetzner) — falling back to the configured triple when
// `CATALYST_HOST_CLUSTER` is the Sovereign FQDN (Huawei, e.g.
// `omantel.biz`).

package controller

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// environmentGVK is the cluster-scoped Environment CR the
// organization-controller find-or-creates. Group/version mirror
// products/catalyst/chart/crds/environment.yaml +
// core/controllers/environment/api/v1.
var environmentGVK = schema.GroupVersionKind{
	Group:   "catalyst.openova.io",
	Version: "v1",
	Kind:    "Environment",
}

// envRegionCodePattern mirrors the Environment CRD's
// spec.regions[].region constraint (`^[a-z]{3}[a-z0-9]?$`): exactly three
// lowercase letters optionally followed by one lowercase-alnum. Used to
// validate both the configured default and any region parsed out of a
// `{provider}-{region}-{bb}-{envType}` host-cluster name.
var envRegionCodePattern = regexp.MustCompile(`^[a-z]{3}[a-z0-9]?$`)

// hostClusterPattern matches the canonical 4-segment host-cluster name
// `{provider}-{region}-{bb}-{envType}` (NAMING §4.1) so we can pull the
// real provider/region/buildingBlock off `r.HostCluster` when it is one
// (Hetzner). On Huawei `CATALYST_HOST_CLUSTER` is the Sovereign FQDN
// (e.g. `omantel.biz`) which does NOT match — we then fall back to the
// configured triple. region is checked against envRegionCodePattern, bb
// against the CRD enum, provider against the CRD enum.
var hostClusterPattern = regexp.MustCompile(`^([a-z][a-z0-9]*)-([a-z]{3}[a-z0-9]?)-([a-z]{3})-([a-z]{2,5})$`)

// envProviderAllowed mirrors the Environment CRD provider enum.
var envProviderAllowed = map[string]bool{
	"hetzner": true, "huawei": true, "oci": true, "aws": true,
	"gcp": true, "azure": true, "contabo": true,
}

// envBuildingBlockAllowed mirrors the Environment CRD buildingBlock enum.
var envBuildingBlockAllowed = map[string]bool{"rtz": true, "dmz": true, "mgt": true}

// envEnvTypeAllowed mirrors the Environment CRD envType enum.
var envEnvTypeAllowed = map[string]bool{"prod": true, "stg": true, "uat": true, "dev": true, "poc": true}

// envRegionDefaults resolves the (provider, region, buildingBlock) triple
// the ensured Environment's single region carries. Preference order:
//
//  1. parse `r.HostCluster` if it is a canonical
//     `{provider}-{region}-{bb}-{envType}` name with valid enum/region
//     segments (Hetzner / the qa-fixture format) — the most precise source.
//  2. fall back to the controller-configured triple
//     (EnvRegionProvider/EnvRegionCode/EnvRegionBuildingBlock), itself
//     defaulted to the canonical huawei/mee/rtz when unset.
//
// The returned triple is always CRD-valid: region matches
// envRegionCodePattern and provider/buildingBlock are in their enums.
func (r *Reconciler) envRegionDefaults() (provider, region, bb string) {
	// (1) Try the host-cluster name.
	if m := hostClusterPattern.FindStringSubmatch(r.HostCluster); m != nil {
		p, reg, block := m[1], m[2], m[3]
		if envProviderAllowed[p] && envRegionCodePattern.MatchString(reg) && envBuildingBlockAllowed[block] {
			return p, reg, block
		}
	}

	// (2) Configured triple with canonical defaults.
	provider = strings.ToLower(strings.TrimSpace(r.EnvRegionProvider))
	if !envProviderAllowed[provider] {
		provider = "huawei"
	}
	region = strings.ToLower(strings.TrimSpace(r.EnvRegionCode))
	if !envRegionCodePattern.MatchString(region) {
		region = "mee"
	}
	bb = strings.ToLower(strings.TrimSpace(r.EnvRegionBuildingBlock))
	if !envBuildingBlockAllowed[bb] {
		bb = "rtz"
	}
	return provider, region, bb
}

// environmentEnvType returns the env_type the ensured Environment uses.
// Defaults to the Org's spec.defaultEnvironmentType, falling back to
// "prod" (the one-Org-one-Env-per-chroot-Sovereign convention the
// Application's environmentRef `<org>-prod` assumes, mirroring
// bootstrap/api environmentRefForOrg).
func environmentEnvType(org *orgapi.Organization) string {
	et := strings.ToLower(strings.TrimSpace(org.Spec.DefaultEnvironmentType))
	if envEnvTypeAllowed[et] {
		return et
	}
	return "prod"
}

// ensureEnvironment find-or-creates the cluster-scoped Environment CR
// `<slug>-<envType>` for the Org so that Applications installed into the
// Org's vCluster can resolve their parent Environment instead of wedging
// at Ready=False:EnvironmentMissing (issue #4077).
//
// Idempotent: a steady-state Environment is a no-op (the Get short-
// circuits before Create, and an AlreadyExists race-loser returns nil).
// We deliberately do NOT update an existing Environment's spec — once an
// operator (or a future multi-region funnel path) has authored regions,
// the organization-controller must not clobber them; it only fills the
// initial gap. Cluster-scoped, so the OwnerReference to the cluster-scoped
// Organization is GC-safe.
func (r *Reconciler) ensureEnvironment(ctx context.Context, org *orgapi.Organization) error {
	envType := environmentEnvType(org)
	name := fmt.Sprintf("%s-%s", org.Spec.Slug, envType)

	// Short-circuit if it already exists — never overwrite operator/funnel
	// authored regions.
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(environmentGVK)
	switch err := r.Get(ctx, client.ObjectKey{Name: name}, existing); {
	case err == nil:
		return nil
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("get Environment %q: %w", name, err)
	}

	provider, region, bb := r.envRegionDefaults()

	// hostCluster override: when r.HostCluster is set use it verbatim so
	// the environment-controller binds the REAL host cluster (Huawei FQDN
	// or the canonical 4-segment name) rather than re-deriving
	// `{provider}-{region}-{bb}-{envType}` from the cosmetic triple.
	regionObj := map[string]any{
		"provider":      provider,
		"region":        region,
		"buildingBlock": bb,
	}
	if hc := strings.TrimSpace(r.HostCluster); hc != "" {
		regionObj["hostCluster"] = hc
	}

	desired := &unstructured.Unstructured{}
	desired.SetGroupVersionKind(environmentGVK)
	desired.SetName(name)
	desired.SetLabels(map[string]string{
		"openova.io/organization":      org.Spec.Slug,
		"openova.io/sovereign":         org.Spec.SovereignRef,
		"openova.io/managed-by":        "organization-controller",
		"app.kubernetes.io/managed-by": "catalyst",
	})
	// Organization is cluster-scoped, so a cluster-scoped owner ref is
	// GC-safe and ties the Environment's lifecycle to the Org.
	desired.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: orgapi.GroupVersion.String(),
		Kind:       "Organization",
		Name:       org.Name,
		UID:        org.UID,
		Controller: ptrBool(true),
	}})
	desired.Object["spec"] = map[string]any{
		"organizationRef": org.Spec.Slug,
		"envType":         envType,
		"placement":       "single-region",
		"regions":         []any{regionObj},
	}

	if err := r.Create(ctx, desired); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create Environment %q: %w", name, err)
	}
	r.Log.Info("ensured Environment for Org",
		"environment", name,
		"organization", org.Spec.Slug,
		"envType", envType,
		"provider", provider,
		"region", region,
		"buildingBlock", bb,
		"hostCluster", r.HostCluster)
	return nil
}
