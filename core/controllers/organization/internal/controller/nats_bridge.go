// nats_bridge wires the canonical Catalyst NATS subjects (D35 consume
// leg) into the organization-controller's reconcile loop.
//
// PR #1626 closed the publish-side of D35 — tenant + billing services
// now emit `catalyst.tenant.created` + `catalyst.billing.order.placed`
// on the NATS JetStream `CATALYST_ORG` stream per ADR-0001 §6. The
// consume-side was missing: no in-cluster controller subscribed, so the
// envelopes accumulated on the broker and the only path that
// reconciled an Organization CR was the 30s informer requeue plus
// whatever wrote the CR in the first place. D35 (gate: "NATS broker
// round-trips end-to-end") therefore stayed yellow even though the
// publish leg shipped.
//
// This bridge subscribes to both subjects and, on each envelope:
//
//  1. Decodes the Event body into the tenant_id / slug fields the
//     publishers stamp (see core/services/tenant/handlers/handlers.go +
//     core/services/billing/handlers/handlers.go dispatchOrderPlaced).
//  2. Looks up the Organization CR whose `spec.slug` matches the event's
//     slug. The CR may not exist yet (e.g. tenant.created arrives
//     before the operator wrote the CR) — that's a soft miss, we log
//     and Ack so JetStream advances.
//  3. Stamps `openova.io/last-event-observed-at` (RFC3339) +
//     `openova.io/last-event-subject` on the CR via a patch. The
//     annotation patch is treated as a generation-2 mutation by
//     controller-runtime, which enqueues a fresh Reconcile within
//     ~50ms — far faster than the 30s informer requeue fallback. The
//     30s requeue is RETAINED inside Reconcile so a missed NATS message
//     never strands a CR; subscription is an accelerator, not the only
//     path.
//
// The bridge is intentionally idempotent — JetStream guarantees
// at-least-once delivery, so the same envelope may arrive twice on a
// broker rebalance. Stamping an annotation with the broker-side
// Event.Timestamp keeps the patch byte-stable on duplicate delivery,
// so controller-runtime does NOT enqueue a redundant Reconcile.
//
// Per HARD CONSTRAINT: no credential write-paths. The bridge reads
// only the Event envelope + the matching CR; it never touches Secrets
// or Keycloak service-account creds.

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

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
	"github.com/openova-io/openova/core/controllers/pkg/natsbus"
)

// Annotation keys stamped on the matching Organization CR when a
// canonical NATS envelope is observed. Stable across pod restarts so
// duplicate JetStream delivery does NOT trigger a redundant
// Reconcile (Event.Timestamp is a stable per-event value).
const (
	AnnotationLastNATSObservedAt = "openova.io/last-event-observed-at"
	AnnotationLastNATSSubject    = "openova.io/last-event-subject"
)

// NATSBridge is the consume-leg adapter for the organization-controller.
// One bridge instance per canonical subject; the bridge handles all
// envelopes that match its handler shape.
type NATSBridge struct {
	Client client.Client
	Log    logr.Logger
}

// HandleTenantCreated reacts to a `catalyst.tenant.created` envelope.
// The publish-side (PR #1626) ships the tenant doc as the Data payload
// — we read `slug` (canonical Org slug) and `id` (tenant id, used for
// the audit log). When the matching Organization CR exists, we stamp
// the observation annotation so controller-runtime enqueues a fresh
// Reconcile. When it does not exist, we log + Ack — the operator (or a
// future provisioning controller) is responsible for creating the CR.
func (b *NATSBridge) HandleTenantCreated(ctx context.Context, ev *natsbus.Event) error {
	if ev == nil {
		return nil
	}
	var payload struct {
		ID    string `json:"id"`
		Slug  string `json:"slug"`
		TenID string `json:"tenant_id"`
	}
	if err := json.Unmarshal(ev.Data, &payload); err != nil {
		// Malformed inside the envelope — log and Ack via the
		// natsbus dispatcher (returning nil acks). Don't Nak; the
		// next delivery would fail identically.
		b.Log.Error(err, "tenant.created: malformed Data payload — ack to skip",
			"event_id", ev.ID)
		return nil
	}
	slug := strings.TrimSpace(payload.Slug)
	if slug == "" {
		// PR #1626 stamps `slug` on the tenant doc; if it's missing
		// the publish side regressed. Log loudly so the operator
		// notices but Ack so the subscriber doesn't hot-loop.
		b.Log.Error(fmt.Errorf("missing slug"), "tenant.created: payload has no slug — ack to skip",
			"event_id", ev.ID, "tenant_id", payload.TenID)
		return nil
	}
	return b.stampObservation(ctx, slug, natsbus.SubjectTenantCreated, ev)
}

