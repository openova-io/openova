package main

import "github.com/go-chi/chi/v5"

func registerPublicHandover(r chi.Router, d routeDeps) {
	h := d.h
	// Sovereign-side handover ARCHIVE RECEIVER (issue #317 / #933,
	// fix #3379). Catalyst-Zero's postTofuArchive POSTs the sealed
	// OpenTofu state here (https://api.<sov-fqdn>/api/v1/handover/
	// tofu-archive) so the new Sovereign writes it to OpenBao
	// `secret/catalyst/tofu-phase0-archive` and auto-fires the cutover
	// engine (ReceiveTofuArchive). The mother→child POST carries ONLY
	// `Content-Type: application/json` — there is NO catalyst_session
	// cookie (it is a server-to-server call, no browser). Therefore this
	// route MUST live OUTSIDE RequireSession, exactly like the kubeconfig
	// PUT (#183/#634), the cloud-init-log PUT (#3132), AuthHandover
	// (#606), the cutover trigger (#935) and the openbao SSO shim (#3374)
	// above. It shipped INSIDE RequireSession (#317) and stayed unnoticed
	// because Catalyst-Zero's RequireSession is a nil-cfg passthrough — but
	// on a REAL Sovereign (CATALYST_KC_ADDR set) the active middleware
	// rejected every handover archive POST with 401 {"error":
	// "unauthenticated"} before ReceiveTofuArchive ever ran, so the
	// tofu-phase0-archive marker never sealed → the cutover never fired →
	// `cutoverComplete` stayed false forever (caught live on the hw136
	// cutover walk, #3379). The handler's own auth is content-based
	// (deploymentId + sovereignFqdn validation) + the OpenBao-configured
	// gate (503 on Catalyst-Zero), the same defence pattern as
	// PutKubeconfig's SHA-compare — no session cookie required.
	r.Post("/api/v1/handover/tofu-archive", h.ReceiveTofuArchive)
}

func registerSessionHandover(rg chi.Router, d routeDeps) {
	h := d.h
	rg.Get("/api/v1/handover/public-key", h.GetHandoverPublicKey)
	// Handover finalisation (issue #317). Catalyst-Zero side ONLY:
	// stops the helmwatch informer, ships the OpenTofu state to the new
	// Sovereign's catalyst-api, and purges every local trace once the
	// new side confirms the archive is sealed in its OpenBao. This route
	// stays INSIDE RequireSession because it is the operator-driven
	// wizard finalise — on Catalyst-Zero `cfg` is nil so RequireSession
	// is a transparent passthrough, and on a Sovereign this route is
	// never legitimately called (the Sovereign is the RECEIVER, not the
	// sender). The receiver half (`/handover/tofu-archive`) is registered
	// OUTSIDE RequireSession above (next to the cutover trigger), because
	// the inbound POST from Catalyst-Zero's postTofuArchive carries NO
	// session cookie — see the #3379 comment there.
	rg.Post("/api/v1/handover/finalise/{id}", h.FinaliseHandover)
}
