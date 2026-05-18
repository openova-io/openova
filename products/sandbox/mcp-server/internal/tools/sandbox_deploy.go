// sandbox_deploy.go — real handlers for sandbox.deploy.* (per-app
// staging + production rollouts via Flux HelmReleases).
//
// Scope (architecture.md §3): when an agent inside a Sandbox has
// produced a container image (CI build / local push / Harbor proxy
// pull), the operator wants ONE tool call to flip the running app's
// HelmRelease image tag rather than hand-editing the Gitea repo or
// rolling a kubectl patch on a Flux-owned resource. The MCP server
// exposes four tools:
//
//   - sandbox.deploy.staging    {image, app?}     → upsert staging HR
//   - sandbox.deploy.production {image, app?}     → upsert prod HR
//                                                   (capability-gated)
//   - sandbox.deploy.status     {env, app?}       → HR status + image
//   - sandbox.deploy.rollback   {env, app?}       → revert to last good
//
// Write target — the Org's `catalyst-tenant` Gitea repo, at path
// `sandbox/<owner-uid>/deploy/<app>/<env>/helmrelease.yaml`. Flux on
// the host cluster reconciles the repo into the Org vcluster (same
// idiom organization-controller + sandbox-controller use; see
// `core/controllers/sandbox/internal/gitops/manifests.go` for the
// shared layout).
//
// Wire model recap (architecture.md §5 + §7):
//
//   1. Agent runs `gitea.repo.list` / `gitea.pr.create` to land code.
//   2. Agent calls `sandbox.deploy.staging image=<registry>/<app>:<sha>`.
//   3. MCP server upserts the HR YAML at the tenant repo path;
//      records the previous image in
//      `openova.io/last-deployed-image` annotation so rollback can
//      reverse it without a roundtrip to the cluster.
//   4. Flux on host picks up the change in ~1 reconcile interval
//      (default 10m, customer-overridable on the Kustomization).
//   5. Agent polls `sandbox.deploy.status env=staging` until
//      `status.ready==true` AND `observed_image == requested_image`.
//
// Hard rules (matches the Wave-13 brief):
//
//   - Sandbox cluster access stays READ-ONLY. The deploy tools NEVER
//     hit `kubectl apply` against the cluster — they write to the
//     tenant Gitea repo and let Flux reconcile. Cluster reads in
//     status/rollback fetch the existing HR + Flux conditions only.
//   - Production calls gate on a SECOND capability
//     (`sandbox.deploy.production`) on top of the base
//     `sandbox.deploy` capability — the Registry hands us the gated
//     surface; we double-check inside the handler so a future
//     wire-change (e.g. the orchestrator re-merging a single
//     production-cleared session) still cannot bypass the check.
//   - No chart bump. We render the HR shape entirely from the Wave
//     contract; the chart at `<sov>/marketplace/catalog/<app>` (or
//     whichever HelmRepository the operator pointed at) is the
//     concern of the app author, not the Sandbox.
//
// Auth: same shape as sandbox.db.* / sandbox.preview.* — claims.OrgID
// must match env.OrgID, RequiredCapability="sandbox.deploy". Production
// additionally requires `sandbox.deploy.production`.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	gitea "github.com/openova-io/openova/core/controllers/pkg/gitea"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// deployHRGVR — Flux v2 HelmRelease. Read-side only (status, rollback
// reads `status.lastAppliedRevision` via this GVR). Writes flow
// through Gitea, so the cluster only sees the change after Flux
// reconciles.
var deployHRGVR = schema.GroupVersionResource{
	Group:    "helm.toolkit.fluxcd.io",
	Version:  "v2",
	Resource: "helmreleases",
}

// deployCapabilityProduction is the SECOND capability we check
// before producing a production HR write. The Registry already
// gates the call on RequiredCapability="sandbox.deploy"; defence in
// depth requires production-specific capability presence too.
const deployCapabilityProduction = "sandbox.deploy.production"

// deployTenantRepo is the canonical name of the Org's tenant repo
// (the same constant the sandbox-controller uses; see
// `core/controllers/sandbox/cmd/sandbox-controller/main.go`).
const deployTenantRepo = "catalyst-tenant"

// deployEnvStaging / deployEnvProduction are the two `env` slugs the
// agent passes. They flow into the rendered HR name + Gitea path.
const (
	deployEnvStaging    = "staging"
	deployEnvProduction = "production"
)

