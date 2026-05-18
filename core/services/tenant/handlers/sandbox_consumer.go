// Sandbox orchestrator (Wave 8 — session-orchestrator slice).
//
// Background. PR #1633 wired CreateOrg to publish a
// `tenant.sandbox_requested` event whenever the marketplace cart
// contains the "sandbox" product. Nobody was listening — so the event
// landed in NATS JetStream's `catalyst.tenant.sandbox_requested` subject
// and was simply retained until expiry. No Sandbox CR was ever minted,
// no namespace, no PVCs, no pty-server — the customer sat on a
// "Provisioning…" spinner forever.
//
// This file closes the loop. It subscribes to the canonical NATS
// subject (and the legacy Kafka topic during the migration window) via
// events.MultiSubscriber (PR #1636 — bridge_subscriber.go), parses the
// event payload, and materialises a Sandbox CR in `catalyst-system`
// (or whatever SANDBOX_NAMESPACE points at). The sandbox-controller
// (PR #1622 — sandbox.openova.io/v1) then reconciles the CR into the
// namespace + RBAC + PVCs + pty-server StatefulSet per Wave 1.
//
// Per products/sandbox/docs/architecture.md §7:
//
//	spec:
//	  owner:
//	    email: <event.owner_email | event.owner_id>
//	    orgRef:
//	      slug: <event.org_slug>
//	  quota:                   # defaulted here — Wave 1
//	    cpu: "4"
//	    memory: "8Gi"
//	    storage: "50Gi"
//	    concurrentSessions: 3
//	  agentCatalogue:          # from the cart picker
//	    - claude-code
//	    - cursor-agent
//
// Idempotency. The CR name is derived deterministically from the owner
// identity (email when available, owner_id otherwise) sanitised into a
// DNS-label-safe form, prefixed with `sandbox-`, truncated to 63 chars.
// A Get-then-Create dance handles "already exists" silently so that a
// NATS redelivery (or a duplicate marketplace submit) does not error.

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openova-io/openova/core/services/shared/events"
)

// SandboxGVR is the namespace-scoped CR the sandbox-controller reconciles
// (core/controllers/sandbox/internal/sandboxapi/types.go). Defined here
// rather than imported from the controllers monorepo to keep
// tenant-service's go.mod free of the controller-runtime dependency
// tree — the orchestrator only needs the dynamic-client GVR shape.
var SandboxGVR = schema.GroupVersionResource{
	Group:    "sandbox.openova.io",
	Version:  "v1",
	Resource: "sandboxes",
}

// DefaultSandboxNamespace is the namespace the orchestrator writes
// Sandbox CRs into when SANDBOX_NAMESPACE is unset. catalyst-system is
// the Sovereign-wide control-plane namespace (matches the UserAccess
// CR target in user_access_owner_seed.go) so the sandbox-controller's
// in-cluster cache watches a single namespace.
const DefaultSandboxNamespace = "catalyst-system"

// SandboxClient is the narrow Kubernetes surface the orchestrator uses
// to materialise a Sandbox CR. Implementations:
//   - production: a thin wrapper around dynamic.Interface in main.go.
//   - tests: a fake recording every Get/Create call.
//
// Keeping this surface interface-shaped lets us unit-test the consumer
// without standing up envtest or pulling in client-go test stubs.
type SandboxClient interface {
	// Get returns the existing Sandbox in (namespace, name) as
	// unstructured. apierrors.IsNotFound on err signals "create me".
	Get(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error)
	// Create writes a Sandbox CR. apierrors.IsAlreadyExists is treated
	// as a benign idempotency no-op by the orchestrator.
	Create(ctx context.Context, namespace string, obj *unstructured.Unstructured) error
}

// memberEmailLookup is the narrow store slice the orchestrator uses to
// enrich the event with the owner's email when the publisher did not
// inline it. Optional — the consumer falls back to owner_id when nil.
type memberEmailLookup interface {
	GetMemberEmail(ctx context.Context, tenantID, userID string) (string, error)
}

