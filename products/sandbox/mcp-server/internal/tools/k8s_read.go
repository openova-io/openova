// k8s_read.go — real handlers for k8s.read.get / k8s.read.list /
// k8s.read.watch.
//
// Scope: READ-ONLY into the Org's vcluster (via the Sandbox pod's
// ServiceAccount; never the host cluster). Per architecture.md §3 +
// the Wave-8 hard rule "READ-ONLY clusters (NO kubectl apply via tools
// — k8s.write.* stays stubbed)".
//
// Client model: we mirror the canonical pattern used by
// products/catalyst/bootstrap/api/internal/k8scache/factory.go — build a
// dynamic.Interface either from an explicit KUBECONFIG path or from
// rest.InClusterConfig() and call GVR.Namespace(ns).{Get,List,Watch}.
// The Factory itself is the right backend when this server runs INSIDE
// catalyst-api's process (it owns cluster bookkeeping); the MCP server
// runs as a pod sidecar with its OWN SA in the Org vcluster, so it
// builds the dynamic client directly. The wrapping style + result
// shape (post-decode JSON of the Unstructured object) matches what the
// catalyst-api's `/api/v1/k8s/...` SSE feed publishes so agents see the
// same wire shape via either path.
package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// dynCache memoises the dynamic.Interface so we don't rebuild the
// REST config + HTTP client on every tool call. The cache is process-
// wide; the env (KubeconfigPath or in-cluster) is fixed at start.
var (
	dynOnce sync.Once
	dynVal  dynamic.Interface
	dynErr  error
)

func dynamicClient(ctx context.Context) (dynamic.Interface, error) {
	env := EnvFrom(ctx)
	if env == nil {
		return nil, errors.New("k8s.read: tool dispatch missing Env on context")
	}
	dynOnce.Do(func() {
		var cfg *rest.Config
		if env.KubeconfigPath != "" {
			// Explicit kubeconfig — used in dev / when the controller
			// mounts the vcluster kubeconfig into the Sandbox pod.
			cfg, dynErr = clientcmd.BuildConfigFromFlags("", env.KubeconfigPath)
		} else {
			// In-cluster — the Sandbox pod's SA token at
			// /var/run/secrets/kubernetes.io/serviceaccount/.
			cfg, dynErr = rest.InClusterConfig()
		}
		if dynErr != nil {
			dynErr = fmt.Errorf("k8s.read: build REST config: %w", dynErr)
			return
		}
		// 15s per-call timeout — read-only ops should complete fast.
		cfg.Timeout = 15 * time.Second
		dynVal, dynErr = dynamic.NewForConfig(cfg)
		if dynErr != nil {
			dynErr = fmt.Errorf("k8s.read: dynamic.NewForConfig: %w", dynErr)
		}
	})
	return dynVal, dynErr
}

// gvrSelector binds the wire identifier (a Kubernetes GroupVersionResource)
// to the request envelope. We use GVR (not GVK) because the dynamic client
// natively keys by resource; the agent passes resource-plural so we don't
// run a per-call RESTMapper lookup.
type gvrSelector struct {
	// Group — "" for core (v1); else like "apps", "postgresql.cnpg.io".
	Group string `json:"group,omitempty"`
	// Version — "v1", "v1beta1", etc.
	Version string `json:"version"`
	// Resource — the lower-case plural ("pods", "deployments",
	// "configmaps", "helmreleases", "clusters"). NOT the Kind.
	Resource string `json:"resource"`
}

func (g gvrSelector) toGVR() (schema.GroupVersionResource, error) {
	if g.Version == "" || g.Resource == "" {
		return schema.GroupVersionResource{}, errors.New("gvr: version + resource are required")
	}
	return schema.GroupVersionResource{
		Group:    strings.TrimSpace(g.Group),
		Version:  strings.TrimSpace(g.Version),
		Resource: strings.TrimSpace(g.Resource),
	}, nil
}

// scopeNamespace applies the Sandbox's namespace policy. The MCP server
// is scoped to the Org vcluster — we don't gate which namespace the
// agent may read inside that vcluster (the SA's RoleBinding does that),
// but we DO default to env.SandboxNamespace when the caller omits it,
// so a forgotten `namespace` field doesn't accidentally cross-tenant.
//
// An empty namespace after defaulting means "cluster-scoped"; we let
// the dynamic client interpret that.
func scopeNamespace(env *Env, want string) string {
	if want != "" {
		return want
	}
	return env.SandboxNamespace
}

// --- k8s.read.get ------------------------------------------------------

