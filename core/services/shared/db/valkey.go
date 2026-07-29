package db

import "github.com/valkey-io/valkey-go"

// ConnectValkey creates a Valkey client connected to the given address.
//
// Backwards-compatible single-arg form. Callers that need to authenticate
// against a Valkey deployment with `requirepass` set (e.g. bp-valkey 1.0.0
// which ships `auth.enabled=true` and auto-generates a random password)
// should use ConnectValkeyWithAuth.
func ConnectValkey(addr string) (valkey.Client, error) {
	return valkey.NewClient(valkey.ClientOption{
		InitAddress: []string{addr},
	})
}

// ConnectValkeyWithAuth creates a Valkey client and authenticates with the
// given username + password.
//
// Why this overload exists (issue #863):
//
//	bp-valkey (Catalyst Blueprint slot 17, bitnami valkey 5.5.1) exposes an
//	auto-generated password via the `valkey-password` key in the `valkey`
//	Secret WHEN auth is enabled. Sovereign-side Organization services consume
//	Valkey cross-namespace from `org-services` ns; the catalyst chart mirrors
//	the password into the `org-valkey-auth` Secret in `org-services` ns
//	(see products/catalyst/chart/templates/org-services/
//	valkey-cross-ns-secret.yaml) and the auth + gateway Deployments wire
//	it into VALKEY_USERNAME / VALKEY_PASSWORD env. Without these, NEWHELLO
//	is rejected with "NOAUTH HELLO must be called with the client already
//	authenticated".
//
//	NOT the default posture (#5487): bp-valkey flipped to
//	`auth.enabled: false` in 1.0.2 (TBD-V12 #2003) to match bp-newapi's
//	passwordless REDIS_CONN_STRING, so on a stock Sovereign no `valkey`
//	Secret exists, no `org-valkey-auth` mirror is produced, and the callers
//	below take the no-auth ConnectValkey path with an empty password. Both
//	the mirror and the VALKEY_PASSWORD env now render only when the operator
//	sets `orgServices.valkey.auth.enabled: true` alongside bp-valkey's own
//	`valkey.auth.enabled: true`. Until then the isolation is carried by the
//	network: bp-valkey 1.1.5 scopes its NetworkPolicy ingress to the
//	`org-services` + `newapi` namespaces (#5487).
//
// Username may be empty — bitnami's auth scheme uses the implicit
// `default` ACL user with the password set via `requirepass`, so passing
// username="" + password=<from-secret> is the canonical contabo / Sovereign
// shape today.
func ConnectValkeyWithAuth(addr, username, password string) (valkey.Client, error) {
	return valkey.NewClient(valkey.ClientOption{
		InitAddress: []string{addr},
		Username:    username,
		Password:    password,
	})
}
