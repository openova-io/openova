package external

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The inbound side: what the operator's billing system tells us. One
// document type per command, each arriving over its own HMAC-verified
// webhook (internal/api/commercial.go) or through the directory poller
// below for a system that batches instead of calling.
//
// Enforcement is the one command with a hard rule (DESIGN.md §9.7): we
// suspend or resume the Organization ONLY on an explicit imported command,
// never inferred from a payment status. A cloud escalation policy is written
// by us and executed by them; what reaches us is the decision.

// Command kinds the poller reads and the webhooks accept.
const (
	CmdInvoiceStatus  = "invoice-status"
	CmdPaymentStatus  = "payment-status"
	CmdAccountBalance = "account-balance"
	CmdEnforcement    = "enforcement"
)

// PaymentStatus is the TMF676 subset that matters: which account, how much,
// when, the reference that proves it, and — when the payment settled one of
// our exported invoices — which one. `status` takes the billing system's
// vocabulary.
type PaymentStatus struct {
	ExternalAccountID string        `json:"external_account_id"`
	CustomerSlug      string        `json:"customer_slug,omitempty"`
	Amount            store.Decimal `json:"amount"`
	PaidAt            string        `json:"paid_at"`
	Reference         string        `json:"reference"`
	Status            string        `json:"status"`
	// ExternalRef names the exported invoice the payment settles; empty is
	// credit on the account.
	ExternalRef string `json:"external_ref,omitempty"`
	// InvoiceNumber names OUR invoice (summary-charge mode).
	InvoiceNumber string `json:"invoice_number,omitempty"`
	Method        string `json:"method,omitempty"`
}

// AccountBalanceImport is the TMF666 balance as the billing system holds it.
type AccountBalanceImport struct {
	ExternalAccountID string        `json:"external_account_id"`
	CustomerSlug      string        `json:"customer_slug,omitempty"`
	Balance           store.Decimal `json:"balance"`
	Currency          string        `json:"currency,omitempty"`
	AsOf              string        `json:"as_of,omitempty"`
}

// Enforcement is the explicit suspend / resume command.
type Enforcement struct {
	ExternalAccountID string `json:"external_account_id"`
	CustomerSlug      string `json:"customer_slug,omitempty"`
	Action            string `json:"action"`
	Reason            string `json:"reason,omitempty"`
}

// MapPaymentStatus translates a billing system's payment status onto ours.
func MapPaymentStatus(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(strings.ReplaceAll(s, "_", "-"))) {
	case "", "settled", "succeeded", "success", "paid", "received", "completed", "captured", "cleared":
		return store.PaymentReceived, nil
	case "pending", "authorized", "authorised", "processing", "initiated":
		return store.PaymentPending, nil
	case "failed", "declined", "rejected", "cancelled", "canceled", "error":
		return store.PaymentFailed, nil
	case "refunded", "reversed", "chargeback", "charged-back":
		return store.PaymentRefunded, nil
	}
	return "", fmt.Errorf("%w: unknown payment status %q", store.ErrInvalid, s)
}

// MapEnforcement validates the action word.
func MapEnforcement(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "suspend", "suspended", "block", "barred", "bar":
		return "suspend", nil
	case "resume", "resumed", "unblock", "unbar", "reactivate", "active":
		return "resume", nil
	}
	return "", fmt.Errorf("%w: enforcement action must be suspend or resume, not %q", store.ErrInvalid, s)
}

// Command is one polled file: its kind and its body.
type Command struct {
	Kind string          `json:"kind"`
	Body json.RawMessage `json:"body"`
	// File is where it came from, for the log.
	File string `json:"-"`
}

// Applier applies each command kind; the webhook handlers implement it over
// the same Importer, so a polled command and a posted one take one path.
type Applier interface {
	ApplyCommand(ctx context.Context, cmd Command) error
}

// ErrPollerNotConfigured is returned when no import directory is set.
var ErrPollerNotConfigured = errors.New("no import directory is configured (COMMERCIAL_IMPORT_DIR)")

// DirectoryPoller is the polling fallback: it reads `*.json` command files
// from a directory the billing system drops them in, applies each through
// the Applier, and moves it to `processed/` (or `failed/` with the error
// beside it) so nothing is applied twice and nothing is lost.
type DirectoryPoller struct {
	Dir     string
	Applier Applier
}

// Poll applies every command file present and reports how many succeeded
// and failed. A failed file is moved aside with its error; the next poll
// does not retry it — a command the billing system sent once is applied
// once, and the operator sees why it was refused.
func (p *DirectoryPoller) Poll(ctx context.Context) (ok, failed int, err error) {
	if p == nil || strings.TrimSpace(p.Dir) == "" {
		return 0, 0, ErrPollerNotConfigured
	}
	if p.Applier == nil {
		return 0, 0, errors.New("no command applier")
	}
	entries, err := os.ReadDir(p.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(p.Dir, name)
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			failed++
			continue
		}
		var cmd Command
		aerr := json.Unmarshal(raw, &cmd)
		if aerr == nil {
			cmd.File = name
			aerr = p.Applier.ApplyCommand(ctx, cmd)
		}
		if aerr != nil {
			failed++
			p.moveAside(path, name, "failed", aerr)
			continue
		}
		ok++
		p.moveAside(path, name, "processed", nil)
	}
	return ok, failed, nil
}

func (p *DirectoryPoller) moveAside(path, name, sub string, cause error) {
	dir := filepath.Join(p.Dir, sub)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return
	}
	_ = os.Rename(path, filepath.Join(dir, name))
	if cause != nil {
		_ = os.WriteFile(filepath.Join(dir, name+".error"), []byte(cause.Error()+"\n"), 0o640)
	}
}
