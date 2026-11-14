package handoverjwt

import "testing"

// TestOwnIssuer (SOV-180): a Sovereign's own console origin comes from
// SOVEREIGN_FQDN; without it the process is the mothership.
func TestOwnIssuer(t *testing.T) {
	for _, c := range []struct{ fqdn, want string }{
		{"alpha.example.org", "https://console.alpha.example.org"},
		{" Alpha.Example.org. ", "https://console.alpha.example.org"},
		{"", mothershipIssuer},
	} {
		t.Setenv("SOVEREIGN_FQDN", c.fqdn)
		if got := OwnIssuer(); got != c.want {
			t.Errorf("SOVEREIGN_FQDN=%q: OwnIssuer()=%q, want %q", c.fqdn, got, c.want)
		}
	}
}
