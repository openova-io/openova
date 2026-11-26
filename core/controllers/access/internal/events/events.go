// Package events holds the payloads of the grant subjects that the access
// controller publishes on the stream CATALYST_ORG (contract C3,
// docs/contracts/C3-event-subjects.yaml; ADR-0033 decision A16), and builds
// their CloudEvents envelope.
package events

import (
	"context"
	"errors"
	"time"

	"github.com/openova-io/openova/core/controllers/pkg/natsbus"
)

// The two subjects of the grant life cycle.
const (
	SubjectGrantActivated = "catalyst.access.grant_activated.v1"
	SubjectGrantEnded     = "catalyst.access.grant_ended.v1"
)

// The reasons of SubjectGrantEnded. A grant that leaves Active for another
// phase gives that phase in lower case; a deleted grant gives ReasonDeleted.
const (
	ReasonExpired   = "expired"
	ReasonSuspended = "suspended"
	ReasonDeleted   = "deleted"
)

// GrantActivated is the data of SubjectGrantActivated: a grant became Active.
// Account and Ticket may be empty; Approvers is a list, empty when the target
// needs no approval. NotBefore and NotAfter are RFC 3339 in UTC.
type GrantActivated struct {
	Namespace string   `json:"namespace"`
	Grant     string   `json:"grant"`
	Target    string   `json:"target"`
	Subject   string   `json:"subject"`
	Account   string   `json:"account"`
	Reason    string   `json:"reason"`
	Ticket    string   `json:"ticket"`
	NotBefore string   `json:"notBefore"`
	NotAfter  string   `json:"notAfter"`
	Approvers []string `json:"approvers"`
}

// GrantEnded is the data of SubjectGrantEnded: an Active grant left that phase.
type GrantEnded struct {
	Namespace string `json:"namespace"`
	Grant     string `json:"grant"`
	Subject   string `json:"subject"`
	Reason    string `json:"reason"`
}

// Sender sends one envelope and returns after the stream acknowledged it.
// *natsbus.Publisher is the Sender of the controller.
type Sender interface {
	SendCloudEvent(ctx context.Context, ce natsbus.CloudEvent) error
}

// Source is the source attribute of every envelope of the controller:
// access-controller/<sovereign-fqdn>/<region-id>.
func Source(sovereignFQDN, regionID string) (string, error) {
	if sovereignFQDN == "" || regionID == "" {
		return "", errors.New("events: the source needs the Sovereign's FQDN and the region id")
	}
	return "access-controller/" + sovereignFQDN + "/" + regionID, nil
}

// Envelope builds the CloudEvent of one subject. The envelope carries no
// Organization: the data names the namespace.
func Envelope(subject, source string, data any) (natsbus.CloudEvent, error) {
	return natsbus.NewCloudEvent(subject, source, "", data)
}

// Time formats a time of the data.
func Time(t time.Time) string { return t.UTC().Format(time.RFC3339) }
