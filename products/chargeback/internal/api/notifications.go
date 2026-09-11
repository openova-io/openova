package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/notify"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Notification management (DESIGN.md §21) — the Configure → Notifications
// screen and the customer's own Notifications tab.
//
//	GET    /api/v1/notifications/events                              metering.read  (Sovereign)
//	GET    /api/v1/notifications/preferences                         metering.read  (Sovereign)
//	PUT    /api/v1/notifications/preferences                         settings.manage
//	DELETE /api/v1/notifications/preferences/{event}                 settings.manage
//	GET    /api/v1/notifications/deliveries                          audit.read     (Sovereign)
//	GET    /api/v1/customers/{id}/notifications/preferences          metering.read  on the customer
//	PUT    /api/v1/customers/{id}/notifications/preferences          customer.self.manage
//	DELETE /api/v1/customers/{id}/notifications/preferences/{event}  customer.self.manage
//	GET    /api/v1/customers/{id}/notifications/deliveries           metering.read  on the customer
//
// READING the catalogue is metering.read: what the product sends is not a
// secret, and every role that receives a notice may see which notices exist.
// WRITING a Sovereign-wide preference is settings.manage — it changes what
// every customer receives. A customer's OWN preferences are
// customer.self.manage, the same permission its users and its PO reference
// sit behind; a Sovereign-wide customers.manage implies it (access.implies),
// so an operator can set a customer's preference without holding a
// customer-scoped binding.
//
// The DELIVERY LOG is audit.read at the Sovereign — it is an audit trail, and
// it carries subject lines. A customer reads its OWN deliveries under
// metering.read, filtered by the store scope to its own rows; a delivery with
// no customer (an operator's sign-in code) is visible to no customer at all.
//
// The §18 CLOSED-PERIOD guard does not apply here and is deliberately absent:
// a notification writes nothing to the journal and changes no money, so a
// closed period has nothing to protect against. Editing a preference in
// January cannot alter what December's ledger says.

// notificationCatalogueDoc is everything the console needs to render the
// catalogue without a second call: the events, the channels with their
// availability, the templates, the locales and the delivery statuses.
type notificationCatalogueDoc struct {
	Events        []notify.Event       `json:"events"`
	Channels      []notify.ChannelDoc  `json:"channels"`
	Templates     []notify.TemplateDoc `json:"templates"`
	Locales       []string             `json:"locales"`
	DefaultLocale string               `json:"default_locale"`
	Statuses      []string             `json:"statuses"`
}

func (h *Handler) notificationEvents(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.MeteringRead); !ok {
		return
	}
	writeJSON(w, http.StatusOK, notificationCatalogueDoc{
		Events:        notify.Events(),
		Channels:      h.notifier().Channels.Docs(),
		Templates:     notify.TemplateDocs(),
		Locales:       notify.Locales(),
		DefaultLocale: notify.DefaultLocale,
		Statuses:      store.NotifyStatuses,
	})
}

// effectivePreference is one event as this scope actually receives it: the
// resolution, and which row decided it.
type effectivePreference struct {
	Event        string   `json:"event"`
	Title        string   `json:"title"`
	Category     string   `json:"category"`
	Mandatory    bool     `json:"mandatory"`
	Enabled      bool     `json:"enabled"`
	Channels     []string `json:"channels"`
	Locale       string   `json:"locale"`
	Source       string   `json:"source"`
	Forced       bool     `json:"forced,omitempty"`
	ForcedReason string   `json:"forced_reason,omitempty"`
}

// notificationPreferencesDoc is one scope's picture: the rows that exist,
// the setting each event RESOLVES to for that scope, and the catalogue the
// console needs to render a switch — all in one call, because a settings
// page that needs four is a settings page that renders in stages.
type notificationPreferencesDoc struct {
	Scope       string                         `json:"scope"`
	CustomerID  *string                        `json:"customer_id,omitempty"`
	Email       string                         `json:"email,omitempty"`
	Preferences []store.NotificationPreference `json:"preferences"`
	Effective   []effectivePreference          `json:"effective"`
	Channels    []notify.ChannelDoc            `json:"channels"`
	Locales     []string                       `json:"locales"`
	Events      []notify.Event                 `json:"events"`
}

