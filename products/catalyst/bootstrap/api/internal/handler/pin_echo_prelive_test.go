package handler

import (
	"encoding/json"
	"testing"
)

// The pre-live PIN echo must be OFF unless explicitly and exactly enabled.
//
// WHY THIS FILE EXISTS. `CATALYST_PIN_ECHO` makes /pin/issue return the 6-digit
// sign-in code in its response body. That is the difference between a funnel
// walk that CI can run unattended and one that needs a human with a mailbox
// (#5799: on hw292 every mail retrieval path 404s and 993/587/8080 are closed,
// so the walk simply cannot finish). It is also, obviously, a switch that must
// never be on where real customers sign in.
//
// So the risk is asymmetric: leaving it off costs an unwalkable row, turning it
// on wrongly leaks an auth factor. Every test here pushes in the safe
// direction — the ON case gets one test, the OFF cases get eight, because
// "fails open on a typo" is the failure that matters.
//
// The parser is deliberately strict. A loose implementation (`v != ""`, or
// `strings.HasPrefix(v, "t")`) would enable the echo on "false", "0", "no", or
// a typo like "ture". strconv.ParseBool rejects all of those, and an unparseable
// value returns false rather than propagating an error the caller might ignore.

func TestPrelivePinEcho_DefaultsOffAndFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		set  bool
		val  string
		want bool
	}{
		// The only ON forms. strconv.ParseBool accepts these, and they are
		// unambiguous statements of intent.
		{"exact true", true, "true", true},
		{"TRUE upper", true, "TRUE", true},
		{"padded true", true, "  true  ", true},
		{"numeric 1", true, "1", true},

		// Everything else is OFF.
		{"unset", false, "", false},
		{"empty", true, "", false},
		{"whitespace only", true, "   ", false},
		{"false", true, "false", false},
		{"zero", true, "0", false},
		{"no", true, "no", false},
		{"off", true, "off", false},
		// The one that motivates strconv over a hand-rolled check: a typo must
		// NOT enable an auth-factor echo.
		{"typo ture", true, "ture", false},
		{"typo yes", true, "yes", false},
		{"garbage", true, "please-enable", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.set {
				t.Setenv("CATALYST_PIN_ECHO", c.val)
			}
			if got := prelivePinEcho(); got != c.want {
				t.Fatalf("prelivePinEcho() with CATALYST_PIN_ECHO=%q (set=%v) = %v, want %v",
					c.val, c.set, got, c.want)
			}
		})
	}
}

// TestPinIssueResponse_OmitsPinFieldEntirelyWhenEmpty pins the wire format.
//
// `omitempty` is what makes this change invisible in production: with the echo
// off, the JSON must be byte-identical to what it was before the field existed.
// If someone drops the tag, every production response starts carrying `"pin":""`
// — harmless-looking, but it advertises the mechanism's existence to anyone
// reading the API, and it is the first step toward someone populating it.
func TestPinIssueResponse_OmitsPinFieldEntirelyWhenEmpty(t *testing.T) {
	b, err := json.Marshal(pinIssueResponse{
		OK: true, Sent: true, RequestID: "req-1", ExpiresInSec: 300,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := m["pin"]; present {
		t.Fatalf(`response carries a "pin" key when the echo is off — omitempty was dropped: %s`, b)
	}

	// Control: the field DOES serialise when populated. Without this, deleting
	// the field from the struct entirely would leave the assertion above green
	// and the feature silently dead.
	b2, err := json.Marshal(pinIssueResponse{
		OK: true, Sent: true, RequestID: "req-2", ExpiresInSec: 300, Pin: "123456",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m2 map[string]any
	if err := json.Unmarshal(b2, &m2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m2["pin"] != "123456" {
		t.Fatalf(`populated pin did not serialise — the echo cannot work: %s`, b2)
	}
}
