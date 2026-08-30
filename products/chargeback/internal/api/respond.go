package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

const maxBodyBytes = 4 << 20

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("write response", "error", err)
	}
}

type errorBody struct {
	Error   string `json:"error"`
	Details any    `json:"details,omitempty"`
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

func writeErrDetails(w http.ResponseWriter, status int, msg string, details any) {
	writeJSON(w, status, errorBody{Error: msg, Details: details})
}

// storeErr maps store sentinels to HTTP statuses.
func storeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		slog.Error("store error", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

// decode reads a JSON body into v, rejecting unknown fields and oversized bodies.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

func normEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func validEmail(s string) bool {
	s = normEmail(s)
	at := strings.Index(s, "@")
	return at > 0 && at < len(s)-1 && !strings.ContainsAny(s, " \t\n") && strings.Contains(s[at:], ".")
}

var slugChars = "abcdefghijklmnopqrstuvwxyz0123456789-"

func validSlug(s string) bool {
	if len(s) < 2 || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune(slugChars, c) {
			return false
		}
	}
	return true
}