type k8sReadGetArgs struct {
	gvrSelector
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

func k8sReadGet(ctx context.Context, raw json.RawMessage) (any, error) {
	var args k8sReadGetArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("k8s.read.get: invalid arguments: %w", err)
	}
	if args.Name == "" {
		return nil, errors.New("k8s.read.get: `name` is required")
	}
	gvr, err := args.toGVR()
	if err != nil {
		return nil, fmt.Errorf("k8s.read.get: %w", err)
	}
	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}
	env := EnvFrom(ctx)
	ns := scopeNamespace(env, args.Namespace)

	var ri dynamic.ResourceInterface
	if ns != "" {
		ri = client.Resource(gvr).Namespace(ns)
	} else {
		ri = client.Resource(gvr)
	}
	obj, err := ri.Get(ctx, args.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("k8s.read.get %s %s/%s: %w", gvr.Resource, ns, args.Name, err)
	}
	return obj.Object, nil
}

func schemaK8sReadGet() map[string]any {
	return map[string]any{
		"type": "object",
		"required": []string{"version", "resource", "name"},
		"properties": map[string]any{
			"group":     map[string]any{"type": "string", "description": "API group; empty for core (v1)."},
			"version":   map[string]any{"type": "string"},
			"resource":  map[string]any{"type": "string", "description": "Lowercase plural (`pods`, `helmreleases`)."},
			"namespace": map[string]any{"type": "string", "description": "Defaults to the Sandbox namespace."},
			"name":      map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}
}

// --- k8s.read.list -----------------------------------------------------

type k8sReadListArgs struct {
	gvrSelector
	Namespace     string `json:"namespace,omitempty"`
	LabelSelector string `json:"label_selector,omitempty"`
	FieldSelector string `json:"field_selector,omitempty"`
	Limit         int64  `json:"limit,omitempty"`
}

func k8sReadList(ctx context.Context, raw json.RawMessage) (any, error) {
	var args k8sReadListArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("k8s.read.list: invalid arguments: %w", err)
	}
	gvr, err := args.toGVR()
	if err != nil {
		return nil, fmt.Errorf("k8s.read.list: %w", err)
	}
	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}
	env := EnvFrom(ctx)
	ns := scopeNamespace(env, args.Namespace)
	opts := metav1.ListOptions{
		LabelSelector: args.LabelSelector,
		FieldSelector: args.FieldSelector,
		Limit:         args.Limit,
	}

	var ri dynamic.ResourceInterface
	if ns != "" {
		ri = client.Resource(gvr).Namespace(ns)
	} else {
		ri = client.Resource(gvr)
	}
	list, err := ri.List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("k8s.read.list %s %s: %w", gvr.Resource, ns, err)
	}
	// Materialise items as plain maps so the JSON output is stable
	// across client-go versions.
	out := make([]map[string]any, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, list.Items[i].Object)
	}
	return map[string]any{
		"resourceVersion": list.GetResourceVersion(),
		"continue":        list.GetContinue(),
		"count":           len(out),
		"items":           out,
	}, nil
}

func schemaK8sReadList() map[string]any {
	return map[string]any{
		"type": "object",
		"required": []string{"version", "resource"},
		"properties": map[string]any{
			"group":          map[string]any{"type": "string"},
			"version":        map[string]any{"type": "string"},
			"resource":       map[string]any{"type": "string"},
			"namespace":      map[string]any{"type": "string"},
			"label_selector": map[string]any{"type": "string"},
			"field_selector": map[string]any{"type": "string"},
			"limit":          map[string]any{"type": "integer", "minimum": 1},
		},
		"additionalProperties": false,
	}
}

// --- k8s.read.watch ----------------------------------------------------

// k8sReadWatchArgs follows the same selector shape as list, plus the
// short-watch limit (max events / max wait) so the JSON-RPC call has a
// bounded turn-around. Long-lived watches go through the MCP
// `resources/subscribe` channel (architecture.md §3) — Wave 9.
type k8sReadWatchArgs struct {
	gvrSelector
	Namespace       string `json:"namespace,omitempty"`
	LabelSelector   string `json:"label_selector,omitempty"`
	FieldSelector   string `json:"field_selector,omitempty"`
	MaxEvents       int    `json:"max_events,omitempty"`      // default 32
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"` // default 10
	ResourceVersion string `json:"resource_version,omitempty"`
}

// watchEvent is the wire shape this tool returns. We deliberately do
// NOT echo back the Kubernetes Watch envelope verbatim (it nests
// {type, object: <Unstructured>}); the flatter form is friendlier for
// agents iterating over the slice.
type watchEvent struct {
	Type   string         `json:"type"`
	Object map[string]any `json:"object,omitempty"`
}

