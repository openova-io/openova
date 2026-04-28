package respond

import (
	"encoding/json"
	"net/http"
)

// JSON writes v as JSON with the given status code.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Error writes a JSON error response.
func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"error": msg})
}

// OK writes a 200 JSON response.
func OK(w http.ResponseWriter, v any) {
	JSON(w, http.StatusOK, v)
}
