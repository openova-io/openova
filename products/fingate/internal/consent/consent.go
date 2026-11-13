// Package consent holds the consent state machine of the open-banking product.
//
// A consent belongs to one family and is in one state. Next applies an event to a state and
// returns the state after it, or ErrTransition when the transition table below does not allow
// it. Wire maps a state to the profile's wire code for the family. Record keeps the state of
// one consent with its history: every accepted event appends exactly one history entry, the
// row that is written to the table consent_history in the same transaction as the state.
//
// The only transitions allowed:
//
//	from        event      to          actors                         families
//	(none)      create     awaiting    third-party                    all four
//	awaiting    authorise  authorised  customer                       all four
//	awaiting    reject     rejected    customer, system               all four
//	authorised  revoke     revoked     customer, bank, third-party    account-access, funds-confirmation, vrp
//	authorised  expire     expired     system                         account-access, funds-confirmation, vrp
//	authorised  consume    consumed    system                         payment
//
// The states rejected, revoked, expired and consumed have no exit. The system rejects a
// consent that is not authorised within the realm's request lifetime. The system expires a
// consent when its expires_at has passed; this is evaluated on read, never by a timer. The
// system consumes a payment consent when its payment was submitted.
package consent

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/openova-io/openova/products/fingate/internal/profile"
)

// State is the internal state of a consent.
type State string

// The states of a consent. None is the state before the consent exists: only Create applies to it.
const (
	None       State = ""
	Awaiting   State = "awaiting"
	Authorised State = "authorised"
	Rejected   State = "rejected"
	Revoked    State = "revoked"
	Expired    State = "expired"
	Consumed   State = "consumed"
)

// Family is the family of a consent. Its value is the key of the family in the profile's consentStatus.
type Family string

// The four consent families.
const (
	AccountAccess     Family = "account-access"
	Payment           Family = "payment"
	FundsConfirmation Family = "funds-confirmation"
	VRP               Family = "vrp"
)

// Event is what happens to a consent.
type Event string

// The events.
const (
	Create    Event = "create"    // the third party creates the consent with a client-credentials token
	Authorise Event = "authorise" // the customer decides authorise, through the authorisation server
	Reject    Event = "reject"    // the customer decides reject, or the request lifetime has passed
	Revoke    Event = "revoke"    // the customer, the bank or the third party (delete) revokes the consent
	Expire    Event = "expire"    // expires_at has passed, seen on read
	Consume   Event = "consume"   // the payment of the consent was submitted
)

// Actor is who causes an event.
type Actor string

// The actors.
const (
	ThirdParty Actor = "third-party"
	Customer   Actor = "customer"
	Bank       Actor = "bank"
	System     Actor = "system"
)

// ErrTransition is returned for a transition, or an actor of a transition, that the table does not allow.
var ErrTransition = errors.New("consent: transition not allowed")

// ErrNoCode is returned by Wire when the profile has no wire code for the family and the state.
var ErrNoCode = errors.New("consent: no wire code")

// transition is one row of the table in the package comment.
type transition struct {
	from     State
	event    Event
	to       State
	actors   []Actor
	families []Family
}

var (
	allFamilies = []Family{AccountAccess, Payment, FundsConfirmation, VRP}
	// standing are the families whose authorised consent ends by revocation or expiry.
	standing = []Family{AccountAccess, FundsConfirmation, VRP}

	table = []transition{
		{None, Create, Awaiting, []Actor{ThirdParty}, allFamilies},
		{Awaiting, Authorise, Authorised, []Actor{Customer}, allFamilies},
		{Awaiting, Reject, Rejected, []Actor{Customer, System}, allFamilies},
		{Authorised, Revoke, Revoked, []Actor{Customer, Bank, ThirdParty}, standing},
		{Authorised, Expire, Expired, []Actor{System}, standing},
		{Authorised, Consume, Consumed, []Actor{System}, []Family{Payment}},
	}
)

// Families returns the four consent families.
func Families() []Family {
	return append([]Family(nil), allFamilies...)
}

// Terminal reports whether a state has no exit.
func Terminal(s State) bool {
	return s == Rejected || s == Revoked || s == Expired || s == Consumed
}

// find returns the row of the table for family, from and event, or false.
func find(family Family, from State, event Event) (transition, bool) {
	for _, t := range table {
		if t.from == from && t.event == event && slices.Contains(t.families, family) {
			return t, true
		}
	}
	return transition{}, false
}

// Next returns the state after applying event to a consent of the family, or ErrTransition.
func Next(family Family, from State, event Event) (State, error) {
	t, ok := find(family, from, event)
	if !ok {
		return from, fmt.Errorf("%w: %s on a %s consent in state %q", ErrTransition, event, family, from)
	}
	return t.to, nil
}

// Wire maps an internal state to the profile's code for the family, or ErrNoCode.
func Wire(p profile.Profile, family Family, s State) (string, error) {
	code, ok := p.ConsentStatus[string(family)][string(s)]
	if !ok {
		return "", fmt.Errorf("%w: profile %s has no code for the state %q of the family %q", ErrNoCode, p.ID, s, family)
	}
	return code, nil
}

// HistoryEntry is one accepted transition: one row of the table consent_history.
type HistoryEntry struct {
	Seq    int
	From   State
	To     State
	Actor  Actor
	Reason string
	At     time.Time
}

// Record is one consent: its family, its state and its history, oldest entry first.
type Record struct {
	Family  Family
	State   State
	History []HistoryEntry
}

// Apply applies event, caused by actor, to the record. When the table allows the transition
// and the actor, the state changes and exactly one entry is appended to History; its Seq is one
// above the Seq of the last entry, or 1 when History is empty. Otherwise Apply returns
// ErrTransition and changes nothing.
func (r *Record) Apply(event Event, actor Actor, reason string, at time.Time) error {
	t, ok := find(r.Family, r.State, event)
	if !ok {
		return fmt.Errorf("%w: %s on a %s consent in state %q", ErrTransition, event, r.Family, r.State)
	}
	if !slices.Contains(t.actors, actor) {
		return fmt.Errorf("%w: the actor %q may not %s a %s consent", ErrTransition, actor, event, r.Family)
	}
	seq := 1
	if n := len(r.History); n > 0 {
		seq = r.History[n-1].Seq + 1
	}
	r.History = append(r.History, HistoryEntry{Seq: seq, From: r.State, To: t.to, Actor: actor, Reason: reason, At: at})
	r.State = t.to
	return nil
}
