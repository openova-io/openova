// nats_bridge wires the canonical Catalyst NATS subject
// `catalyst.tenant.sandbox_requested` (D35 consume leg, sandbox-controller side)
// into the sandbox-controller's reconcile loop.
//
// Why this lives in sandbox-controller, not just in tenant-service:
// the tenant-service SandboxOrchestrator (PR #1633) already consumes
// `catalyst.tenant.sandbox_requested` and creates the Sandbox CR. The
// missing leg was an in-cluster controller that, after the CR
// materialised, OBSERVES the same envelope on its broker side and
// triggers a fresh Reconcile within ~50ms instead of waiting for the
// 30s informer requeue. That tightens the cart-completion → CR-Ready
// loop end-to-end and closes D35: NATS round-trips end-to-end with
// the controllers as the consume-side leg.
//
// The bridge looks up the matching Sandbox CR by the same name
// derivation tenant-service uses (sanitised owner email/UID inside the
// sandbox namespace) and stamps two annotations:
//
//   - openova.io/last-event-observed-at: RFC3339 timestamp from the
//     broker envelope. Stable across duplicate JetStream delivery so
//     the annotation patch is byte-equal on replay.
//   - openova.io/last-event-subject: the canonical subject string.
//
// Patching either annotation triggers an informer event →
// controller-runtime enqueues the CR's NamespacedName → Reconcile
// runs within ~50ms.
//
// The 30s RequeueAfter in r.Reconcile remains untouched — this bridge
// is an accelerator, not the only path. NATS message loss never
// strands a CR.
//
// Per HARD CONSTRAINT: no credential write-paths. The bridge reads
// only the Event envelope + the matching CR; it never touches Secrets
// or NewAPI bearer tokens.

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openova-io/openova/core/controllers/pkg/natsbus"
	sandboxapi "github.com/openova-io/openova/core/controllers/sandbox/internal/sandboxapi"
)

// Annotation keys stamped on the matching Sandbox CR when a canonical
// NATS envelope is observed. Identical to the organization-controller
// keys for operator-visible symmetry across the two Group-C
// controllers — `kubectl get sandboxes,organizations -o jsonpath`
// surfaces the same field across both kinds.
const (
	AnnotationLastNATSObservedAt = "openova.io/last-event-observed-at"
	AnnotationLastNATSSubject    = "openova.io/last-event-subject"
)

// DefaultSandboxNamespace is the namespace tenant-service writes
// Sandbox CRs into when SANDBOX_NAMESPACE is unset. Mirrors the
// publish-side default in core/services/tenant/handlers/sandbox_consumer.go.
const DefaultSandboxNamespace = "catalyst-system"

// NATSBridge is the consume-leg adapter for the sandbox-controller.
type NATSBridge struct {
	Client client.Client
	Log    logr.Logger

	// Namespace is the sandbox-namespace tenant-service writes Sandbox
	// CRs into. Defaults to DefaultSandboxNamespace when empty.
	Namespace string
}

