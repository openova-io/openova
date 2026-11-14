package runresult

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func full() Document {
	return Document{
		Outcome:     Succeeded,
		HostKeys:    []HostKey{{Type: "ssh-ed25519", SHA256: strings.Repeat("a", 43)}},
		Fingerprint: &Fingerprint{MachineID: "0f1e"},
		OS:          &OS{Family: "ubuntu", Version: "24.04", Arch: "x86_64", Kernel: "6.8.0-48-generic"},
		Evidence:    []Evidence{{Kind: "sbom", Key: "acme/ledger-db-1/scan-1/sbom.cdx.json", SHA256: strings.Repeat("b", 64)}},
		Hardening:   &Hardening{ProfileID: "p", ContentRelease: "0.1.82", Pass: 3, ResultsKey: "k"},
		Patch:       &Patch{PackagesChanged: 2, HealthChecksPassed: true},
		Test:        &Test{Findings: 1, ResultsKey: "t"},
		Completed:   true,
	}
}

func TestOutcomesAreTheClosedList(t *testing.T) {
	want := []Outcome{"Succeeded", "NoChange", "Failed", "Incomplete", "Unreachable", "AuthFailed",
		"IdentityMismatch", "NoAddress", "Refused", "WindowClosed", "Timeout", "Cancelled", "Killed"}
	if got := Outcomes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Outcomes() = %v, want %v", got, want)
	}
	for _, o := range want {
		if !o.Valid() {
			t.Errorf("%s is not valid", o)
		}
	}
	if Outcome("Success").Valid() || Outcome("").Valid() {
		t.Error("a value outside the list is valid")
	}
}

func TestEncodeKeepsTheMemberOrder(t *testing.T) {
	out, err := Encode(Document{Outcome: Incomplete, Message: "trivy was stopped"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema":"openova.io/machine-run-result/v1","outcome":"Incomplete","message":"trivy was stopped","completed":false}`
	if string(out) != want {
		t.Fatalf("got %s\nwant %s", out, want)
	}
}

func TestEncodeRoundTripsEveryMember(t *testing.T) {
	d := full()
	out, err := Encode(d)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	d.Schema = Schema
	if !reflect.DeepEqual(got, d) {
		t.Fatalf("round trip changed the document:\n%+v\n%+v", got, d)
	}
}

func TestEncodeShortensTheMessageOnly(t *testing.T) {
	d := full()
	d.Message = strings.Repeat("ab<é>", 1500) // 9000 bytes; < and > are escaped by JSON
	out, err := Encode(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > MaxBytes || len(out) < MaxBytes-16 {
		t.Fatalf("encoded length %d, want between %d and %d", len(out), MaxBytes-16, MaxBytes)
	}
	got, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(got.Message) || !strings.HasSuffix(got.Message, "...") ||
		!strings.HasPrefix(d.Message, strings.TrimSuffix(got.Message, "...")) {
		t.Fatalf("the message is not a leading part of the original followed by ...: %q", got.Message)
	}
	got.Message, d.Message, d.Schema = "", "", Schema
	if !reflect.DeepEqual(got, d) {
		t.Fatalf("a member other than the message changed:\n%+v\n%+v", got, d)
	}
}

func TestEncodeFailsWhenTooLargeWithoutAMessage(t *testing.T) {
	d := full()
	d.Message = "x"
	for i := 0; i < 60; i++ {
		d.Evidence = append(d.Evidence, Evidence{Kind: "sbom", Key: strings.Repeat("k", 40), SHA256: strings.Repeat("c", 64)})
	}
	if _, err := Encode(d); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestEncodeRefusesAnUnknownOutcome(t *testing.T) {
	if _, err := Encode(Document{Outcome: "Done"}); err == nil {
		t.Fatal("an unknown outcome was encoded")
	}
}

func TestOptionalMembersAreLeftOut(t *testing.T) {
	out, err := Encode(Document{Outcome: Refused, Message: "not-supported: mode ssh"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != 4 {
		t.Fatalf("members %v, want schema, outcome, message and completed only", m)
	}
}

func TestOrNoResult(t *testing.T) {
	good, _ := Encode(Document{Outcome: Succeeded, Completed: true})
	cases := map[string]struct {
		text    string
		outcome Outcome
		message string
	}{
		"a parsable document":  {string(good), Succeeded, ""},
		"no document":          {"", Failed, MessageNoResult},
		"text that is no JSON": {"killed", Failed, MessageNoResult},
		"another schema":       {`{"schema":"x/v1","outcome":"Succeeded","message":"","completed":true}`, Failed, MessageNoResult},
		"an unknown outcome":   {`{"schema":"openova.io/machine-run-result/v1","outcome":"Done","message":"","completed":true}`, Failed, MessageNoResult},
	}
	for name, c := range cases {
		got := OrNoResult([]byte(c.text))
		if got.Outcome != c.outcome || got.Message != c.message || got.Schema != Schema {
			t.Errorf("%s: got %+v, want outcome %s message %q", name, got, c.outcome, c.message)
		}
	}
}
