// sandbox_preview.go — real handlers for sandbox.preview.* (per-PR
// preview Deployments).
//
// Scope (architecture.md §3 + §7): when an agent opens a PR from inside
// a Sandbox, the operator usually wants a live URL pointing at the PR's
// build of the app — `pr-1483.eventforge.sb-emrah.acme.omani.homes` —
// without provoking the marketplace BYOD pipeline or pushing through
// the production Helm chart. The Sandbox provides ON-DEMAND
// Deployment + Service + HTTPRoute triples in the Sandbox's OWN
// namespace, gated by a `openova.io/preview-pr=<num>` label so the
// teardown / list / status calls never cross into operator-managed
// workloads in the same namespace.
//
// Tools shipped here (Wave 12):
//
//   - sandbox.preview.create   {pr_number, image, repo?}
//   - sandbox.preview.list                          → managed-by-only items
//   - sandbox.preview.status   {pr_number}          → Deployment + Pod state
//   - sandbox.preview.teardown {pr_number}          → DELETE D/S/R triple
//   - sandbox.preview.rebuild  {pr_number, image}   → patch image + restart
//
// All five are scoped to env.SandboxNamespace — the agent CANNOT pass a
// namespace argument and the controller's RoleBinding limits the
// Sandbox SA to its own namespace anyway. The managed-by guard adds a
// second layer of defence so a malicious sandbox cannot teardown the
// pty-server or the openova-sandbox-mcp Deployment by guessing names.
//
// Hostname pattern: `pr-<num>.<app>.sandbox.<sov-fqdn>` — the `sandbox.`
// host is already routed to the Sovereign's Cilium Gateway via the
// pty-server HTTPRoute the controller renders (manifests.go
// httpRouteTemplate), so the Sandbox's preview HTTPRoute attaches to
// the same Gateway and surfaces under a deeper subdomain.
//
// Auth: same shape as sandbox.db.* / sandbox.auth.* / sandbox.secrets.*
// — claims.OrgID must match env.OrgID, RequiredCapability =
// "sandbox.preview".
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Preview resource GVRs. Centralised so a future apps/v1 → apps/v2 or
// gateway.networking.k8s.io/v1 → v1beta1 swap is a one-line edit.
var (
	previewDeploymentGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	previewServiceGVR    = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}
	previewHTTPRouteGVR  = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	previewPodGVR        = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
)

// previewPRLabel marks every preview triple the MCP server creates with
// the PR number so list/status/teardown can address the triple by PR
// rather than by guessing names.
const (
	previewPRLabel    = "openova.io/preview-pr"
	previewAppLabel   = "openova.io/preview-app"
	previewSandboxID  = "openova.io/sandbox-id"
	previewRepoLabel  = "openova.io/preview-repo"
	previewImageAnnot = "openova.io/preview-image"
)

// previewAppNameRE constrains the `app` slug we derive from the optional
// `repo` argument (or default to env.SandboxID). The slug appears in
// the rendered hostname so it MUST be a valid RFC-1123 DNS label and
// short enough that `pr-<num>.<app>.sandbox.<sov-fqdn>` stays inside
// the 253-byte DNS-name limit + each label inside the 63-byte limit.
var previewAppNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)

// previewImageRE is a lax check: container image references vary widely
// (tag, digest, with-or-without registry); we reject only the
// obviously-broken forms (empty / whitespace / shell-meta). Substantive
// validation is the operator's responsibility — kubelet will surface
// pull errors via sandbox.preview.status anyway.
var previewImageRE = regexp.MustCompile(`^[A-Za-z0-9._\-/:@]{1,512}$`)

// previewPort is the canonical containerPort the Deployment + Service
// agree on. Apps that listen on a different port need a future
// `port` argument or an env override — for Wave 12 we standardise on
// 8080 (matches the convention every catalyst org-service uses).
const previewPort int32 = 8080

// previewReplicas is the default replica count. Preview Deployments are
// per-PR ephemerals — a single replica is enough. Future waves may
// expose a `replicas` argument.
const previewReplicas int32 = 1

