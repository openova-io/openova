package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

// fakeStore is the preference table and the delivery log, in memory.
type fakeStore struct {
	mu    sync.Mutex
	prefs []store.NotificationPreference
	log   []store.NotificationDeliveryInput
	// readErr makes the preference read fail, which must never stop a send.
	readErr error
}

func (f *fakeStore) NotificationPreferencesFor(_ context.Context, customerID, email string) ([]store.NotificationPreference, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	out := []store.NotificationPreference{}
	for _, p := range f.prefs {
		if p.CustomerID != nil && *p.CustomerID != "" && *p.CustomerID != customerID {
			continue
		}
		if p.Email != "" && !strings.EqualFold(p.Email, email) {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeStore) RecordNotificationDelivery(_ context.Context, in store.NotificationDeliveryInput) (store.NotificationDelivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, in)
	return store.NotificationDelivery{Event: in.Event, Status: in.Status, Attempt: in.Attempt}, nil
}

func (f *fakeStore) attempts() []store.NotificationDeliveryInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.NotificationDeliveryInput, len(f.log))
	copy(out, f.log)
	return out
}

// flakyChannel fails the first failures attempts, then succeeds.
type flakyChannel struct {
	mu       sync.Mutex
	failures int
	calls    int
	perm     bool
	sent     []Message
}

func (c *flakyChannel) Name() string        { return ChannelEmail }
func (c *flakyChannel) Available() bool     { return true }
func (c *flakyChannel) Unavailable() string { return "" }

func (c *flakyChannel) Send(_ context.Context, _ string, m Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls <= c.failures {
		if c.perm {
			return fmt.Errorf("%w: mailbox does not exist", ErrPermanent)
		}
		return errors.New("smtp dial: connection refused")
	}
	c.sent = append(c.sent, m)
	return nil
}

func pinPayload() map[string]any { return map[string]any{"code": "424242", "minutes": 10} }

func notifierFor(c Channel, fs *fakeStore) (*Notifier, *[]time.Duration) {
	waits := []time.Duration{}
	n := &Notifier{Channels: ChannelSet{c.Name(): c, ChannelSMS: SMSChannel{}}, Store: fs}
	n.SetSleep(func(_ context.Context, d time.Duration) { waits = append(waits, d) })
	return n, &waits
}

// ---------------------------------------------------------------------------
// the send path
// ---------------------------------------------------------------------------

func TestSendRecordsTheAttemptAndReportsTheChannel(t *testing.T) {
	fs := &fakeStore{}
	ch := &flakyChannel{}
	n, _ := notifierFor(ch, fs)
	cust := "c-acme"
	res, err := n.Send(context.Background(), Request{Event: EventAuthPIN, To: "ops@nc.example", CustomerID: &cust, Payload: pinPayload()})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Sent || res.Suppressed || len(res.Channels) != 1 || res.Channels[0].Status != store.NotifyStatusSent {
		t.Fatalf("result = %+v", res)
	}
	if res.Subject != "Your sign-in code" || res.Locale != DefaultLocale || res.Source != "catalogue default" {
		t.Fatalf("result = %+v", res)
	}
	log := fs.attempts()
	if len(log) != 1 {
		t.Fatalf("delivery log = %d rows, want 1: %+v", len(log), log)
	}
	if log[0].Event != EventAuthPIN || log[0].Status != store.NotifyStatusSent || log[0].Attempt != 1 ||
		log[0].Recipient != "ops@nc.example" || log[0].Channel != ChannelEmail || log[0].CustomerID == nil || *log[0].CustomerID != cust {
		t.Fatalf("delivery row = %+v", log[0])
	}
}