// deployManagedByValue is the canonical label value the Sandbox MCP
// server stamps on every HR it writes. Used by status/rollback to
// refuse mutating a HR that some other controller authored.
// Reuses the same managed-by namespace as sandbox.db.* / sandbox.preview.*.
const deployManagedByValue = cnpgManagedByValue

// deployLastImageAnnotation persists the previously-deployed image
// reference on the HR so `sandbox.deploy.rollback` can reverse a bad
// staging/prod push without a Git history walk.
const deployLastImageAnnotation = "openova.io/last-deployed-image"

// deployRequestedImageAnnotation records the image the latest
// `sandbox.deploy.staging|production` request asked for. Surfaces
// alongside status.observedImage so the agent can spot
// "asked-for vs Flux-picked-up" drift while reconciliation is in
// flight.
const deployRequestedImageAnnotation = "openova.io/requested-image"

// deployAppNameRE constrains the optional `app` slug. Empty → defaults
// to env.SandboxID. Same alphabet sandbox.preview uses so an agent that
// already opened a preview can deploy with the same identifier.
var deployAppNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)

// deployImageRE matches a wide container image reference — registry,
// repo, optional tag/digest. Substantive validation is the operator's
// concern (kubelet surfaces pull errors via the HR status).
var deployImageRE = regexp.MustCompile(`^[A-Za-z0-9._\-/:@]{1,512}$`)

// --- argument types ----------------------------------------------------

type sandboxDeployImageArgs struct {
	// Image — the full container reference the HR's values.image.tag
	// (or digest) will point at. Accepts `<registry>/<repo>:<tag>`,
	// `<registry>/<repo>@sha256:<digest>`, or bare `<repo>:<tag>` (the
	// HR repository will resolve via the chart default).
	Image string `json:"image"`

	// App — optional app slug. Defaults to env.SandboxID. The slug
	// drives the HR name + Gitea path + status query.
	App string `json:"app,omitempty"`
}

type sandboxDeployEnvArgs struct {
	// Env — "staging" | "production". Wire-validated against the two
	// canonical slugs; no free-form environment names.
	Env string `json:"env"`

	// App — optional app slug, defaults to env.SandboxID.
	App string `json:"app,omitempty"`
}

// --- helpers -----------------------------------------------------------

// deployAppSlug returns the canonical app slug for an HR name + Gitea
// path. Mirrors the sandbox.preview app-slug rule so callers that have
// been deploying ad-hoc previews can deploy with the same identifier.
func deployAppSlug(env *Env, app string) (string, error) {
	if app = strings.TrimSpace(app); app != "" {
		slug := strings.ToLower(app)
		if !deployAppNameRE.MatchString(slug) {
			return "", fmt.Errorf("`app` %q must match %s", slug, deployAppNameRE.String())
		}
		return slug, nil
	}
	if id := strings.ToLower(strings.TrimSpace(env.SandboxID)); id != "" {
		if !deployAppNameRE.MatchString(id) {
			return "", fmt.Errorf("derived app slug %q from SANDBOX_ID must match %s", id, deployAppNameRE.String())
		}
		return id, nil
	}
	return "", errors.New("`app` not supplied and SANDBOX_ID is empty (cannot derive app slug)")
}

// deployValidEnv checks the `env` argument against the canonical
// staging|production whitelist. Any other value is rejected so a
// typo'd `sandbox.deploy.status env=stagging` can't return the wrong
// HR.
func deployValidEnv(env string) error {
	switch env {
	case deployEnvStaging, deployEnvProduction:
		return nil
	}
	return fmt.Errorf("`env` must be %q or %q (got %q)", deployEnvStaging, deployEnvProduction, env)
}

// deployHRName is the HR's metadata.name. Stable so status/rollback
// can address it deterministically across the Gitea + cluster surfaces.
func deployHRName(app, env string) string {
	return fmt.Sprintf("%s-%s", app, env)
}

// deployGiteaPath is the file path inside `<org>/catalyst-tenant` the
// MCP server writes the HR YAML to. The sandbox-controller already
// reserves `sandbox/<owner-uid>/` for its own renders; deploys live
// in a sibling `deploy/<app>/<env>/` subtree so a future
// `sandbox.delete` purge of the Sandbox cleans up only the
// controller's renders without touching deploys (the rollback /
// teardown path uses the same path explicitly).
func deployGiteaPath(ownerUID, app, env string) string {
	return fmt.Sprintf("sandbox/%s/deploy/%s/%s/helmrelease.yaml",
		strings.ToLower(strings.TrimSpace(ownerUID)),
		app, env,
	)
}

