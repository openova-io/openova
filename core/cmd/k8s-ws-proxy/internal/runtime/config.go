// Package runtime loads + validates the k8s-ws-proxy's runtime
// configuration from environment variables (per
// docs/INVIOLABLE-PRINCIPLES.md #4 — never hardcode).
package runtime

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Config is the fully-resolved runtime configuration. Construct via
// LoadFromEnv; main.go writes nothing else.
type Config struct {
	// ListenAddr is the bind address for the HTTP listener. Defaults
	// to ":8080".
	ListenAddr string

	// SharedSecretFile is the absolute path to a file holding the
	// shared HMAC secret. Mounted from a K8s Secret by the chart.
	// REQUIRED — startup fails when empty/missing/empty-content.
	SharedSecretFile string

	// SkewSeconds bounds the HMAC timestamp acceptance window in
	// seconds (caller- AND proxy-side). Defaults to 300 (5min).
	SkewSeconds int

	// TmuxCascade enables the tmux bastion-shell wrapper. Defaults
	// to false (transparent passthrough).
	TmuxCascade bool

	// AllowedNamespaces is an optional comma-separated allowlist of
	// namespaces the proxy will dial into. Empty (default) = all
	// namespaces; the operator MUST pair with NetworkPolicy + RBAC.
	AllowedNamespaces []string

	// LogLevel — debug / info / warn / error. Defaults to info.
	LogLevel string

	// PingPeriod controls WebSocket keepalive ping frequency.
	// Defaults to 30s.
	PingPeriod time.Duration

	// HandshakeTimeout caps the per-request HTTP→WebSocket upgrade.
	// Defaults to 10s.
	HandshakeTimeout time.Duration

	// ── TLS listener + mTLS client-certificate auth (#5991) ──────────
	//
	// All of this is ADDITIVE. With TLSCertFile/TLSKeyFile unset the
	// binary starts exactly one plaintext listener on ListenAddr, as it
	// always has, and ClientCertSubjects is inert.

	// TLSListenAddr is the bind address for the TLS listener. Only used
	// when TLSCertFile and TLSKeyFile are both set. Defaults to ":8443".
	TLSListenAddr string

	// TLSCertFile / TLSKeyFile are the proxy's server certificate and
	// private key, mounted from a cert-manager-issued Secret. BOTH must
	// be set to start the TLS listener; one without the other is a
	// configuration error rather than a silent downgrade to plaintext.
	TLSCertFile string
	TLSKeyFile  string

	// TLSClientCAFile is the CA bundle that client certificates must
	// chain to. Required whenever ClientCertSubjects is non-empty: an
	// allowlist with no CA to verify against would accept a subject
	// claimed by anyone.
	TLSClientCAFile string

	// ClientCertSubjects is the explicit allowlist of client-certificate
	// identities (CN or DNS SAN) permitted to authenticate. EMPTY
	// DISABLES client-certificate auth entirely — enabling TLS alone
	// never opens the second credential leg.
	ClientCertSubjects []string

	// PodAliasLabel enables workload-name resolution of the pod segment
	// of an exec URL (see internal/proxy/podresolve.go). Empty (default)
	// = literal pod names only, no apiserver read per request.
	PodAliasLabel string

	// NodeName is this Pod's node, from the downward API. Used only to
	// prefer a node-local Pod when an alias resolves to several.
	NodeName string
}

// TLSEnabled reports whether the binary should start the TLS listener.
func (c Config) TLSEnabled() bool {
	return c.TLSCertFile != "" && c.TLSKeyFile != ""
}

// ClientCertAuthEnabled reports whether the mTLS credential leg is
// configured. Both halves are required: an allowlist AND a CA to verify
// chains against.
func (c Config) ClientCertAuthEnabled() bool {
	return len(c.ClientCertSubjects) > 0 && c.TLSClientCAFile != ""
}

