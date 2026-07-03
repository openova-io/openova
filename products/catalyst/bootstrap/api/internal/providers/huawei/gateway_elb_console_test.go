package huawei

import "strings"

import "testing"

// TestConsoleELBSuffixesDistinctFromPrimary (#4706) locks the invariant the
// two-ELB reconcile relies on: the console ELB/pool name suffixes must NOT be
// suffix-matched by the primary classification (or ReconcileGatewayELBMembers
// would touch the console ELB / mis-port its pools) and vice-versa. tofu names:
// primary "-elb-primary" / "-elb-pool-{https,http}"; console "-elb-console" /
// "-elb-pool-console-{https,http}".
func TestConsoleELBSuffixesDistinctFromPrimary(t *testing.T) {
	// A real console pool name must classify as console, never as primary.
	consoleHTTPS := "catalyst-x-elb-pool-console-https"
	consoleHTTP := "catalyst-x-elb-pool-console-http"
	if strings.HasSuffix(consoleHTTPS, gatewayPoolHTTPSSuffix) || strings.HasSuffix(consoleHTTPS, gatewayPoolHTTPSuffix) {
		t.Errorf("console HTTPS pool %q must NOT match a primary pool suffix", consoleHTTPS)
	}
	if strings.HasSuffix(consoleHTTP, gatewayPoolHTTPSSuffix) || strings.HasSuffix(consoleHTTP, gatewayPoolHTTPSuffix) {
		t.Errorf("console HTTP pool %q must NOT match a primary pool suffix", consoleHTTP)
	}
	// Console classification: https matches only https; http only http.
	if !strings.HasSuffix(consoleHTTPS, consolePoolHTTPSSuffix) || strings.HasSuffix(consoleHTTPS, consolePoolHTTPSuffix) {
		t.Errorf("console HTTPS pool %q must match consolePoolHTTPSSuffix and NOT the http one", consoleHTTPS)
	}
	if !strings.HasSuffix(consoleHTTP, consolePoolHTTPSuffix) {
		t.Errorf("console HTTP pool %q must match consolePoolHTTPSuffix", consoleHTTP)
	}
	// A real primary pool must never match a console suffix.
	primHTTPS := "catalyst-x-elb-pool-https"
	if strings.HasSuffix(primHTTPS, consolePoolHTTPSSuffix) {
		t.Errorf("primary HTTPS pool %q must NOT match a console suffix", primHTTPS)
	}
	// The two ELB name suffixes must be distinct.
	if consoleELBNameSuffix == gatewayELBNameSuffix || strings.HasSuffix(consoleELBNameSuffix, gatewayELBNameSuffix) {
		t.Errorf("console ELB suffix %q must differ from primary %q", consoleELBNameSuffix, gatewayELBNameSuffix)
	}
}
