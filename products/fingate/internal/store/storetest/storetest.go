// Package storetest holds the tests that every implementation of store.Store must pass.
//
// Run takes a function that returns an empty store whose clock is the given function, so
// the tests can move time forward to prove that an expired consent is expired on read.
package storetest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/openova-io/openova/products/fingate/internal/consent"
	"github.com/openova-io/openova/products/fingate/internal/store"
)

// Run runs every test of the port against stores made by open.
func Run(t *testing.T, open func(t *testing.T, now func() time.Time) store.Store) {
	t0 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	ctx := context.Background()
	setup := func(t *testing.T) (store.Store, *time.Time) {
		now := t0
		return open(t, func() time.Time { return now }), &now
	}
	create := func(t *testing.T, s store.Store, id string, expires time.Time) store.Consent {
		t.Helper()
		c, err := s.Create(ctx, store.Consent{ID: id, Family: consent.AccountAccess, ClientID: "acme-pay.wallet",
			Request: []byte(`{"Data":{"Permissions":["ReadAccountsBasic"]},"Risk":{}}`), ExpiresAt: expires})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	states := func(c store.Consent) []consent.State {
		var out []consent.State
		for _, h := range c.History {
			out = append(out, h.To)
		}
		return out
	}

	t.Run("CreateThenGet", func(t *testing.T) {
		s, _ := setup(t)
		c := create(t, s, "c1", time.Time{})
		if c.State != consent.Awaiting || len(c.History) != 1 || c.History[0].Actor != consent.ThirdParty || !c.CreatedAt.Equal(t0) {
			t.Fatalf("created: %+v", c)
		}
		got, ok, err := s.Get(ctx, "c1")
		if err != nil || !ok {
			t.Fatalf("get: %v %v", ok, err)
		}
		if got.ClientID != "acme-pay.wallet" || got.Family != consent.AccountAccess || string(got.Request) != string(c.Request) || got.State != consent.Awaiting {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("CreateTwiceFails", func(t *testing.T) {
		s, _ := setup(t)
		create(t, s, "c1", time.Time{})
		if _, err := s.Create(ctx, store.Consent{ID: "c1", Family: consent.AccountAccess, ClientID: "x.y"}); !errors.Is(err, store.ErrExists) {
			t.Fatalf("want ErrExists, got %v", err)
		}
	})
	t.Run("CreateNeedsIdentifierClientAndFamily", func(t *testing.T) {
		s, _ := setup(t)
		for _, c := range []store.Consent{
			{Family: consent.AccountAccess, ClientID: "x.y"},
			{ID: "a", Family: consent.AccountAccess},
			{ID: "a", Family: "loans", ClientID: "x.y"},
		} {
			if _, err := s.Create(ctx, c); err == nil {
				t.Errorf("created %+v", c)
			}
		}
	})
	t.Run("GetUnknown", func(t *testing.T) {
		s, _ := setup(t)
		if _, ok, err := s.Get(ctx, "nope"); ok || err != nil {
			t.Fatalf("want not found, got %v %v", ok, err)
		}
	})
	t.Run("AuthoriseRecordsCustomerAndAccounts", func(t *testing.T) {
		s, now := setup(t)
		create(t, s, "c1", time.Time{})
		*now = t0.Add(time.Minute)
		c, err := s.Authorise(ctx, "c1", "cust-1", []string{"acc-1", "acc-2"})
		if err != nil {
			t.Fatal(err)
		}
		got, _, _ := s.Get(ctx, "c1")
		for _, x := range []store.Consent{c, got} {
			if x.State != consent.Authorised || x.CustomerID != "cust-1" || !slices.Equal(x.Accounts, []string{"acc-1", "acc-2"}) ||
				!x.StateAt.Equal(*now) || !slices.Equal(states(x), []consent.State{consent.Awaiting, consent.Authorised}) {
				t.Fatalf("authorised: %+v", x)
			}
		}
	})
	t.Run("AuthoriseTwiceIsRefusedAndChangesNothing", func(t *testing.T) {
		s, _ := setup(t)
		create(t, s, "c1", time.Time{})
		if _, err := s.Authorise(ctx, "c1", "cust-1", []string{"acc-1"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Authorise(ctx, "c1", "cust-2", []string{"acc-9"}); !errors.Is(err, consent.ErrTransition) {
			t.Fatalf("want ErrTransition, got %v", err)
		}
		got, _, _ := s.Get(ctx, "c1")
		if got.CustomerID != "cust-1" || !slices.Equal(got.Accounts, []string{"acc-1"}) || len(got.History) != 2 {
			t.Fatalf("a refused transition changed the consent: %+v", got)
		}
	})
	t.Run("RevokeAnAuthorisedConsent", func(t *testing.T) {
		s, _ := setup(t)
		create(t, s, "c1", time.Time{})
		if _, err := s.Authorise(ctx, "c1", "cust-1", nil); err != nil {
			t.Fatal(err)
		}
		c, err := s.Apply(ctx, "c1", consent.Revoke, consent.ThirdParty, "deleted by the third party")
		if err != nil {
			t.Fatal(err)
		}
		if c.State != consent.Revoked || len(c.History) != 3 || c.History[2].Actor != consent.ThirdParty || c.History[2].Reason != "deleted by the third party" {
			t.Fatalf("revoked: %+v", c)
		}
	})
	t.Run("RefusedTransitionChangesNothing", func(t *testing.T) {
		s, _ := setup(t)
		create(t, s, "c1", time.Time{})
		if _, err := s.Apply(ctx, "c1", consent.Revoke, consent.ThirdParty, "x"); !errors.Is(err, consent.ErrTransition) {
			t.Fatalf("revoking an awaiting consent: want ErrTransition, got %v", err)
		}
		if got, _, _ := s.Get(ctx, "c1"); got.State != consent.Awaiting || len(got.History) != 1 {
			t.Fatalf("changed: %+v", got)
		}
	})
	t.Run("ApplyUnknown", func(t *testing.T) {
		s, _ := setup(t)
		if _, err := s.Apply(ctx, "nope", consent.Revoke, consent.ThirdParty, "x"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
		if _, err := s.Authorise(ctx, "nope", "cust-1", nil); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})
	t.Run("ExpiredOnRead", func(t *testing.T) {
		s, now := setup(t)
		create(t, s, "c1", t0.Add(time.Hour))
		if _, err := s.Authorise(ctx, "c1", "cust-1", nil); err != nil {
			t.Fatal(err)
		}
		*now = t0.Add(time.Hour - time.Second)
		if got, _, _ := s.Get(ctx, "c1"); got.State != consent.Authorised {
			t.Fatalf("before the expiry: %s", got.State)
		}
		*now = t0.Add(time.Hour)
		got, _, err := s.Get(ctx, "c1")
		if err != nil || got.State != consent.Expired || got.History[len(got.History)-1].Actor != consent.System {
			t.Fatalf("at the expiry: %+v %v", got, err)
		}
		if again, _, _ := s.Get(ctx, "c1"); len(again.History) != len(got.History) {
			t.Fatal("a second read appended another history entry")
		}
	})
	t.Run("AwaitingIsNotExpiredOnRead", func(t *testing.T) {
		s, now := setup(t)
		create(t, s, "c1", t0.Add(time.Hour))
		*now = t0.Add(2 * time.Hour)
		if got, _, _ := s.Get(ctx, "c1"); got.State != consent.Awaiting {
			t.Fatalf("only an authorised consent expires: %s", got.State)
		}
	})
	t.Run("ReturnedValuesAreCopies", func(t *testing.T) {
		s, _ := setup(t)
		c := create(t, s, "c1", time.Time{})
		c.Request[0] = 'X'
		c.History[0].Reason = "changed"
		if got, _, _ := s.Get(ctx, "c1"); got.Request[0] != '{' || got.History[0].Reason == "changed" {
			t.Fatal("a caller changed the stored consent through a returned value")
		}
	})
}