// SandboxRequestedPayload mirrors the JSON the tenant service emits in
// handlers.go (PR #1633). owner_email is best-effort; the orchestrator
// falls back to a store lookup, then to owner_id, when it is empty.
//
// PlanID is the catalog Plan.Slug (sandbox-free | sandbox-pro |
// sandbox-ent) the customer picked at checkout. Stamped onto the
// Sandbox CR as openova.io/plan-id so the sandbox-controller can derive
// spec.quota from the plan's IncludedQuotas. Empty PlanID leaves the
// CR on the orchestrator's DefaultQuota (Wave 1 baseline = Sandbox
// Free).
type SandboxRequestedPayload struct {
	TenantID    string   `json:"tenant_id"`
	OrgSlug     string   `json:"org_slug"`
	OwnerID     string   `json:"owner_id"`
	OwnerEmail  string   `json:"owner_email"`
	Agents      []string `json:"agents"`
	Sovereign   string   `json:"sovereign"`
	PlanID      string   `json:"plan_id"`
	RequestedAt string   `json:"requested_at"`
}

// SandboxOrchestrator consumes tenant.sandbox_requested events and
// materialises Sandbox CRs. Field semantics:
//
//   - Client       — Kubernetes write surface. Required.
//   - Namespace    — target namespace (defaults to catalyst-system).
//   - Members      — optional store slice for owner-email enrichment.
//     When nil the consumer skips the lookup and uses
//     payload.OwnerEmail or payload.OwnerID directly.
//   - DefaultQuota — quota stamped onto every new Sandbox. Per
//     architecture §7 the Wave 1 defaults are 4 CPU / 8 Gi / 50 Gi /
//     3 sessions; overridden via main.go for sized plans.
type SandboxOrchestrator struct {
	Client       SandboxClient
	Namespace    string
	Members      memberEmailLookup
	DefaultQuota SandboxQuota
}

// SandboxQuota is the small DTO the orchestrator stamps into spec.quota.
// Mirrors core/controllers/sandbox/internal/sandboxapi/types.go's
// SandboxQuota without forcing a dependency on the controllers module.
type SandboxQuota struct {
	CPU                string
	Memory             string
	Storage            string
	ConcurrentSessions int
}

// DefaultSandboxQuota returns the Wave 1 quota baseline per
// architecture §7.
func DefaultSandboxQuota() SandboxQuota {
	return SandboxQuota{
		CPU:                "4",
		Memory:             "8Gi",
		Storage:            "50Gi",
		ConcurrentSessions: 3,
	}
}

// Start subscribes to the supplied BrokerSubscriber and dispatches every
// tenant.sandbox_requested event into handleSandboxRequested. Non-match
// event types are ignored (the subscriber may multiplex via a single
// stream when the operator narrows the EventTypes filter).
func (o *SandboxOrchestrator) Start(ctx context.Context, sub events.BrokerSubscriber) error {
	if o == nil {
		return errors.New("sandbox-orchestrator: nil receiver")
	}
	if o.Client == nil {
		return errors.New("sandbox-orchestrator: nil SandboxClient")
	}
	slog.Info("starting sandbox-orchestrator consumer",
		"namespace", o.namespace(),
		"default_quota_cpu", o.quota().CPU,
		"default_quota_memory", o.quota().Memory,
	)
	return sub.Subscribe(ctx, func(event *events.Event) error {
		if event.Type != "tenant.sandbox_requested" {
			return nil
		}
		return o.handleSandboxRequested(ctx, event)
	})
}