// requireSandboxDeployScope is the canonical gate every
// sandbox.deploy.* handler runs first: env wired, OrgID known,
// OwnerUID set (used in the Gitea path), Gitea base/token present
// (the writes require them).
func requireSandboxDeployScope(ctx context.Context) (*Env, error) {
	env := EnvFrom(ctx)
	if env == nil {
		return nil, errors.New("sandbox.deploy: server env missing on context")
	}
	if strings.TrimSpace(env.OrgID) == "" {
		return nil, errors.New("sandbox.deploy: server env missing SANDBOX_ORG_ID")
	}
	if strings.TrimSpace(env.OwnerUID) == "" {
		return nil, errors.New("sandbox.deploy: server env missing SANDBOX_OWNER_UID")
	}
	if strings.TrimSpace(env.GiteaBaseURL) == "" || strings.TrimSpace(env.GiteaToken) == "" {
		return nil, errors.New("sandbox.deploy: server env missing SANDBOX_GITEA_BASE_URL/TOKEN")
	}
	return env, nil
}

// requireProductionCapability is the second-line check applied inside
// the production handler. The Registry already enforced
// `RequiredCapability=sandbox.deploy`; here we further demand
// `sandbox.deploy.production` is in the claims' Capabilities list.
//
// On test mode (env.JWTSecret empty), claims is nil and we let the
// call through — tests address one tool at a time and the registry
// shape is asserted in registry_test.go.
func requireProductionCapability(ctx context.Context) error {
	env := EnvFrom(ctx)
	if env == nil || len(env.JWTSecret) == 0 {
		return nil
	}
	claims, ok := ClaimsFrom(ctx)
	if !ok || claims == nil {
		return errors.New("sandbox.deploy.production: bearer claims missing on context")
	}
	if !claims.HasCapability(deployCapabilityProduction) {
		return fmt.Errorf("sandbox.deploy.production: forbidden (missing capability %q)", deployCapabilityProduction)
	}
	return nil
}

// buildHRObject renders the Flux HelmRelease for one app+env. The HR
// is intentionally minimal: a chart pointing at the operator's
// in-cluster HelmRepository, values.image.{repository,tag}, and the
// canonical label set. Each customer/operator owns chart
// authoring — Sandbox only flips the image.
//
// `lastImage` is non-empty on update; we propagate it onto the
// `last-deployed-image` annotation so rollback can find it without
// touching the cluster.
func buildHRObject(env *Env, app, deployEnv, image, lastImage string) *unstructured.Unstructured {
	name := deployHRName(app, deployEnv)
	repoPart, tagPart := splitImage(image)
	labels := map[string]any{
		cnpgManagedByLabel:        deployManagedByValue,
		"openova.io/sandbox-id":   env.SandboxID,
		"openova.io/sandbox-app":  app,
		"openova.io/sandbox-env":  deployEnv,
		"openova.io/sandbox-org":  env.OrgID,
	}
	if env.OwnerUID != "" {
		labels["openova.io/sandbox-owner"] = env.OwnerUID
	}
	annotations := map[string]any{
		"openova.io/created-by":          "sandbox.deploy",
		"openova.io/created-at":          time.Now().UTC().Format(time.RFC3339),
		deployRequestedImageAnnotation:   image,
	}
	if lastImage != "" {
		annotations[deployLastImageAnnotation] = lastImage
	}
	imageValues := map[string]any{}
	if repoPart != "" {
		imageValues["repository"] = repoPart
	}
	if tagPart != "" {
		// `tag` covers both `:v1.2.3` and `@sha256:<digest>` — Helm
		// charts that template `image: {{ .Values.image.repository }}:{{ .Values.image.tag }}`
		// will reproduce whatever the agent passed because we keep
		// the separator (`:` vs `@`) in tagPart.
		imageValues["tag"] = tagPart
	}
	// If the image is a single token (no `:` or `@`), still surface it
	// as `repository` so the chart can attach a default tag.
	if len(imageValues) == 0 {
		imageValues["repository"] = image
	}

	values := map[string]any{
		"image": imageValues,
	}

	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "helm.toolkit.fluxcd.io/v2",
			"kind":       "HelmRelease",
			"metadata": map[string]any{
				"name":        name,
				"namespace":   env.OrgID,
				"labels":      labels,
				"annotations": annotations,
			},
			"spec": map[string]any{
				"interval": "5m",
				"chart": map[string]any{
					"spec": map[string]any{
						"chart":   app,
						"version": "*",
						"sourceRef": map[string]any{
							"kind":      "HelmRepository",
							"name":      "catalog",
							"namespace": "flux-system",
						},
					},
				},
				"values": values,
			},
		},
	}
}

