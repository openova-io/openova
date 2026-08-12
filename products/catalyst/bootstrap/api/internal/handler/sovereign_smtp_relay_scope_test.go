// #5921 — the NINTH tether: customer sign-in mail.
//
// catalyst-system/marketplace-api reads catalyst-system/sovereign-smtp-
// credentials (chart template marketplace-api/deployment.yaml) to send every
// customer sign-in code. Seeding a mothership relay into that Secret means a
// post-cutover Sovereign with cutoverComplete=true cannot sell anything unless
// OUR mail server answers. ADR-0002 enumerates EIGHT tethers and outbound
// customer-auth SMTP is not among them, so no cutover step pivots it.
//
// These tests pin two properties the pre-#5921 seed could not express:
//
//  1. A Sovereign-local relay is reachable AT ALL. Before this change the host
//     was `mail.openova.io` or an operator-supplied literal; there was no way
//     to say "use the Sovereign's own mail host" and no code path anywhere in
//     the seed that consulted dep.Request.SovereignFQDN.
//  2. The Secret's provenance annotation reports the RESOLVED host rather than
//     the code path. The old constant `phase-1-mothership-relay` was stamped
//     even onto a Secret pointing at a Sovereign-local relay, so anything
//     keying on it read a self-report that could not go wrong — the
//     "guard that tested a surface that cannot fail" shape.
package handler

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	kfake "k8s.io/client-go/kubernetes/fake"
)

// TestResolveSovereignSMTPRelay_ScopeMatrix drives the resolver directly so
// each precedence rung is asserted in BOTH directions. A resolver that always
// returned "mothership" would satisfy any single case here on its own.
func TestResolveSovereignSMTPRelay_ScopeMatrix(t *testing.T) {
	const fqdn = "t99.omani.works"

	cases := []struct {
		name      string
		mode      string
		hostEnv   string
		fromEnv   string
		portEnv   string
		fqdn      string
		wantHost  string
		wantFrom  string
		wantPort  string
		wantScope string
	}{
		{
			// POSITIVE CONTROL — the shipped default is deliberately
			// unchanged. Flipping it would point every fresh Sovereign at a
			// relay nothing serves (slot 95 bp-stalwart-sovereign was removed
			// from the bootstrap-kit in 94ffe01ff), which is an outage on the
			// purchase path, not a sovereignty fix.
			name: "default mode is the mothership relay (no regression)",
			fqdn: fqdn, wantHost: "mail.openova.io", wantFrom: "noreply@openova.io",
			wantPort: "587", wantScope: SovereignSMTPRelayScopeMothership,
		},
		{
			// THE FIX — the path that did not exist before #5921.
			name: "sovereign-local mode derives mail.<fqdn> from the deployment",
			mode: SovereignSMTPRelayScopeLocal, fqdn: fqdn,
			wantHost: "mail." + fqdn, wantFrom: "noreply@" + fqdn,
			wantPort: "587", wantScope: SovereignSMTPRelayScopeLocal,
		},
		{
			// FAIL-SAFE — sovereign-local cannot compose a host without an
			// FQDN. Seeding the bare string `mail.` would break PIN delivery
			// for the life of the cluster, so fall back to a deliverable relay
			// AND report the tether honestly rather than claiming local.
			name: "sovereign-local with empty FQDN falls back and reports mothership",
			mode: SovereignSMTPRelayScopeLocal, fqdn: "",
			wantHost: "mail.openova.io", wantFrom: "noreply@openova.io",
			wantPort: "587", wantScope: SovereignSMTPRelayScopeMothership,
		},
		{
			// THE ANNOTATION BUG — an operator-pinned Sovereign-local relay
			// used to be stamped `phase-1-mothership-relay`. Scope must be
			// measured from the resolved HOST, never from the requested mode.
			name:    "explicit host wins and is classified from the host itself",
			hostEnv: "mail." + fqdn, fqdn: fqdn,
			wantHost: "mail." + fqdn, wantFrom: "noreply@openova.io",
			wantPort: "587", wantScope: SovereignSMTPRelayScopeLocal,
		},
		{
			// VACUITY, THE OTHER WAY — an explicit host that is NOT the
			// Sovereign's own stays a tether even in sovereign-local mode.
			// This proves the previous case turned on the HOST and not merely
			// on the presence of an override.
			name: "explicit foreign host stays a tether even in sovereign-local mode",
			mode: SovereignSMTPRelayScopeLocal, hostEnv: "smtp.sendgrid.net", fqdn: fqdn,
			wantHost: "smtp.sendgrid.net", wantFrom: "noreply@openova.io",
			wantPort: "587", wantScope: SovereignSMTPRelayScopeMothership,
		},
		{
			name: "explicit from and port are honoured",
			mode: SovereignSMTPRelayScopeLocal, fromEnv: "hello@" + fqdn, portEnv: "2525", fqdn: fqdn,
			wantHost: "mail." + fqdn, wantFrom: "hello@" + fqdn,
			wantPort: "2525", wantScope: SovereignSMTPRelayScopeLocal,
		},
		{
			// An in-cluster relay is the Sovereign's own too — this is the
			// shape a restored slot 95 would produce via its ClusterIP
			// Service, and it must not be reported as a mothership tether.
			name:    "in-cluster service host classifies as sovereign-local",
			hostEnv: "stalwart-web.stalwart.svc.cluster.local", fqdn: fqdn,
			wantHost: "stalwart-web.stalwart.svc.cluster.local", wantFrom: "noreply@openova.io",
			wantPort: "587", wantScope: SovereignSMTPRelayScopeLocal,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(sovereignSMTPRelayModeEnv, tc.mode)
			t.Setenv("CATALYST_SOVEREIGN_SMTP_RELAY_HOST", tc.hostEnv)
			t.Setenv("CATALYST_SOVEREIGN_SMTP_RELAY_FROM", tc.fromEnv)
			t.Setenv("CATALYST_SOVEREIGN_SMTP_RELAY_PORT", tc.portEnv)

			got := resolveSovereignSMTPRelay(tc.fqdn, "noreply@openova.io")

			if got.Host != tc.wantHost {
				t.Errorf("Host = %q, want %q", got.Host, tc.wantHost)
			}
			if got.From != tc.wantFrom {
				t.Errorf("From = %q, want %q", got.From, tc.wantFrom)
			}
			if got.Port != tc.wantPort {
				t.Errorf("Port = %q, want %q", got.Port, tc.wantPort)
			}
			if got.Scope != tc.wantScope {
				t.Errorf("Scope = %q, want %q (scope must be measured from the resolved host, not the requested mode)", got.Scope, tc.wantScope)
			}
		})
	}
}

