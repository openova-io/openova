package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/openova-io/openova/core/services/shared/events"
)

// captureMailer records calls to Send so handler tests can assert on
// recipient / subject / body without talking to SMTP. It satisfies
// MailSender.
type captureMailer struct {
	mu   sync.Mutex
	sent []sentEmail
	fail bool
}

type sentEmail struct {
	To      string
	Subject string
	Body    string
}

func (c *captureMailer) Send(to, subject, body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return errTransient
	}
	c.sent = append(c.sent, sentEmail{To: to, Subject: subject, Body: body})
	return nil
}

var errTransient = &transientErr{}

type transientErr struct{}

func (transientErr) Error() string { return "transient SMTP failure" }

// handlerWithMocks returns a *Handler wired to in-memory fakes for
// every collaborator. The httptest.Servers it spins up must be closed
// by the caller.
func handlerWithMocks(t *testing.T) (*Handler, *captureMailer, *httptest.Server, *httptest.Server) {
	t.Helper()

	cap := &captureMailer{}

	tenantSvc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/tenant/admin/tenants/") {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/tenant/admin/tenants/")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":        id,
			"name":      "Acme Inc",
			"owner_id":  "user-1",
			"subdomain": "acme",
		})
	}))

	authSvc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/auth/admin/users/") {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user": map[string]any{
				"id":    "user-1",
				"email": "owner@acme.example",
				"name":  "Alice Owner",
			},
		})
	}))

	enricher := NewEnricher(tenantSvc.URL, authSvc.URL, []byte("test-secret"))

	h := &Handler{
		Mailer:   cap,
		Enricher: enricher,
	}
	return h, cap, tenantSvc, authSvc
}

// TestStartConsumer_DispatchTable exercises the switch statement in
// StartConsumer by driving a fake Subscriber. It confirms every event
// type the service cares about routes to some handler and produces
// (or correctly skips) an email.
func TestStartConsumer_DispatchTable(t *testing.T) {
	h, cap, tenantSvc, authSvc := handlerWithMocks(t)
	defer tenantSvc.Close()
	defer authSvc.Close()

	cases := []struct {
		name    string
		event   *events.Event
		wantTo  string
		wantSub string
	}{
		{
			name: "user.login first login sends welcome",
			event: mkEvent(t, "user.login", map[string]any{
				"email":    "new@example.com",
				"name":     "Newbie",
				"is_first": true,
			}),
			wantTo:  "new@example.com",
			wantSub: "Welcome to OpenOva SME",
		},
		{
			name: "user.login later login skips",
			event: mkEvent(t, "user.login", map[string]any{
				"email":    "repeat@example.com",
				"is_first": false,
			}),
		},
		{
			name: "payment.received sends receipt",
			event: mkEvent(t, "payment.received", map[string]any{
				"email":    "pay@example.com",
				"org_name": "PayCo",
				"amount":   2500,
			}),
			wantTo:  "pay@example.com",
			wantSub: "Payment confirmation",
		},
		{
			name: "provision.app_ready enriches and sends",
			event: mkEventTenant(t, "provision.app_ready", "tenant-abc", map[string]any{
				"app_slug": "ghost",
				"app_id":   "app-1",
				"action":   "install",
			}),
			wantTo:  "owner@acme.example",
			wantSub: "ghost is ready on Acme Inc",
		},
		{
			name: "provision.app_removed enriches and sends",
			event: mkEventTenant(t, "provision.app_removed", "tenant-abc", map[string]any{
				"app_slug": "wordpress",
				"action":   "uninstall",
			}),
			wantTo:  "owner@acme.example",
			wantSub: "wordpress uninstalled from Acme Inc",
		},
		{
			name: "provision.app_failed surfaces error",
			event: mkEventTenant(t, "provision.app_failed", "tenant-abc", map[string]any{
				"app_slug": "redis",
				"action":   "install",
				"error":    "pod crashed",
			}),
			wantTo:  "owner@acme.example",
			wantSub: "redis install failed on Acme Inc",
		},
		{
			name: "domain.registered sends reminder",
			event: mkEventTenant(t, "domain.registered", "tenant-abc", map[string]any{
				"domain":    "shop.acme.com",
				"tenant_id": "tenant-abc",
			}),
			wantTo:  "owner@acme.example",
			wantSub: "Domain added: shop.acme.com",
		},
		{
			name: "domain.verified sends confirmation",
			event: mkEventTenant(t, "domain.verified", "tenant-abc", map[string]any{
				"domain":    "shop.acme.com",
				"tenant_id": "tenant-abc",
			}),
			wantTo:  "owner@acme.example",
			wantSub: "Domain verified: shop.acme.com",
		},
		{
			name:  "unknown type is silently skipped",
			event: mkEvent(t, "something.weird", map[string]any{}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cap.mu.Lock()
			cap.sent = nil
			cap.mu.Unlock()
			sub := &scriptedSubscriber{events: []*events.Event{tc.event}}
			if err := h.StartConsumer(context.Background(), sub); err != nil && err != errStopIteration {
				t.Fatalf("unexpected err: %v", err)
			}
			cap.mu.Lock()
			defer cap.mu.Unlock()
			if tc.wantTo == "" {
				if len(cap.sent) != 0 {
					t.Fatalf("expected no email, got %+v", cap.sent)
				}
				return
			}
			if len(cap.sent) != 1 {
				t.Fatalf("expected 1 email, got %d: %+v", len(cap.sent), cap.sent)
			}
			got := cap.sent[0]
			if got.To != tc.wantTo {
				t.Fatalf("recipient: want %q, got %q", tc.wantTo, got.To)
			}
			if got.Subject != tc.wantSub {
				t.Fatalf("subject: want %q, got %q", tc.wantSub, got.Subject)
			}
		})
	}
}

