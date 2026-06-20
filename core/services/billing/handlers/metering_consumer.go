package handlers

// MeteringConsumer is the JetStream durable consumer for
// `catalyst.usage.recorded` (per #795 [Q-mine-3] + #798).
//
// On every envelope:
//
//  1. Validate the payload — request_id (idempotency key), customer_id,
//     reason, and a non-zero amount in either OMR or micro-OMR.
//  2. Resolve the Organization-vcluster Keycloak user UUID (CustomerID) to the
//     billing service's customer row (auto-create if missing — the
//     ADR-0003 hook may publish the user-create event AFTER the first
//     usage event lands during a cold start).
//  3. INSERT into credit_ledger with external_ref = request_id. The
//     UNIQUE partial index on external_ref makes the insert idempotent;
//     redelivery from JetStream is naturally collapsed.
//  4. Ack on success, Nak with backoff on DB error (transient → retry).
//
// The consumer does NOT reach back into NewAPI to confirm the request
// was billable — by the time the envelope lands, NewAPI has already
// finalised the response and decided this is a charge. The
// request_id-keyed dedupe is the only guard we need.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/openova-io/openova/core/services/billing/store"
	"github.com/openova-io/openova/core/services/shared/events"
)

// MeteringConsumer drives catalyst.usage.recorded into credit_ledger.
type MeteringConsumer struct {
	Store *store.Store

	// CustomerResolver resolves the Organization-vcluster Keycloak user UUID
	// (the payload's CustomerID) to the billing customer row. Defaults
	// to a Postgres-backed lookup that auto-creates a customer row on
	// first sighting; tests substitute a fake.
	CustomerResolver CustomerResolver

	// SourceTenantID surfaces in events.NewEvent as the envelope source.
	// Defaults to "billing" if empty.
	SourceTenantID string
}

// CustomerResolver looks up (or auto-creates) the billing customer for
// a NewAPI external_id. The metering envelope carries the Organization-vcluster
// Keycloak user UUID as customer_id; that maps to the billing-service
// customer row by user_id (which carries the same UUID per ADR-0003).
type CustomerResolver interface {
	// Resolve returns the billing customer row for the given userID
	// (= Organization-vcluster Keycloak user UUID = NewAPI external_id). When
	// no row exists AND tenantID is non-empty, an empty Customer is
	// auto-created so the metering insert can succeed.
	Resolve(ctx context.Context, userID, tenantID string) (*store.Customer, error)
}

// DefaultCustomerResolver is the production CustomerResolver wired
// against the Postgres-backed store.
type DefaultCustomerResolver struct {
	Store *store.Store
}

// Resolve implements CustomerResolver. Auto-creates the customer row
// on first sighting. The auto-create path is critical for Organization-tier
// cold starts where the first metered LLM call may arrive before the
// unified-rbac NATS publisher (#802) has emitted the corresponding
// org.user.created event into billing.
func (r DefaultCustomerResolver) Resolve(ctx context.Context, userID, tenantID string) (*store.Customer, error) {
	if userID == "" {
		return nil, errors.New("metering: empty user_id")
	}
	if r.Store == nil {
		return nil, errors.New("metering: store not configured")
	}
	cust, err := r.Store.GetCustomerByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("metering: get customer: %w", err)
	}
	if cust != nil {
		return cust, nil
	}
	if tenantID == "" {
		// Without a tenant_id we cannot satisfy the customers row's
		// NOT NULL constraint. Treat as a transient resolution miss —
		// the NATS subscriber will redeliver after AckWait and the
		// rbac-side user-create will likely have populated the row by
		// then.
		return nil, fmt.Errorf("metering: customer %s not found and no tenant_id to auto-create", userID)
	}
	cust = &store.Customer{
		UserID:   userID,
		TenantID: tenantID,
		// Email is unknown at this point; rbac fills it on the
		// canonical org.user.created envelope. Empty email is allowed
		// by the customers schema (TEXT NOT NULL with DEFAULT '' is
		// effectively the same once we update via rbac).
		Email: "",
	}
	if err := r.Store.CreateCustomer(ctx, cust); err != nil {
		return nil, fmt.Errorf("metering: auto-create customer: %w", err)
	}
	return cust, nil
}