// HandleSandboxRequested reacts to a `catalyst.tenant.sandbox_requested`
// envelope. The publish-side (tenant.handlers.CreateOrg in PR #1633)
// stamps owner_email + owner_id + org_slug + agents on the Data
// payload. We derive the deterministic Sandbox CR name using the same
// rules tenant-service applies (sanitised email leaf, "sandbox-"
// prefix, RFC1123-bounded) and patch the observation annotations.
func (b *NATSBridge) HandleSandboxRequested(ctx context.Context, ev *natsbus.Event) error {
	if ev == nil {
		return nil
	}
	var payload struct {
		TenantID   string `json:"tenant_id"`
		OrgSlug    string `json:"org_slug"`
		OwnerID    string `json:"owner_id"`
		OwnerEmail string `json:"owner_email"`
	}
	if err := json.Unmarshal(ev.Data, &payload); err != nil {
		b.Log.Error(err, "sandbox_requested: malformed Data payload — ack to skip",
			"event_id", ev.ID)
		return nil
	}

	name := sandboxCRNameFromEvent(payload.OwnerEmail, payload.OwnerID)
	if name == "" {
		b.Log.Error(fmt.Errorf("empty derived name"),
			"sandbox_requested: payload has neither owner_email nor owner_id — ack to skip",
			"event_id", ev.ID, "tenant_id", payload.TenantID)
		return nil
	}

	ns := b.Namespace
	if ns == "" {
		ns = DefaultSandboxNamespace
	}

	var sb sandboxapi.Sandbox
	if err := b.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &sb); err != nil {
		if apierrors.IsNotFound(err) {
			// Cold-start ordering: the broker delivered our copy of
			// the envelope before the tenant-service orchestrator
			// finished writing the CR. Soft-miss; tenant-service's
			// Sandbox CR Create will fire an informer event of its
			// own when it lands, so we don't need to retry.
			b.Log.Info("nats observation: no matching Sandbox CR — ack and skip",
				"subject", natsbus.SubjectTenantSandboxRequested,
				"namespace", ns, "name", name, "event_id", ev.ID)
			return nil
		}
		return fmt.Errorf("get sandbox %s/%s: %w", ns, name, err)
	}

	observedAt := ev.Timestamp.UTC().Format(time.RFC3339Nano)
	if observedAt == "" || ev.Timestamp.IsZero() {
		observedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}

	// Byte-stable patch on duplicate JetStream delivery: skip when
	// the annotations already match.
	cur := sb.GetAnnotations()
	if cur != nil &&
		cur[AnnotationLastNATSObservedAt] == observedAt &&
		cur[AnnotationLastNATSSubject] == natsbus.SubjectTenantSandboxRequested {
		b.Log.V(1).Info("nats observation: duplicate envelope — skip patch",
			"subject", natsbus.SubjectTenantSandboxRequested,
			"namespace", ns, "name", name, "event_id", ev.ID)
		return nil
	}

	desired := &sandboxapi.Sandbox{}
	sb.DeepCopyInto(desired)
	anns := desired.GetAnnotations()
	if anns == nil {
		anns = map[string]string{}
	}
	anns[AnnotationLastNATSObservedAt] = observedAt
	anns[AnnotationLastNATSSubject] = natsbus.SubjectTenantSandboxRequested
	desired.SetAnnotations(anns)

	if err := b.Client.Patch(ctx, desired, client.MergeFrom(&sb)); err != nil {
		return fmt.Errorf("patch sandbox %s/%s: %w", ns, name, err)
	}
	b.Log.Info("nats observation stamped — reconcile enqueued",
		"subject", natsbus.SubjectTenantSandboxRequested,
		"namespace", ns, "name", name,
		"event_id", ev.ID, "observed_at", observedAt)
	return nil
}

// sandboxCRNameFromEvent mirrors core/services/tenant/handlers/sandbox_consumer.go
// `sandboxCRName(email, ownerID)`. The two functions MUST stay in
// sync — tenant-service writes the CR under this name, and
// sandbox-controller's NATSBridge looks it up by the same name.
//
// Rules (verbatim from the publish-side):
//
//  1. Prefer the email; fall back to ownerID when email is empty.
//  2. Sanitise to a DNS-1123 leaf via sanitizeSandboxLeaf.
//  3. Empty post-sanitise → literal "user" so the consumer never
//     returns an empty-name lookup.
//  4. Final name = "sandbox-" + leaf, truncated to 63 chars and
//     trailing-hyphen-stripped.
func sandboxCRNameFromEvent(email, ownerID string) string {
	candidate := strings.TrimSpace(email)
	if candidate == "" {
		candidate = strings.TrimSpace(ownerID)
	}
	leaf := sanitizeSandboxLeaf(candidate)
	if leaf == "" {
		leaf = "user"
	}
	name := "sandbox-" + leaf
	if len(name) > 63 {
		name = name[:63]
	}
	name = strings.TrimRight(name, "-")
	return name
}

// sanitizeSandboxLeaf mirrors core/services/tenant/handlers/sandbox_consumer.go
// `sanitizeSandboxLeaf`. Lowercases, replaces @ + . + + + _ with -,
// strips everything outside [a-z0-9-], collapses double-hyphens, and
// trims leading/trailing hyphens.
func sanitizeSandboxLeaf(in string) string {
	out := strings.ToLower(in)
	out = strings.ReplaceAll(out, "@", "-at-")
	out = strings.ReplaceAll(out, ".", "-")
	out = strings.ReplaceAll(out, "+", "-plus-")
	out = strings.ReplaceAll(out, "_", "-")
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
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.Trim(out, "-")
	return out
}