// splitImage breaks "<registry>/<repo>:<tag>" or "<...>@sha256:<digest>"
// into (repository, tag) for the values.image map. When neither
// separator is present, returns (image, "") so the chart picks a
// default tag.
func splitImage(image string) (repo, tag string) {
	image = strings.TrimSpace(image)
	if image == "" {
		return "", ""
	}
	// digest form first — `@sha256:<hex>`
	if idx := strings.Index(image, "@"); idx >= 0 {
		return image[:idx], image[idx:]
	}
	// tag form — `:<tag>` after the LAST `/` so registries with ports
	// (`harbor.example.com:5000/foo:v1`) don't confuse the parser.
	lastSlash := strings.LastIndex(image, "/")
	colonIdx := strings.LastIndex(image, ":")
	if colonIdx > lastSlash {
		return image[:colonIdx], image[colonIdx+1:]
	}
	return image, ""
}

// marshalHRYAML renders the HR unstructured into YAML bytes for the
// Gitea write. We use a tiny hand-written renderer instead of
// pulling sigs.k8s.io/yaml — the HR shape is fixed + small, no
// recursive maps deeper than the values block, and a hand-renderer
// keeps the output diff-friendly (stable key order). The tradeoff:
// any future field that goes deeper than 4 levels needs to be added
// here explicitly.
func marshalHRYAML(obj *unstructured.Unstructured) ([]byte, error) {
	// Re-shape into a deterministic key order: apiVersion/kind/
	// metadata/spec. JSON marshal first (stable key sort per Go's
	// encoder when the map is map[string]any with deterministic
	// keys) then translate to a YAML-style document by hand. We
	// accept the JSON-flavoured-YAML output Flux is happy to accept
	// (Flux uses k8s.io/apimachinery YAML decoding which round-trips
	// JSON cleanly).
	type yamlDoc struct {
		APIVersion string                 `json:"apiVersion"`
		Kind       string                 `json:"kind"`
		Metadata   map[string]interface{} `json:"metadata"`
		Spec       map[string]interface{} `json:"spec"`
	}
	doc := yamlDoc{
		APIVersion: "helm.toolkit.fluxcd.io/v2",
		Kind:       "HelmRelease",
	}
	if m, _, _ := unstructured.NestedMap(obj.Object, "metadata"); m != nil {
		doc.Metadata = m
	}
	if s, _, _ := unstructured.NestedMap(obj.Object, "spec"); s != nil {
		doc.Spec = s
	}
	// JSON is a strict superset Flux + k8s accept on disk; emit
	// indent=2 so the diff in the tenant repo is human-readable.
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal HR YAML: %w", err)
	}
	// Append a trailing newline so PutFile's byte-equal short-circuit
	// is deterministic.
	return append(out, '\n'), nil
}

// readExistingHRFromGitea fetches the current HR YAML from the tenant
// repo, if any. Returns (object, sha, false, nil) when absent so the
// caller can branch on create vs update without a typed switch.
func readExistingHRFromGitea(ctx context.Context, env *Env, owner, app, deployEnv string) (*unstructured.Unstructured, string, bool, error) {
	gc, err := giteaClient(ctx)
	if err != nil {
		return nil, "", false, err
	}
	path := deployGiteaPath(env.OwnerUID, app, deployEnv)
	file, err := gc.GetFile(ctx, owner, deployTenantRepo, "main", path)
	if errors.Is(err, gitea.ErrFileNotFound) {
		return nil, "", false, nil
	}
	if err != nil {
		if errors.Is(err, gitea.ErrRepoNotFound) {
			return nil, "", false, fmt.Errorf("sandbox.deploy: tenant repo %s/%s missing — provision the Org first", owner, deployTenantRepo)
		}
		return nil, "", false, fmt.Errorf("sandbox.deploy: read existing HR %s/%s/%s: %w", owner, deployTenantRepo, path, err)
	}
	raw, err := file.Decoded()
	if err != nil {
		return nil, "", false, fmt.Errorf("sandbox.deploy: decode existing HR: %w", err)
	}
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(raw); err != nil {
		// Tolerate older YAML-form HRs: only the annotations matter
		// for rollback bookkeeping. A failure to parse leaves
		// lastImage empty — the create path still works.
		return nil, file.SHA, true, nil
	}
	return obj, file.SHA, true, nil
}

