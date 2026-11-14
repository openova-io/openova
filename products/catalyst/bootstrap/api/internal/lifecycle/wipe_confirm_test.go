package lifecycle

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestWipeConfirm covers every rule of WipeConfirmed and the answer
// RefuseUnconfirmedWipe writes.
func TestWipeConfirm(t *testing.T) {
	now := time.Now().UTC()
	const fqdn = "t01.omani.works"
	cases := []struct {
		name      string
		fqdn      string
		header    string
		setHeader bool
		stamps    []*time.Time
		refused   bool
	}{
		{name: "never handed over, no header", fqdn: fqdn, refused: false},
		{name: "never handed over, nil stamps, no header", fqdn: fqdn, stamps: []*time.Time{nil, nil}, refused: false},
		{name: "never handed over, wrong header", fqdn: fqdn, header: "other.omani.works", setHeader: true, refused: false},
		{name: "handed over, no header", fqdn: fqdn, stamps: []*time.Time{&now}, refused: true},
		{name: "handed over, empty header", fqdn: fqdn, header: "", setHeader: true, stamps: []*time.Time{&now}, refused: true},
		{name: "handed over, wrong header", fqdn: fqdn, header: "t02.omani.works", setHeader: true, stamps: []*time.Time{&now}, refused: true},
		{name: "handed over, right header", fqdn: fqdn, header: fqdn, setHeader: true, stamps: []*time.Time{&now}, refused: false},
		{name: "handed over, right header with spaces and capitals", fqdn: fqdn, header: "  T01.Omani.Works ", setHeader: true, stamps: []*time.Time{&now}, refused: false},
		{name: "one of several stamps set, no header", fqdn: fqdn, stamps: []*time.Time{nil, &now, nil}, refused: true},
		{name: "one of several stamps set, right header", fqdn: fqdn, header: fqdn, setHeader: true, stamps: []*time.Time{nil, nil, &now}, refused: false},
		{name: "handed over, empty FQDN, empty header", fqdn: "", header: "", setHeader: true, stamps: []*time.Time{&now}, refused: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WipeConfirmed(tc.fqdn, tc.header, tc.stamps...); got == tc.refused {
				t.Fatalf("WipeConfirmed = %v, want %v", got, !tc.refused)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/d1/wipe", nil)
			if tc.setHeader {
				req.Header.Set(ConfirmFQDNHeader, tc.header)
			}
			w := httptest.NewRecorder()
			if got := RefuseUnconfirmedWipe(w, req, tc.fqdn, tc.stamps...); got != tc.refused {
				t.Fatalf("RefuseUnconfirmedWipe = %v, want %v", got, tc.refused)
			}
			if !tc.refused {
				if w.Body.Len() != 0 || w.Code != http.StatusOK {
					t.Fatalf("an allowed wipe must write nothing; code=%d body=%q", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != http.StatusPreconditionFailed {
				t.Fatalf("code = %d, want 412", w.Code)
			}
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not JSON: %v; body=%q", err, w.Body.String())
			}
			if body["error"] != "confirm-fqdn-required" {
				t.Fatalf("error = %q, want confirm-fqdn-required", body["error"])
			}
			if !strings.Contains(body["message"], "virtual machines the Sovereign created itself") {
				t.Fatalf("message does not say that the Sovereign's own virtual machines are destroyed: %q", body["message"])
			}
			if !strings.Contains(body["message"], "X-Confirm-Sovereign-FQDN") {
				t.Fatalf("message does not name the header: %q", body["message"])
			}
		})
	}
}

// TestHandedOver checks the timestamp rule on its own.
func TestHandedOver(t *testing.T) {
	now := time.Now()
	if HandedOver() {
		t.Error("no timestamps: want false")
	}
	if HandedOver(nil, nil, nil) {
		t.Error("only nil timestamps: want false")
	}
	if !HandedOver(nil, &now) {
		t.Error("one timestamp set: want true")
	}
}