func k8sReadWatch(ctx context.Context, raw json.RawMessage) (any, error) {
	var args k8sReadWatchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("k8s.read.watch: invalid arguments: %w", err)
	}
	gvr, err := args.toGVR()
	if err != nil {
		return nil, fmt.Errorf("k8s.read.watch: %w", err)
	}
	if args.MaxEvents <= 0 {
		args.MaxEvents = 32
	}
	if args.TimeoutSeconds <= 0 {
		args.TimeoutSeconds = 10
	}
	timeout := time.Duration(args.TimeoutSeconds) * time.Second

	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}
	env := EnvFrom(ctx)
	ns := scopeNamespace(env, args.Namespace)
	timeoutSec := int64(args.TimeoutSeconds)
	opts := metav1.ListOptions{
		LabelSelector:   args.LabelSelector,
		FieldSelector:   args.FieldSelector,
		ResourceVersion: args.ResourceVersion,
		TimeoutSeconds:  &timeoutSec,
		Watch:           true,
	}

	var ri dynamic.ResourceInterface
	if ns != "" {
		ri = client.Resource(gvr).Namespace(ns)
	} else {
		ri = client.Resource(gvr)
	}

	wctx, cancel := context.WithTimeout(ctx, timeout+2*time.Second)
	defer cancel()
	w, err := ri.Watch(wctx, opts)
	if err != nil {
		return nil, fmt.Errorf("k8s.read.watch %s %s: %w", gvr.Resource, ns, err)
	}
	defer w.Stop()

	events := make([]watchEvent, 0, args.MaxEvents)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

LOOP:
	for len(events) < args.MaxEvents {
		select {
		case <-wctx.Done():
			break LOOP
		case <-deadline.C:
			break LOOP
		case ev, ok := <-w.ResultChan():
			if !ok {
				break LOOP
			}
			out := watchEvent{Type: string(ev.Type)}
			if u, ok := ev.Object.(*unstructured.Unstructured); ok {
				out.Object = u.Object
			}
			events = append(events, out)
		}
	}
	return map[string]any{
		"count":  len(events),
		"events": events,
	}, nil
}