// readObservedHRFromCluster fetches the live HR from the Org's
// vcluster so status + rollback can surface Flux's observed state.
// Returns (nil, false, nil) on 404 so the caller can branch on "Flux
// hasn't picked it up yet".
func readObservedHRFromCluster(ctx context.Context, env *Env, name string) (*unstructured.Unstructured, bool, error) {
	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, false, err
	}
	obj, err := client.Resource(deployHRGVR).Namespace(env.OrgID).
		Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("sandbox.deploy: GET HR %s/%s: %w", env.OrgID, name, err)
	}
	return obj, true, nil
}

// hrReady summarises status.conditions for the agent. Returns
// (readyBool, reason, message, observedImage). observedImage is read
// from the live HR's spec.values.image.{repository,tag} — that's the
// value Flux last reconciled into the cluster.
func hrReady(obj *unstructured.Unstructured) (bool, string, string, string) {
	if obj == nil {
		return false, "", "", ""
	}
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	ready, reason, message := false, "", ""
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		t, _ := cm["type"].(string)
		if t != "Ready" {
			continue
		}
		s, _ := cm["status"].(string)
		ready = s == "True"
		reason, _ = cm["reason"].(string)
		message, _ = cm["message"].(string)
		break
	}
	repo, _, _ := unstructured.NestedString(obj.Object, "spec", "values", "image", "repository")
	tag, _, _ := unstructured.NestedString(obj.Object, "spec", "values", "image", "tag")
	observed := repo
	if tag != "" {
		if strings.HasPrefix(tag, "@") {
			observed = repo + tag
		} else {
			observed = repo + ":" + tag
		}
	}
	return ready, reason, message, observed
}

// deployUpsertHR is the shared write path for staging + production.
// Reads the existing HR (if any), bumps the requested image +
// annotations, writes the new YAML back to the tenant repo via
// PutFile's byte-equal short-circuit semantics.
func deployUpsertHR(ctx context.Context, env *Env, deployEnv, app, image string) (map[string]any, error) {
	gc, err := giteaClient(ctx)
	if err != nil {
		return nil, err
	}
	// Read existing to preserve last-deployed-image (rollback target).
	prior, _, existed, err := readExistingHRFromGitea(ctx, env, env.OrgID, app, deployEnv)
	if err != nil {
		return nil, err
	}
	priorImage := ""
	if prior != nil {
		anns := prior.GetAnnotations()
		priorImage = anns[deployRequestedImageAnnotation]
		if priorImage == "" {
			// Fall back to spec.values.image — present even on
			// HRs we did NOT author (e.g. operator hand-edits).
			_, _, _, priorImage = hrReady(prior)
		}
	}
	// New HR carries the prior image as the rollback target. The
	// FIRST deploy has no prior — rollback returns a clean "no prior".
	obj := buildHRObject(env, app, deployEnv, image, priorImage)
	body, err := marshalHRYAML(obj)
	if err != nil {
		return nil, err
	}
	path := deployGiteaPath(env.OwnerUID, app, deployEnv)
	msg := fmt.Sprintf("sandbox.deploy.%s: %s/%s → %s", deployEnv, app, deployEnv, image)
	file, committed, err := gc.PutFile(ctx, env.OrgID, deployTenantRepo, "main", path, body, msg)
	if err != nil {
		if errors.Is(err, gitea.ErrRepoNotFound) {
			return nil, fmt.Errorf("sandbox.deploy.%s: tenant repo %s/%s missing — provision the Org first", deployEnv, env.OrgID, deployTenantRepo)
		}
		return nil, fmt.Errorf("sandbox.deploy.%s: PutFile %s: %w", deployEnv, path, err)
	}
	status := "Unchanged"
	switch {
	case !existed:
		status = "Created"
	case committed:
		status = "Updated"
	}
	return map[string]any{
		"status":           status,
		"env":              deployEnv,
		"app":              app,
		"image":            image,
		"previous_image":   priorImage,
		"namespace":        env.OrgID,
		"hr_name":          deployHRName(app, deployEnv),
		"gitea":            map[string]any{"owner": env.OrgID, "repo": deployTenantRepo, "branch": "main", "path": path, "sha": file.SHA, "committed": committed},
		"note":             "HR written to tenant Gitea repo. Flux picks up the change on next reconcile (~1-10 min); poll sandbox.deploy.status until ready=true and observed_image matches.",
	}, nil
}