// --- argument types ----------------------------------------------------

type sandboxPreviewCreateArgs struct {
	// PRNumber — the Gitea PR number this preview tracks. Used to
	// derive the canonical resource name `preview-pr-<num>` and the
	// `openova.io/preview-pr` label.
	PRNumber int `json:"pr_number"`

	// Image — the container image the preview Pod runs. The agent is
	// expected to push this image to the Sovereign's Harbor (or any
	// registry the cluster can pull from) BEFORE calling create.
	Image string `json:"image"`

	// Repo — the Gitea repo slug (`<org>/<name>`) the preview is built
	// from. Optional — when omitted we fall back to env.SandboxID. The
	// repo name (after the slash) is used as the `<app>` slug in the
	// rendered hostname.
	Repo string `json:"repo,omitempty"`
}

type sandboxPreviewPRNumberArgs struct {
	PRNumber int `json:"pr_number"`
}

type sandboxPreviewRebuildArgs struct {
	PRNumber int    `json:"pr_number"`
	Image    string `json:"image"`
}

// --- helpers -----------------------------------------------------------

// validatePRNumber gates the agent's input against the canonical
// gitea-issue numbering scheme (positive, fits in an int32). Catches
// 0 / negative / overflow before any K8s round-trip.
func validatePRNumber(n int) error {
	if n <= 0 {
		return errors.New("`pr_number` must be a positive integer")
	}
	if n > 1_000_000 {
		return errors.New("`pr_number` is suspiciously large (>1e6); rejecting")
	}
	return nil
}

// previewAppSlug returns the `<app>` segment used in resource names +
// hostname. Preference order: explicit `repo` arg (basename), then
// env.SandboxID (lowercased). Empty after both → error in the caller.
func previewAppSlug(env *Env, repo string) (string, error) {
	if repo = strings.TrimSpace(repo); repo != "" {
		// Repo may be "org/name" or just "name" — strip the org segment.
		if idx := strings.LastIndex(repo, "/"); idx >= 0 {
			repo = repo[idx+1:]
		}
		slug := strings.ToLower(repo)
		if !previewAppNameRE.MatchString(slug) {
			return "", fmt.Errorf("`repo` basename %q must match %s", slug, previewAppNameRE.String())
		}
		return slug, nil
	}
	if id := strings.ToLower(strings.TrimSpace(env.SandboxID)); id != "" {
		if !previewAppNameRE.MatchString(id) {
			return "", fmt.Errorf("derived app slug %q from SANDBOX_ID must match %s", id, previewAppNameRE.String())
		}
		return id, nil
	}
	return "", errors.New("`repo` not supplied and SANDBOX_ID is empty (cannot derive app slug)")
}

// previewResourceName is the canonical name shared by Deployment +
// Service + HTTPRoute for one PR. Stable so list/status/teardown can
// address the triple deterministically.
func previewResourceName(prNumber int) string {
	return fmt.Sprintf("preview-pr-%d", prNumber)
}

// previewHostname renders `pr-<num>.<app>.sandbox.<sov-fqdn>`. The
// `sandbox.` host is already routed via the pty-server HTTPRoute the
// sandbox-controller renders (manifests.go httpRouteTemplate); previews
// surface at deeper subdomains under the same Gateway.
func previewHostname(env *Env, app string, prNumber int) string {
	return fmt.Sprintf("pr-%d.%s.sandbox.%s", prNumber, app, env.SovereignFQDN)
}

// previewLabels returns the canonical label map every preview resource
// inherits. Always includes the managed-by guard so list/teardown can
// safely filter by `openova.io/managed-by=openova-sandbox-mcp`.
func previewLabels(env *Env, prNumber int, app, repo string) map[string]any {
	labels := map[string]any{
		cnpgManagedByLabel: cnpgManagedByValue,
		previewPRLabel:     strconv.Itoa(prNumber),
		previewAppLabel:    app,
	}
	if env.SandboxID != "" {
		labels[previewSandboxID] = env.SandboxID
	}
	if repo != "" {
		// Repo may carry `/`; the label value must be DNS-safe — replace.
		labels[previewRepoLabel] = strings.ReplaceAll(repo, "/", "-")
	}
	return labels
}

