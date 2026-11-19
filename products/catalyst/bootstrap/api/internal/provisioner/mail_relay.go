package provisioner

import (
	"errors"
	"strings"

	"github.com/openova-io/openova/core/controllers/pkg/upstream"
)

// MailRelaySecretName is the Secret in catalyst-system of a Sovereign in
// relay mode that holds the keys smtp-user and smtp-pass of the relay
// credential issued for that Sovereign alone (contract C5 MAIL_RELAY_SECRET).
const MailRelaySecretName = "sovereign-mail-relay"

// MailRelay is the SMTP submission relay of a new Sovereign whose sign-in
// codes cannot go through a mail server of its own (SOV-323). It carries no
// credential: the relay issues one for this Sovereign alone, and it is kept
// only in the Secret MailRelaySecretName inside that Sovereign.
type MailRelay struct {
	Host string `json:"host"`
	Port string `json:"port,omitempty"`
	From string `json:"from"`
}

// validate refuses a relay without a host or a sender address, the keys
// contract rule 3 requires in relay mode, before anything is provisioned.
func (r *MailRelay) validate() error {
	if r == nil {
		return nil
	}
	if strings.TrimSpace(r.Host) == "" || strings.TrimSpace(r.From) == "" {
		return errors.New("mailRelay needs a host and a from address")
	}
	return nil
}

// apply sets the mail keys of the variables document: MAIL_MODE local when
// r is nil, and relay with the relay's address, sender and Secret name
// otherwise. An empty port keeps the contract default.
func (r *MailRelay) apply(v *upstream.Variables) {
	if r == nil {
		v.MailMode = "local"
		return
	}
	v.MailMode = "relay"
	v.MailRelayHost = strings.TrimSpace(r.Host)
	if port := strings.TrimSpace(r.Port); port != "" {
		v.MailRelayPort = port
	}
	v.MailFrom = strings.TrimSpace(r.From)
	v.MailRelaySecret = MailRelaySecretName
}
