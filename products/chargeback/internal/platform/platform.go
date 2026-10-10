// Package platform is the chargeback → platform seam for ENFORCEMENT
// (DESIGN.md §9.6): suspending an Organization when collections escalate or
// a prepaid balance reaches zero, and resuming it when the account is
// settled.
//
// The Organization sync (internal/adapter/openova) runs the other way — CR →
// customer — and the platform had no reverse door: nothing let a billing
// decision pause an Organization. The sovereign-admin API now has the
// narrowest one, `POST /api/v1/internal/organizations/{slug}/suspend` and
// `.../resume`, which stamp `spec.suspended` on the Organization CR; the
// org-controller parks the per-Org Flux reconciliation and surfaces a
// Suspended condition. This client is what calls it.
//
// Those routes are ServiceAccount-authenticated: the bearer is a projected
// ServiceAccount token the sovereign-admin API verifies with a TokenReview
// and checks against its allow-list (system:serviceaccount:chargeback:
// chargeback). The chart projects that token to a file and this client
// re-reads the file on every call — projected tokens rotate, and a bearer
// read once at start-up would begin failing an hour later.
//
// It is deliberately a seam: a Fake for tests, a Nop that only logs when no
// platform URL is configured, and an HTTP client for the real thing. Every
// call is audited by the caller, whatever the outcome.
package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Client suspends and resumes an Organization at the platform.
type Client interface {
	// SuspendOrganization asks the platform to suspend the Organization
	// named by slug, with the reason the Organization's owners will see.
	SuspendOrganization(ctx context.Context, slug, reason string) error
	// ResumeOrganization lifts a suspension this product asked for.
	ResumeOrganization(ctx context.Context, slug string) error
	// Name is what the audit entry records as the enforcement target.
	Name() string
}

// ErrNotConfigured is returned by Nop: no platform URL, so nothing can be
// suspended — the customer's own status still flips, and the audit says so.
var ErrNotConfigured = errors.New("no platform API is configured (PLATFORM_API_URL); the Organization was not suspended at the platform")

// Nop is the client wired when PLATFORM_API_URL is unset.
type Nop struct{}

func (Nop) SuspendOrganization(_ context.Context, slug, _ string) error {
	slog.Warn("platform enforcement: no platform API configured; Organization not suspended at the platform", "org", slug)
	return ErrNotConfigured
}

func (Nop) ResumeOrganization(_ context.Context, slug string) error {
	slog.Warn("platform enforcement: no platform API configured; Organization not resumed at the platform", "org", slug)
	return ErrNotConfigured
}

func (Nop) Name() string { return "none" }

// HTTP calls the sovereign-admin API.
type HTTP struct {
	// BaseURL is the sovereign-admin API base — in-cluster,
	// http://catalyst-api.catalyst-system.svc.cluster.local:8080.
	BaseURL string
	// TokenFile, when set, is read on EVERY call and its contents presented
	// as the bearer: the projected ServiceAccount token the chart mounts at
	// /var/run/secrets/platform-api/token, which the kubelet rotates.
	TokenFile string
	// Token is the literal bearer used when TokenFile is empty.
	Token  string
	Client *http.Client
}

// NewHTTP returns a client for the sovereign-admin API. tokenFile wins over
// token when both are given.
func NewHTTP(baseURL, token, tokenFile string) *HTTP {
	return &HTTP{
		BaseURL:   strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:     strings.TrimSpace(token),
		TokenFile: strings.TrimSpace(tokenFile),
	}
}

func (h *HTTP) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// bearer is the token to present now: the file's current contents when a
// file is configured (never cached — rotation is the point), else the
// literal. A configured file that cannot be read is an error, not a silent
// fall-back to a stale literal: the call would be refused anyway, and the
// audit should say why.
func (h *HTTP) bearer() (string, error) {
	if h.TokenFile == "" {
		return h.Token, nil
	}
	raw, err := os.ReadFile(h.TokenFile)
	if err != nil {
		return "", fmt.Errorf("platform enforcement: read the ServiceAccount token %s: %w", h.TokenFile, err)
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return "", fmt.Errorf("platform enforcement: the ServiceAccount token file %s is empty", h.TokenFile)
	}
	return tok, nil
}

