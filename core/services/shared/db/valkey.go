package db

import "github.com/valkey-io/valkey-go"

// ConnectValkey creates a Valkey client connected to the given address.
func ConnectValkey(addr string) (valkey.Client, error) {
	return valkey.NewClient(valkey.ClientOption{
		InitAddress: []string{addr},
	})
}
