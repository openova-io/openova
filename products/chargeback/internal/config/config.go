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

	// The daily cost rollup (DESIGN.md §20, #6926). CostRollupEnabled is the
	// kill switch for BOTH halves — with it off nothing is built and no
	// window is served from the rollup, so every read is rated over the
	// hourly usage ledger. The answers do not change; §20.8 measures what
	// the cache is worth. CostRollupInterval is how often the builder looks
	// for partitions a usage write has marked.
	CostRollupEnabled  bool
	CostRollupInterval time.Duration

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
	//
	// The file's env is PLATFORM_API_BEARER_FILE. It was
	// PLATFORM_API_TOKEN_FILE through chart 0.1.32; a path is not a secret,
	// but the Sovereign's Kyverno `secret-not-in-env` policy flags any env
	// whose NAME matches `(?i)(PASSWORD|TOKEN|KEY|SECRET)` and carries a
	// literal value, so the chart renders the new name and the old one is
	// read as a deprecated alias for one release (the new name wins).
	PlatformAPIURL       string
	PlatformAPIToken     string
	PlatformAPITokenFile string

	// DocRenderURL reaches the document renderer (EPIC #6867), the
	// stateless in-cluster service that turns a statement into a PDF:
	// http://<release>-docrender.<namespace>.svc.cluster.local:8080. Unset ⇒
	// the feature is OFF and GET /api/v1/statements/{id}.pdf answers 503;
	// every other surface behaves exactly as it did.
	//
	// DocRenderToken is the optional shared secret sent as X-Render-Token.
	// It is defence in depth on top of the renderer's NetworkPolicy, which
	// admits these pods and nothing else — never the perimeter by itself.
	DocRenderURL   string
	DocRenderToken string

	// E-invoicing (DESIGN.md §17). EInvoiceProfile is EINVOICE_PROFILE:
	// empty (the default) = OFF, and nothing about issuing changes. "oman"
	// builds, validates, signs and archives an e-invoice at issue.
	//
	// The signing key comes from a mounted Secret. EInvoiceSigningKeyFile
	// names the FILE (EINVOICE_SIGNER_PATH, e.g.
	// /var/run/secrets/einvoice/tls.key) and is read once at start-up;
	// EInvoiceSigningKey is the literal PEM for a local run where no file
	// exists. The FILE wins when both are set. Neither is ever logged, and
	// the key is never written to the archive — the archive holds the
	// document and its hash.
	//
	// A path is not a secret, but the Sovereign's Kyverno
	// `secret-not-in-env` policy flags any env whose NAME matches
	// (?i)(PASSWORD|TOKEN|KEY|SECRET) carrying a LITERAL value. That match
	// is on a SUBSTRING, so a _FILE suffix does not save a name containing
	// KEY: EINVOICE_SIGNING_KEY_FILE is refused on a Sovereign exactly as
	// PLATFORM_API_TOKEN_FILE was. The chart therefore renders
	// EINVOICE_SIGNER_PATH / EINVOICE_SIGNER_ID — the same treatment as
	// catalyst-api's CATALYST_HANDOVER_SIGNER_PATH — and the binary reads
	// the old names as deprecated aliases for one release. The literal PEM
	// (EINVOICE_SIGNING_KEY) is never chart-rendered; it exists only for a
	// local run.
	EInvoiceProfile        string
	EInvoiceSigningKey     string
	EInvoiceSigningKeyFile string
	// EInvoiceKeyID names the key on the document so a rotation is
	// traceable. It is a NAME, never key material.
	EInvoiceKeyID string

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

	// The public calculator (DESIGN.md §11). PublicCalculatorOrigins are the
	// origins allowed to call /api/v1/public/* cross-origin and to frame the
	// /estimate page (the marketplace, a partner's site); empty (the default)
	// = same origin only and the page cannot be framed. "*" allows any.
	// PublicCalculatorRatePerMinute is the per-client-address budget on the
	// public routes (token bucket; 429 with Retry-After beyond it).
	PublicCalculatorOrigins       []string
	PublicCalculatorRatePerMinute int

	// NotificationRetention is how long the notification DELIVERY LOG is
	// kept (DESIGN.md §21.6). It is configuration rather than a constant
	// because how long a record of what was sent to whom may be held is a
	// data-retention decision the operator's own policy makes, not this
	// product's. The default is 180 days — long enough that "did they get
	// the August invoice" is answerable when it is asked in December. Zero
	// or negative disables the purge and keeps the log for ever.
	//
	// Nothing else about §21 is configurable, deliberately: the retry ladder
	// is bounded in code so no deployment can turn a failed delivery into an
	// unbounded loop, and the SMS channel has nothing to configure until a
	// gateway specification exists to configure it against.
	NotificationRetention time.Duration
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
		CostRollupEnabled:         boolEnv("COST_ROLLUP_ENABLED", true),
		CostRollupInterval:        durEnv("COST_ROLLUP_INTERVAL", time.Minute),
		NotificationRetention:     daysEnv("NOTIFICATION_RETENTION_DAYS", DefaultNotificationRetention),
		AdapterEnabled:            strings.ToLower(strings.TrimSpace(os.Getenv("ADAPTER_ENABLED"))),
		BillingHookURL:            strings.TrimRight(strings.TrimSpace(os.Getenv("BILLING_HOOK_URL")), "/"),
		BillingHookToken:          strings.TrimSpace(os.Getenv("BILLING_HOOK_TOKEN")),
		BillingHookCallbackSecret: strings.TrimSpace(os.Getenv("BILLING_HOOK_CALLBACK_SECRET")),
		CommercialExportDir:       strings.TrimSpace(os.Getenv("COMMERCIAL_EXPORT_DIR")),
		CommercialImportSecret:    strings.TrimSpace(os.Getenv("COMMERCIAL_IMPORT_SECRET")),
		CommercialImportDir:       strings.TrimSpace(os.Getenv("COMMERCIAL_IMPORT_DIR")),
		PlatformAPIURL:            strings.TrimRight(strings.TrimSpace(os.Getenv("PLATFORM_API_URL")), "/"),
		DocRenderURL:              strings.TrimRight(strings.TrimSpace(os.Getenv("DOCRENDER_URL")), "/"),
		DocRenderToken:            strings.TrimSpace(os.Getenv("DOCRENDER_TOKEN")),
		EInvoiceProfile:           strings.ToLower(strings.TrimSpace(os.Getenv("EINVOICE_PROFILE"))),
		EInvoiceSigningKey:        os.Getenv("EINVOICE_SIGNING_KEY"),
		// New names first; EINVOICE_SIGNING_KEY_FILE / EINVOICE_KEY_ID are
		// the deprecated aliases (see the field comment).
		EInvoiceSigningKeyFile: get("EINVOICE_SIGNER_PATH", strings.TrimSpace(os.Getenv("EINVOICE_SIGNING_KEY_FILE"))),
		EInvoiceKeyID:          get("EINVOICE_SIGNER_ID", strings.TrimSpace(os.Getenv("EINVOICE_KEY_ID"))),
		PlatformAPIToken:          strings.TrimSpace(os.Getenv("PLATFORM_API_TOKEN")),
		// New name first; PLATFORM_API_TOKEN_FILE is the deprecated alias
		// (see the field comment).
		PlatformAPITokenFile: get("PLATFORM_API_BEARER_FILE", strings.TrimSpace(os.Getenv("PLATFORM_API_TOKEN_FILE"))),
		TrustedForwardAuthHeader: http.CanonicalHeaderKey(
			strings.TrimSpace(os.Getenv("TRUSTED_FORWARD_AUTH_HEADER"))),
		TrustedForwardGroupsHeader:    http.CanonicalHeaderKey(get("TRUSTED_FORWARD_GROUPS_HEADER", "X-Forwarded-Groups")),
		PublicCalculatorOrigins:       splitList(os.Getenv("PUBLIC_CALCULATOR_ORIGINS")),
		PublicCalculatorRatePerMinute: intEnv("PUBLIC_CALCULATOR_RATE_PER_MINUTE", 60),
	}
	if c.PlatformAPITokenFile != "" && strings.TrimSpace(os.Getenv("PLATFORM_API_BEARER_FILE")) == "" {
		slog.Warn("PLATFORM_API_TOKEN_FILE is a deprecated alias read for one release only; set PLATFORM_API_BEARER_FILE to the same path")
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

// DefaultNotificationRetention is how long the notification delivery log is
// kept when NOTIFICATION_RETENTION_DAYS is unset (DESIGN.md §21.6).
const DefaultNotificationRetention = 180 * 24 * time.Hour

// daysEnv reads a whole number of DAYS. Days rather than a Go duration
// because a retention policy is written in days and "4320h" is not how
// anyone states one. A zero or negative value disables the purge, which is
// a deliberate setting and not a misconfiguration: some operators are
// required to keep the record indefinitely.
func daysEnv(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		slog.Warn("ignoring unusable day count, using default", "env", key, "value", raw, "default", fallback)
		return fallback
	}
	if n <= 0 {
		return 0
	}
	return time.Duration(n) * 24 * time.Hour
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