// buildPreviewDeployment renders the apps/v1 Deployment Unstructured
// for one PR. Kept in its own function so unit tests can assert the
// shape without a live API server.
func buildPreviewDeployment(env *Env, prNumber int, app, image, repo string) *unstructured.Unstructured {
	name := previewResourceName(prNumber)
	labels := previewLabels(env, prNumber, app, repo)
	// Selector + pod-template labels: a strict subset of the metadata
	// labels (just enough to match the Service selector, kept stable so
	// rebuild() doesn't trip immutable-selector errors).
	podLabels := map[string]any{
		previewPRLabel:  strconv.Itoa(prNumber),
		previewAppLabel: app,
	}
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]any{
				"name":      name,
				"namespace": env.SandboxNamespace,
				"labels":    labels,
				"annotations": map[string]any{
					"openova.io/created-by": "sandbox.preview.create",
					previewImageAnnot:       image,
				},
			},
			"spec": map[string]any{
				"replicas": int64(previewReplicas),
				"selector": map[string]any{
					"matchLabels": podLabels,
				},
				"template": map[string]any{
					"metadata": map[string]any{
						"labels": podLabels,
					},
					"spec": map[string]any{
						"containers": []any{
							map[string]any{
								"name":  "app",
								"image": image,
								"ports": []any{
									map[string]any{
										"containerPort": int64(previewPort),
										"protocol":      "TCP",
									},
								},
								// Inject the PR number as PORT so apps
								// listening on $PORT pick up the canonical
								// containerPort by convention.
								"env": []any{
									map[string]any{"name": "PORT", "value": strconv.Itoa(int(previewPort))},
									map[string]any{"name": "PREVIEW_PR_NUMBER", "value": strconv.Itoa(prNumber)},
								},
							},
						},
					},
				},
			},
		},
	}
}

// buildPreviewService renders the ClusterIP Service that fronts the
// preview Pod set.
func buildPreviewService(env *Env, prNumber int, app, repo string) *unstructured.Unstructured {
	name := previewResourceName(prNumber)
	labels := previewLabels(env, prNumber, app, repo)
	selector := map[string]any{
		previewPRLabel:  strconv.Itoa(prNumber),
		previewAppLabel: app,
	}
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Service",
			"metadata": map[string]any{
				"name":      name,
				"namespace": env.SandboxNamespace,
				"labels":    labels,
				"annotations": map[string]any{
					"openova.io/created-by": "sandbox.preview.create",
				},
			},
			"spec": map[string]any{
				"type":     "ClusterIP",
				"selector": selector,
				"ports": []any{
					map[string]any{
						"name":       "http",
						"port":       int64(80),
						"targetPort": int64(previewPort),
						"protocol":   "TCP",
					},
				},
			},
		},
	}
}

// buildPreviewHTTPRoute renders the HTTPRoute attaching the preview to
// the canonical Cilium Gateway. Matches the same parentRef shape the
// sandbox-controller already uses for pty-server (manifests.go
// httpRouteTemplate) so the Gateway listener picks it up under the
// existing wildcard *.<sov-fqdn> HTTPS listener.
func buildPreviewHTTPRoute(env *Env, prNumber int, app, repo string) *unstructured.Unstructured {
	name := previewResourceName(prNumber)
	labels := previewLabels(env, prNumber, app, repo)
	hostname := previewHostname(env, app, prNumber)
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "HTTPRoute",
			"metadata": map[string]any{
				"name":      name,
				"namespace": env.SandboxNamespace,
				"labels":    labels,
				"annotations": map[string]any{
					"openova.io/created-by": "sandbox.preview.create",
				},
			},
			"spec": map[string]any{
				"parentRefs": []any{
					map[string]any{
						"name":      "cilium-gateway",
						"namespace": "kube-system",
					},
				},
				"hostnames": []any{hostname},
				"rules": []any{
					map[string]any{
						"matches": []any{
							map[string]any{
								"path": map[string]any{
									"type":  "PathPrefix",
									"value": "/",
								},
							},
						},
						"backendRefs": []any{
							map[string]any{
								"name": name,
								"port": int64(80),
							},
						},
					},
				},
			},
		},
	}
}