func (h *HTTP) Name() string { return h.BaseURL }

func (h *HTTP) SuspendOrganization(ctx context.Context, slug, reason string) error {
	return h.post(ctx, slug, "suspend", map[string]string{"reason": reason})
}

func (h *HTTP) ResumeOrganization(ctx context.Context, slug string) error {
	return h.post(ctx, slug, "resume", map[string]string{})
}

func (h *HTTP) post(ctx context.Context, slug, action string, body any) error {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return errors.New("platform enforcement: the customer has no Organization slug")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	tok, err := h.bearer()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/api/v1/internal/organizations/%s/%s", h.BaseURL, slug, action), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := h.client().Do(req)
	if err != nil {
		return fmt.Errorf("platform %s %s: %w", action, slug, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("platform %s %s: answered %d: %s", action, slug, resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return nil
}

// Fake records every call, for tests. Err, when set, is returned by both
// methods so a test can prove the failure is audited and retried.
type Fake struct {
	mu    sync.Mutex
	Calls []FakeCall
	Err   error
}

// FakeCall is one recorded call.
type FakeCall struct {
	Action string
	Slug   string
	Reason string
	// Until is the instant an overage stop lasts to (overage-stop only).
	Until time.Time
}

func (f *Fake) SuspendOrganization(_ context.Context, slug, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, FakeCall{Action: "suspend", Slug: slug, Reason: reason})
	return f.Err
}

func (f *Fake) ResumeOrganization(_ context.Context, slug string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, FakeCall{Action: "resume", Slug: slug})
	return f.Err
}

func (f *Fake) Name() string { return "fake" }

// Recorded returns a copy of the calls so far.
func (f *Fake) Recorded() []FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeCall{}, f.Calls...)
}

// OverageClient holds a grow Organization at its package headline while its
// spend limit is reached (DESIGN.md §22.11), through the same
// ServiceAccount-authenticated door as suspension:
//
//	POST /api/v1/internal/organizations/{slug}/overage-stop    {"until": RFC3339, "reason": "..."}
//	POST /api/v1/internal/organizations/{slug}/overage-resume  {}
//
// which merge-patch spec.overageStoppedUntil (and spec.overageStopReason) on
// the Organization CR. The org-controller renders the ResourceQuota and the
// application storage quota as capped while now < spec.overageStoppedUntil,
// and requeues itself at that instant, so grow returns at the period start
// even if the resume never arrives.
type OverageClient interface {
	StopOverage(ctx context.Context, slug string, until time.Time, reason string) error
	ResumeOverage(ctx context.Context, slug string) error
}

func (Nop) StopOverage(_ context.Context, slug string, _ time.Time, _ string) error {
	slog.Warn("platform enforcement: no platform API configured; the spend limit is recorded but the Organization's quota is not lowered", "org", slug)
	return ErrNotConfigured
}

func (Nop) ResumeOverage(_ context.Context, slug string) error {
	slog.Warn("platform enforcement: no platform API configured; the Organization's grow quota is not restored at the platform", "org", slug)
	return ErrNotConfigured
}

func (h *HTTP) StopOverage(ctx context.Context, slug string, until time.Time, reason string) error {
	return h.post(ctx, slug, "overage-stop", map[string]string{"until": until.UTC().Format(time.RFC3339), "reason": reason})
}

func (h *HTTP) ResumeOverage(ctx context.Context, slug string) error {
	return h.post(ctx, slug, "overage-resume", map[string]string{})
}

func (f *Fake) StopOverage(_ context.Context, slug string, until time.Time, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, FakeCall{Action: "overage-stop", Slug: slug, Reason: reason, Until: until.UTC()})
	return f.Err
}

func (f *Fake) ResumeOverage(_ context.Context, slug string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, FakeCall{Action: "overage-resume", Slug: slug})
	return f.Err
}
