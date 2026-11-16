package meter

import (
	"errors"
	"net/http"
	"strings"
)

// The identity headers (docs/contracts/model-contract.md, section 3). The model gateway sets
// them from the verified credential, after it has removed any header of the same name that the
// caller sent. Only gateway pods may open connections to the meter, so the meter trusts them.
// The meter reads the identity of a request from these five headers and from no other header.
const (
	HeaderOrganization  = "X-Openova-Organization"
	HeaderPrincipalKind = "X-Openova-Principal-Kind"
	HeaderPrincipal     = "X-Openova-Principal"
	HeaderAlias         = "X-Openova-Alias"
	HeaderRequestID     = "X-Request-Id"
)

// ErrMissingIdentity is returned when an identity header is absent or empty.
var ErrMissingIdentity = errors.New("meter: an identity header is absent or empty")

// Identity is what the gateway asserted for one request.
type Identity struct {
	Organization  string // x-openova-organization
	PrincipalKind string // x-openova-principal-kind: agent or application
	Principal     string // x-openova-principal
	Alias         string // x-openova-alias
	RequestID     string // x-request-id
}

// IdentityFromHeaders returns the identity. When one of the five headers is absent or empty it
// also returns ErrMissingIdentity, with the headers that were present, for the log line. Every
// header is required.
func IdentityFromHeaders(h http.Header) (Identity, error) {
	id := Identity{
		Organization:  strings.TrimSpace(h.Get(HeaderOrganization)),
		PrincipalKind: strings.TrimSpace(h.Get(HeaderPrincipalKind)),
		Principal:     strings.TrimSpace(h.Get(HeaderPrincipal)),
		Alias:         strings.TrimSpace(h.Get(HeaderAlias)),
		RequestID:     strings.TrimSpace(h.Get(HeaderRequestID)),
	}
	if id.Organization == "" || id.PrincipalKind == "" || id.Principal == "" || id.Alias == "" || id.RequestID == "" {
		return id, ErrMissingIdentity
	}
	return id, nil
}
