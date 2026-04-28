package handlers

import "net/http"

// Routes returns an http.Handler with all notification routes registered.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /notification/send", h.SendNotification)
	return mux
}
