package provisioner

import (
	"strings"
	"testing"
)

// INF-054: the request names the Sovereign's own time zone and the parent
// writes it into the child's variables document as SOVEREIGN_TIME_ZONE, UTC
// when the request names none. The reboot span of kured keeps its night
// default, 01:00 to 05:00, read in that zone (KURED_TIME_ZONE stays empty).
func TestChildVariablesTimeZone(t *testing.T) {
	for _, tc := range []struct {
		name, zone, want string
	}{
		{"no zone in the request", "", "UTC"},
		{"the zone of the request", " Asia/Muscat ", "Asia/Muscat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{SovereignFQDN: "t31.omani.works", SovereignTimeZone: tc.zone}
			doc, err := childVariables(req, "https://console.openova.io/sovereign")
			if err != nil {
				t.Fatalf("childVariables: %v", err)
			}
			checkVariablesDocument(t, tc.name, doc, req.SovereignFQDN)
			got := strings.Join([]string{doc["SOVEREIGN_TIME_ZONE"], doc["KURED_START_TIME"], doc["KURED_END_TIME"], doc["KURED_TIME_ZONE"]}, "|")
			if want := tc.want + "|01:00|05:00|"; got != want {
				t.Errorf("time zone and reboot span = %q, want %q", got, want)
			}
		})
	}
	for _, zone := range []string{"Asia/Nowhere", "Local", "asia muscat"} {
		req := baseValidateRequest(t)
		req.SovereignTimeZone = zone
		if err := req.Validate(); err == nil || !strings.Contains(err.Error(), "sovereignTimeZone") {
			t.Errorf("zone %q: Validate = %v, want the sovereignTimeZone error", zone, err)
		}
	}
	for _, zone := range []string{"", "UTC", "Asia/Muscat", "America/Argentina/ComodRivadavia"} {
		req := baseValidateRequest(t)
		req.SovereignTimeZone = zone
		if err := req.Validate(); err != nil {
			t.Errorf("zone %q: Validate = %v", zone, err)
		}
	}
}
