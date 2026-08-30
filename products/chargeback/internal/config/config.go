// Package config reads the service configuration from the environment.
package config

import (
	"fmt"
	"log/slog"
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
}

// FromEnv builds the configuration; it fails only on values that would make
// the process unsafe to run (a malformed duration falls back with a warning).
func FromEnv() (Config, error) {
	c := Config{
		DatabaseURL:            get("DATABASE_URL", "postgres://chargeback:chargeback@localhost:5432/chargeback?sslmode=disable"),
		EncryptionKeyB64:       os.Getenv("APP_ENCRYPTION_KEY"),
		OperatorEmails:         splitList(os.Getenv("OPERATOR_EMAILS")),
		PublicURL:              strings.TrimRight(get("PUBLIC_URL", "http://localhost:8080"), "/"),
		ListenAddr:             get("LISTEN_ADDR", ":8080"),
		Profile:                get("PROFILE", "sovereign"),
		SMTPHost:               os.Getenv("SMTP_HOST"),
		SMTPPort:               intEnv("SMTP_PORT", 587),
		SMTPUser:               os.Getenv("SMTP_USER"),
		SMTPPass:               os.Getenv("SMTP_PASS"),
		SMTPFrom:               get("SMTP_FROM", "chargeback@localhost"),
		HuaweiEndpointTemplate: get("HUAWEI_ENDPOINT_TEMPLATE", "https://%s.%s.kom4dc.nationalcloud.om"),
		HuaweiInsecureTLS:      boolEnv("HUAWEI_INSECURE_TLS", true),
		CollectInterval:        durEnv("COLLECT_INTERVAL", 15*time.Minute),
		CTSPollInterval:        durEnv("CTS_POLL_INTERVAL", 5*time.Minute),
		CESInterval:            durEnv("CES_INTERVAL", time.Hour),
		CollectorEnabled:       boolEnv("COLLECTOR_ENABLED", true),
		AdapterEnabled:         strings.ToLower(strings.TrimSpace(os.Getenv("ADAPTER_ENABLED"))),
		BillingHookURL:         strings.TrimRight(strings.TrimSpace(os.Getenv("BILLING_HOOK_URL")), "/"),
		BillingHookToken:       strings.TrimSpace(os.Getenv("BILLING_HOOK_TOKEN")),
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
