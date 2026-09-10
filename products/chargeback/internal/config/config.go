// Package config reads the service configuration from the environment.
package config

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	DatabaseURL      string
	EncryptionKeyB64 string
	OperatorEmails   []string
	PublicURL        string
	ListenAddr       string
	Profile          string // sovereign | operator-central

	SMTPHost string
	SMTPPort int
	SMTPUser string
	SMTPPass string
	SMTPFrom string

	HuaweiEndpointTemplate string
	HuaweiInsecureTLS      bool
	CollectInterval        time.Duration
	CTSPollInterval        time.Duration
	CESInterval            time.Duration
	CollectorEnabled       bool

	// OpenOva adapter (ADR-0014 D2 case 1; #6723 lane D). AdapterEnabled
	// is the raw ADAPTER_ENABLED override: "" = auto (on when the profile
	// is sovereign AND in-cluster Kubernetes configuration is available),
	// truthy = force on, falsy = force off — openova.Decide holds the rule.
	AdapterEnabled string
	// BillingHookURL is the billing service base URL for the D6 seam;
	// unset ⇒ the statement-issued hook is off. BillingHookToken is the
	// superadmin bearer token the metering endpoint requires.
	BillingHookURL   string
	BillingHookToken string
	// BillingHookCallbackSecret signs the billing service's payment
	// callbacks to POST /api/v1/gateways/stripe/callback (DESIGN.md §9.2);
	// unset ⇒ that route refuses every callback for the stripe gateway.
	BillingHookCallbackSecret string

	// CommercialExportDir is where the csvfile exporter writes rated bills
	// when the Sovereign invoices through the operator's own billing system
	// (DESIGN.md §8.10). Unset ⇒ no exporter is wired and queued documents
	// wait in the outbox. CommercialImportSecret is the shared secret the
	// inbound invoice-status webhook is HMAC-signed with; unset ⇒ that
	// endpoint answers 503 rather than accepting an unauthenticated write.
	CommercialExportDir    string
	CommercialImportSecret string
	// CommercialImportDir is the polling fallback of the import webhooks
	// (DESIGN.md §9.1): a directory the billing system drops command files
	// in. Unset ⇒ no poller.
	CommercialImportDir string

	// PlatformAPIURL reaches the sovereign-admin API's ServiceAccount-
	// authenticated Organization suspend / resume routes (DESIGN.md §9.6,
	// /api/v1/internal/organizations/{slug}/suspend | /resume). Unset ⇒ a
	// suspension flips the customer here and is recorded as not executed
	// at the platform.
	//
	// The bearer is one of two: PlatformAPITokenFile names a file — the
	// chart's projected ServiceAccount token at
	// /var/run/secrets/platform-api/token — that is re-read on EVERY call,
	// because projected tokens rotate; PlatformAPIToken is a literal for
	// the cases where no file exists (a local run against a Sovereign).
	// The file wins when both are set.
	PlatformAPIURL       string
	PlatformAPIToken     string
	PlatformAPITokenFile string

	// TrustedForwardAuthHeader is the request header carrying an identity
	// already verified by the Sovereign's OIDC gate. oauth2-proxy passes the
	// address UPSTREAM as X-Forwarded-Email; X-Auth-Request-Email is a
	// RESPONSE header for nginx auth_request mode and never arrives here
	// (verified from the v7.6.0 binary's --convert-config-to-alpha output).
	// Empty (the default) = off, and the header is ignored entirely.
	//
	// This header is TRUSTED WITHOUT VERIFICATION, so it may only be
	// enabled where the app is unreachable except through the gate: the
	// gate must own the public hostname (the app's own HTTPRoute disabled)
	// and the ingress NetworkPolicy must admit only the gate. Enabling it
	// while the app keeps its own public route would let anyone set the
	// header and assume any identity — the chart refuses to render that
	// combination, and scripts/check-chargeback-sso-no-bypass.sh gates it.
	TrustedForwardAuthHeader string
	// TrustedForwardGroupsHeader is the request header carrying the
	// directory groups of the identity in TrustedForwardAuthHeader, comma-
	// separated (oauth2-proxy's X-Forwarded-Groups). Each group is looked up
	// in group_role_mappings and its roles are added to the session's
	// bindings (DESIGN.md §10). Only honoured when TrustedForwardAuthHeader
	// is set — the groups header is trusted for exactly the same reason and
	// under exactly the same conditions as the identity header, and is inert
	// without it. Default X-Forwarded-Groups.
	TrustedForwardGroupsHeader string
}

