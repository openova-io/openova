package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// The credentials map (CLD-103) is stored with its keys and with every
// non-empty value replaced by the redaction marker; the installation name is
// kept verbatim, and rehydration returns a copy of the map.
func TestRedact_CredentialsMap(t *testing.T) {
	req := provisioner.Request{
		Provider:     "huawei",
		Installation: "inst-1",
		Credentials:  map[string]string{"access_key": "AK-SENTINEL", "secret_key": "SK-SENTINEL", "project_id": ""},
	}
	red := Redact(req)
	raw, err := json.Marshal(red)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, sentinel := range []string{"AK-SENTINEL", "SK-SENTINEL"} {
		if strings.Contains(string(raw), sentinel) {
			t.Errorf("credential value %s reached the stored record: %s", sentinel, raw)
		}
	}
	want := map[string]string{"access_key": redactedMarker, "secret_key": redactedMarker, "project_id": ""}
	if len(red.Credentials) != len(want) {
		t.Fatalf("stored credentials = %v, want %v", red.Credentials, want)
	}
	for k, v := range want {
		if red.Credentials[k] != v {
			t.Errorf("stored credentials[%s] = %q, want %q", k, red.Credentials[k], v)
		}
	}
	if red.Installation != "inst-1" {
		t.Errorf("stored installation = %q, want inst-1", red.Installation)
	}
	back := red.ToProvisionerRequest()
	if back.Installation != "inst-1" || back.Credentials["access_key"] != redactedMarker {
		t.Errorf("rehydrated = installation %q, credentials %v", back.Installation, back.Credentials)
	}
	back.Credentials["access_key"] = "changed"
	if red.Credentials["access_key"] != redactedMarker {
		t.Error("the rehydrated map shares storage with the record")
	}
	if Redact(provisioner.Request{}).Credentials != nil {
		t.Error("a request without credentials stores no map")
	}
}
