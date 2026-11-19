package provisioner

import (
	"strings"
	"testing"
)

// SOV-323: a request with a mail relay gives a child whose variables document
// says MAIL_MODE relay, with the relay's address, its sender and the name of
// the Secret that holds the credential issued for that child. Without a relay
// the child keeps its own mail server. The document never holds a credential.
func TestChildVariablesMailRelay(t *testing.T) {
	const apiURL = "https://console.openova.io/sovereign"
	mail := func(doc map[string]string) string {
		return strings.Join([]string{doc["MAIL_MODE"], doc["MAIL_RELAY_HOST"], doc["MAIL_RELAY_PORT"], doc["MAIL_FROM"], doc["MAIL_RELAY_SECRET"]}, "|")
	}
	for _, tc := range []struct {
		name  string
		relay *MailRelay
		want  string
	}{
		{"no relay", nil, "local||587||"},
		{"a relay on the default port", &MailRelay{Host: " smtp.relay.example.net ", From: "noreply@t31.omani.works"},
			"relay|smtp.relay.example.net|587|noreply@t31.omani.works|sovereign-mail-relay"},
		{"a relay on its own port", &MailRelay{Host: "smtp.relay.example.net", Port: "2525", From: "noreply@t31.omani.works"},
			"relay|smtp.relay.example.net|2525|noreply@t31.omani.works|sovereign-mail-relay"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{SovereignFQDN: "t31.omani.works", MailRelay: tc.relay}
			doc, err := childVariables(req, apiURL)
			if err != nil {
				t.Fatalf("childVariables: %v", err)
			}
			checkVariablesDocument(t, tc.name, doc, req.SovereignFQDN)
			if got := mail(doc); got != tc.want {
				t.Errorf("mail keys = %q, want %q", got, tc.want)
			}
		})
	}
}

// SOV-323: a relay without a host or a sender is refused when the request is
// validated, and the variables document refuses it too (contract rule 3).
func TestValidateMailRelay(t *testing.T) {
	if err := (*MailRelay)(nil).validate(); err != nil {
		t.Errorf("no relay: %v", err)
	}
	if err := (&MailRelay{Host: "smtp.relay.example.net", From: "noreply@t31.omani.works"}).validate(); err != nil {
		t.Errorf("a whole relay: %v", err)
	}
	for _, r := range []*MailRelay{{From: "noreply@t31.omani.works"}, {Host: "smtp.relay.example.net"}, {Host: " ", From: " "}} {
		if err := r.validate(); err == nil {
			t.Errorf("relay %+v: validate passed, want an error", *r)
		}
		req := Request{SovereignFQDN: "t31.omani.works", MailRelay: r}
		if _, err := childVariables(req, "https://console.openova.io/sovereign"); err == nil || !strings.Contains(err.Error(), "rule 3") {
			t.Errorf("relay %+v: childVariables error = %v, want contract rule 3", *r, err)
		}
	}
	req := baseValidateRequest(t)
	req.MailRelay = &MailRelay{From: "noreply@acme.omani.works"}
	if err := req.Validate(); err == nil || !strings.Contains(err.Error(), "mailRelay") {
		t.Errorf("Validate = %v, want the mailRelay error", err)
	}
	req = baseValidateRequest(t)
	req.MailRelay = &MailRelay{Host: "smtp.relay.example.net", From: "noreply@acme.omani.works"}
	if err := req.Validate(); err != nil {
		t.Errorf("Validate with a whole relay: %v", err)
	}
}
