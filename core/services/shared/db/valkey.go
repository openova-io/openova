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
//	bp-valkey (Catalyst Blueprint slot 17, bitnami valkey 5.5.1) defaults
//	to `auth.enabled=true` and exposes the auto-generated password via the
//	`valkey-password` key in the `valkey` Secret. Sovereign-side SME
//	services consume Valkey cross-namespace from `sme` ns; the catalyst
//	chart mirrors the password into `sme-valkey-auth` Secret in `sme` ns
//	(see products/catalyst/chart/templates/org-services/
//	valkey-cross-ns-secret.yaml) and the auth + gateway Deployments wire
//	it into VALKEY_USERNAME / VALKEY_PASSWORD env. Without these, NEWHELLO
//	is rejected with "NOAUTH HELLO must be called with the client already
//	authenticated".
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
