package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Store is what the notifier needs from the database. An interface rather
// than *store.Store so a unit test can drive the whole send path — the
// preference rule, the retry ladder, the delivery log — with no Postgres.
type Store interface {
	NotificationPreferencesFor(ctx context.Context, customerID, email string) ([]store.NotificationPreference, error)
	RecordNotificationDelivery(ctx context.Context, in store.NotificationDeliveryInput) (store.NotificationDelivery, error)
}

// Notifier is the ONE way this product sends a message (DESIGN.md §21). It
// resolves the recipient's preference, renders the event's template, carries
// it on each resolved channel, retries a transient failure with bounded
// backoff, and records EVERY attempt.
//
// A nil Store is a notifier with no preferences and no log: every event
// resolves to its catalogue default and nothing is recorded. That is what a
// unit test of some other package gets when it wires only a mail sender, and
// it is why such a test keeps behaving exactly as it did.
type Notifier struct {
	// Channels are the transports. Empty = DefaultChannels(nil), which can
	// carry nothing and says so on every attempt.
	Channels ChannelSet
	// Store carries preferences and the delivery log; nil = neither.
	Store Store
	// Metrics counts attempts by event, channel and outcome; nil = none.
	Metrics *metrics.Registry
	// Now defaults to time.Now.
	Now func() time.Time
	// Attempts is how many times ONE channel is tried before the failure is
	// permanent; default DefaultAttempts. A permanent failure is never
	// retried whatever this says.
	Attempts int
	// Backoff is the wait before the second attempt; it doubles for each
	// one after. Default DefaultBackoff.
	Backoff time.Duration
	// sleep is the wait, replaced in tests so the retry ladder is walked
	// without walking the clock.
	sleep func(ctx context.Context, d time.Duration)
}

// DefaultAttempts is how many times a channel is tried. Three is the
// ordinary shape: the first try, one for a blip, one for a longer one.
const DefaultAttempts = 3

// DefaultBackoff is the wait before the second attempt; it doubles after.
// Short, because the two call sites that block on a send are a sign-in code
// and an HTTP request, and a person is waiting on both.
const DefaultBackoff = 250 * time.Millisecond

// Request is one notification to one recipient.
type Request struct {
	// Event is the catalogue key. An unknown key is an error, never a mail.
	Event string
	// To is the recipient address on the channel.
	To string
	// CustomerID scopes the preference lookup and the delivery log. nil for
	// a message that belongs to no customer — an operator's sign-in code.
	CustomerID *string
	// Locale overrides the resolved locale; empty = whatever the preference
	// says, or DefaultLocale.
	Locale string
	// Payload is what the template renders from.
	Payload map[string]any
}

// ChannelOutcome is what happened on one channel.
type ChannelOutcome struct {
	Channel  string `json:"channel"`
	Status   string `json:"status"`
	Attempts int    `json:"attempts"`
	Reason   string `json:"reason,omitempty"`
}

// Result is what one Send did.
type Result struct {
	Event     string           `json:"event"`
	Recipient string           `json:"recipient"`
	Locale    string           `json:"locale"`
	Subject   string           `json:"subject,omitempty"`
	Channels  []ChannelOutcome `json:"channels"`
	// Sent is true when at least one channel accepted the message.
	Sent bool `json:"sent"`
	// Suppressed is true when a preference switched the event off. It is
	// not a failure: the recipient asked not to be told.
	Suppressed bool `json:"suppressed"`
	// Source names the preference that decided it.
	Source string `json:"source"`
}

