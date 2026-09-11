package notify

import (
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Preference resolution (DESIGN.md §21.4).
//
// THE RULE, whole: the most specific preference row that names the recipient
// wins; with no row at all the catalogue's default applies; and a MANDATORY
// event is received whatever any row says.
//
// "Most specific" is arithmetic, not a list of cases — rank =
// 2·(the row names a person) + (the row names a customer):
//
//	rank 3  this person, on this customer
//	rank 2  this person, Sovereign-wide
//	rank 1  this customer
//	rank 0  the Sovereign default
//	(none)  the catalogue's own default
//
// A row naming YOU beats a row naming only your organisation, because a
// preference is a person's choice; between two rows naming you, the one that
// also names the customer wins, because that is the narrower statement.
//
// THE DEFAULT FOR AN UNSET PREFERENCE is the event's DefaultOn, and every
// event in the shipped catalogue is DefaultOn — deliberately: each one was
// being sent unconditionally before §21 existed, and a fresh Sovereign has
// no preference rows at all, so anything else would silently stop mail that
// was going out the day before.

// Resolution is what one recipient gets for one event.
type Resolution struct {
	Event Event
	// Enabled is whether the recipient receives the event at all.
	Enabled bool
	// Channels are the channels to deliver on, in order.
	Channels []string
	// Locale is the template locale to render in.
	Locale string
	// Source names what decided it, for the console and for the delivery
	// log's reason column.
	Source string
	// Forced records that the event is mandatory and a preference row tried
	// to weaken it — switch it off, or leave it only on a channel that
	// cannot carry it. The row is kept for every OTHER event; it simply
	// does not apply here.
	Forced bool
	// ForcedReason says exactly what was overridden.
	ForcedReason string
}

// Resolve applies the rule to one recipient. rows is everything that could
// apply — what store.NotificationPreferencesFor returns; rows for other
// events and for scopes that do not name this recipient are ignored here, so
// a caller may hand over one read and resolve many events from it.
func Resolve(e Event, rows []store.NotificationPreference, customerID, email string) Resolution {
	email = strings.ToLower(strings.TrimSpace(email))
	customerID = strings.TrimSpace(customerID)

	out := Resolution{
		Event:    e,
		Enabled:  e.DefaultOn,
		Channels: append([]string(nil), e.Channels...),
		Locale:   DefaultLocale,
		Source:   "catalogue default",
	}

	best := -1
	var chosen store.NotificationPreference
	for _, p := range rows {
		if p.Event != e.Key {
			continue
		}
		if p.CustomerID != nil && *p.CustomerID != "" && *p.CustomerID != customerID {
			continue
		}
		if p.Email != "" && !strings.EqualFold(p.Email, email) {
			continue
		}
		if r := p.Rank(); r > best {
			best, chosen = r, p
		}
	}
	if best >= 0 {
		out.Enabled = chosen.Enabled
		out.Source = chosen.ScopeLabel()
		if ch := cleanChannels(chosen.Channels); len(ch) > 0 {
			out.Channels = ch
		}
		if l := strings.ToLower(strings.TrimSpace(chosen.Locale)); l != "" {
			out.Locale = l
		}
	}

	if !e.Mandatory {
		return out
	}
	// MANDATORY. Two ways a row could weaken one, and both are refused
	// here rather than only at the write path: the resolver is what
	// actually decides whether a customer is told it owes money, and a row
	// that reached the table by any other route — an import, a restored
	// backup, a hand-written UPDATE — must not be able to stop an invoice.
	if !out.Enabled {
		out.Enabled, out.Forced = true, true
		out.ForcedReason = "mandatory: this notice cannot be switched off"
	}
	var missing []string
	for _, want := range e.Channels {
		if !containsChannel(out.Channels, want) {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		out.Channels = append(append([]string(nil), missing...), out.Channels...)
		out.Forced = true
		add := "mandatory: " + strings.Join(missing, ", ") + " cannot be removed from this notice"
		if out.ForcedReason == "" {
			out.ForcedReason = add
		} else {
			out.ForcedReason += "; " + add
		}
	}
	return out
}

func cleanChannels(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, c := range in {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

func containsChannel(list []string, want string) bool {
	for _, c := range list {
		if strings.EqualFold(c, want) {
			return true
		}
	}
	return false
}

// ValidatePreference reports why a preference row may not be written, or nil.
// It is the WRITE-side half of the mandatory guarantee: the resolver already
// ignores a row that would weaken a mandatory notice, and refusing to store
// one means an operator is told so instead of saving a switch that does
// nothing. An unknown event or channel is refused here too — a preference
// for something that cannot be sent is a typo, not a setting.
func ValidatePreference(p store.NotificationPreference, channels ChannelSet) error {
	e, ok := Lookup(strings.TrimSpace(p.Event))
	if !ok {
		return &PreferenceError{Message: "unknown notification event " + quote(p.Event) + "; GET /api/v1/notifications/events lists every one"}
	}
	for _, c := range p.Channels {
		name := strings.ToLower(strings.TrimSpace(c))
		if name == "" {
			continue
		}
		if _, ok := channels[name]; !ok {
			return &PreferenceError{Message: "unknown channel " + quote(c) + "; this build carries " + strings.Join(channels.Names(), ", ")}
		}
	}
	if l := strings.ToLower(strings.TrimSpace(p.Locale)); l != "" {
		if !hasLocale(l) {
			return &PreferenceError{Message: "no templates are registered for locale " + quote(p.Locale) + "; this build carries " + strings.Join(Locales(), ", ")}
		}
	}
	if !e.Mandatory {
		return nil
	}
	if !p.Enabled {
		return &PreferenceError{Message: e.Title + " is a mandatory notice and cannot be switched off: " + e.Desc}
	}
	if ch := cleanChannels(p.Channels); len(ch) > 0 {
		for _, want := range e.Channels {
			if !containsChannel(ch, want) {
				return &PreferenceError{Message: e.Title + " is a mandatory notice and must keep the " + want + " channel; add a channel rather than replacing it"}
			}
		}
	}
	return nil
}

// PreferenceError is a refusal an operator reads. The API maps it to 400.
type PreferenceError struct{ Message string }

func (e *PreferenceError) Error() string { return e.Message }

func hasLocale(l string) bool {
	for _, have := range Locales() {
		if have == l {
			return true
		}
	}
	return false
}

func quote(s string) string { return "\"" + s + "\"" }