// requirePreviewScope is the canonical gate every sandbox.preview.*
// handler runs first: env wired, SandboxNamespace set, SovereignFQDN
// set (the hostname rendering needs it).
func requirePreviewScope(ctx context.Context) (*Env, string, error) {
	env, ns, err := requireSandboxNS(ctx)
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(env.SovereignFQDN) == "" {
		return nil, "", errors.New("sandbox.preview: env missing SANDBOX_SOVEREIGN_FQDN (cannot render preview hostname)")
	}
	return env, ns, nil
}

// previewSelectorLabel is the canonical LabelSelector list/teardown use
// to fetch ONLY MCP-managed previews. Combining managed-by with
// preview-pr keeps us safe even if a future controller writes a Deployment
// labelled with one but not the other.
func previewSelectorLabel(prNumber int) string {
	sel := cnpgManagedByLabel + "=" + cnpgManagedByValue
	if prNumber > 0 {
		sel += "," + previewPRLabel + "=" + strconv.Itoa(prNumber)
	} else {
		// list-all → managed-by + preview-pr exists (any value).
		sel += "," + previewPRLabel
	}
	return sel
}

// summarizeDeployment distils Deployment.status — Pod-replica counts
// and the bottom-line Available condition. Other tools (sandbox.db.get)
// echo only the conditions table; we surface counts inline so the
// agent can poll "ready" without a second hop.
func summarizeDeployment(obj map[string]any) map[string]any {
	st, _, _ := unstructured.NestedMap(obj, "status")
	if st == nil {
		return map[string]any{"phase": "Pending"}
	}
	out := map[string]any{}
	for _, k := range []string{
		"replicas", "readyReplicas", "availableReplicas",
		"updatedReplicas", "unavailableReplicas", "observedGeneration",
	} {
		if v, ok := st[k]; ok {
			out[k] = v
		}
	}
	if conds, found, _ := unstructured.NestedSlice(st, "conditions"); found {
		for _, c := range conds {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if cm["type"] == "Available" {
				out["available"] = cm
			}
			if cm["type"] == "Progressing" {
				out["progressing"] = cm
			}
		}
	}
	return out
}

// summarizePod distils Pod.status — phase + the per-container
// readiness + waiting reason. The agent uses this to spot ImagePullErr
// / CrashLoopBackOff without parsing the full Pod object.
func summarizePod(obj map[string]any) map[string]any {
	st, _, _ := unstructured.NestedMap(obj, "status")
	if st == nil {
		return map[string]any{"phase": "Pending"}
	}
	out := map[string]any{
		"phase": st["phase"],
	}
	if cs, found, _ := unstructured.NestedSlice(st, "containerStatuses"); found {
		simplified := make([]map[string]any, 0, len(cs))
		for _, c := range cs {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			entry := map[string]any{
				"name":         cm["name"],
				"ready":        cm["ready"],
				"restartCount": cm["restartCount"],
			}
			if stateMap, ok := cm["state"].(map[string]any); ok {
				for k, v := range stateMap {
					entry["state"] = map[string]any{k: v}
				}
			}
			simplified = append(simplified, entry)
		}
		out["containers"] = simplified
	}
	return out
}

// --- sandbox.preview.create --------------------------------------------

