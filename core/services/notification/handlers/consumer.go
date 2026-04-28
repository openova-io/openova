package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/openova-io/openova/core/services/notification/templates"
	"github.com/openova-io/openova/core/services/shared/events"
)

// StartConsumer subscribes to every topic notification listens on and
// routes each event to the right email handler. Consumers use
// events.DLQSubscriber so a poison record is shipped to sme.dlq after
// 3 in-memory retries instead of blocking the partition (issue #72).
//
// Topic fan-in bridges the transition described in issues #69 and #70:
// both the canonical sme.*.events names and the legacy auth.events /
// domain-events names are subscribed, so this service does not depend
// on publisher-side renames landing first.
func (h *Handler) StartConsumer(ctx context.Context, sub Subscriber) error {
	return sub.Subscribe(ctx, func(event *events.Event) error {
		slog.Info("received event",
			"type", event.Type,
			"source", event.Source,
			"tenant", event.TenantID,
			"event_id", event.ID,
		)

		switch event.Type {
		// User / auth events (#69 — notification was subscribing to
		// sme.user.events while auth published to auth.events).
		case "user.login":
			return h.handleUserLogin(event)

		// Billing / order events (#69 — billing publishes on
		// sme.order.events, notification used to subscribe only to
		// sme.billing.events).
		case "payment.received":
			return h.handlePaymentReceived(event)
		case "order.placed":
			return h.handleOrderPlaced(event)

		// Day-1 provisioning lifecycle (unchanged).
		case "provision.started":
			return h.handleProvisionStarted(event)
		case "provision.completed":
			return h.handleProvisionCompleted(event)
		case "provision.failed":
			return h.handleProvisionFailed(event)

		// Day-2 app lifecycle (#74 — add the three missing emails).
		case "provision.app_ready":
			return h.handleAppReady(ctx, event)
		case "provision.app_removed":
			return h.handleAppRemoved(ctx, event)
		case "provision.app_failed":
			return h.handleAppFailed(ctx, event)

		// Domain events (#70 — domain-service publishes these; nobody
		// consumed them before).
		case "domain.registered":
			return h.handleDomainRegistered(ctx, event)
		case "domain.verified":
			return h.handleDomainVerified(ctx, event)
		case "domain.removed":
			return h.handleDomainRemoved(ctx, event)

		// Member invitation (unchanged).
		case "member.invited":
			return h.handleMemberInvited(event)

		default:
			slog.Debug("ignoring unhandled event type", "type", event.Type)
			return nil
		}
	})
}

// Subscriber is the minimal interface StartConsumer needs. It matches
// both events.Consumer.Subscribe and events.DLQSubscriber.Subscribe so
// production wires the DLQ-backed variant while unit tests can pass a
// fake.
type Subscriber interface {
	Subscribe(ctx context.Context, handler func(*events.Event) error) error
}

// ---------------------------------------------------------------------------
// user / auth
// ---------------------------------------------------------------------------

func (h *Handler) handleUserLogin(event *events.Event) error {
	var data struct {
		Email   string `json:"email"`
		Name    string `json:"name"`
		IsFirst bool   `json:"is_first"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return err
	}
	// Only send welcome email on first login.
	if !data.IsFirst {
		return nil
	}
	if data.Email == "" {
		slog.Debug("user.login missing email; skipping welcome email", "event_id", event.ID)
		return nil
	}
	body := templates.WelcomeEmail(data.Name)
	return h.Mailer.Send(data.Email, "Welcome to OpenOva SME", body)
}

// ---------------------------------------------------------------------------
// billing / orders
// ---------------------------------------------------------------------------

func (h *Handler) handlePaymentReceived(event *events.Event) error {
	var data struct {
		Email   string `json:"email"`
		OrgName string `json:"org_name"`
		Amount  int    `json:"amount"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return err
	}
	if data.Email == "" {
		return nil
	}
	body := templates.PaymentReceivedEmail(data.OrgName, data.Amount)
	return h.Mailer.Send(data.Email, "Payment confirmation", body)
}

