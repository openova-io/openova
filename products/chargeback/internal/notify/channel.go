package notify

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/mail"
)

// Channels (DESIGN.md §21.5) — one interface, two declared channels.
//
// EMAIL is implemented over the existing mail.Sender, which is the only
// transport this product has ever had.
//
// SMS is DECLARED and has NO TRANSPORT. Omantel's gateway specification has
// not been provided: there is no endpoint, no credential shape, no payload
// and no delivery-receipt semantics published to this build. So the SMS
// channel exists in the interface, can be named in a preference and in the
// console, and REFUSES AT SEND TIME with the reason — exactly the way §17's
// einvoice Submit returns not_submitted with omanSubmitReason rather than
// posting an invoice at an endpoint nobody published. Nothing here pretends
// to send: there is no stub that returns nil, no queue that swallows the
// message, and no fabricated API.

// Channel names.
const (
	// ChannelEmail is SMTP through mail.Sender.
	ChannelEmail = "email"
	// ChannelSMS is declared, not implemented. See smsUnavailableReason.
	ChannelSMS = "sms"
)

// ChannelNames lists every declared channel, in display order.
var ChannelNames = []string{ChannelEmail, ChannelSMS}

// ValidChannel reports whether s names a declared channel.
func ValidChannel(s string) bool {
	for _, c := range ChannelNames {
		if c == s {
			return true
		}
	}
	return false
}

// Channel carries one rendered message to one recipient.
type Channel interface {
	// Name is the channel's key, e.g. "email".
	Name() string
	// Available reports whether the channel has a transport at all. A
	// channel that is declared and unavailable still appears everywhere a
	// channel appears; it simply refuses.
	Available() bool
	// Unavailable is the reason Available is false — the exact sentence the
	// delivery log records and the console shows. Empty when available.
	Unavailable() string
	// Send delivers the message. An error wrapping ErrPermanent is never
	// retried; anything else is treated as transient.
	Send(ctx context.Context, to string, m Message) error
}

// ErrPermanent marks a failure retrying cannot fix: an address that is not
// an address, a channel with no transport. A channel wraps it to say "do not
// try again"; everything else is transient and is retried with backoff.
var ErrPermanent = errors.New("permanent delivery failure")

// ErrChannelUnavailable is the failure a declared channel with no transport
// returns. It is permanent by construction: no number of retries will
// publish an API.
var ErrChannelUnavailable = fmt.Errorf("%w: channel unavailable", ErrPermanent)

// ---------------------------------------------------------------------------
// email
// ---------------------------------------------------------------------------

// EmailChannel carries messages over the SMTP sender the service already
// has. A nil Sender is a channel that reports itself unavailable rather than
// one that silently drops mail.
type EmailChannel struct {
	Sender mail.Sender
}

// Name is "email".
func (EmailChannel) Name() string { return ChannelEmail }

// Available reports whether a sender is wired.
func (c EmailChannel) Available() bool { return c.Sender != nil }

// Unavailable says why there is no transport.
func (c EmailChannel) Unavailable() string {
	if c.Sender != nil {
		return ""
	}
	return "no mail sender is configured (SMTP_HOST and friends); with SMTP_HOST unset the development sender logs messages instead, and with no sender at all nothing is sent"
}

// Send delivers one message.
func (c EmailChannel) Send(ctx context.Context, to string, m Message) error {
	if c.Sender == nil {
		return fmt.Errorf("%w: %s", ErrChannelUnavailable, c.Unavailable())
	}
	if !looksLikeEmail(to) {
		return fmt.Errorf("%w: %q is not an email address", ErrPermanent, to)
	}
	return c.Sender.Send(ctx, to, m.Subject, m.Body)
}

func looksLikeEmail(s string) bool {
	s = strings.TrimSpace(s)
	at := strings.Index(s, "@")
	return at > 0 && at < len(s)-1 && !strings.ContainsAny(s, " \t\r\n")
}

// ---------------------------------------------------------------------------
// SMS — declared, no transport
// ---------------------------------------------------------------------------

// smsUnavailableReason is the exact, honest reason the SMS channel refuses.
// It names what is missing and who would supply it, because there is nothing
// an operator can configure here today — the same posture, and for the same
// reason, as einvoice's omanSubmitReason.
const smsUnavailableReason = "SMS has no transport in this build: Omantel's gateway specification has not been provided, so there is no endpoint, credential shape, message payload or delivery-receipt semantics to implement against. The channel is declared so that preferences, the console and the delivery log already carry it; it refuses every send and records the refusal rather than pretending to deliver. Wiring it is one Channel implementation registered here, once the specification exists."

// SMSChannel is the declared SMS channel. It holds nothing, because there is
// nothing to hold.
type SMSChannel struct{}

// Name is "sms".
func (SMSChannel) Name() string { return ChannelSMS }

// Available is false, always, in this build.
func (SMSChannel) Available() bool { return false }

// Unavailable is smsUnavailableReason.
func (SMSChannel) Unavailable() string { return smsUnavailableReason }

// Send refuses and says why. It never returns nil: a channel that reported
// success without a transport would be the one defect worse than having no
// channel at all.
func (SMSChannel) Send(context.Context, string, Message) error {
	return fmt.Errorf("%w: %s", ErrChannelUnavailable, smsUnavailableReason)
}

// ---------------------------------------------------------------------------
// the set
// ---------------------------------------------------------------------------

// ChannelSet is the channels a notifier can reach, by name.
type ChannelSet map[string]Channel

// DefaultChannels is every declared channel: email over the given sender,
// and SMS, declared and refusing.
func DefaultChannels(sender mail.Sender) ChannelSet {
	return ChannelSet{
		ChannelEmail: EmailChannel{Sender: sender},
		ChannelSMS:   SMSChannel{},
	}
}

// Names lists the set's channels in ChannelNames order, with any channel the
// set adds beyond the declared ones after them, sorted.
func (s ChannelSet) Names() []string {
	out := []string{}
	seen := map[string]bool{}
	for _, name := range ChannelNames {
		if _, ok := s[name]; ok {
			seen[name] = true
			out = append(out, name)
		}
	}
	var rest []string
	for name := range s {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// ChannelDoc is what the console reads about one channel.
type ChannelDoc struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// Docs describes every channel in the set.
func (s ChannelSet) Docs() []ChannelDoc {
	out := []ChannelDoc{}
	for _, name := range s.Names() {
		c := s[name]
		out = append(out, ChannelDoc{Name: name, Available: c.Available(), Reason: c.Unavailable()})
	}
	return out
}