// TestSeedSovereignSMTPCredentials_SovereignLocalRelay is the end-to-end
// assertion on the SEEDED BYTES. The resolver being right is not enough if the
// seed never consults the deployment's FQDN and never writes what it returns.
// This case fails outright before #5921: the pre-fix seed contained no
// reference to dep.Request.SovereignFQDN anywhere.
func TestSeedSovereignSMTPCredentials_SovereignLocalRelay(t *testing.T) {
	const fqdn = "t99.omani.works"
	setSeedSMTPEnv(t, "noreply@openova.io", "p455w0rd-bytes-here-not-real")
	t.Setenv(sovereignSMTPRelayModeEnv, SovereignSMTPRelayScopeLocal)

	core := kfake.NewSimpleClientset()
	h := &Handler{log: silentLogger()}
	h.SetSovereignSMTPSeedClientFactory(func(string) (kubernetes.Interface, error) { return core, nil })

	dep := seedTestDeployment("dep-local-relay")
	dep.Request.SovereignFQDN = fqdn

	if outcome := h.seedSovereignSMTPCredentials(context.Background(), dep, "kubeconfig-yaml-bytes"); outcome != SovereignSMTPSeedOutcomeCreated {
		t.Fatalf("outcome = %q, want %q", outcome, SovereignSMTPSeedOutcomeCreated)
	}

	got, err := core.CoreV1().Secrets(sovereignSMTPSeedNamespace).Get(context.Background(), sovereignSMTPSeedSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Secret not created: %v", err)
	}

	// The bytes marketplace-api actually dials. This is the purchase path.
	if host := string(got.Data["smtp-host"]); host != "mail."+fqdn {
		t.Errorf("smtp-host = %q, want %q — a post-cutover Sovereign must not send customer sign-in codes through the mothership", host, "mail."+fqdn)
	}
	if from := string(got.Data["smtp-from"]); from != "noreply@"+fqdn {
		t.Errorf("smtp-from = %q, want %q", from, "noreply@"+fqdn)
	}
	// Provenance must describe the bytes, not the code path.
	if s := got.Annotations["catalyst.openova.io/relay-scope"]; s != SovereignSMTPRelayScopeLocal {
		t.Errorf("relay-scope annotation = %q, want %q", s, SovereignSMTPRelayScopeLocal)
	}
	if p := got.Annotations["catalyst.openova.io/seed-phase"]; p != "phase-2-sovereign-local-relay" {
		t.Errorf("seed-phase annotation = %q, want phase-2-sovereign-local-relay", p)
	}
}

// TestSeedSovereignSMTPCredentials_MothershipRelayIsStampedAsTethered pins the
// other direction: the default seed still writes a deliverable relay AND now
// says out loud that it is a tether. Without this case the test above is also
// satisfied by a seed that stamps `sovereign-local` unconditionally.
func TestSeedSovereignSMTPCredentials_MothershipRelayIsStampedAsTethered(t *testing.T) {
	setSeedSMTPEnv(t, "noreply@openova.io", "p455w0rd-bytes-here-not-real")

	core := kfake.NewSimpleClientset()
	h := &Handler{log: silentLogger()}
	h.SetSovereignSMTPSeedClientFactory(func(string) (kubernetes.Interface, error) { return core, nil })

	dep := seedTestDeployment("dep-mothership-relay")
	dep.Request.SovereignFQDN = "t99.omani.works"

	if outcome := h.seedSovereignSMTPCredentials(context.Background(), dep, "kubeconfig-yaml-bytes"); outcome != SovereignSMTPSeedOutcomeCreated {
		t.Fatalf("outcome = %q, want %q", outcome, SovereignSMTPSeedOutcomeCreated)
	}

	got, err := core.CoreV1().Secrets(sovereignSMTPSeedNamespace).Get(context.Background(), sovereignSMTPSeedSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Secret not created: %v", err)
	}
	if host := string(got.Data["smtp-host"]); host != "mail.openova.io" {
		t.Errorf("smtp-host = %q, want mail.openova.io (the default must stay deliverable)", host)
	}
	if s := got.Annotations["catalyst.openova.io/relay-scope"]; s != SovereignSMTPRelayScopeMothership {
		t.Errorf("relay-scope annotation = %q, want %q — the default seed IS a tether and must say so", s, SovereignSMTPRelayScopeMothership)
	}
}