// Send delivers one event to one recipient.
//
// The error is non-nil only when the message was OWED and did not go: an
// unknown event, a template that would not render, or every resolved channel
// failing. A suppressed event returns no error, because nothing went wrong —
// Result.Suppressed says so, and a row in the delivery log records it.
func (n *Notifier) Send(ctx context.Context, req Request) (Result, error) {
	res := Result{Event: strings.TrimSpace(req.Event), Recipient: strings.TrimSpace(req.To), Channels: []ChannelOutcome{}}
	e, ok := Lookup(res.Event)
	if !ok {
		return res, fmt.Errorf("notify: unknown event %q", req.Event)
	}
	if res.Recipient == "" {
		return res, fmt.Errorf("notify: %s has no recipient", e.Key)
	}

	var rows []store.NotificationPreference
	if n.Store != nil {
		var err error
		rows, err = n.Store.NotificationPreferencesFor(ctx, deref(req.CustomerID), res.Recipient)
		if err != nil {
			// A preference read that failed must not stop an invoice. The
			// catalogue default applies and the reason is logged: falling
			// back to "send it" is the safe direction, and falling back
			// silently is not.
			slog.Warn("notify: read preferences", "event", e.Key, "error", err)
			rows = nil
		}
	}
	resolved := Resolve(e, rows, deref(req.CustomerID), res.Recipient)
	res.Source, res.Locale = resolved.Source, resolved.Locale
	if req.Locale != "" {
		res.Locale = strings.ToLower(strings.TrimSpace(req.Locale))
	}
	if resolved.Forced {
		slog.Info("notify: preference overridden", "event", e.Key, "recipient", res.Recipient, "reason", resolved.ForcedReason)
	}

	if !resolved.Enabled {
		res.Suppressed = true
		reason := "switched off by the " + resolved.Source + " preference"
		res.Channels = append(res.Channels, ChannelOutcome{Channel: firstOr(resolved.Channels, ChannelEmail), Status: store.NotifyStatusSuppressed, Reason: reason})
		n.record(ctx, req, e, firstOr(resolved.Channels, ChannelEmail), res.Locale, "", 1, store.NotifyStatusSuppressed, reason)
		n.count(e.Key, firstOr(resolved.Channels, ChannelEmail), store.NotifyStatusSuppressed)
		return res, nil
	}

	msg, err := Render(e.Key, res.Locale, req.Payload)
	if err != nil {
		// A template that will not render is a code defect, not a delivery
		// failure — but it is still a message that did not reach a
		// customer, so it is recorded as one.
		n.record(ctx, req, e, firstOr(resolved.Channels, ChannelEmail), res.Locale, "", 1, store.NotifyStatusFailed, err.Error())
		n.count(e.Key, firstOr(resolved.Channels, ChannelEmail), store.NotifyStatusFailed)
		return res, err
	}
	res.Locale, res.Subject = msg.Locale, msg.Subject

	channels := n.channels()
	var failures []string
	for _, name := range resolved.Channels {
		c, ok := channels[name]
		if !ok {
			reason := "no channel named " + name + " in this build"
			res.Channels = append(res.Channels, ChannelOutcome{Channel: name, Status: store.NotifyStatusUnavailable, Attempts: 0, Reason: reason})
			n.record(ctx, req, e, name, msg.Locale, msg.Subject, 1, store.NotifyStatusUnavailable, reason)
			n.count(e.Key, name, store.NotifyStatusUnavailable)
			failures = append(failures, name+": "+reason)
			continue
		}
		if !c.Available() {
			// DESIGN.md §21.5 — a DECLARED channel with no transport
			// refuses and says why, in the same shape §17's Submit returns
			// not_submitted with its reason. It is never a retry: no
			// number of attempts publishes an API.
			reason := c.Unavailable()
			res.Channels = append(res.Channels, ChannelOutcome{Channel: name, Status: store.NotifyStatusUnavailable, Attempts: 0, Reason: reason})
			n.record(ctx, req, e, name, msg.Locale, msg.Subject, 1, store.NotifyStatusUnavailable, reason)
			n.count(e.Key, name, store.NotifyStatusUnavailable)
			failures = append(failures, name+": "+reason)
			continue
		}
		out := n.deliver(ctx, req, e, c, msg)
		res.Channels = append(res.Channels, out)
		if out.Status == store.NotifyStatusSent {
			res.Sent = true
			continue
		}
		failures = append(failures, name+": "+out.Reason)
	}
	if res.Sent {
		return res, nil
	}
	if len(failures) == 0 {
		// Resolved to no channel at all. Not reachable through the
		// catalogue (every event declares one) but a preference could name
		// only channels this build does not have, and silence would be the
		// wrong answer.
		reason := "no channel resolved for " + e.Key
		n.record(ctx, req, e, ChannelEmail, msg.Locale, msg.Subject, 1, store.NotifyStatusFailed, reason)
		n.count(e.Key, ChannelEmail, store.NotifyStatusFailed)
		return res, errors.New("notify: " + reason)
	}
	return res, fmt.Errorf("notify: %s to %s: %s", e.Key, res.Recipient, strings.Join(failures, "; "))
}

