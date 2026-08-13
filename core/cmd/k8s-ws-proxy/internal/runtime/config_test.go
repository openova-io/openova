package runtime

import (
	"testing"
)

// The #5991 knobs are all fail-fast rather than fail-quiet, because
// every silent failure here is a security downgrade the operator cannot
// see: half a TLS keypair looks like "TLS off", and an allowlist with no
// CA looks like "mTLS on" while verifying nothing.

func TestLoadFromEnv_TLSDefaultsOff(t *testing.T) {
	t.Setenv("SHARED_SECRET_FILE", "/dev/null")
	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLSEnabled() {
		t.Fatal("TLS enabled with no TLS_CERT_FILE/TLS_KEY_FILE — the listener must be opt-in")
	}
	if cfg.ClientCertAuthEnabled() {
		t.Fatal("client-certificate auth enabled by default")
	}
	if cfg.PodAliasLabel != "" {
		t.Fatalf("PodAliasLabel = %q, want empty (literal pod names, no apiserver read)", cfg.PodAliasLabel)
	}
	if cfg.TLSListenAddr != ":8443" {
		t.Fatalf("TLSListenAddr = %q, want :8443", cfg.TLSListenAddr)
	}
}

func TestLoadFromEnv_HalfAKeypairFails(t *testing.T) {
	t.Setenv("SHARED_SECRET_FILE", "/dev/null")
	t.Setenv("TLS_CERT_FILE", "/etc/tls/tls.crt")
	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("TLS_CERT_FILE without TLS_KEY_FILE was accepted — the proxy would serve plaintext while the operator believed otherwise")
	}
}

func TestLoadFromEnv_AllowlistWithoutCAFails(t *testing.T) {
	t.Setenv("SHARED_SECRET_FILE", "/dev/null")
	t.Setenv("TLS_CERT_FILE", "/etc/tls/tls.crt")
	t.Setenv("TLS_KEY_FILE", "/etc/tls/tls.key")
	t.Setenv("CLIENT_CERT_ALLOWED_SUBJECTS", "guacd.guacamole.svc")
	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("an allowlist with no TLS_CLIENT_CA_FILE was accepted — it would 'verify' a subject anyone can claim")
	}
}

func TestLoadFromEnv_AllowlistWithoutTLSListenerFails(t *testing.T) {
	t.Setenv("SHARED_SECRET_FILE", "/dev/null")
	t.Setenv("TLS_CLIENT_CA_FILE", "/etc/tls/ca.crt")
	t.Setenv("CLIENT_CERT_ALLOWED_SUBJECTS", "guacd.guacamole.svc")
	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("client-certificate auth configured with no TLS listener was accepted — no request could ever carry a certificate")
	}
}

func TestLoadFromEnv_FullMTLSConfigParses(t *testing.T) {
	t.Setenv("SHARED_SECRET_FILE", "/dev/null")
	t.Setenv("TLS_CERT_FILE", "/etc/tls/tls.crt")
	t.Setenv("TLS_KEY_FILE", "/etc/tls/tls.key")
	t.Setenv("TLS_CLIENT_CA_FILE", "/etc/tls/ca.crt")
	t.Setenv("CLIENT_CERT_ALLOWED_SUBJECTS", " guacd.guacamole.svc , , other.svc ")
	t.Setenv("POD_ALIAS_LABEL", "app.kubernetes.io/name")
	t.Setenv("NODE_NAME", "node-a")
	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.TLSEnabled() || !cfg.ClientCertAuthEnabled() {
		t.Fatalf("TLSEnabled=%v ClientCertAuthEnabled=%v, want both true", cfg.TLSEnabled(), cfg.ClientCertAuthEnabled())
	}
	if len(cfg.ClientCertSubjects) != 2 ||
		cfg.ClientCertSubjects[0] != "guacd.guacamole.svc" ||
		cfg.ClientCertSubjects[1] != "other.svc" {
		t.Fatalf("ClientCertSubjects = %v, want the two trimmed entries with the empty one dropped", cfg.ClientCertSubjects)
	}
	if cfg.PodAliasLabel != "app.kubernetes.io/name" || cfg.NodeName != "node-a" {
		t.Fatalf("PodAliasLabel=%q NodeName=%q", cfg.PodAliasLabel, cfg.NodeName)
	}
}
