package openova

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// BillingHook is the ADR-0014 D6 seam: after a statement is ISSUED for a
// customer that is a synced Organization with billing_mode=real, the total
// is posted to the billing service's existing metering endpoint
// (core/services/billing/handlers/metering.go) as a credit debit.
// Idempotent by construction: metadata.request_id — billing's external_ref —
// is the statement id, so re-posting the same statement records nothing
// twice (billing answers duplicate=true).
//
// Off when BILLING_HOOK_URL is unset (the handler is simply not wired).
// Chargeback never touches money beyond this call; billing never learns
// about cloud providers.
type BillingHook struct {
	URL     string // base URL of the billing service (no trailing slash)
	Token   string // BILLING_HOOK_TOKEN — a superadmin bearer token
	Client  *http.Client
	Metrics *metrics.Registry
}

func (b *BillingHook) client() *http.Client {
	if b.Client != nil {
		return b.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (b *BillingHook) metricsReg() *metrics.Registry {
	if b.Metrics != nil {
		return b.Metrics
	}
	return metrics.Default
}

// meteringPayload mirrors events.UsageRecordedPayload
// (core/services/shared/events/nats.go) — the exact shape
// POST /billing/metering/record decodes.
type meteringPayload struct {
	CustomerID     string           `json:"customer_id"`
	AmountMicroOMR int64            `json:"amount_micro_omr"`
	Reason         string           `json:"reason"`
	Metadata       meteringMetadata `json:"metadata"`
}

type meteringMetadata struct {
	RequestID string `json:"request_id"`
	TenantID  string `json:"tenant_id"`
}

// StatementIssued implements the api.StatementHook seam. Only issued
// statements of kind=organization, billing_mode=real customers reach
// billing; everything else is a silent no-op (D6 is adapter-only).
func (b *BillingHook) StatementIssued(ctx context.Context, st store.Statement, c store.Customer) error {
	if b == nil || b.URL == "" {
		return nil
	}
	if c.Kind != "organization" || c.BillingMode != "real" {
		return nil
	}
	micro, err := microOMR(st.Total)
	if err != nil {
		return fmt.Errorf("statement %s total %q: %w", st.ID, st.Total, err)
	}
	if micro <= 0 {
		// Billing refuses non-negative usage amounts; a zero statement has
		// nothing to debit.
		return nil
	}
	slug := c.Slug
	if c.OrgSlug != nil && *c.OrgSlug != "" {
		slug = *c.OrgSlug
	}
	period := st.PeriodStart
	if len(period) >= 7 {
		period = period[:7]
	}
	payload := meteringPayload{
		CustomerID:     slug,
		AmountMicroOMR: -micro,
		Reason:         "usage:chargeback:" + period,
		Metadata:       meteringMetadata{RequestID: st.ID, TenantID: slug},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.URL+"/billing/metering/record", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.Token)
	}
	resp, err := b.client().Do(req)
	if err != nil {
		b.metricsReg().Inc("chargeback_billing_hook_total", "Billing hook posts by result", map[string]string{"result": "error"}, 1)
		return fmt.Errorf("post metering record: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b.metricsReg().Inc("chargeback_billing_hook_total", "Billing hook posts by result", map[string]string{"result": "error"}, 1)
		return fmt.Errorf("billing answered %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	result := "ok"
	if dup := duplicateFlag(raw); dup {
		result = "duplicate"
	}
	b.metricsReg().Inc("chargeback_billing_hook_total", "Billing hook posts by result", map[string]string{"result": result}, 1)
	slog.Info("billing hook: statement debited", "statement", st.ID, "customer", slug, "period", period, "amount_micro_omr", -micro, "result", result)
	return nil
}

// duplicateFlag reads the `duplicate` field of billing's response, at the
// top level or nested one level (respond-wrapper tolerant).
func duplicateFlag(raw []byte) bool {
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return false
	}
	if d, ok := top["duplicate"].(bool); ok {
		return d
	}
	for _, v := range top {
		if m, ok := v.(map[string]any); ok {
			if d, ok := m["duplicate"].(bool); ok {
				return d
			}
		}
	}
	return false
}

// microOMR converts an exact decimal OMR amount into micro-OMR without
// passing through floating point (1 OMR = 1,000,000 micro-OMR). Statement
// totals carry at most 6 decimals, so nothing is lost.
func microOMR(d store.Decimal) (int64, error) {
	s := strings.TrimSpace(string(d))
	if s == "" {
		return 0, nil
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, frac, _ := strings.Cut(s, ".")
	if intPart == "" {
		intPart = "0"
	}
	if len(frac) > 6 {
		frac = frac[:6]
	}
	for len(frac) < 6 {
		frac += "0"
	}
	n, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, errors.New("not a decimal amount")
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, errors.New("not a decimal amount")
	}
	v := n*1_000_000 + f
	if neg {
		v = -v
	}
	return v, nil
}
