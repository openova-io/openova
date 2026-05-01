// credentials_test.go — handler-level tests for the credential
// validators (issue #371).
//
// We exercise the input-validation branches end-to-end through the
// HTTP handler — short-input rejection, region whitelist, body decode
// errors. The Hetzner-Object-Storage live ListBuckets is an integration
// boundary covered by a real `tofu apply` against the staging tenant;
// here we only ensure the validator gates the network call on the
// inputs the wizard provides.
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
	if !strings.Contains(w.Body.String(), "fsn1") {
		t.Errorf("body must mention fsn1/nbg1/hel1 enumeration, got %s", w.Body.String())
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
