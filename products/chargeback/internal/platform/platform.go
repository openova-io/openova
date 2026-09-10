// Package platform is the chargeback → platform seam for ENFORCEMENT
// (DESIGN.md §9.7): suspending an Organization when collections escalate or
// a prepaid balance reaches zero, and resuming it when the account is
// settled.
//
// The Organization sync (internal/adapter/openova) runs the other way — CR →
// customer — and the platform had no reverse door: nothing let a billing
// decision pause an Organization. The sovereign-admin API now has the
// narrowest one, `POST /api/v1/organizations/{slug}/suspend` and
// `.../resume`, which stamp `spec.suspended` on the Organization CR; the
// org-controller parks the per-Org Flux reconciliation and surfaces a
// Suspended condition. This client is what calls it.
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
	// BaseURL is the sovereign-admin API base, e.g. https://console.t99.omani.works
	BaseURL string
	// Token is the bearer the operator-only routes accept.
	Token  string
	Client *http.Client
}

// NewHTTP returns a client for the sovereign-admin API.
func NewHTTP(baseURL, token string) *HTTP {
	return &HTTP{BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), Token: strings.TrimSpace(token)}
}

func (h *HTTP) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/api/v1/organizations/%s/%s", h.BaseURL, slug, action), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.Token != "" {
		req.Header.Set("Authorization", "Bearer "+h.Token)
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