// handleSandboxRequested is the dispatcher. Returning a non-nil error
// triggers a Nak so the broker redelivers; malformed payloads are
// ack-skipped (log + nil) to avoid poison-pill hot loops.
func (o *SandboxOrchestrator) handleSandboxRequested(ctx context.Context, event *events.Event) error {
	var payload SandboxRequestedPayload
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		slog.Error("sandbox-orchestrator: malformed payload — ack to skip",
			"event_id", event.ID, "error", err)
		return nil
	}
	if payload.OwnerID == "" && payload.OwnerEmail == "" {
		slog.Warn("sandbox-orchestrator: payload has no owner identity — skipping",
			"event_id", event.ID, "tenant_id", payload.TenantID)
		return nil
	}
	if payload.OrgSlug == "" {
		slog.Warn("sandbox-orchestrator: payload missing org_slug — skipping",
			"event_id", event.ID, "tenant_id", payload.TenantID)
		return nil
	}

	email := payload.OwnerEmail
	if email == "" && o.Members != nil && payload.TenantID != "" && payload.OwnerID != "" {
		if v, err := o.Members.GetMemberEmail(ctx, payload.TenantID, payload.OwnerID); err == nil && v != "" {
			email = v
		}
	}
	// Final fallback: owner_id (UUID-shaped). Better than dropping the
	// event when the JWT didn't carry an email and the member row was
	// inserted without one.
	if email == "" {
		email = payload.OwnerID
	}

	name := sandboxCRName(email, payload.OwnerID)
	ns := o.namespace()

	// Idempotency: Get first. The sandbox-controller is the SoR for
	// spec.* once the CR exists, so a re-emit of the same event MUST
	// NOT overwrite a controller-tweaked CR.
	if existing, err := o.Client.Get(ctx, ns, name); err == nil && existing != nil {
		slog.Info("sandbox-orchestrator: Sandbox already exists — no-op",
			"namespace", ns, "name", name,
			"event_id", event.ID, "tenant_id", payload.TenantID)
		return nil
	} else if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get Sandbox %s/%s: %w", ns, name, err)
	}

	obj := o.buildSandbox(name, ns, email, payload)
	if err := o.Client.Create(ctx, ns, obj); err != nil {
		if apierrors.IsAlreadyExists(err) {
			slog.Info("sandbox-orchestrator: Sandbox already exists (race) — no-op",
				"namespace", ns, "name", name)
			return nil
		}
		return fmt.Errorf("create Sandbox %s/%s: %w", ns, name, err)
	}
	slog.Info("sandbox-orchestrator: Sandbox CR materialised",
		"namespace", ns, "name", name,
		"event_id", event.ID, "tenant_id", payload.TenantID,
		"org_slug", payload.OrgSlug, "agents", payload.Agents)
	return nil
}

// buildSandbox composes the unstructured CR. Shape mirrors
// products/sandbox/docs/architecture.md §7 + the SandboxSpec Go type at
// core/controllers/sandbox/internal/sandboxapi/types.go.
func (o *SandboxOrchestrator) buildSandbox(name, ns, email string, p SandboxRequestedPayload) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   SandboxGVR.Group,
		Version: SandboxGVR.Version,
		Kind:    "Sandbox",
	})
	obj.SetName(name)
	obj.SetNamespace(ns)
	labels := map[string]string{
		"openova.io/organization": p.OrgSlug,
		"openova.io/owner-id":     p.OwnerID,
		"openova.io/managed-by":   "tenant-service",
	}
	if p.Sovereign != "" {
		labels["openova.io/sovereign"] = p.Sovereign
	}
	obj.SetLabels(labels)
	annotations := map[string]string{
		"openova.io/source-event":        "tenant.sandbox_requested",
		"openova.io/source-tenant-id":    p.TenantID,
		"openova.io/source-requested-at": p.RequestedAt,
	}
	// PR #wave9 (catalog Sandbox plans): stamp the customer-picked plan
	// onto the CR. The sandbox-controller reads this annotation to look
	// up the plan's IncludedQuotas in the catalog, then re-stamps
	// spec.quota when the customer upgrades from Free → Pro → Ent
	// without re-creating the CR. Empty plan_id leaves the orchestrator's
	// DefaultQuota in place — equivalent to sandbox-free.
	if strings.TrimSpace(p.PlanID) != "" {
		annotations["openova.io/plan-id"] = strings.TrimSpace(p.PlanID)
	}
	obj.SetAnnotations(annotations)

	q := quotaForPlan(p.PlanID, o.quota())
	spec := map[string]any{
		"owner": map[string]any{
			"email": email,
			"orgRef": map[string]any{
				"slug": p.OrgSlug,
			},
		},
		"quota": map[string]any{
			"cpu":                q.CPU,
			"memory":             q.Memory,
			"storage":            q.Storage,
			"concurrentSessions": int64(q.ConcurrentSessions),
		},
	}
	if strings.TrimSpace(p.PlanID) != "" {
		spec["planId"] = strings.TrimSpace(p.PlanID)
	}
	if len(p.Agents) > 0 {
		// Copy into []any so json.Marshal renders a plain JSON list
		// rather than a typed []string (unstructured accepts both but
		// the controller's deepcopy expects []interface{}).
		out := make([]any, 0, len(p.Agents))
		for _, a := range p.Agents {
			a = strings.TrimSpace(a)
			if a == "" {
				continue
			}
			out = append(out, a)
		}
		if len(out) > 0 {
			spec["agentCatalogue"] = out
		}
	}
	obj.Object["spec"] = spec
	// Empty TypeMeta is set above; we also want CreationTimestamp empty
	// so apiserver assigns it. SetCreationTimestamp(metav1.Time{}) is
	// implicit for a fresh unstructured.
	_ = metav1.Time{}
	return obj
}