// effectiveFor resolves every event for one (customer, email) pair from the
// rows that could apply, which is ONE read however many events there are.
func effectiveFor(rows []store.NotificationPreference, customerID, email string) []effectivePreference {
	out := []effectivePreference{}
	for _, e := range notify.Events() {
		res := notify.Resolve(e, rows, customerID, email)
		out = append(out, effectivePreference{
			Event: e.Key, Title: e.Title, Category: e.Category, Mandatory: e.Mandatory,
			Enabled: res.Enabled, Channels: res.Channels, Locale: res.Locale, Source: res.Source,
			Forced: res.Forced, ForcedReason: res.ForcedReason,
		})
	}
	return out
}

// listNotificationPreferences answers the Sovereign view: every row the
// caller may read, and the effective setting at the Sovereign scope.
func (h *Handler) listNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.MeteringRead)
	if !ok {
		return
	}
	rows, err := h.Store.ListNotificationPreferences(r.Context(), s.Scope())
	if err != nil {
		storeErr(w, err)
		return
	}
	email := strings.TrimSpace(r.URL.Query().Get("email"))
	writeJSON(w, http.StatusOK, notificationPreferencesDoc{
		Scope:       access.ScopeSovereign,
		Email:       email,
		Preferences: rows,
		Effective:   effectiveFor(rows, "", email),
		Channels:    h.notifier().Channels.Docs(),
		Locales:     notify.Locales(),
		Events:      notify.Events(),
	})
}

// customerNotificationPreferences answers one customer's view: the rows that
// apply to it — its own, and the Sovereign ones above it — and the effective
// setting for the customer, or for one of its users when ?email= is given.
func (h *Handler) customerNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireCustomer(w, r, id, false); !ok {
		return
	}
	email := normEmail(r.URL.Query().Get("email"))
	rows, err := h.Store.NotificationPreferencesFor(r.Context(), id, email)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, notificationPreferencesDoc{
		Scope:       access.ScopeCustomer + ":" + id,
		CustomerID:  &id,
		Email:       email,
		Preferences: rows,
		Effective:   effectiveFor(rows, id, email),
		Channels:    h.notifier().Channels.Docs(),
		Locales:     notify.Locales(),
		Events:      notify.Events(),
	})
}

// preferenceInput is the PUT body.
type preferenceInput struct {
	Event    string   `json:"event"`
	Enabled  *bool    `json:"enabled"`
	Channels []string `json:"channels"`
	Locale   string   `json:"locale"`
	// Email narrows the row to one person; empty is the whole scope.
	Email string `json:"email"`
	// CustomerID is honoured only on the Sovereign route; the customer
	// route forces it from the path so a customer principal can never write
	// another customer's row.
	CustomerID *string `json:"customer_id"`
}

func (h *Handler) putNotificationPreference(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.SettingsManage); !ok {
		return
	}
	var in preferenceInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	var customerID *string
	if in.CustomerID != nil && strings.TrimSpace(*in.CustomerID) != "" {
		id := strings.TrimSpace(*in.CustomerID)
		customerID = &id
	}
	h.writeNotificationPreference(w, r, in, customerID)
}

func (h *Handler) putCustomerNotificationPreference(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireCustomer(w, r, id, true); !ok {
		return
	}
	var in preferenceInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	// The path wins over the body, always: a customer principal writing
	// another customer's id would otherwise be writing outside its scope.
	h.writeNotificationPreference(w, r, in, &id)
}