// HandleOrderPlaced reacts to a `catalyst.billing.order.placed`
// envelope. The publish-side (PR #1626 dispatchOrderPlaced) ships a
// payload enriched with the tenant's subdomain — we read `subdomain`
// (matches Org slug on the Sovereign-side wildcard tenancy model) and
// `tenant_id` for the audit trail.
func (b *NATSBridge) HandleOrderPlaced(ctx context.Context, ev *natsbus.Event) error {
	if ev == nil {
		return nil
	}
	var payload struct {
		TenantID  string `json:"tenant_id"`
		Subdomain string `json:"subdomain"`
		OrgSlug   string `json:"org_slug"`
	}
	if err := json.Unmarshal(ev.Data, &payload); err != nil {
		b.Log.Error(err, "order.placed: malformed Data payload — ack to skip",
			"event_id", ev.ID)
		return nil
	}
	// Prefer the explicit org_slug field when present (forward-compat);
	// fall back to subdomain which dispatchOrderPlaced currently stamps.
	slug := strings.TrimSpace(payload.OrgSlug)
	if slug == "" {
		slug = strings.TrimSpace(payload.Subdomain)
	}
	if slug == "" {
		b.Log.Error(fmt.Errorf("missing slug"), "order.placed: payload has neither org_slug nor subdomain — ack to skip",
			"event_id", ev.ID, "tenant_id", payload.TenantID)
		return nil
	}
	return b.stampObservation(ctx, slug, natsbus.SubjectOrderPlaced, ev)
}

// stampObservation looks up the Organization CR by slug and patches in
// the two observation annotations. The patch is byte-stable on
// duplicate delivery (Event.Timestamp is the broker-side timestamp,
// which is fixed per envelope), so controller-runtime does NOT enqueue
// a redundant Reconcile.
//
// Missing CR is not an error — log + return nil so the natsbus
// dispatcher Acks. A Nak on a soft miss would hot-loop the subscriber
// against a permanently-absent CR.
func (b *NATSBridge) stampObservation(ctx context.Context, slug, subject string, ev *natsbus.Event) error {
	var org orgapi.Organization
	// Organization is cluster-scoped (see orgapi/types.go), name == slug.
	if err := b.Client.Get(ctx, types.NamespacedName{Name: slug}, &org); err != nil {
		if apierrors.IsNotFound(err) {
			b.Log.Info("nats observation: no matching Organization CR — ack and skip",
				"subject", subject, "slug", slug, "event_id", ev.ID)
			return nil
		}
		// Transient API-server error — return so the dispatcher Naks
		// and JetStream redelivers after backoff.
		return fmt.Errorf("get organization %s: %w", slug, err)
	}

	observedAt := ev.Timestamp.UTC().Format(time.RFC3339Nano)
	if observedAt == "" || ev.Timestamp.IsZero() {
		observedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}

	// Skip the patch when the annotations already match — JetStream's
	// at-least-once delivery means we will see the same envelope on
	// broker rebalance, and a redundant patch would churn the informer.
	cur := org.GetAnnotations()
	if cur != nil &&
		cur[AnnotationLastNATSObservedAt] == observedAt &&
		cur[AnnotationLastNATSSubject] == subject {
		b.Log.V(1).Info("nats observation: duplicate envelope — skip patch",
			"subject", subject, "slug", slug, "event_id", ev.ID)
		return nil
	}

	desired := &orgapi.Organization{}
	org.DeepCopyInto(desired)
	anns := desired.GetAnnotations()
	if anns == nil {
		anns = map[string]string{}
	}
	anns[AnnotationLastNATSObservedAt] = observedAt
	anns[AnnotationLastNATSSubject] = subject
	desired.SetAnnotations(anns)

	if err := b.Client.Patch(ctx, desired, client.MergeFrom(&org)); err != nil {
		return fmt.Errorf("patch organization %s: %w", slug, err)
	}
	b.Log.Info("nats observation stamped — reconcile enqueued",
		"subject", subject, "slug", slug, "event_id", ev.ID, "observed_at", observedAt)
	return nil
}