func sandboxPreviewCreate(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxPreviewCreateArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.preview.create: invalid arguments: %w", err)
	}
	if err := validatePRNumber(args.PRNumber); err != nil {
		return nil, fmt.Errorf("sandbox.preview.create: %w", err)
	}
	args.Image = strings.TrimSpace(args.Image)
	if !previewImageRE.MatchString(args.Image) {
		return nil, fmt.Errorf("sandbox.preview.create: `image` must match %s", previewImageRE.String())
	}
	env, ns, err := requirePreviewScope(ctx)
	if err != nil {
		return nil, err
	}
	app, err := previewAppSlug(env, args.Repo)
	if err != nil {
		return nil, fmt.Errorf("sandbox.preview.create: %w", err)
	}
	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}

	name := previewResourceName(args.PRNumber)
	hostname := previewHostname(env, app, args.PRNumber)
	url := "https://" + hostname

	// Order: Deployment → Service → HTTPRoute. If a later create fails
	// we attempt to clean up earlier-created resources so a half-created
	// triple doesn't linger. Best-effort; the caller can always
	// teardown explicitly to converge.
	deployment := buildPreviewDeployment(env, args.PRNumber, app, args.Image, args.Repo)
	createdDep, err := client.Resource(previewDeploymentGVR).Namespace(ns).
		Create(ctx, deployment, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("sandbox.preview.create: preview for PR %d already exists (use sandbox.preview.rebuild to update image)", args.PRNumber)
		}
		return nil, fmt.Errorf("sandbox.preview.create: create Deployment %s/%s: %w", ns, name, err)
	}

	service := buildPreviewService(env, args.PRNumber, app, args.Repo)
	createdSvc, err := client.Resource(previewServiceGVR).Namespace(ns).
		Create(ctx, service, metav1.CreateOptions{})
	if err != nil {
		// Best-effort rollback of the Deployment.
		_ = client.Resource(previewDeploymentGVR).Namespace(ns).
			Delete(ctx, name, metav1.DeleteOptions{})
		return nil, fmt.Errorf("sandbox.preview.create: create Service %s/%s: %w (Deployment rolled back)", ns, name, err)
	}

	route := buildPreviewHTTPRoute(env, args.PRNumber, app, args.Repo)
	createdRoute, err := client.Resource(previewHTTPRouteGVR).Namespace(ns).
		Create(ctx, route, metav1.CreateOptions{})
	if err != nil {
		_ = client.Resource(previewServiceGVR).Namespace(ns).
			Delete(ctx, name, metav1.DeleteOptions{})
		_ = client.Resource(previewDeploymentGVR).Namespace(ns).
			Delete(ctx, name, metav1.DeleteOptions{})
		return nil, fmt.Errorf("sandbox.preview.create: create HTTPRoute %s/%s: %w (Deployment + Service rolled back)", ns, name, err)
	}

	return map[string]any{
		"status":    "Provisioning",
		"pr_number": args.PRNumber,
		"app":       app,
		"image":     args.Image,
		"hostname":  hostname,
		"url":       url,
		"namespace": ns,
		"resources": map[string]any{
			"deployment": map[string]any{
				"name":            createdDep.GetName(),
				"resourceVersion": createdDep.GetResourceVersion(),
				"uid":             string(createdDep.GetUID()),
			},
			"service": map[string]any{
				"name":            createdSvc.GetName(),
				"resourceVersion": createdSvc.GetResourceVersion(),
				"uid":             string(createdSvc.GetUID()),
			},
			"httproute": map[string]any{
				"name":            createdRoute.GetName(),
				"resourceVersion": createdRoute.GetResourceVersion(),
				"uid":             string(createdRoute.GetUID()),
			},
		},
		"note": "Preview Deployment created. Poll `sandbox.preview.status pr_number=<n>` until status.readyReplicas == 1; the URL is reachable once the Gateway picks up the HTTPRoute (~5-15s after Programmed condition flips).",
	}, nil
}

func schemaSandboxPreviewCreate() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"pr_number", "image"},
		"properties": map[string]any{
			"pr_number": map[string]any{
				"type":        "integer",
				"description": "Gitea PR number this preview tracks. Used as the canonical key for list/status/rebuild/teardown.",
				"minimum":     1,
				"maximum":     1_000_000,
			},
			"image": map[string]any{
				"type":        "string",
				"description": "Container image the preview Pod runs (push to Harbor first). Standard reference: `<registry>/<repo>:<tag>` or `<...>@sha256:<digest>`.",
				"pattern":     previewImageRE.String(),
			},
			"repo": map[string]any{
				"type":        "string",
				"description": "Optional Gitea repo slug (`<org>/<name>`). Basename becomes the `<app>` segment in the hostname. Defaults to SANDBOX_ID when omitted.",
			},
		},
		"additionalProperties": false,
	}
}

