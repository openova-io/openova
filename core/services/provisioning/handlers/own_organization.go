// own_organization.go — every path that acts on `<slug>` for an order first
// proves `<slug>` is that order's own Organization.
//
// The slug is the key of everything the funnel touches after the creation
// path: the order's timeline waits on `<slug>/vcluster`, publishes
// provision.completed and PATCHes organizations/<slug> spec.tenantPublic, and
// each cart app is committed into `<slug>/catalyst-tenant`. A reused name (the
// tenant service frees a slug when the previous customer's tenant row goes,
// while that customer's Organization may still exist or still be Terminating)
// made every one of those act on the previous customer's Organization. The
// creation path refusing the name was not enough: its refusal event has no
// reader, and these paths never asked.
package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// errOrganizationOfAnotherTenant: `<slug>` is a live Organization of another
// tenant. Permanent — the order can never be bound to it.
var errOrganizationOfAnotherTenant = errors.New("the name is held by another tenant's Organization")

// errOrganizationNotYetOwn: `<slug>` is not this tenant's Organization YET —
// absent, being removed, or unreadable this time. A later read can clear it.
var errOrganizationNotYetOwn = errors.New("the Organization is not this tenant's yet")

// ownOrganizationWaitTimeout bounds how long the order's timeline waits for
// its own Organization. It outlasts the tenant.created redelivery window
// (MaxDeliver 10 with the 5s..5m backoff, about 20 minutes), so the creation
// path has made its last try before the timeline gives up.
var (
	ownOrganizationWaitTimeout = 30 * time.Minute
	ownOrganizationPollEvery   = 10 * time.Second
)

// checkOwnOrganization reads organizations/<slug> once and answers whether it
// is tenantID's: nil when it is, an error wrapping
// errOrganizationOfAnotherTenant when another tenant's live Organization holds
// the name, and an error wrapping errOrganizationNotYetOwn otherwise.
func (h *Handler) checkOwnOrganization(slug, tenantID string) error {
	body, err := h.k8sGet("/apis/orgs.openova.io/v1/organizations/" + slug)
	if err != nil {
		return fmt.Errorf("%w: Organization %s could not be read: %v", errOrganizationNotYetOwn, slug, err)
	}
	switch organizationOwnership(body, tenantID) {
	case orgOwnedByTenant:
		return nil
	case orgOwnedByOther:
		return fmt.Errorf("%w: %q", errOrganizationOfAnotherTenant, slug)
	case orgTerminating:
		return fmt.Errorf("%w: Organization %s is being removed", errOrganizationNotYetOwn, slug)
	default:
		return fmt.Errorf("%w: Organization %s could not be decoded", errOrganizationNotYetOwn, slug)
	}
}

// awaitOwnOrganization waits until `<slug>` is tenantID's own Organization.
// Another tenant's live Organization ends the wait at once (the creation path
// refuses that name for good); absent, Terminating or unreadable keeps
// waiting until timeout.
func (h *Handler) awaitOwnOrganization(slug, tenantID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := h.checkOwnOrganization(slug, tenantID)
		if err == nil || errors.Is(err, errOrganizationOfAnotherTenant) {
			return err
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("no Organization of this order after %s: %w", timeout, err)
		}
		slog.Info("order timeline: waiting for the order's own Organization",
			"slug", slug, "tenant_id", tenantID, "reason", err.Error())
		time.Sleep(ownOrganizationPollEvery)
	}
}

// ownOrganizationFailureMessage is the customer-facing text for a timeline that
// could not prove its own Organization. It names neither tenant id.
func ownOrganizationFailureMessage(slug string, err error) string {
	if errors.Is(err, errOrganizationOfAnotherTenant) {
		return fmt.Sprintf("the name %q is already taken by another Organization — this order was not bound to it", slug)
	}
	return fmt.Sprintf("the Organization %q for this order was not created in time", strings.TrimSpace(slug))
}
