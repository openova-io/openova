// client_email_normalization_41_test.go — UAT row 41.
//
// Clause: "Keycloak sovereign realm → Users lists the single owner principal,
// the owner (enabled)."
//
// Measured on hw292: the realm held FOUR enabled principals where the clause
// demands one. #5722 closed the unauthenticated pin/issue write that minted
// three of them, so an ARBITRARY address can no longer become a principal
// without someone reading the PIN. This file covers what #5722 did not: the
// address that reaches the realm write is still passed through VERBATIM, so
// the owner's OWN address in a different case mints a SECOND principal for the
// same human.
//
// WHY THAT IS A DEFECT AND NOT A PREFERENCE. The two paths that write the
// owner into the sovereign realm disagree:
//
//   - DECLARATIVE (the seed that mints the real owner):
//     platform/keycloak/chart/templates/configmap-sovereign-realm.yaml:1248
//     {{- $ownerEmail := lower (trim (default "" .Values.sovereignRealm.ownerEmail)) }}
//     — lowercased, always.
//   - IMPERATIVE (this client, reached from the PIN verify + handover paths):
//     EnsureUser -> findUserByEmail(?email=<verbatim>&exact=true) and
//     createUser({"email": <verbatim>, "username": <verbatim>}) — no
//     normalization of any kind.
//
// The seed therefore guarantees the owner principal is lowercase, and the
// login form guarantees nothing. An owner who types Emrah.Baysal@… at a form
// whose seeded principal is emrah.baysal@… misses on an exact-match lookup and
// creates a duplicate. Nothing in the repo ever reconciles or removes the
// loser: there is no DELETE /admin/realms/{realm}/users/{id} call anywhere
// (the only DELETEs are IdP, groups, clients, roles), so every duplicate is
// permanent and the "single owner principal" clause is broken for good.
//
// WHAT THESE TESTS ASSERT, PRECISELY. They assert on the bytes THIS CLIENT
// SENDS — the lookup query string and the create payload — never on Keycloak's
// own server-side case semantics, which are not this repo's to define and are
// not observable from a unit test. The fake realm below models exact-match
// lookup because that is literally what the client asks for (`exact=true`).
package keycloak

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// seededOwner is the address the chart seed writes — already lowercased by
// `lower (trim ...)`, which is what makes the case-variant miss possible.
const seededOwner = "emrah.baysal@openova.io"

// fakeSovereignRealm stands up a realm pre-seeded with ONE owner principal and
// records what the client asked for. Lookup is exact-match on the raw query
// value, mirroring the `exact=true` the client sends.
type fakeSovereignRealm struct {
	srv          *httptest.Server
	lookupEmails []string // every ?email= value the client sent, in order
	createdUsers []string // every "email" the client POSTed
	createCalls  atomic.Int32
}

func newFakeSovereignRealm(t *testing.T) *fakeSovereignRealm {
	t.Helper()
	f := &fakeSovereignRealm{}
	users := map[string]string{seededOwner: "owner-uuid-seeded-by-chart"}

	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/sovereign/protocol/openid-connect/token":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"sa-tok","token_type":"Bearer","expires_in":300}`))

		case r.URL.Path == "/admin/realms/sovereign/users" && r.Method == http.MethodGet:
			q := r.URL.Query().Get("email")
			f.lookupEmails = append(f.lookupEmails, q)
			w.Header().Set("Content-Type", "application/json")
			if id, ok := users[q]; ok {
				w.Write([]byte(`[{"id":"` + id + `"}]`))
				return
			}
			w.Write([]byte(`[]`))

		case r.URL.Path == "/admin/realms/sovereign/users" && r.Method == http.MethodPost:
			f.createCalls.Add(1)
			raw, _ := io.ReadAll(r.Body)
			var payload struct {
				Email    string `json:"email"`
				Username string `json:"username"`
			}
			_ = json.Unmarshal(raw, &payload)
			f.createdUsers = append(f.createdUsers, payload.Email)
			users[payload.Email] = "duplicate-uuid"
			w.Header().Set("Location", "/admin/realms/sovereign/users/duplicate-uuid")
			w.WriteHeader(http.StatusCreated)

		// Group legs are incidental to what this file measures; model the
		// happy path (group exists, membership PUT accepted) so a group
		// failure can never masquerade as a normalisation failure.
		case r.URL.Path == "/admin/realms/sovereign/groups" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":"grp-sovereign-admins"}]`))
		case strings.Contains(r.URL.Path, "/groups/") && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusNoContent)

		default:
			t.Logf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSovereignRealm) client() *Client {
	return NewWithHTTP(f.srv.URL, "sovereign", "catalyst-zero-server", "shh",
		&http.Client{Timeout: 5 * time.Second})
}