// --- sandbox.preview.list ----------------------------------------------

func sandboxPreviewList(ctx context.Context, _ json.RawMessage) (any, error) {
	_, ns, err := requirePreviewScope(ctx)
	if err != nil {
		return nil, err
	}
	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}
	list, err := client.Resource(previewDeploymentGVR).Namespace(ns).
		List(ctx, metav1.ListOptions{LabelSelector: previewSelectorLabel(0)})
	if err != nil {
		return nil, fmt.Errorf("sandbox.preview.list: %w", err)
	}
	items := make([]map[string]any, 0, len(list.Items))
	for i := range list.Items {
		obj := list.Items[i].Object
		labels := list.Items[i].GetLabels()
		annotations := list.Items[i].GetAnnotations()
		prStr := labels[previewPRLabel]
		prNum, _ := strconv.Atoi(prStr)
		app := labels[previewAppLabel]
		repo := strings.ReplaceAll(labels[previewRepoLabel], "-", "/")
		image := annotations[previewImageAnnot]
		items = append(items, map[string]any{
			"pr_number":         prNum,
			"app":               app,
			"repo":              repo,
			"image":             image,
			"name":              list.Items[i].GetName(),
			"namespace":         ns,
			"creationTimestamp": list.Items[i].GetCreationTimestamp().Time.UTC().Format(time.RFC3339),
			"status":            summarizeDeployment(obj),
		})
	}
	return map[string]any{
		"namespace": ns,
		"count":     len(items),
		"items":     items,
	}, nil
}

// --- sandbox.preview.status --------------------------------------------

func sandboxPreviewStatus(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxPreviewPRNumberArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.preview.status: invalid arguments: %w", err)
	}
	if err := validatePRNumber(args.PRNumber); err != nil {
		return nil, fmt.Errorf("sandbox.preview.status: %w", err)
	}
	env, ns, err := requirePreviewScope(ctx)
	if err != nil {
		return nil, err
	}
	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}
	name := previewResourceName(args.PRNumber)

	dep, err := client.Resource(previewDeploymentGVR).Namespace(ns).
		Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return map[string]any{
				"status":    "NotFound",
				"pr_number": args.PRNumber,
				"namespace": ns,
				"note":      "No preview Deployment for this PR. Use sandbox.preview.create first.",
			}, nil
		}
		return nil, fmt.Errorf("sandbox.preview.status: GET Deployment %s/%s: %w", ns, name, err)
	}
	// Defence in depth: refuse to surface a non-Sandbox-managed Deployment.
	if dep.GetLabels()[cnpgManagedByLabel] != cnpgManagedByValue {
		return nil, fmt.Errorf("sandbox.preview.status: Deployment %s/%s is not Sandbox-managed (no %s=%s label)",
			ns, name, cnpgManagedByLabel, cnpgManagedByValue)
	}

	labels := dep.GetLabels()
	annotations := dep.GetAnnotations()
	app := labels[previewAppLabel]
	repo := strings.ReplaceAll(labels[previewRepoLabel], "-", "/")
	image := annotations[previewImageAnnot]

	// Fetch Pods so the agent can see ImagePullErr / CrashLoopBackOff.
	podSelector := fmt.Sprintf("%s=%d,%s=%s",
		previewPRLabel, args.PRNumber, previewAppLabel, app)
	pods, err := client.Resource(previewPodGVR).Namespace(ns).
		List(ctx, metav1.ListOptions{LabelSelector: podSelector})
	if err != nil {
		return nil, fmt.Errorf("sandbox.preview.status: LIST Pods: %w", err)
	}
	podSummaries := make([]map[string]any, 0, len(pods.Items))
	for i := range pods.Items {
		obj := pods.Items[i].Object
		entry := summarizePod(obj)
		entry["name"] = pods.Items[i].GetName()
		podSummaries = append(podSummaries, entry)
	}

	hostname := previewHostname(env, app, args.PRNumber)
	return map[string]any{
		"pr_number": args.PRNumber,
		"app":       app,
		"repo":      repo,
		"image":     image,
		"hostname":  hostname,
		"url":       "https://" + hostname,
		"namespace": ns,
		"deployment": map[string]any{
			"name":            dep.GetName(),
			"resourceVersion": dep.GetResourceVersion(),
			"uid":             string(dep.GetUID()),
			"status":          summarizeDeployment(dep.Object),
		},
		"pods": podSummaries,
	}, nil
}