// A TRANSIENT failure is retried with a BOUNDED, doubling backoff, and every
// attempt is recorded: two retrying rows and one sent row, not one row that
// hides the two failures.
func TestTransientFailureIsRetriedWithBoundedBackoffAndEveryAttemptIsRecorded(t *testing.T) {
	fs := &fakeStore{}
	ch := &flakyChannel{failures: 2}
	n, waits := notifierFor(ch, fs)
	n.Attempts, n.Backoff = 3, 100*time.Millisecond

	res, err := n.Send(context.Background(), Request{Event: EventAuthPIN, To: "ops@nc.example", Payload: pinPayload()})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Sent || res.Channels[0].Attempts != 3 {
		t.Fatalf("result = %+v", res)
	}
	if ch.calls != 3 {
		t.Fatalf("channel calls = %d, want 3", ch.calls)
	}
	if len(*waits) != 2 || (*waits)[0] != 100*time.Millisecond || (*waits)[1] != 200*time.Millisecond {
		t.Fatalf("backoff = %v, want doubling 100ms, 200ms", *waits)
	}
	log := fs.attempts()
	if len(log) != 3 {
		t.Fatalf("delivery log = %d rows, want one per attempt: %+v", len(log), log)
	}
	for i, want := range []string{store.NotifyStatusRetrying, store.NotifyStatusRetrying, store.NotifyStatusSent} {
		if log[i].Status != want || log[i].Attempt != i+1 {
			t.Fatalf("attempt %d recorded as %s/%d, want %s/%d", i+1, log[i].Status, log[i].Attempt, want, i+1)
		}
	}
	if log[0].Reason == "" || log[2].Reason != "" {
		t.Fatalf("the failed attempts must carry their reason and the successful one none: %+v", log)
	}
}

// The retry ladder is BOUNDED: after the last attempt the delivery is FAILED
// and VISIBLE — a failed row in the log and an error to the caller, never a
// silent drop and never an unbounded loop.
func TestAPermanentlyFailedDeliveryIsVisibleNotSilent(t *testing.T) {
	fs := &fakeStore{}
	ch := &flakyChannel{failures: 99}
	n, waits := notifierFor(ch, fs)
	n.Attempts, n.Backoff = 3, time.Millisecond

	res, err := n.Send(context.Background(), Request{Event: EventStatementIssued, To: "ap@acme.example",
		Payload: map[string]any{"subject": "s", "document": "d", "statement_id": "st-1", "period": "2026-08"}})
	if err == nil {
		t.Fatal("a delivery that never got through must return an error")
	}
	if res.Sent {
		t.Fatalf("result = %+v", res)
	}
	if ch.calls != 3 || len(*waits) != 2 {
		t.Fatalf("calls = %d, waits = %v: the ladder must be bounded by Attempts", ch.calls, *waits)
	}
	log := fs.attempts()
	if len(log) != 3 || log[2].Status != store.NotifyStatusFailed {
		t.Fatalf("the last attempt must be recorded as failed: %+v", log)
	}
	if !strings.Contains(err.Error(), "ap@acme.example") || !strings.Contains(err.Error(), EventStatementIssued) {
		t.Fatalf("the error must name the event and the recipient: %v", err)
	}
}

// A PERMANENT failure is not retried at all: no number of attempts fixes an
// address that is not an address.
func TestPermanentFailureIsNotRetried(t *testing.T) {
	fs := &fakeStore{}
	ch := &flakyChannel{failures: 99, perm: true}
	n, waits := notifierFor(ch, fs)
	n.Attempts, n.Backoff = 5, time.Millisecond

	if _, err := n.Send(context.Background(), Request{Event: EventAuthPIN, To: "ops@nc.example", Payload: pinPayload()}); err == nil {
		t.Fatal("want an error")
	}
	if ch.calls != 1 || len(*waits) != 0 {
		t.Fatalf("calls = %d, waits = %v: a permanent failure must not be retried", ch.calls, *waits)
	}
	if log := fs.attempts(); len(log) != 1 || log[0].Status != store.NotifyStatusFailed {
		t.Fatalf("delivery log = %+v", log)
	}
}