// handleOrderPlaced sends a receipt when billing records a new order.
// The payload is the order struct as published by
// billing.dispatchOrderPlaced. When the producer doesn't carry
// email/org_name, we fall back to the enricher so the receipt still
// goes out (#69).
func (h *Handler) handleOrderPlaced(event *events.Event) error {
	var data struct {
		Email         string `json:"email"`
		OrgName       string `json:"org_name"`
		TotalOMRCents int    `json:"total_omr_cents"`
		Amount        int    `json:"amount"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return err
	}
	if data.Email == "" && h.Enricher != nil && event.TenantID != "" {
		info, err := h.Enricher.Lookup(context.Background(), event.TenantID)
		if err != nil {
			return err
		}
		if info != nil {
			data.Email = info.OwnerEmail
			if data.OrgName == "" {
				data.OrgName = info.OrgName
			}
		}
	}
	if data.Email == "" {
		slog.Warn("order.placed missing email; skipping receipt",
			"event_id", event.ID, "tenant_id", event.TenantID)
		return nil
	}
	amount := data.TotalOMRCents
	if amount == 0 {
		amount = data.Amount
	}
	body := templates.PaymentReceivedEmail(data.OrgName, amount)
	return h.Mailer.Send(data.Email, "Order confirmation", body)
}

// ---------------------------------------------------------------------------
// provisioning (day-1)
// ---------------------------------------------------------------------------

func (h *Handler) handleProvisionStarted(event *events.Event) error {
	var data struct {
		Email   string   `json:"email"`
		OrgName string   `json:"org_name"`
		Apps    []string `json:"apps"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return err
	}
	if data.Email == "" {
		return nil
	}
	body := templates.ProvisionStartedEmail(data.OrgName, data.Apps)
	return h.Mailer.Send(data.Email, "We're setting up your tenant", body)
}

func (h *Handler) handleProvisionCompleted(event *events.Event) error {
	var data struct {
		Email        string   `json:"email"`
		OrgName      string   `json:"org_name"`
		WorkspaceURL string   `json:"workspace_url"`
		Apps         []string `json:"apps"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return err
	}
	if data.Email == "" {
		return nil
	}
	body := templates.ProvisionCompletedEmail(data.OrgName, data.WorkspaceURL, data.Apps)
	return h.Mailer.Send(data.Email, "Your tenant is ready!", body)
}

func (h *Handler) handleProvisionFailed(event *events.Event) error {
	var data struct {
		Email    string `json:"email"`
		OrgName  string `json:"org_name"`
		ErrorMsg string `json:"error_msg"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return err
	}
	if data.Email == "" {
		return nil
	}
	body := templates.ProvisionFailedEmail(data.OrgName, data.ErrorMsg)
	return h.Mailer.Send(data.Email, "Something went wrong with your tenant", body)
}

// ---------------------------------------------------------------------------
// provisioning (day-2) — #74
// ---------------------------------------------------------------------------

// appChangePayload mirrors the map[string]any published by provisioning
// on day-2 install/uninstall completion. See
// services/provisioning/handlers/consumer.go for the producer.
type appChangePayload struct {
	AppSlug   string   `json:"app_slug"`
	AppID     string   `json:"app_id"`
	DeployIDs []string `json:"deploy_ids"`
	Action    string   `json:"action"`
	Error     string   `json:"error,omitempty"`
}

func (h *Handler) handleAppReady(ctx context.Context, event *events.Event) error {
	var data appChangePayload
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return err
	}
	info, err := h.enrichOrSkip(ctx, event, "provision.app_ready")
	if err != nil {
		return err
	}
	if info == nil {
		return nil
	}
	subject := data.AppSlug + " is ready on " + info.OrgName
	body := templates.AppReadyEmail(info.OrgName, data.AppSlug, info.WorkspaceURL)
	return h.Mailer.Send(info.OwnerEmail, subject, body)
}

func (h *Handler) handleAppRemoved(ctx context.Context, event *events.Event) error {
	var data appChangePayload
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return err
	}
	info, err := h.enrichOrSkip(ctx, event, "provision.app_removed")
	if err != nil {
		return err
	}
	if info == nil {
		return nil
	}
	subject := data.AppSlug + " uninstalled from " + info.OrgName
	body := templates.AppRemovedEmail(info.OrgName, data.AppSlug)
	return h.Mailer.Send(info.OwnerEmail, subject, body)
}