// --- sandbox.deploy.staging --------------------------------------------

func sandboxDeployStaging(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxDeployImageArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.deploy.staging: invalid arguments: %w", err)
	}
	args.Image = strings.TrimSpace(args.Image)
	if !deployImageRE.MatchString(args.Image) {
		return nil, fmt.Errorf("sandbox.deploy.staging: `image` must match %s", deployImageRE.String())
	}
	env, err := requireSandboxDeployScope(ctx)
	if err != nil {
		return nil, err
	}
	app, err := deployAppSlug(env, args.App)
	if err != nil {
		return nil, fmt.Errorf("sandbox.deploy.staging: %w", err)
	}
	return deployUpsertHR(ctx, env, deployEnvStaging, app, args.Image)
}

func schemaSandboxDeployImage() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"image"},
		"properties": map[string]any{
			"image": map[string]any{
				"type":        "string",
				"description": "Container image to roll out. Accepts `<registry>/<repo>:<tag>` or `<...>@sha256:<digest>`. Push to Harbor before calling.",
				"pattern":     deployImageRE.String(),
			},
			"app": map[string]any{
				"type":        "string",
				"description": "App slug. Defaults to SANDBOX_ID when omitted. Drives the HR name (`<app>-<env>`) and Gitea path.",
				"pattern":     deployAppNameRE.String(),
			},
		},
		"additionalProperties": false,
	}
}

// --- sandbox.deploy.production -----------------------------------------

func sandboxDeployProduction(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxDeployImageArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.deploy.production: invalid arguments: %w", err)
	}
	if err := requireProductionCapability(ctx); err != nil {
		return nil, err
	}
	args.Image = strings.TrimSpace(args.Image)
	if !deployImageRE.MatchString(args.Image) {
		return nil, fmt.Errorf("sandbox.deploy.production: `image` must match %s", deployImageRE.String())
	}
	env, err := requireSandboxDeployScope(ctx)
	if err != nil {
		return nil, err
	}
	app, err := deployAppSlug(env, args.App)
	if err != nil {
		return nil, fmt.Errorf("sandbox.deploy.production: %w", err)
	}
	return deployUpsertHR(ctx, env, deployEnvProduction, app, args.Image)
}

// --- sandbox.deploy.status ---------------------------------------------

func sandboxDeployStatus(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxDeployEnvArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.deploy.status: invalid arguments: %w", err)
	}
	if err := deployValidEnv(args.Env); err != nil {
		return nil, fmt.Errorf("sandbox.deploy.status: %w", err)
	}
	env, err := requireSandboxDeployScope(ctx)
	if err != nil {
		return nil, err
	}
	app, err := deployAppSlug(env, args.App)
	if err != nil {
		return nil, fmt.Errorf("sandbox.deploy.status: %w", err)
	}
	hrName := deployHRName(app, args.Env)

	// Source of truth for "what we asked for" — the HR YAML in Gitea.
	requested, _, existed, err := readExistingHRFromGitea(ctx, env, env.OrgID, app, args.Env)
	if err != nil {
		return nil, err
	}
	if !existed {
		return map[string]any{
			"status":    "NotDeployed",
			"env":       args.Env,
			"app":       app,
			"namespace": env.OrgID,
			"hr_name":   hrName,
			"note":      "No HR for this app+env in the tenant Gitea repo yet — call sandbox.deploy." + args.Env + " first.",
		}, nil
	}
	requestedImage := ""
	rollbackTarget := ""
	if requested != nil {
		anns := requested.GetAnnotations()
		requestedImage = anns[deployRequestedImageAnnotation]
		rollbackTarget = anns[deployLastImageAnnotation]
		if requestedImage == "" {
			_, _, _, requestedImage = hrReady(requested)
		}
	}

	// Live state from the cluster — Flux's `observed`.
	observed, observedExists, err := readObservedHRFromCluster(ctx, env, hrName)
	if err != nil {
		// Cluster read failure is non-fatal — still surface what
		// Gitea says so the agent isn't blocked by a transient
		// kube-apiserver hiccup.
		return map[string]any{
			"status":          "PendingReconcile",
			"env":             args.Env,
			"app":             app,
			"namespace":       env.OrgID,
			"hr_name":         hrName,
			"requested_image": requestedImage,
			"rollback_target": rollbackTarget,
			"cluster_error":   err.Error(),
			"note":            "Gitea has the HR; cluster read failed (transient). Retry status in a few seconds.",
		}, nil
	}
	if !observedExists {
		return map[string]any{
			"status":          "PendingReconcile",
			"env":             args.Env,
			"app":             app,
			"namespace":       env.OrgID,
			"hr_name":         hrName,
			"requested_image": requestedImage,
			"rollback_target": rollbackTarget,
			"note":             "HR written but Flux has not reconciled it into the cluster yet. Wait for the Kustomization interval.",
		}, nil
	}
	ready, reason, message, observedImage := hrReady(observed)
	rolloutStatus := "Reconciling"
	if ready && observedImage == requestedImage {
		rolloutStatus = "Ready"
	} else if ready && observedImage != requestedImage {
		rolloutStatus = "ReadyStale"
	}
	return map[string]any{
		"status":          rolloutStatus,
		"env":             args.Env,
		"app":             app,
		"namespace":       env.OrgID,
		"hr_name":         hrName,
		"ready":           ready,
		"reason":          reason,
		"message":         message,
		"requested_image": requestedImage,
		"observed_image":  observedImage,
		"rollback_target": rollbackTarget,
	}, nil
}