// TestEnsureUser_CaseVariantOwnerDoesNotMintSecondPrincipal is the row-41
// reproduction: the owner types their own address with different capitalisation
// and must resolve to the ALREADY-SEEDED principal, not a new one.
func TestEnsureUser_CaseVariantOwnerDoesNotMintSecondPrincipal(t *testing.T) {
	f := newFakeSovereignRealm(t)

	id, err := f.client().EnsureUser(context.Background(), "Emrah.Baysal@openova.io", "sovereign-admins")
	if err != nil {
		t.Fatalf("EnsureUser err = %v; want nil", err)
	}

	if n := f.createCalls.Load(); n != 0 {
		t.Errorf("createUser calls = %d; want 0 — a case variant of the seeded owner minted %v, "+
			"so the realm now holds TWO principals for one human and nothing in the repo can remove either",
			n, f.createdUsers)
	}
	if id != "owner-uuid-seeded-by-chart" {
		t.Errorf("EnsureUser id = %q; want the seeded owner %q", id, "owner-uuid-seeded-by-chart")
	}
	for _, q := range f.lookupEmails {
		if q != seededOwner {
			t.Errorf("lookup queried ?email=%q; want %q — the client must normalise before an exact-match lookup, "+
				"because the chart seed already stores the owner lowercased "+
				"(configmap-sovereign-realm.yaml:1248 `lower (trim ...)`)", q, seededOwner)
		}
	}
}

// TestEnsureUser_SurroundingWhitespaceDoesNotMintSecondPrincipal covers the
// other shape the same verbatim pass-through admits: a pasted address carrying
// whitespace. The handler trims, but the client is also reached from the
// handover path, so the normalisation belongs at the write site.
func TestEnsureUser_SurroundingWhitespaceDoesNotMintSecondPrincipal(t *testing.T) {
	f := newFakeSovereignRealm(t)

	if _, err := f.client().EnsureUser(context.Background(), "  emrah.baysal@openova.io\t", "sovereign-admins"); err != nil {
		t.Fatalf("EnsureUser err = %v; want nil", err)
	}
	if n := f.createCalls.Load(); n != 0 {
		t.Errorf("createUser calls = %d; want 0 — a whitespace-padded copy of the owner minted %v", n, f.createdUsers)
	}
}

// TestEnsureUser_DistinctAddressStillCreates is the CONTROL, answering the
// other way. Normalisation must fold CASE, never fold two genuinely different
// humans together — and in particular must NOT collapse the transposed-letter
// near-duplicate emrha.baysal@ that hw292 actually held, which is a different
// address and a legitimately separate principal.
func TestEnsureUser_DistinctAddressStillCreates(t *testing.T) {
	f := newFakeSovereignRealm(t)

	for _, addr := range []string{"emrha.baysal@openova.io", "someone.else@openova.io"} {
		if _, err := f.client().EnsureUser(context.Background(), addr, "sovereign-admins"); err != nil {
			t.Fatalf("EnsureUser(%q) err = %v; want nil", addr, err)
		}
	}
	if n := f.createCalls.Load(); n != 2 {
		t.Errorf("createUser calls = %d; want 2 — two genuinely distinct addresses must each get a principal "+
			"(created %v). Folding them would be a worse defect than the one under fix.", n, f.createdUsers)
	}
	for _, got := range f.createdUsers {
		if got != strings.ToLower(got) {
			t.Errorf("created principal %q is not normalised; the create payload must carry the same "+
				"lowercased form the lookup used, or find-then-create disagree", got)
		}
	}
}

// TestEnsureUser_CreatePayloadIsNormalised pins the create side specifically:
// an address that does NOT already exist must still be STORED normalised, or
// the next lookup for its lowercase form misses and mints yet another copy.
func TestEnsureUser_CreatePayloadIsNormalised(t *testing.T) {
	f := newFakeSovereignRealm(t)

	if _, err := f.client().EnsureUser(context.Background(), "New.Owner@Openova.IO", "sovereign-admins"); err != nil {
		t.Fatalf("EnsureUser err = %v; want nil", err)
	}
	if n := f.createCalls.Load(); n != 1 {
		t.Fatalf("createUser calls = %d; want 1", n)
	}
	if got, want := f.createdUsers[0], "new.owner@openova.io"; got != want {
		t.Errorf("created principal email = %q; want %q — storing the typed casing makes the very next "+
			"lookup for the lowercase form miss, minting a third copy", got, want)
	}
}
