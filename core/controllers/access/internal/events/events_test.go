package events

import (
	"testing"
	"time"
)

func TestSource(t *testing.T) {
	if got, err := Source("t1.omani.works", "region-a"); err != nil || got != "access-controller/t1.omani.works/region-a" {
		t.Fatalf("Source: %q %v", got, err)
	}
	for _, c := range [][2]string{{"", "region-a"}, {"t1.omani.works", ""}} {
		if _, err := Source(c[0], c[1]); err == nil {
			t.Errorf("Source(%q, %q) gives no error", c[0], c[1])
		}
	}
}

// The envelope of each subject: its type, source, no Organization, and the
// data keys of contract C3, with the approvers a list even when empty.
func TestEnvelope(t *testing.T) {
	at := time.Date(2026, 11, 20, 18, 0, 0, 0, time.FixedZone("Muscat", 4*3600))
	for subject, c := range map[string]struct {
		data any
		want string
	}{
		SubjectGrantActivated: {GrantActivated{Namespace: "acme", Grant: "g", Target: "shell", Subject: "jane.roe@example.org",
			Reason: "Investigate failed settlement batch 4417", NotBefore: Time(at), NotAfter: Time(at.Add(time.Hour)), Approvers: []string{}},
			`{"namespace":"acme","grant":"g","target":"shell","subject":"jane.roe@example.org","account":"","reason":"Investigate failed settlement batch 4417",` +
				`"ticket":"","notBefore":"2026-11-20T14:00:00Z","notAfter":"2026-11-20T15:00:00Z","approvers":[]}`},
		SubjectGrantEnded: {GrantEnded{Namespace: "acme", Grant: "g", Subject: "jane.roe@example.org", Reason: ReasonExpired},
			`{"namespace":"acme","grant":"g","subject":"jane.roe@example.org","reason":"expired"}`},
	} {
		ce, err := Envelope(subject, "access-controller/t1.omani.works/region-a", c.data)
		if err != nil {
			t.Fatalf("%s: %v", subject, err)
		}
		if ce.Type != "io.openova."+subject || ce.Source != "access-controller/t1.omani.works/region-a" || ce.Subject != "" || len(ce.ID) != 26 {
			t.Errorf("%s: envelope %+v", subject, ce)
		}
		if string(ce.Data) != c.want {
			t.Errorf("%s: data\n got %s\nwant %s", subject, ce.Data, c.want)
		}
	}
}
