package events

// Canonical event topic names. Producers and consumers MUST use these
// constants instead of string literals so topic symmetry is enforced at
// compile time. Every change here must be mirrored on the subscriber side.
//
// Naming rule: sme.<producer>.events for standard domain topics, sme.dlq
// for the cross-service dead-letter topic (see dlq.go).
const (
	// TopicUserEvents carries auth + user lifecycle events (user.login,
	// user.created, ...). Consumed by notification for welcome / magic-link
	// follow-up emails.
	TopicUserEvents = "sme.user.events"

	// TopicOrderEvents carries billing order events (order.placed,
	// payment.received, payment.failed). Consumed by notification for
	// payment receipts.
	TopicOrderEvents = "sme.order.events"

	// TopicBillingEvents carries billing lifecycle events (subscription
	// renewed, invoice issued). Kept for forward compatibility; notification
	// subscribes so any producer can move here without another consumer
	// rewire.
	TopicBillingEvents = "sme.billing.events"

	// TopicProvisionEvents carries provisioning lifecycle events,
	// including day-1 (provision.started/completed/failed) and day-2
	// (provision.app_ready/app_removed/app_failed). Consumed by tenant
	// (state sync) and notification (customer emails).
	TopicProvisionEvents = "sme.provision.events"

	// TopicTenantEvents carries tenant lifecycle + app-change-requested
	// events (tenant.created, tenant.deleted, tenant.app_install_requested,
	// tenant.app_uninstall_requested). Consumed by provisioning
	// (orchestration) and notification (audit emails).
	TopicTenantEvents = "sme.tenant.events"

	// TopicDomainEvents carries domain lifecycle events
	// (domain.registered, domain.verified, domain.removed). Consumed by
	// notification for BYOD-verified emails.
	TopicDomainEvents = "sme.domain.events"

	// TopicDLQ is the cross-service dead-letter topic for events that fail
	// handler invocation after the configured retry budget. See dlq.go.
	TopicDLQ = "sme.dlq"
)

// LegacyTopics lists topic names that were in use before the
// sme.<producer>.events convention was adopted. Consumers can fan in both
// the canonical and legacy names to bridge a publisher-side rename without
// an atomic flag day.
//
// Producers should NOT use any of these — they exist for the consumer
// side during the transition window documented in issues #69 and #70.
var LegacyTopics = struct {
	AuthEvents   string
	DomainEvents string
}{
	AuthEvents:   "auth.events",
	DomainEvents: "domain-events",
}