func (h *Handler) writeNotificationPreference(w http.ResponseWriter, r *http.Request, in preferenceInput, customerID *string) {
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	p := store.NotificationPreference{
		Event:      strings.TrimSpace(in.Event),
		CustomerID: customerID,
		Email:      normEmail(in.Email),
		Enabled:    enabled,
		Channels:   in.Channels,
		Locale:     strings.ToLower(strings.TrimSpace(in.Locale)),
	}
	if p.Email != "" && !validEmail(p.Email) {
		writeErr(w, http.StatusBadRequest, "email must be an address, or empty for the whole scope")
		return
	}
	// The MANDATORY guarantee, write side (DESIGN.md §21.4): an operator is
	// told the switch would do nothing rather than being allowed to save
	// one that the resolver then ignores.
	if err := notify.ValidatePreference(p, h.notifier().Channels); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := h.Store.PutNotificationPreference(r.Context(), p)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrInvalid):
			writeErr(w, http.StatusBadRequest, invalidMessage(err))
		case errors.Is(err, store.ErrNotFound):
			writeErr(w, http.StatusNotFound, "no such customer")
		default:
			storeErr(w, err)
		}
		return
	}
	h.audit(r, out.CustomerID, "notification.preference.put", map[string]any{
		"event": out.Event, "enabled": out.Enabled, "channels": out.Channels, "locale": out.Locale, "email": out.Email, "scope": out.ScopeLabel(),
	})
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) deleteNotificationPreference(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.SettingsManage); !ok {
		return
	}
	var customerID *string
	if id := strings.TrimSpace(r.URL.Query().Get("customer_id")); id != "" {
		customerID = &id
	}
	h.removeNotificationPreference(w, r, r.PathValue("event"), customerID, r.URL.Query().Get("email"))
}

func (h *Handler) deleteCustomerNotificationPreference(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireCustomer(w, r, id, true); !ok {
		return
	}
	h.removeNotificationPreference(w, r, r.PathValue("event"), &id, r.URL.Query().Get("email"))
}

func (h *Handler) removeNotificationPreference(w http.ResponseWriter, r *http.Request, event string, customerID *string, email string) {
	event = strings.TrimSpace(event)
	if _, ok := notify.Lookup(event); !ok {
		writeErr(w, http.StatusNotFound, "unknown notification event "+event)
		return
	}
	if err := h.Store.DeleteNotificationPreference(r.Context(), event, customerID, normEmail(email)); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, customerID, "notification.preference.delete", map[string]any{"event": event, "email": normEmail(email)})
	w.WriteHeader(http.StatusNoContent)
}

// notificationDeliveriesDoc is the log plus the tallies the console's tiles
// read, so "what went out, and what did not" is one call.
type notificationDeliveriesDoc struct {
	Deliveries []store.NotificationDelivery      `json:"deliveries"`
	Stats      []store.NotificationDeliveryCount `json:"stats"`
	Since      time.Time                         `json:"since"`
	Limit      int                               `json:"limit"`
	Statuses   []string                          `json:"statuses"`
}

// deliveryStatsWindow is the window the tiles count over. Seven days is the
// span an operator asks "did anything fail this week" over.
const deliveryStatsWindow = 7 * 24 * time.Hour

func (h *Handler) notificationDeliveries(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.AuditRead)
	if !ok {
		return
	}
	h.writeDeliveries(w, r, s.Scope(), notificationFilterFrom(r))
}

func (h *Handler) customerNotificationDeliveries(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	f := notificationFilterFrom(r)
	// The path pins the customer whatever the query said, and the store
	// scope narrows it again underneath.
	f.CustomerID = id
	h.writeDeliveries(w, r, s.Scope(), f)
}

func notificationFilterFrom(r *http.Request) store.NotificationDeliveryFilter {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(strings.TrimSpace(q.Get("limit")))
	return store.NotificationDeliveryFilter{
		Event:      strings.TrimSpace(q.Get("event")),
		Status:     strings.TrimSpace(q.Get("status")),
		CustomerID: strings.TrimSpace(q.Get("customer_id")),
		Recipient:  strings.TrimSpace(q.Get("recipient")),
		Limit:      limit,
	}
}

func (h *Handler) writeDeliveries(w http.ResponseWriter, r *http.Request, scope store.Scope, f store.NotificationDeliveryFilter) {
	rows, err := h.Store.ListNotificationDeliveries(r.Context(), scope, f)
	if err != nil {
		storeErr(w, err)
		return
	}
	since := h.Now().UTC().Add(-deliveryStatsWindow)
	stats, err := h.Store.NotificationDeliveryStats(r.Context(), scope, since)
	if err != nil {
		storeErr(w, err)
		return
	}
	limit := f.Limit
	if limit <= 0 {
		limit = store.DefaultNotificationDeliveryLimit
	}
	if limit > store.MaxNotificationDeliveryLimit {
		limit = store.MaxNotificationDeliveryLimit
	}
	writeJSON(w, http.StatusOK, notificationDeliveriesDoc{Deliveries: rows, Stats: stats, Since: since, Limit: limit, Statuses: store.NotifyStatuses})
}