func schemaSandboxDeployEnv() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"env"},
		"properties": map[string]any{
			"env": map[string]any{
				"type":        "string",
				"enum":        []string{deployEnvStaging, deployEnvProduction},
				"description": "Deployment environment slug — `staging` or `production`.",
			},
			"app": map[string]any{
				"type":        "string",
				"description": "App slug. Defaults to SANDBOX_ID when omitted.",
				"pattern":     deployAppNameRE.String(),
			},
		},
		"additionalProperties": false,
	}
}

// --- sandbox.deploy.rollback -------------------------------------------

func sandboxDeployRollback(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxDeployEnvArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.deploy.rollback: invalid arguments: %w", err)
	}
	if err := deployValidEnv(args.Env); err != nil {
		return nil, fmt.Errorf("sandbox.deploy.rollback: %w", err)
	}
	// Production rollbacks must also clear the production capability
	// gate — a staging-only operator should not be able to revert a
	// production push.
	if args.Env == deployEnvProduction {
		if err := requireProductionCapability(ctx); err != nil {
			return nil, err
		}
	}
	env, err := requireSandboxDeployScope(ctx)
	if err != nil {
		return nil, err
	}
	app, err := deployAppSlug(env, args.App)
	if err != nil {
		return nil, fmt.Errorf("sandbox.deploy.rollback: %w", err)
	}
	prior, _, existed, err := readExistingHRFromGitea(ctx, env, env.OrgID, app, args.Env)
	if err != nil {
		return nil, err
	}
	if !existed || prior == nil {
		return map[string]any{
			"status":    "NotDeployed",
			"env":       args.Env,
			"app":       app,
			"namespace": env.OrgID,
			"note":      "No HR to rollback — sandbox.deploy." + args.Env + " has never been called.",
		}, nil
	}
	anns := prior.GetAnnotations()
	rollbackImage := anns[deployLastImageAnnotation]
	currentImage := anns[deployRequestedImageAnnotation]
	if rollbackImage == "" {
		return map[string]any{
			"status":         "NoPrior",
			"env":            args.Env,
			"app":            app,
			"namespace":      env.OrgID,
			"current_image":  currentImage,
			"note":           "Latest deploy has no recorded previous image (first deploy ever, or operator hand-edited). Re-run sandbox.deploy." + args.Env + " with the desired image instead.",
		}, nil
	}
	// Rewrite the HR YAML with the rollback target as the new
	// requested image. After the rewrite, the prior `last-deployed`
	// becomes the CURRENT image (so a subsequent rollback re-flips
	// back — convenient when an operator wants to A/B).
	res, err := deployUpsertHR(ctx, env, args.Env, app, rollbackImage)
	if err != nil {
		return nil, err
	}
	res["status"] = "Rolledback"
	res["from_image"] = currentImage
	res["to_image"] = rollbackImage
	res["note"] = "HR reverted to previous image in tenant Gitea repo. Flux reconciles on next interval; poll sandbox.deploy.status until ready=true."
	return res, nil
}