// --- sandbox.preview.teardown ------------------------------------------

func sandboxPreviewTeardown(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxPreviewPRNumberArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.preview.teardown: invalid arguments: %w", err)
	}
	if err := validatePRNumber(args.PRNumber); err != nil {
		return nil, fmt.Errorf("sandbox.preview.teardown: %w", err)
	}
	_, ns, err := requirePreviewScope(ctx)
	if err != nil {
		return nil, err
	}
	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}
	name := previewResourceName(args.PRNumber)

	// Verify managed-by on the Deployment before deleting any of the
	// three resources. Refusing here keeps a malicious agent from
	// teardown'ing a hand-rolled Deployment that happens to be named
	// `preview-pr-<n>` in the same namespace.
	dep, err := client.Resource(previewDeploymentGVR).Namespace(ns).
		Get(ctx, name, metav1.GetOptions{})
	depMissing := apierrors.IsNotFound(err)
	if err != nil && !depMissing {
		return nil, fmt.Errorf("sandbox.preview.teardown: lookup Deployment %s/%s: %w", ns, name, err)
	}
	if !depMissing && dep.GetLabels()[cnpgManagedByLabel] != cnpgManagedByValue {
		return nil, fmt.Errorf("sandbox.preview.teardown: refusing to delete non-Sandbox Deployment %s/%s", ns, name)
	}

	// Foreground deletion for the Deployment so ReplicaSets + Pods are
	// finalised before the API call returns. Service + HTTPRoute have
	// no children so default propagation is fine.
	policy := metav1.DeletePropagationForeground
	deletedResources := []string{}
	skipped := []string{}

	if !depMissing {
		if err := client.Resource(previewDeploymentGVR).Namespace(ns).
			Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &policy}); err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("sandbox.preview.teardown: delete Deployment: %w", err)
		}
		deletedResources = append(deletedResources, "deployment/"+name)
	} else {
		skipped = append(skipped, "deployment/"+name)
	}

	if err := client.Resource(previewServiceGVR).Namespace(ns).
		Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("sandbox.preview.teardown: delete Service: %w", err)
		}
		skipped = append(skipped, "service/"+name)
	} else {
		deletedResources = append(deletedResources, "service/"+name)
	}

	if err := client.Resource(previewHTTPRouteGVR).Namespace(ns).
		Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("sandbox.preview.teardown: delete HTTPRoute: %w", err)
		}
		skipped = append(skipped, "httproute/"+name)
	} else {
		deletedResources = append(deletedResources, "httproute/"+name)
	}

	status := "Deleted"
	if len(deletedResources) == 0 {
		status = "NotFound"
	} else if len(skipped) > 0 {
		status = "PartiallyDeleted"
	}
	return map[string]any{
		"status":    status,
		"pr_number": args.PRNumber,
		"namespace": ns,
		"deleted":   deletedResources,
		"skipped":   skipped,
		"note":      "Cascade is foreground on the Deployment; Service + HTTPRoute deletes are immediate.",
	}, nil
}

// --- sandbox.preview.rebuild -------------------------------------------