// TestHandler_AppReadyWithoutEnricher ensures day-2 events degrade
// gracefully when TENANT_URL/AUTH_URL are unset — we must not error
// (which would push the record into the DLQ) when enrichment is
// disabled by configuration.
func TestHandler_AppReadyWithoutEnricher(t *testing.T) {
	h := &Handler{Mailer: &captureMailer{}, Enricher: nil}
	evt := mkEventTenant(t, "provision.app_ready", "tenant-xyz", map[string]any{"app_slug": "ghost"})
	sub := &scriptedSubscriber{events: []*events.Event{evt}}
	if err := h.StartConsumer(context.Background(), sub); err != nil && err != errStopIteration {
		t.Fatalf("unexpected err: %v", err)
	}
}

// TestHandler_DomainEventMissingField ensures a malformed domain event
// (missing `domain`) returns an error so it enters the retry → DLQ
// flow rather than silently vanishing.
func TestHandler_DomainEventMissingField(t *testing.T) {
	h, _, tenantSvc, authSvc := handlerWithMocks(t)
	defer tenantSvc.Close()
	defer authSvc.Close()

	evt := mkEventTenant(t, "domain.registered", "tenant-abc", map[string]any{})
	err := h.handleDomainRegistered(context.Background(), evt)
	if err == nil {
		t.Fatal("expected error for missing domain field")
	}
}

// scriptedSubscriber feeds a fixed slice of events to StartConsumer
// and returns errStopIteration when exhausted.
type scriptedSubscriber struct {
	events []*events.Event
}

func (s *scriptedSubscriber) Subscribe(_ context.Context, handler func(*events.Event) error) error {
	for _, evt := range s.events {
		_ = handler(evt)
	}
	return errStopIteration
}

var errStopIteration = &stopErr{}

type stopErr struct{}

func (stopErr) Error() string { return "test: subscriber exhausted" }

func mkEvent(t *testing.T, eventType string, data any) *events.Event {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return &events.Event{ID: "evt-" + eventType, Type: eventType, Data: raw}
}

func mkEventTenant(t *testing.T, eventType, tenantID string, data any) *events.Event {
	t.Helper()
	evt := mkEvent(t, eventType, data)
	evt.TenantID = tenantID
	return evt
}