// Handle is the shared message processor. Used directly by tests with a
// fake jetstream.Msg; called from the live consumer's callback below.
//
// Returns nil to indicate the handler succeeded (caller should Ack);
// returns a non-nil error to signal the handler failed and the message
// should be Nak'd (JetStream redelivers).
func (c *MeteringConsumer) Handle(ctx context.Context, body []byte) error {
	var payload events.UsageRecordedPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		// Malformed envelopes are NOT retryable. We log + return nil
		// so the caller Acks; replaying the same poison pill forever
		// would block the entire stream behind a bad message.
		slog.Error("metering: malformed payload — ack to skip",
			"error", err, "body_size", len(body))
		return nil
	}

	if payload.Metadata.RequestID == "" {
		slog.Error("metering: missing metadata.request_id — ack to skip")
		return nil
	}
	if payload.CustomerID == "" {
		slog.Error("metering: missing customer_id — ack to skip",
			"request_id", payload.Metadata.RequestID)
		return nil
	}
	if payload.Reason == "" {
		slog.Error("metering: missing reason — ack to skip",
			"request_id", payload.Metadata.RequestID)
		return nil
	}

	// Compute the canonical micro-OMR amount. The publisher SHOULD set
	// AmountMicroOMR; if it didn't, derive from AmountOMR.
	micro := payload.AmountMicroOMR
	if micro == 0 && payload.AmountOMR != 0 {
		// round-half-away-from-zero so a -0.0000005 rounds to -1
		// (favours the customer being charged correctly rather than
		// silently dropping fractional events).
		if payload.AmountOMR < 0 {
			micro = int64(payload.AmountOMR*1_000_000 - 0.5)
		} else {
			micro = int64(payload.AmountOMR*1_000_000 + 0.5)
		}
	}
	if micro >= 0 {
		// Usage MUST be a negative spend; positive (or zero) values are
		// suspicious — log + ack to skip.
		slog.Error("metering: non-negative amount — ack to skip",
			"request_id", payload.Metadata.RequestID,
			"amount_micro_omr", micro,
			"amount_omr", payload.AmountOMR,
			"reason", payload.Reason)
		return nil
	}

	// Resolve customer.
	resolver := c.CustomerResolver
	if resolver == nil {
		resolver = DefaultCustomerResolver{Store: c.Store}
	}
	cust, err := resolver.Resolve(ctx, payload.CustomerID, payload.Metadata.TenantID)
	if err != nil {
		// Transient — Nak with a 5s backoff so the rbac user-create has
		// time to land if this is a cold-start race.
		slog.Warn("metering: customer resolve failed — nak for retry",
			"request_id", payload.Metadata.RequestID,
			"customer_id", payload.CustomerID,
			"tenant_id", payload.Metadata.TenantID,
			"error", err)
		return err
	}

	metaJSON, err := json.Marshal(payload.Metadata)
	if err != nil {
		// Marshalling our own struct should never fail; log defensively
		// and ack to skip.
		slog.Error("metering: marshal metadata failed — ack to skip",
			"request_id", payload.Metadata.RequestID, "error", err)
		return nil
	}

	ledgerID, balanceMicroOMR, dup, err := c.Store.RecordUsage(ctx, store.UsageEntry{
		CustomerID:     cust.ID,
		AmountMicroOMR: micro,
		Reason:         payload.Reason,
		ExternalRef:    payload.Metadata.RequestID,
		Metadata:       metaJSON,
	})
	if err != nil {
		// DB failure is transient.
		slog.Error("metering: record usage failed — nak for retry",
			"request_id", payload.Metadata.RequestID, "error", err)
		return err
	}
	if dup {
		slog.Info("metering: duplicate envelope — collapsed to existing row",
			"request_id", payload.Metadata.RequestID,
			"ledger_id", ledgerID)
		return nil
	}

	slog.Info("metering: ledger row written",
		"request_id", payload.Metadata.RequestID,
		"customer_id", cust.ID,
		"reason", payload.Reason,
		"amount_micro_omr", micro,
		"balance_after_micro_omr", balanceMicroOMR,
		"ledger_id", ledgerID,
		"model", payload.Metadata.Model,
		"tokens_used", payload.Metadata.TokensUsed,
	)
	return nil
}

// Start drives the durable consumer until ctx is cancelled. Returns
// the first non-recoverable error (e.g., subscription closed by the
// broker).
func (c *MeteringConsumer) Start(ctx context.Context, sub events.NATSSubscription) error {
	slog.Info("metering: starting consumer",
		"subject", events.SubjectUsageRecorded,
		"durable", events.ConsumerOrgBillingMetering)
	return sub.Consume(ctx, func(msg jetstream.Msg) {
		// Per-message ctx with a budget so a hung handler cannot stall
		// the consumer indefinitely. AckWait on the consumer side is
		// 30s; we do the work in 25s to stay inside that envelope.
		mctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()

		if err := c.Handle(mctx, msg.Data()); err != nil {
			// Nak with backoff — let JetStream redeliver after a delay
			// so transient DB outages don't hot-loop.
			if nakErr := msg.NakWithDelay(5 * time.Second); nakErr != nil {
				slog.Error("metering: nak failed", "error", nakErr)
			}
			return
		}
		if ackErr := msg.Ack(); ackErr != nil {
			slog.Error("metering: ack failed", "error", ackErr)
		}
	})
}

// HumanReason renders a usage reason for log lines without exposing
// the full envelope. Kept exported so tests assert formatting.
func HumanReason(model, prefix string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		model = "unknown"
	}
	if prefix == "" {
		prefix = "usage:newapi"
	}
	return prefix + ":" + model
}