// The email channel refuses a malformed address permanently, rather than
// asking SMTP three times whether "not-an-address" is one.
func TestEmailChannelRefusesAMalformedAddressPermanently(t *testing.T) {
	err := EmailChannel{Sender: recordingSender{}}.Send(context.Background(), "not-an-address", Message{})
	if !errors.Is(err, ErrPermanent) {
		t.Fatalf("err = %v, want permanent", err)
	}
}

type recordingSender struct{}

func (recordingSender) Send(context.Context, string, string, string) error { return nil }

// ---------------------------------------------------------------------------
// preferences
// ---------------------------------------------------------------------------

func pref(event string, customerID *string, email string, enabled bool, channels ...string) store.NotificationPreference {
	return store.NotificationPreference{Event: event, CustomerID: customerID, Email: email, Enabled: enabled, Channels: channels}
}

func TestResolveTakesTheMostSpecificRowAndOtherwiseTheCatalogueDefault(t *testing.T) {
	cust := "c-acme"
	other := "c-other"
	low, _ := Lookup(EventAccountLowBalance)

	// No rows at all: the catalogue default, which is ON.
	got := Resolve(low, nil, cust, "fin@acme.example")
	if !got.Enabled || got.Source != "catalogue default" || got.Locale != DefaultLocale {
		t.Fatalf("unset preference = %+v; an unset preference must reproduce what the product already sent", got)
	}

	rows := []store.NotificationPreference{
		pref(EventAccountLowBalance, nil, "", true),                    // rank 0, Sovereign default
		pref(EventAccountLowBalance, &cust, "", false),                 // rank 1, this customer
		pref(EventAccountLowBalance, nil, "fin@acme.example", true),    // rank 2, this person anywhere
		pref(EventAccountLowBalance, &cust, "fin@acme.example", false), // rank 3, this person here
		pref(EventAccountLowBalance, &other, "fin@acme.example", true), // another customer: ignored
		pref(EventBudgetThreshold, &cust, "fin@acme.example", true),    // another event: ignored
	}
	// Rank 3 wins: it is the only row that says OFF for this person here.
	if got := Resolve(low, rows, cust, "fin@acme.example"); got.Enabled {
		t.Fatalf("rank 3 must win: %+v", got)
	}
	// Take rank 3 away and the PERSONAL Sovereign-wide row (rank 2) wins
	// over the customer row (rank 1) — a rule naming you beats one naming
	// only your organisation.
	if got := Resolve(low, rows[:3], cust, "fin@acme.example"); !got.Enabled {
		t.Fatalf("rank 2 must beat rank 1: %+v", got)
	}
	// A different person on the same customer sees rank 1.
	if got := Resolve(low, rows, cust, "cfo@acme.example"); got.Enabled {
		t.Fatalf("rank 1 must apply to another person on the customer: %+v", got)
	}
	// A person on no customer at all sees rank 2, then rank 0.
	if got := Resolve(low, rows, "", "fin@acme.example"); !got.Enabled {
		t.Fatalf("rank 2 applies with no customer: %+v", got)
	}
	if got := Resolve(low, rows, "", "nobody@nc.example"); !got.Enabled || got.Source != "sovereign" {
		t.Fatalf("rank 0 is the last row before the catalogue: %+v", got)
	}
}

func TestResolveTakesChannelsAndLocaleFromTheWinningRow(t *testing.T) {
	cust := "c-acme"
	low, _ := Lookup(EventAccountLowBalance)
	row := pref(EventAccountLowBalance, &cust, "", true, ChannelSMS)
	row.Locale = "zz"
	got := Resolve(low, []store.NotificationPreference{row}, cust, "fin@acme.example")
	if len(got.Channels) != 1 || got.Channels[0] != ChannelSMS || got.Locale != "zz" {
		t.Fatalf("resolution = %+v", got)
	}
	// A row with NO channels keeps the event's own.
	plain := pref(EventAccountLowBalance, &cust, "", true)
	if got := Resolve(low, []store.NotificationPreference{plain}, cust, "x@y.z"); len(got.Channels) != 1 || got.Channels[0] != ChannelEmail {
		t.Fatalf("an empty channel list must fall back to the event's own: %+v", got)
	}
}