func (h *Handler) handleAppFailed(ctx context.Context, event *events.Event) error {
	var data appChangePayload
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return err
	}
	info, err := h.enrichOrSkip(ctx, event, "provision.app_failed")
	if err != nil {
		return err
	}
	if info == nil {
		return nil
	}
	action := data.Action
	if action == "" {
		action = "change"
	}
	subject := data.AppSlug + " " + action + " failed on " + info.OrgName
	body := templates.AppFailedEmail(info.OrgName, data.AppSlug, action, data.Error)
	return h.Mailer.Send(info.OwnerEmail, subject, body)
}

// enrichOrSkip is the shared enrichment path for day-2 and domain
// events. Returns (nil, nil) without error when the Enricher is
// unconfigured — so operators can disable enrichment by removing the
// TENANT_URL / AUTH_URL env vars without the consumer erroring into
// the DLQ. Returns (nil, err) on transport failure so the DLQ path
// still catches genuine broker/HTTP outages.
func (h *Handler) enrichOrSkip(ctx context.Context, event *events.Event, kind string) (*TenantInfo, error) {
	if h.Enricher == nil {
		slog.Warn("no enricher configured; skipping email",
			"kind", kind, "event_id", event.ID, "tenant_id", event.TenantID)
		return nil, nil
	}
	info, err := h.Enricher.Lookup(ctx, event.TenantID)
	if err != nil {
		return nil, err
	}
	if info == nil {
		slog.Warn("enricher disabled; skipping email",
			"kind", kind, "event_id", event.ID, "tenant_id", event.TenantID)
		return nil, nil
	}
	if info.OwnerEmail == "" {
		slog.Warn("enricher returned empty email; skipping email",
			"kind", kind, "event_id", event.ID, "tenant_id", event.TenantID)
		return nil, nil
	}
	return info, nil
}

// ---------------------------------------------------------------------------
// domain — #70
// ---------------------------------------------------------------------------

type domainPayload struct {
	Domain   string `json:"domain"`
	TenantID string `json:"tenant_id"`
	Status   string `json:"status"`
}

func (h *Handler) handleDomainRegistered(ctx context.Context, event *events.Event) error {
	return h.sendDomainEmail(ctx, event, "domain.registered", "Domain added", templates.DomainRegisteredEmail)
}

func (h *Handler) handleDomainVerified(ctx context.Context, event *events.Event) error {
	return h.sendDomainEmail(ctx, event, "domain.verified", "Domain verified", templates.DomainVerifiedEmail)
}

func (h *Handler) handleDomainRemoved(ctx context.Context, event *events.Event) error {
	return h.sendDomainEmail(ctx, event, "domain.removed", "Domain removed", templates.DomainRemovedEmail)
}

func (h *Handler) sendDomainEmail(ctx context.Context, event *events.Event, kind, subjectPrefix string, renderer func(orgName, domain string) string) error {
	var data domainPayload
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return err
	}
	if data.Domain == "" {
		return errors.New("domain event missing 'domain' field")
	}
	info, err := h.enrichOrSkip(ctx, event, kind)
	if err != nil {
		return err
	}
	if info == nil {
		return nil
	}
	subject := subjectPrefix + ": " + data.Domain
	body := renderer(info.OrgName, data.Domain)
	return h.Mailer.Send(info.OwnerEmail, subject, body)
}

// ---------------------------------------------------------------------------
// misc (unchanged)
// ---------------------------------------------------------------------------

func (h *Handler) handleMemberInvited(event *events.Event) error {
	var data struct {
		Email       string `json:"email"`
		OrgName     string `json:"org_name"`
		InviterName string `json:"inviter_name"`
		Role        string `json:"role"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return err
	}
	if data.Email == "" {
		return nil
	}
	body := templates.InviteMemberEmail(data.OrgName, data.InviterName, data.Role)
	return h.Mailer.Send(data.Email, "You've been invited to "+data.OrgName, body)
}
