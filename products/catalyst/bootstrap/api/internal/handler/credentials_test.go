// credentials_test.go — handler-level tests for the credential
// validators (issue #371, vendor-agnostic since #425).
//
// We exercise the input-validation branches end-to-end through the
// HTTP handler — the checks of the cloud profile, short-input rejection,
// region whitelist, body decode errors. No test reaches a cloud: each
// one is answered before the adapter calls it.
package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

func newCredentialsHandler() *Handler {
	log := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	return New(log)
}

func TestValidateObjectStorageCredentials_BadJSON(t *testing.T) {
	h := newCredentialsHandler()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost,
		"/api/v1/credentials/object-storage/validate",
		bytes.NewReader([]byte("{not-json")))
	h.ValidateObjectStorageCredentials(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", w.Code)
	}
}

func TestValidateObjectStorageCredentials_MissingRegion(t *testing.T) {
	h := newCredentialsHandler()
	body, _ := json.Marshal(map[string]string{
		"accessKey": "TESTACCESSKEY1234567",
		"secretKey": "TESTSECRETKEY1234567890123456789012345678",
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost,
		"/api/v1/credentials/object-storage/validate",
		bytes.NewReader(body))
	h.ValidateObjectStorageCredentials(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "region") {
		t.Errorf("body must mention region, got %s", w.Body.String())
	}
}

func TestValidateObjectStorageCredentials_InvalidRegion(t *testing.T) {
	h := newCredentialsHandler()
	body, _ := json.Marshal(map[string]string{
		"region":    "us-east-1",
		"accessKey": "TESTACCESSKEY1234567",
		"secretKey": "TESTSECRETKEY1234567890123456789012345678",
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost,
		"/api/v1/credentials/object-storage/validate",
		bytes.NewReader(body))
	h.ValidateObjectStorageCredentials(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400, body=%s", w.Code, w.Body.String())
	}
	// No provider: the one adapter with ObjectStorageValidator answers;
	// us-east-1 is not one of its regions, so ObjectStorageEndpoint
	// returns "" and the handler surfaces "region not supported by the
	// chosen object storage provider".
	if !strings.Contains(w.Body.String(), "region not supported") {
		t.Errorf("body must mention 'region not supported', got %s", w.Body.String())
	}
}

func TestValidateObjectStorageCredentials_UnsupportedProvider(t *testing.T) {
	h := newCredentialsHandler()
	body, _ := json.Marshal(map[string]string{
		"provider":  "made-up-cloud",
		"region":    "fsn1",
		"accessKey": "TESTACCESSKEY1234567",
		"secretKey": "TESTSECRETKEY1234567890123456789012345678",
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost,
		"/api/v1/credentials/object-storage/validate",
		bytes.NewReader(body))
	h.ValidateObjectStorageCredentials(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unsupported object storage provider") {
		t.Errorf("body must mention unsupported provider, got %s", w.Body.String())
	}
}

func TestValidateObjectStorageCredentials_ShortAccessKey(t *testing.T) {
	h := newCredentialsHandler()
	body, _ := json.Marshal(map[string]string{
		"region":    "fsn1",
		"accessKey": "tooshort",
		"secretKey": "TESTSECRETKEY1234567890123456789012345678",
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost,
		"/api/v1/credentials/object-storage/validate",
		bytes.NewReader(body))
	h.ValidateObjectStorageCredentials(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "access key") {
		t.Errorf("body must mention access-key, got %s", w.Body.String())
	}
}

func TestValidateObjectStorageCredentials_ShortSecretKey(t *testing.T) {
	h := newCredentialsHandler()
	body, _ := json.Marshal(map[string]string{
		"region":    "fsn1",
		"accessKey": "TESTACCESSKEY1234567",
		"secretKey": "tooshort",
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost,
		"/api/v1/credentials/object-storage/validate",
		bytes.NewReader(body))
	h.ValidateObjectStorageCredentials(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "secret key") {
		t.Errorf("body must mention secret-key, got %s", w.Body.String())
	}
}

// postValidate sends body to ValidateCredentials and returns the status and
// the decoded answer.
func postValidate(t *testing.T, body string) (int, validateResponse) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/credentials/validate", strings.NewReader(body))
	newCredentialsHandler().ValidateCredentials(w, r)
	var got validateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %s: decode the answer %q: %v", body, w.Body.String(), err)
	}
	return w.Code, got
}

// A 10-character token fails the pattern of the Hetzner profile's credential
// field hcloud_token, in the legacy body of the wizard and in the credentials
// map, before the cloud is called.
func TestValidateCredentials_TokenFailsTheProfilePattern(t *testing.T) {
	for _, body := range []string{
		`{"provider":"hetzner","token":"0123456789"}`,
		`{"provider":"hetzner","credentials":{"hcloud_token":"0123456789"}}`,
	} {
		code, got := postValidate(t, body)
		if code != http.StatusBadRequest || got.Valid || got.Field != "hcloud_token" {
			t.Errorf("body %s: got %d %+v, want 400 with field hcloud_token", body, code, got)
		}
	}
}

// A credentials map is checked for every field its profile marks required.
func TestValidateCredentials_RequiredFieldOfTheProfile(t *testing.T) {
	token := strings.Repeat("t", 64)
	for body, want := range map[string]string{
		`{"provider":"huawei","credentials":{"access_key":"a"}}`:                "secret_key",
		`{"provider":"hetzner","credentials":{"hcloud_token":"` + token + `"}}`: "object_storage_access_key",
		`{"provider":"hetzner","credentials":{}}`:                               "hcloud_token",
	} {
		code, got := postValidate(t, body)
		if code != http.StatusBadRequest || got.Field != want {
			t.Errorf("body %s: got %d %+v, want 400 with field %s", body, code, got, want)
		}
	}
}

// The provider is never assumed, and an unknown one is 404.
func TestValidateCredentials_ProviderNamedAndKnown(t *testing.T) {
	token := strings.Repeat("t", 64)
	if code, got := postValidate(t, `{"token":"`+token+`"}`); code != http.StatusBadRequest || got.Field != "provider" {
		t.Errorf("no provider: got %d %+v, want 400 with field provider", code, got)
	}
	if code, got := postValidate(t, `{"provider":"made-up-cloud","token":"`+token+`"}`); code != http.StatusNotFound || got.Valid {
		t.Errorf("unknown provider: got %d %+v, want 404", code, got)
	}
	if code, got := postValidate(t, `{"provider":"hetzner","installation":"site-x","token":"`+token+`"}`); code != http.StatusBadRequest || got.Field != "installation" {
		t.Errorf("unreadable installation: got %d %+v, want 400 with field installation", code, got)
	}
}

// The wizard's legacy body carries only the token: it reaches the adapter's
// cloud check, because the required fields are not checked for it.
func TestValidateCredentials_LegacyTokenBodyReachesTheCloudCheck(t *testing.T) {
	token := strings.Repeat("t", 64)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"provider":"hetzner","token":"`+token+`"}`), &fields); err != nil {
		t.Fatal(err)
	}
	cp, err := providers.Get("hetzner")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := provisioner.EffectiveProfile("hetzner", "")
	if err != nil {
		t.Fatal(err)
	}
	creds, legacy := credentialsOf(cp, nil, fields)
	if !legacy || creds.Get("hcloud_token") != token {
		t.Fatalf("credentialsOf = %v, legacy %v; want hcloud_token from the token field", creds.Raw, legacy)
	}
	if err := providers.CheckCredentialValues(creds, profile, !legacy); err != nil {
		t.Errorf("the legacy token body is refused before the cloud check: %v", err)
	}
}