// THE MANDATORY GUARANTEE. A customer cannot switch off an invoice or a
// dunning notice — not by writing a row (the write is refused) and not by
// having one (the resolver ignores it).
func TestACustomerCannotSwitchOffAMandatoryNotice(t *testing.T) {
	cust := "c-acme"
	mandatory := []string{EventStatementIssued, EventCollectionsReminder, EventCollectionsEscalation, EventAuthPIN, EventCustomerInvite}
	channels := DefaultChannels(recordingSender{})
	for _, key := range mandatory {
		e, ok := Lookup(key)
		if !ok || !e.Mandatory {
			t.Fatalf("%s must be mandatory in the catalogue", key)
		}
		// (a) The WRITE is refused, with a message an operator can act on.
		err := ValidatePreference(pref(key, &cust, "ap@acme.example", false), channels)
		if err == nil {
			t.Fatalf("%s: switching off a mandatory notice must be refused", key)
		}
		if !strings.Contains(err.Error(), "mandatory") {
			t.Fatalf("%s: refusal must say why: %v", key, err)
		}
		// (b) The READ ignores such a row however it got there.
		got := Resolve(e, []store.NotificationPreference{pref(key, &cust, "ap@acme.example", false)}, cust, "ap@acme.example")
		if !got.Enabled {
			t.Fatalf("%s: a disabling row must be ignored by the resolver", key)
		}
		if !got.Forced || !strings.Contains(got.ForcedReason, "cannot be switched off") {
			t.Fatalf("%s: the override must be recorded and explained: %+v", key, got)
		}
	}
	// A NON-mandatory event can be switched off, both ways.
	low, _ := Lookup(EventAccountLowBalance)
	if err := ValidatePreference(pref(EventAccountLowBalance, &cust, "", false), channels); err != nil {
		t.Fatalf("a non-mandatory event must be switchable: %v", err)
	}
	if got := Resolve(low, []store.NotificationPreference{pref(EventAccountLowBalance, &cust, "", false)}, cust, "x@y.z"); got.Enabled || got.Forced {
		t.Fatalf("a non-mandatory event must actually switch off: %+v", got)
	}
}

// The back door: leaving a mandatory notice enabled but moving it to a
// channel that cannot carry it would switch it off in everything but name.
// The mandatory channels are a FLOOR — a preference may add, never remove.
func TestAMandatoryNoticeKeepsItsChannelWhenAPreferenceNamesAnotherOne(t *testing.T) {
	cust := "c-acme"
	e, _ := Lookup(EventStatementIssued)
	row := pref(EventStatementIssued, &cust, "", true, ChannelSMS)

	if err := ValidatePreference(row, DefaultChannels(recordingSender{})); err == nil {
		t.Fatal("replacing a mandatory notice's channel must be refused at the write")
	}
	got := Resolve(e, []store.NotificationPreference{row}, cust, "ap@acme.example")
	if !containsChannel(got.Channels, ChannelEmail) {
		t.Fatalf("email must survive: %+v", got)
	}
	if !got.Forced || !strings.Contains(got.ForcedReason, "cannot be removed") {
		t.Fatalf("the override must be recorded and explained: %+v", got)
	}
	// And on a NON-mandatory event, choosing SMS alone is honoured — this
	// is a floor under mandatory notices, not a refusal to let anyone
	// choose a channel.
	low, _ := Lookup(EventAccountLowBalance)
	if got := Resolve(low, []store.NotificationPreference{pref(EventAccountLowBalance, &cust, "", true, ChannelSMS)}, cust, "x@y.z"); containsChannel(got.Channels, ChannelEmail) {
		t.Fatalf("a non-mandatory event must honour the channel chosen: %+v", got)
	}
}