// FromEnv builds the configuration; it fails only on values that would make
// the process unsafe to run (a malformed duration falls back with a warning).
func FromEnv() (Config, error) {
	c := Config{
		DatabaseURL:               get("DATABASE_URL", "postgres://chargeback:chargeback@localhost:5432/chargeback?sslmode=disable"),
		EncryptionKeyB64:          os.Getenv("APP_ENCRYPTION_KEY"),
		OperatorEmails:            splitList(os.Getenv("OPERATOR_EMAILS")),
		PublicURL:                 strings.TrimRight(get("PUBLIC_URL", "http://localhost:8080"), "/"),
		ListenAddr:                get("LISTEN_ADDR", ":8080"),
		Profile:                   get("PROFILE", "sovereign"),
		SMTPHost:                  os.Getenv("SMTP_HOST"),
		SMTPPort:                  intEnv("SMTP_PORT", 587),
		SMTPUser:                  os.Getenv("SMTP_USER"),
		SMTPPass:                  os.Getenv("SMTP_PASS"),
		SMTPFrom:                  get("SMTP_FROM", "chargeback@localhost"),
		HuaweiEndpointTemplate:    get("HUAWEI_ENDPOINT_TEMPLATE", "https://%s.%s.kom4dc.nationalcloud.om"),
		HuaweiInsecureTLS:         boolEnv("HUAWEI_INSECURE_TLS", true),
		CollectInterval:           durEnv("COLLECT_INTERVAL", 15*time.Minute),
		CTSPollInterval:           durEnv("CTS_POLL_INTERVAL", 5*time.Minute),
		CESInterval:               durEnv("CES_INTERVAL", time.Hour),
		CollectorEnabled:          boolEnv("COLLECTOR_ENABLED", true),
		AdapterEnabled:            strings.ToLower(strings.TrimSpace(os.Getenv("ADAPTER_ENABLED"))),
		BillingHookURL:            strings.TrimRight(strings.TrimSpace(os.Getenv("BILLING_HOOK_URL")), "/"),
		BillingHookToken:          strings.TrimSpace(os.Getenv("BILLING_HOOK_TOKEN")),
		BillingHookCallbackSecret: strings.TrimSpace(os.Getenv("BILLING_HOOK_CALLBACK_SECRET")),
		CommercialExportDir:       strings.TrimSpace(os.Getenv("COMMERCIAL_EXPORT_DIR")),
		CommercialImportSecret:    strings.TrimSpace(os.Getenv("COMMERCIAL_IMPORT_SECRET")),
		CommercialImportDir:       strings.TrimSpace(os.Getenv("COMMERCIAL_IMPORT_DIR")),
		PlatformAPIURL:            strings.TrimRight(strings.TrimSpace(os.Getenv("PLATFORM_API_URL")), "/"),
		PlatformAPIToken:          strings.TrimSpace(os.Getenv("PLATFORM_API_TOKEN")),
		PlatformAPITokenFile:      strings.TrimSpace(os.Getenv("PLATFORM_API_TOKEN_FILE")),
		TrustedForwardAuthHeader: http.CanonicalHeaderKey(
			strings.TrimSpace(os.Getenv("TRUSTED_FORWARD_AUTH_HEADER"))),
		TrustedForwardGroupsHeader: http.CanonicalHeaderKey(get("TRUSTED_FORWARD_GROUPS_HEADER", "X-Forwarded-Groups")),
	}
	if c.Profile != "sovereign" && c.Profile != "operator-central" {
		return c, fmt.Errorf("PROFILE must be sovereign or operator-central, got %q", c.Profile)
	}
	if strings.Count(c.HuaweiEndpointTemplate, "%s") != 2 {
		return c, fmt.Errorf("HUAWEI_ENDPOINT_TEMPLATE must contain two %%s placeholders (service, region)")
	}
	return c, nil
}

// IsOperator reports whether an email is in OPERATOR_EMAILS (case-insensitive).
func (c Config) IsOperator(email string) bool {
	e := strings.ToLower(strings.TrimSpace(email))
	for _, o := range c.OperatorEmails {
		if o == e {
			return true
		}
	}
	return false
}

func get(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if v := strings.ToLower(strings.TrimSpace(p)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func intEnv(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		slog.Warn("ignoring unusable integer env, using default", "env", key, "value", raw, "default", fallback)
		return fallback
	}
	return n
}

func boolEnv(key string, fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch raw {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	slog.Warn("ignoring unusable boolean env, using default", "env", key, "value", raw, "default", fallback)
	return fallback
}

func durEnv(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		slog.Warn("ignoring unusable duration env, using default", "env", key, "value", raw, "default", fallback)
		return fallback
	}
	return d
}
