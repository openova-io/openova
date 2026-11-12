// Package lifecycle holds the Sovereign lifecycle rules that the API
// server applies to its routes. New lifecycle code is written here, not
// in internal/handler; a handler only calls it.
package lifecycle

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// ConfirmFQDNHeader is the request header that confirms the wipe of a
// Sovereign that has been handed over. Its value is the Sovereign's FQDN.
const ConfirmFQDNHeader = "X-Confirm-Sovereign-FQDN"

// ErrConfirmFQDNRequired is the "error" value of the 412 answer.
const ErrConfirmFQDNRequired = "confirm-fqdn-required"

// WipeConfirmMessage is the "message" value of the 412 answer. It says
// what a wipe destroys and how to confirm it.
const WipeConfirmMessage = "This Sovereign has been handed over to its owner. " +
	"A wipe destroys all of its cloud resources, including the virtual machines " +
	"the Sovereign created itself. To confirm, send the request again with the header " +
	ConfirmFQDNHeader + " set to the Sovereign's FQDN."

// HandoverStamps returns the hand-over timestamps of a deployment record:
// adoptedAt (set when the owner adopted the Sovereign, and on the
// Sovereign's own copy of the record) and, when res is not nil,
// res.HandoverFiredAt and res.HandoverCompletedAt. The caller holds the
// record's lock.
func HandoverStamps(adoptedAt *time.Time, res *provisioner.Result) []*time.Time {
	stamps := []*time.Time{adoptedAt}
	if res != nil {
		stamps = append(stamps, res.HandoverFiredAt, res.HandoverCompletedAt)
	}
	return stamps
}

// HandedOver reports whether a deployment has been handed over: at least
// one of its hand-over timestamps is set. A nil timestamp is not set.
func HandedOver(stamps ...*time.Time) bool {
	for _, s := range stamps {
		if s != nil {
			return true
		}
	}
	return false
}

// WipeConfirmed reports whether a wipe may go on. A deployment that was
// never handed over needs no confirmation. A handed-over deployment needs
// a header value equal to its FQDN; spaces around either value are
// ignored and letter case is not compared. An empty FQDN never confirms.
func WipeConfirmed(fqdn, header string, stamps ...*time.Time) bool {
	if !HandedOver(stamps...) {
		return true
	}
	want := strings.TrimSpace(fqdn)
	got := strings.TrimSpace(header)
	return want != "" && strings.EqualFold(got, want)
}

// RefuseUnconfirmedWipe answers 412 and returns true when the wipe of the
// deployment must not go on. It returns false, and writes nothing, when
// the wipe may go on. fqdn is the deployment's Sovereign FQDN; stamps are
// its hand-over timestamps.
func RefuseUnconfirmedWipe(w http.ResponseWriter, r *http.Request, fqdn string, stamps ...*time.Time) bool {
	if WipeConfirmed(fqdn, r.Header.Get(ConfirmFQDNHeader), stamps...) {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPreconditionFailed)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   ErrConfirmFQDNRequired,
		"message": WipeConfirmMessage,
	})
	return true
}