// namespace returns the target namespace, defaulting to
// DefaultSandboxNamespace.
func (o *SandboxOrchestrator) namespace() string {
	if o == nil || strings.TrimSpace(o.Namespace) == "" {
		return DefaultSandboxNamespace
	}
	return o.Namespace
}

// quota returns the configured default quota, defaulting to the Wave 1
// baseline.
func (o *SandboxOrchestrator) quota() SandboxQuota {
	if o == nil {
		return DefaultSandboxQuota()
	}
	if o.DefaultQuota.CPU == "" && o.DefaultQuota.Memory == "" {
		return DefaultSandboxQuota()
	}
	q := o.DefaultQuota
	if q.CPU == "" {
		q.CPU = "4"
	}
	if q.Memory == "" {
		q.Memory = "8Gi"
	}
	if q.Storage == "" {
		q.Storage = "50Gi"
	}
	if q.ConcurrentSessions <= 0 {
		q.ConcurrentSessions = 3
	}
	return q
}

// sandboxCRName builds the deterministic CR name. Prefers a sanitised
// email when available so operators reading `kubectl get sandbox -A`
// see human-scannable identities; falls back to owner_id (typically a
// UUID) when the publisher didn't carry one. The result is bounded at
// 63 chars (RFC 1123) and trimmed of trailing hyphens.
func sandboxCRName(email, ownerID string) string {
	candidate := strings.TrimSpace(email)
	if candidate == "" {
		candidate = strings.TrimSpace(ownerID)
	}
	leaf := sanitizeSandboxLeaf(candidate)
	if leaf == "" {
		// Pathological — every char stripped. Use a stable literal so
		// the consumer never returns an empty-name Create.
		leaf = "user"
	}
	name := "sandbox-" + leaf
	if len(name) > 63 {
		name = name[:63]
	}
	name = strings.TrimRight(name, "-")
	return name
}

// quotaForPlan maps a catalog Sandbox plan slug to the SandboxQuota the
// orchestrator stamps on the CR. Mirrors the IncludedQuotas in
// core/services/catalog/handlers/seed.go's expectedSandboxPlans —
// duplicated here so tenant-service has no compile-time dep on the
// catalog package (catalog uses go.mongodb.org/mongo-driver/v2 which
// pulls in a hefty BSON stack we don't otherwise need in this service).
//
// Empty or unknown plan slug falls through to `fallback`, which is the
// orchestrator's DefaultQuota (Wave 1 baseline = Sandbox Free shape).
// "0" concurrent_sessions in the catalog signals unlimited; we encode
// that as ConcurrentSessions=0 on the CR and the controller treats
// `<=0` as unbounded.
func quotaForPlan(planID string, fallback SandboxQuota) SandboxQuota {
	switch strings.TrimSpace(planID) {
	case "sandbox-free":
		return SandboxQuota{CPU: "500m", Memory: "1Gi", Storage: "5Gi", ConcurrentSessions: 1}
	case "sandbox-pro":
		return SandboxQuota{CPU: "2", Memory: "4Gi", Storage: "50Gi", ConcurrentSessions: 3}
	case "sandbox-ent":
		// 0 = unlimited per controller convention
		return SandboxQuota{CPU: "8", Memory: "16Gi", Storage: "500Gi", ConcurrentSessions: 0}
	default:
		return fallback
	}
}

// sanitizeSandboxLeaf is the same shape as
// organization_controller.sanitizeEmail but local to this package so
// the orchestrator does not pull in the controllers module.
func sanitizeSandboxLeaf(in string) string {
	out := strings.ToLower(in)
	out = strings.ReplaceAll(out, "@", "-at-")
	out = strings.ReplaceAll(out, ".", "-")
	out = strings.ReplaceAll(out, "+", "-plus-")
	out = strings.ReplaceAll(out, "_", "-")
	// Strip anything not [a-z0-9-].
	var b strings.Builder
	b.Grow(len(out))
	for _, r := range out {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out = b.String()
	// Collapse runs of hyphens (-- → -).
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-")
}