func sandboxPreviewRebuild(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxPreviewRebuildArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.preview.rebuild: invalid arguments: %w", err)
	}
	if err := validatePRNumber(args.PRNumber); err != nil {
		return nil, fmt.Errorf("sandbox.preview.rebuild: %w", err)
	}
	args.Image = strings.TrimSpace(args.Image)
	if !previewImageRE.MatchString(args.Image) {
		return nil, fmt.Errorf("sandbox.preview.rebuild: `image` must match %s", previewImageRE.String())
	}
	_, ns, err := requirePreviewScope(ctx)
	if err != nil {
		return nil, err
	}
	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}
	name := previewResourceName(args.PRNumber)

	dep, err := client.Resource(previewDeploymentGVR).Namespace(ns).
		Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("sandbox.preview.rebuild: no preview for PR %d (use sandbox.preview.create first)", args.PRNumber)
		}
		return nil, fmt.Errorf("sandbox.preview.rebuild: GET Deployment %s/%s: %w", ns, name, err)
	}
	if dep.GetLabels()[cnpgManagedByLabel] != cnpgManagedByValue {
		return nil, fmt.Errorf("sandbox.preview.rebuild: refusing to mutate non-Sandbox Deployment %s/%s", ns, name)
	}

	// Patch image at spec.template.spec.containers[0].image. The
	// container array shape was set by buildPreviewDeployment — a single
	// container named `app`. Future plans with multi-container Pods
	// need a `container` argument.
	containers, found, err := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
	if err != nil || !found || len(containers) == 0 {
		return nil, fmt.Errorf("sandbox.preview.rebuild: Deployment %s/%s has no containers", ns, name)
	}
	container0, ok := containers[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("sandbox.preview.rebuild: containers[0] has unexpected type %T", containers[0])
	}
	previousImage, _ := container0["image"].(string)
	container0["image"] = args.Image
	containers[0] = container0
	if err := unstructured.SetNestedSlice(dep.Object, containers, "spec", "template", "spec", "containers"); err != nil {
		return nil, fmt.Errorf("sandbox.preview.rebuild: set containers: %w", err)
	}

	// Bump the pod-template annotation `kubectl.kubernetes.io/restartedAt`
	// (the canonical kubectl-rollout-restart marker) so a rebuild with
	// the SAME tag (`:latest`, `:main`) still triggers a rollout.
	annotations, _, _ := unstructured.NestedMap(dep.Object, "spec", "template", "metadata", "annotations")
	if annotations == nil {
		annotations = map[string]any{}
	}
	annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().UTC().Format(time.RFC3339)
	if err := unstructured.SetNestedMap(dep.Object, annotations, "spec", "template", "metadata", "annotations"); err != nil {
		return nil, fmt.Errorf("sandbox.preview.rebuild: set restart annotation: %w", err)
	}

	// Also update the top-level image annotation so list/status reflect
	// the new image immediately, not after the Pod comes Ready.
	topAnnotations := dep.GetAnnotations()
	if topAnnotations == nil {
		topAnnotations = map[string]string{}
	}
	topAnnotations[previewImageAnnot] = args.Image
	dep.SetAnnotations(topAnnotations)

	updated, err := client.Resource(previewDeploymentGVR).Namespace(ns).
		Update(ctx, dep, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("sandbox.preview.rebuild: UPDATE Deployment %s/%s: %w", ns, name, err)
	}
	return map[string]any{
		"status":          "RolloutTriggered",
		"pr_number":       args.PRNumber,
		"namespace":       ns,
		"previous_image":  previousImage,
		"new_image":       args.Image,
		"resourceVersion": updated.GetResourceVersion(),
		"note":            "Deployment image patched + restartedAt annotation bumped. Poll sandbox.preview.status until status.readyReplicas == 1 with the new spec.",
	}, nil
}

func schemaSandboxPreviewPRNumber() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"pr_number"},
		"properties": map[string]any{
			"pr_number": map[string]any{
				"type":    "integer",
				"minimum": 1,
				"maximum": 1_000_000,
			},
		},
		"additionalProperties": false,
	}
}

func schemaSandboxPreviewRebuild() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"pr_number", "image"},
		"properties": map[string]any{
			"pr_number": map[string]any{
				"type":    "integer",
				"minimum": 1,
				"maximum": 1_000_000,
			},
			"image": map[string]any{
				"type":        "string",
				"description": "New container image. Same shape as sandbox.preview.create.image.",
				"pattern":     previewImageRE.String(),
			},
		},
		"additionalProperties": false,
	}
}