// A suppressed event is RECORDED. "We chose not to tell them" is an answer;
// an unrecorded non-send is not.
func TestASuppressedEventIsRecordedNotSilent(t *testing.T) {
	cust := "c-acme"
	fs := &fakeStore{prefs: []store.NotificationPreference{pref(EventAccountLowBalance, &cust, "", false)}}
	ch := &flakyChannel{}
	n, _ := notifierFor(ch, fs)

	res, err := n.Send(context.Background(), Request{Event: EventAccountLowBalance, To: "fin@acme.example", CustomerID: &cust,
		Payload: map[string]any{"customer_name": "A", "available": "1", "threshold": "5", "currency": "OMR", "suspend_at_zero": false, "link": "l"}})
	if err != nil {
		t.Fatalf("a suppressed event is not a failure: %v", err)
	}
	if res.Sent || !res.Suppressed {
		t.Fatalf("result = %+v", res)
	}
	if ch.calls != 0 {
		t.Fatal("a suppressed event must not reach the channel")
	}
	log := fs.attempts()
	if len(log) != 1 || log[0].Status != store.NotifyStatusSuppressed || !strings.Contains(log[0].Reason, "customer:"+cust) {
		t.Fatalf("the suppression must be recorded, naming the preference that decided it: %+v", log)
	}
}