// deliver carries one message on one channel, retrying a transient failure
// with bounded backoff and recording every attempt.
func (n *Notifier) deliver(ctx context.Context, req Request, e Event, c Channel, msg Message) ChannelOutcome {
	attempts := n.Attempts
	if attempts <= 0 {
		attempts = DefaultAttempts
	}
	backoff := n.Backoff
	if backoff <= 0 {
		backoff = DefaultBackoff
	}
	out := ChannelOutcome{Channel: c.Name()}
	for attempt := 1; attempt <= attempts; attempt++ {
		out.Attempts = attempt
		err := c.Send(ctx, req.To, msg)
		if err == nil {
			n.record(ctx, req, e, c.Name(), msg.Locale, msg.Subject, attempt, store.NotifyStatusSent, "")
			n.count(e.Key, c.Name(), store.NotifyStatusSent)
			out.Status = store.NotifyStatusSent
			return out
		}
		out.Reason = err.Error()
		permanent := errors.Is(err, ErrPermanent) || ctx.Err() != nil
		last := attempt == attempts || permanent
		status := store.NotifyStatusRetrying
		if last {
			status = store.NotifyStatusFailed
		}
		n.record(ctx, req, e, c.Name(), msg.Locale, msg.Subject, attempt, status, out.Reason)
		n.count(e.Key, c.Name(), status)
		if last {
			// A permanently failed delivery is VISIBLE: a row in the log
			// the console lists first, a counter, and an error line naming
			// the event and the recipient. Never a silent drop.
			slog.Error("notification delivery failed", "event", e.Key, "channel", c.Name(), "recipient", req.To,
				"attempts", attempt, "permanent", permanent, "error", out.Reason)
			out.Status = store.NotifyStatusFailed
			return out
		}
		n.wait(ctx, backoff)
		backoff *= 2
	}
	out.Status = store.NotifyStatusFailed
	return out
}

func (n *Notifier) wait(ctx context.Context, d time.Duration) {
	if n.sleep != nil {
		n.sleep(ctx, d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// SetSleep replaces the backoff wait. Tests use it to walk the retry ladder
// without walking the clock; nothing else should.
func (n *Notifier) SetSleep(f func(ctx context.Context, d time.Duration)) { n.sleep = f }

func (n *Notifier) channels() ChannelSet {
	if len(n.Channels) > 0 {
		return n.Channels
	}
	return DefaultChannels(nil)
}

func (n *Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now().UTC()
	}
	return time.Now().UTC()
}

func (n *Notifier) record(ctx context.Context, req Request, e Event, channel, locale, subject string, attempt int, status, reason string) {
	if n.Store == nil {
		return
	}
	// The write uses a context of its own: a request that was cancelled
	// mid-send is exactly the case where the attempt most needs recording,
	// and a cancelled context would drop the row instead.
	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := n.Store.RecordNotificationDelivery(recCtx, store.NotificationDeliveryInput{
		Event:      e.Key,
		CustomerID: req.CustomerID,
		Channel:    channel,
		Recipient:  req.To,
		Locale:     locale,
		Subject:    subject,
		Attempt:    attempt,
		Status:     status,
		Reason:     reason,
		At:         n.now(),
	}); err != nil {
		slog.Warn("notify: record delivery", "event", e.Key, "status", status, "error", err)
	}
}

func (n *Notifier) count(event, channel, status string) {
	if n.Metrics == nil {
		return
	}
	n.Metrics.Inc("chargeback_notifications_total", "Notification delivery attempts by event, channel and outcome",
		map[string]string{"event": event, "channel": channel, "status": status}, 1)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

func firstOr(list []string, fallback string) string {
	if len(list) > 0 {
		return list[0]
	}
	return fallback
}