func schemaK8sReadWatch() map[string]any {
	return map[string]any{
		"type": "object",
		"required": []string{"version", "resource"},
		"properties": map[string]any{
			"group":            map[string]any{"type": "string"},
			"version":          map[string]any{"type": "string"},
			"resource":         map[string]any{"type": "string"},
			"namespace":        map[string]any{"type": "string"},
			"label_selector":   map[string]any{"type": "string"},
			"field_selector":   map[string]any{"type": "string"},
			"max_events":       map[string]any{"type": "integer", "minimum": 1, "maximum": 1024, "default": 32},
			"timeout_seconds":  map[string]any{"type": "integer", "minimum": 1, "maximum": 60, "default": 10},
			"resource_version": map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}
}

// Compile-time check: keep watch import live for the type assertion above.
var _ watch.Event

// --- k8s.read.logs ----------------------------------------------------
//
// Bounded fetch of a pod container's tail. We deliberately do NOT
// expose follow=true on this tool — the MCP JSON-RPC turn-around
// expects a finite result; long-lived log streams go through the
// catalyst-api WebSocket surface (`/k8s/logs/...`) instead. The agent
// uses k8s.read.logs to grab the recent N lines, diagnose, and move on.
//
// Wire: GET /api/v1/namespaces/{ns}/pods/{name}/log?container=...&tailLines=N&previous=bool
// via client-go's CoreV1().Pods(ns).GetLogs(name, opts).Stream(ctx).

// typedCache memoises the kubernetes.Interface used by k8s.read.logs.
// Built lazily so the binary keeps working on machines without an
// in-cluster config / kubeconfig (k8s.read.logs surfaces the build
// error when first invoked rather than aborting startup).
var (
	typedOnce sync.Once
	typedVal  kubernetes.Interface
	typedErr  error
)

// typedClient lazily builds a kubernetes.Interface. Only k8s.read.logs
// needs the typed surface today (the dynamic client doesn't expose the
// `Pods(ns).GetLogs(...)` subresource); other read-tools keep using
// dynamicClient().
func typedClient(ctx context.Context) (kubernetes.Interface, error) {
	env := EnvFrom(ctx)
	if env == nil {
		return nil, errors.New("k8s.read: tool dispatch missing Env on context")
	}
	typedOnce.Do(func() {
		var cfg *rest.Config
		if env.KubeconfigPath != "" {
			cfg, typedErr = clientcmd.BuildConfigFromFlags("", env.KubeconfigPath)
		} else {
			cfg, typedErr = rest.InClusterConfig()
		}
		if typedErr != nil {
			typedErr = fmt.Errorf("k8s.read: build REST config: %w", typedErr)
			return
		}
		// PodLogs is a streaming subresource. We leave rest.Config.Timeout
		// unset and bound the call via the per-request context + TailLines
		// so a long-tail doesn't get cut off mid-stream.
		typedVal, typedErr = kubernetes.NewForConfig(cfg)
		if typedErr != nil {
			typedErr = fmt.Errorf("k8s.read: kubernetes.NewForConfig: %w", typedErr)
		}
	})
	return typedVal, typedErr
}

// maxLogTailLines caps the per-call output so a misbehaving agent
// can't accidentally pull a 10MB log into the MCP transcript.
const maxLogTailLines = 5000

// maxLogBytes is the hard byte cap on the returned payload — even if
// `tail_lines` is small, individual lines may be huge (stack traces,
// JSON blobs). 1MiB keeps each result inside JSON-RPC frame limits.
const maxLogBytes = 1 << 20 // 1 MiB

type k8sReadLogsArgs struct {
	Namespace string `json:"namespace,omitempty"`
	// Pod — required. The MCP tool doesn't do label-selector → pod
	// resolution; callers should k8s.read.list pods first.
	Pod string `json:"pod"`
	// Container — optional. Empty → kubelet's default-container choice.
	Container string `json:"container,omitempty"`
	// TailLines — last N lines. Default 200, capped at maxLogTailLines.
	TailLines int64 `json:"tail_lines,omitempty"`
	// Previous — read logs from the previous (crashed) container.
	Previous bool `json:"previous,omitempty"`
	// SinceSeconds — only logs from the last N seconds. Composes with
	// TailLines (kubelet returns the intersection).
	SinceSeconds int64 `json:"since_seconds,omitempty"`
	// Timestamps — prefix every line with an RFC3339 timestamp.
	Timestamps bool `json:"timestamps,omitempty"`
}

func k8sReadLogs(ctx context.Context, raw json.RawMessage) (any, error) {
	var args k8sReadLogsArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("k8s.read.logs: invalid arguments: %w", err)
		}
	}
	if args.Pod == "" {
		return nil, errors.New("k8s.read.logs: `pod` is required")
	}
	if args.TailLines <= 0 {
		args.TailLines = 200
	}
	if args.TailLines > maxLogTailLines {
		args.TailLines = maxLogTailLines
	}

	env := EnvFrom(ctx)
	ns := scopeNamespace(env, args.Namespace)
	if ns == "" {
		return nil, errors.New("k8s.read.logs: namespace is required (no SANDBOX_NAMESPACE default)")
	}

	client, err := typedClient(ctx)
	if err != nil {
		return nil, err
	}

	tail := args.TailLines
	opts := &corev1.PodLogOptions{
		Container:  args.Container,
		Follow:     false,
		TailLines:  &tail,
		Previous:   args.Previous,
		Timestamps: args.Timestamps,
	}
	if args.SinceSeconds > 0 {
		ss := args.SinceSeconds
		opts.SinceSeconds = &ss
	}

	// 30s call deadline — pod-logs subresource can hang on a stuck
	// kubelet; bound the worst-case so an agent isn't stuck waiting.
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req := client.CoreV1().Pods(ns).GetLogs(args.Pod, opts)
	stream, err := req.Stream(callCtx)
	if err != nil {
		return nil, fmt.Errorf("k8s.read.logs %s/%s: %w", ns, args.Pod, err)
	}
	defer stream.Close()

	// Bounded byte+line read: stop at the byte cap OR when we've
	// consumed the kubelet stream cleanly. bufio.Scanner has a per-
	// line 1 MiB cap so a pathologically-long line doesn't NUL the read.
	limited := io.LimitReader(stream, maxLogBytes)
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	lines := make([]string, 0, args.TailLines)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("k8s.read.logs %s/%s: scan: %w", ns, args.Pod, err)
	}
	// Truncation flag: if we hit the line cap, the kubelet stream
	// almost certainly had more — caller can re-issue with a larger
	// tail_lines or fall back to the WebSocket surface.
	truncated := int64(len(lines)) >= args.TailLines

	return map[string]any{
		"namespace": ns,
		"pod":       args.Pod,
		"container": args.Container,
		"previous":  args.Previous,
		"count":     len(lines),
		"lines":     lines,
		"truncated": truncated,
	}, nil
}

func schemaK8sReadLogs() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"pod"},
		"properties": map[string]any{
			"namespace":     map[string]any{"type": "string", "description": "Defaults to the Sandbox namespace."},
			"pod":           map[string]any{"type": "string"},
			"container":     map[string]any{"type": "string", "description": "Container name (default: kubelet picks)."},
			"tail_lines":    map[string]any{"type": "integer", "minimum": 1, "maximum": maxLogTailLines, "default": 200},
			"previous":      map[string]any{"type": "boolean", "default": false, "description": "Read logs from the previous (crashed) container instance."},
			"since_seconds": map[string]any{"type": "integer", "minimum": 1},
			"timestamps":    map[string]any{"type": "boolean", "default": false},
		},
		"additionalProperties": false,
	}
}