// LoadFromEnv reads the env-var contract and returns a validated Config.
// Returns an error on missing required values; main.go logs the error
// and exits with code 2 (per the cert-manager-dynadot-webhook pattern).
//
// Env-var contract:
//
//	WS_PROXY_LISTEN_ADDR        - default ":8080"
//	SHARED_SECRET_FILE          - REQUIRED, path to HMAC secret
//	HMAC_SKEW_SECONDS           - default 300
//	TMUX_CASCADE                - "true" / "false", default false
//	ALLOWED_NAMESPACES          - comma-separated, optional
//	LOG_LEVEL                   - debug/info/warn/error, default info
//	WS_PING_PERIOD              - duration, default "30s"
//	WS_HANDSHAKE_TIMEOUT        - duration, default "10s"
//	TLS_LISTEN_ADDR             - default ":8443" (used only with TLS)
//	TLS_CERT_FILE               - server cert PEM; pairs with TLS_KEY_FILE
//	TLS_KEY_FILE                - server key PEM; pairs with TLS_CERT_FILE
//	TLS_CLIENT_CA_FILE          - CA bundle client certs must chain to
//	CLIENT_CERT_ALLOWED_SUBJECTS - comma-separated CN/DNS-SAN allowlist;
//	                              EMPTY disables mTLS auth (#5991)
//	POD_ALIAS_LABEL             - label key for workload-name resolution;
//	                              empty = literal pod names only
//	NODE_NAME                   - downward API; node-locality preference
func LoadFromEnv() (Config, error) {
	cfg := Config{
		ListenAddr:       getenvDefault("WS_PROXY_LISTEN_ADDR", ":8080"),
		SharedSecretFile: strings.TrimSpace(os.Getenv("SHARED_SECRET_FILE")),
		LogLevel:         getenvDefault("LOG_LEVEL", "info"),
		TmuxCascade:      strings.EqualFold(os.Getenv("TMUX_CASCADE"), "true"),
	}
	if cfg.SharedSecretFile == "" {
		return cfg, errors.New("SHARED_SECRET_FILE is required (path to a file holding the HMAC secret)")
	}

	cfg.SkewSeconds = 300
	if v := strings.TrimSpace(os.Getenv("HMAC_SKEW_SECONDS")); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
			return cfg, fmt.Errorf("HMAC_SKEW_SECONDS=%q invalid (positive int)", v)
		}
		cfg.SkewSeconds = n
	}

	if v := strings.TrimSpace(os.Getenv("ALLOWED_NAMESPACES")); v != "" {
		for _, ns := range strings.Split(v, ",") {
			ns = strings.TrimSpace(ns)
			if ns != "" {
				cfg.AllowedNamespaces = append(cfg.AllowedNamespaces, ns)
			}
		}
	}

	cfg.PingPeriod = mustParseDur(getenvDefault("WS_PING_PERIOD", "30s"), 30*time.Second)
	cfg.HandshakeTimeout = mustParseDur(getenvDefault("WS_HANDSHAKE_TIMEOUT", "10s"), 10*time.Second)

	cfg.TLSListenAddr = getenvDefault("TLS_LISTEN_ADDR", ":8443")
	cfg.TLSCertFile = strings.TrimSpace(os.Getenv("TLS_CERT_FILE"))
	cfg.TLSKeyFile = strings.TrimSpace(os.Getenv("TLS_KEY_FILE"))
	cfg.TLSClientCAFile = strings.TrimSpace(os.Getenv("TLS_CLIENT_CA_FILE"))
	// One of the pair without the other would otherwise fall through to
	// "TLS not enabled" and serve the exec endpoint in plaintext while
	// the operator believed it was mTLS-gated. Fail startup instead.
	if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
		return cfg, errors.New("TLS_CERT_FILE and TLS_KEY_FILE must be set together")
	}
	for _, s := range strings.Split(os.Getenv("CLIENT_CERT_ALLOWED_SUBJECTS"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			cfg.ClientCertSubjects = append(cfg.ClientCertSubjects, s)
		}
	}
	// Same reasoning: an allowlist with no CA bundle cannot verify a
	// chain, so honouring it would accept any self-signed certificate
	// claiming an allowlisted CN.
	if len(cfg.ClientCertSubjects) > 0 && cfg.TLSClientCAFile == "" {
		return cfg, errors.New("CLIENT_CERT_ALLOWED_SUBJECTS is set but TLS_CLIENT_CA_FILE is empty — an allowlist with no CA verifies nothing")
	}
	if len(cfg.ClientCertSubjects) > 0 && !cfg.TLSEnabled() {
		return cfg, errors.New("CLIENT_CERT_ALLOWED_SUBJECTS is set but the TLS listener is not configured (TLS_CERT_FILE/TLS_KEY_FILE)")
	}

	cfg.PodAliasLabel = strings.TrimSpace(os.Getenv("POD_ALIAS_LABEL"))
	cfg.NodeName = strings.TrimSpace(os.Getenv("NODE_NAME"))

	return cfg, nil
}

// ReadSecret reads the shared secret from cfg.SharedSecretFile.
// Trailing whitespace (newlines from `kubectl create secret`) is
// trimmed before returning.
func ReadSecret(cfg Config) ([]byte, error) {
	raw, err := os.ReadFile(cfg.SharedSecretFile)
	if err != nil {
		return nil, fmt.Errorf("read SHARED_SECRET_FILE %q: %w", cfg.SharedSecretFile, err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, fmt.Errorf("SHARED_SECRET_FILE %q is empty", cfg.SharedSecretFile)
	}
	return []byte(trimmed), nil
}

// ParseLogLevel maps the env LOG_LEVEL string to a slog.Level.
func ParseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func getenvDefault(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}

func mustParseDur(raw string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