// A preference read that fails must never be the reason an invoice did not
// go out. The catalogue default applies — which is to send.
func TestAFailedPreferenceReadStillSends(t *testing.T) {
	fs := &fakeStore{readErr: errors.New("database is down")}
	ch := &flakyChannel{}
	n, _ := notifierFor(ch, fs)
	res, err := n.Send(context.Background(), Request{Event: EventAuthPIN, To: "ops@nc.example", Payload: pinPayload()})
	if err != nil || !res.Sent {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}

// ---------------------------------------------------------------------------
// the SMS channel
// ---------------------------------------------------------------------------

// SMS is DECLARED and has NO TRANSPORT. It must refuse clearly, say why,
// never be retried, and never report success — the §17 posture, not a stub
// that pretends to send.
func TestSMSChannelRefusesClearlyAndSaysWhy(t *testing.T) {
	c := SMSChannel{}
	if c.Name() != ChannelSMS {
		t.Fatalf("name = %q", c.Name())
	}
	if c.Available() {
		t.Fatal("no transport exists for SMS in this build")
	}
	reason := c.Unavailable()
	for _, want := range []string{"Omantel", "no endpoint", "declared"} {
		if !strings.Contains(strings.ToLower(reason), strings.ToLower(want)) {
			t.Errorf("the reason must name what is missing; %q does not contain %q", reason, want)
		}
	}
	err := c.Send(context.Background(), "+96812345678", Message{Subject: "s", Body: "b"})
	if err == nil {
		t.Fatal("a channel with no transport must never report success")
	}
	if !errors.Is(err, ErrChannelUnavailable) || !errors.Is(err, ErrPermanent) {
		t.Fatalf("err = %v, want a permanent channel-unavailable error", err)
	}
	if !strings.Contains(err.Error(), reason) {
		t.Fatalf("the refusal must carry the reason: %v", err)
	}
	// It is DECLARED: it appears everywhere a channel appears.
	if !ValidChannel(ChannelSMS) {
		t.Fatal("sms must be a declared channel name")
	}
	docs := DefaultChannels(recordingSender{}).Docs()
	var found bool
	for _, d := range docs {
		if d.Name == ChannelSMS {
			found = true
			if d.Available || d.Reason == "" {
				t.Fatalf("sms doc = %+v", d)
			}
		}
	}
	if !found {
		t.Fatalf("sms must be listed among the channels: %+v", docs)
	}
}

// A send routed onto the unavailable channel is recorded as `unavailable`
// with the reason, attempted ZERO times, and reported as not sent.
func TestSendOnAnUnavailableChannelRecordsTheReasonAndIsNeverRetried(t *testing.T) {
	cust := "c-acme"
	fs := &fakeStore{prefs: []store.NotificationPreference{pref(EventAccountLowBalance, &cust, "", true, ChannelSMS)}}
	n := &Notifier{Channels: DefaultChannels(recordingSender{}), Store: fs, Attempts: 4}
	n.SetSleep(func(context.Context, time.Duration) { t.Fatal("an unavailable channel must never be retried") })

	res, err := n.Send(context.Background(), Request{Event: EventAccountLowBalance, To: "fin@acme.example", CustomerID: &cust,
		Payload: map[string]any{"customer_name": "A", "available": "1", "threshold": "5", "currency": "OMR", "suspend_at_zero": false, "link": "l"}})
	if err == nil {
		t.Fatal("nothing was delivered, so the caller must be told")
	}
	if res.Sent || len(res.Channels) != 1 || res.Channels[0].Status != store.NotifyStatusUnavailable || res.Channels[0].Attempts != 0 {
		t.Fatalf("result = %+v", res)
	}
	log := fs.attempts()
	if len(log) != 1 || log[0].Status != store.NotifyStatusUnavailable || !strings.Contains(log[0].Reason, "Omantel") {
		t.Fatalf("the delivery log must carry the reason: %+v", log)
	}
}

// ---------------------------------------------------------------------------
// refusals
// ---------------------------------------------------------------------------

func TestSendRefusesAnUnknownEventAndAnEmptyRecipient(t *testing.T) {
	n := &Notifier{Channels: DefaultChannels(recordingSender{})}
	if _, err := n.Send(context.Background(), Request{Event: "invented.event", To: "a@b.c"}); err == nil {
		t.Fatal("an event that is not in the catalogue cannot be sent")
	}
	if _, err := n.Send(context.Background(), Request{Event: EventAuthPIN, To: "  "}); err == nil {
		t.Fatal("a send with no recipient must be refused")
	}
}

func TestValidatePreferenceRefusesUnknownEventsChannelsAndLocales(t *testing.T) {
	channels := DefaultChannels(recordingSender{})
	if err := ValidatePreference(pref("not.an.event", nil, "", true), channels); err == nil {
		t.Fatal("an unknown event must be refused")
	}
	if err := ValidatePreference(pref(EventAccountLowBalance, nil, "", true, "carrier-pigeon"), channels); err == nil {
		t.Fatal("an unknown channel must be refused")
	}
	bad := pref(EventAccountLowBalance, nil, "", true)
	bad.Locale = "not-a-locale"
	if err := ValidatePreference(bad, channels); err == nil {
		t.Fatal("a locale with no templates must be refused")
	}
	ok := pref(EventAccountLowBalance, nil, "", true, ChannelEmail)
	ok.Locale = DefaultLocale
	if err := ValidatePreference(ok, channels); err != nil {
		t.Fatalf("a valid preference must be accepted: %v", err)
	}
}

// A notifier with no store is what a unit test of some other package gets
// when it wires only a mail sender: catalogue defaults, no log, and mail
// that still goes out exactly as it did.
func TestANotifierWithNoStoreStillSends(t *testing.T) {
	ch := &flakyChannel{}
	n := &Notifier{Channels: ChannelSet{ChannelEmail: ch}}
	res, err := n.Send(context.Background(), Request{Event: EventAuthPIN, To: "ops@nc.example", Payload: pinPayload()})
	if err != nil || !res.Sent || len(ch.sent) != 1 {
		t.Fatalf("res = %+v err = %v sent = %d", res, err, len(ch.sent))
	}
	if ch.sent[0].Subject != "Your sign-in code" || !strings.Contains(ch.sent[0].Body, "424242") {
		t.Fatalf("message = %+v", ch.sent[0])
	}
}
