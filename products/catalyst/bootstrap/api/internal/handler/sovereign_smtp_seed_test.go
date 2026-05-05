// Tests for Sovereign-side SMTP credential seeding (issue #883).
//
// Coverage:
//   - mothership env unset → SkippedNoEnv outcome, no Secret created
//   - happy path → Created outcome, Secret data matches mothership env
//     bytes verbatim, Namespace pre-created, labels/annotations stamped
//   - already-exists pre-Create → AlreadyExists, no overwrite
//   - already-exists race during Create → AlreadyExists, no overwrite
//   - client-build failure (factory returns err) → ClientFailure
//   - api-failure on Get (non-NotFound) → APIFailure
//   - emit event matrix → every outcome maps to a non-empty SSE message
//
// Tests use kfake.NewSimpleClientset and inject via
// SetSovereignSMTPSeedClientFactory so no real cluster is needed.
package handler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	kfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// seedTestDeployment returns a *Deployment shaped for emit-event
// assertions. eventsCh is buffered so non-blocking sends in
// emitWatchEvent don't drop; eventsBuf is the durable buffer the
// test reads under dep.mu.
func seedTestDeployment(id string) *Deployment {
	return &Deployment{
		ID:        id,
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
	}
}

func setSeedSMTPEnv(t *testing.T, user, pass string) {
	t.Helper()
	t.Setenv("CATALYST_SMTP_USER", user)
	t.Setenv("CATALYST_SMTP_PASS", pass)
}

func unsetSeedSMTPEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CATALYST_SMTP_USER", "")
	t.Setenv("CATALYST_SMTP_PASS", "")
}

// TestSeedSovereignSMTPCredentials_SkippedNoEnv — mothership env
// vars empty → SkippedNoEnv, no factory call, no Secret created.
func TestSeedSovereignSMTPCredentials_SkippedNoEnv(t *testing.T) {
	unsetSeedSMTPEnv(t)

	factoryCalled := false
	h := &Handler{log: silentLogger()}
	h.SetSovereignSMTPSeedClientFactory(func(string) (kubernetes.Interface, error) {
		factoryCalled = true
		return nil, errors.New("should not be called")
	})

	dep := seedTestDeployment("dep-noenv")
	outcome := h.seedSovereignSMTPCredentials(context.Background(), dep, "ignored-kubeconfig")

	if outcome != SovereignSMTPSeedOutcomeSkippedNoEnv {
		t.Errorf("outcome = %q, want %q", outcome, SovereignSMTPSeedOutcomeSkippedNoEnv)
	}
	if factoryCalled {
		t.Errorf("client factory must not be called when env is unset")
	}
}

// TestSeedSovereignSMTPCredentials_HappyPath — env present, no
// existing Secret → Created. Secret + Namespace exist on the fake
// clientset with the expected data, labels, and annotations.
func TestSeedSovereignSMTPCredentials_HappyPath(t *testing.T) {
	setSeedSMTPEnv(t, "noreply@openova.io", "p455w0rd-bytes-here-not-real")

	core := kfake.NewSimpleClientset()
	h := &Handler{log: silentLogger()}
	h.SetSovereignSMTPSeedClientFactory(func(string) (kubernetes.Interface, error) {
		return core, nil
	})

	dep := seedTestDeployment("dep-happy")
	outcome := h.seedSovereignSMTPCredentials(context.Background(), dep, "kubeconfig-yaml-bytes")

	if outcome != SovereignSMTPSeedOutcomeCreated {
		t.Fatalf("outcome = %q, want %q", outcome, SovereignSMTPSeedOutcomeCreated)
	}

	ns, err := core.CoreV1().Namespaces().Get(context.Background(), sovereignSMTPSeedNamespace, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Namespace not created: %v", err)
	}
	if ns.Name != sovereignSMTPSeedNamespace {
		t.Errorf("Namespace name = %q, want %q", ns.Name, sovereignSMTPSeedNamespace)
	}

	got, err := core.CoreV1().Secrets(sovereignSMTPSeedNamespace).Get(context.Background(), sovereignSMTPSeedSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Secret not created: %v", err)
	}
	if string(got.Data["smtp-user"]) != "noreply@openova.io" {
		t.Errorf("smtp-user data length = %d, want %d", len(got.Data["smtp-user"]), len("noreply@openova.io"))
	}
	if string(got.Data["smtp-pass"]) != "p455w0rd-bytes-here-not-real" {
		// Per principle #10, never print the password in logs. Length
		// comparison is enough to flag a regression without leaking.
		t.Errorf("smtp-pass data length = %d, want %d", len(got.Data["smtp-pass"]), len("p455w0rd-bytes-here-not-real"))
	}
	if got.Type != corev1.SecretTypeOpaque {
		t.Errorf("Secret type = %q, want %q", got.Type, corev1.SecretTypeOpaque)
	}
	if got.Labels["app.kubernetes.io/managed-by"] != "catalyst-api" {
		t.Errorf("missing managed-by label, got: %#v", got.Labels)
	}
	if got.Annotations["catalyst.openova.io/seeded-by-deployment-id"] != "dep-happy" {
		t.Errorf("seeded-by annotation = %q, want %q", got.Annotations["catalyst.openova.io/seeded-by-deployment-id"], "dep-happy")
	}
	if got.Annotations["catalyst.openova.io/seed-phase"] != "phase-1-mothership-relay" {
		t.Errorf("seed-phase annotation = %q, want %q", got.Annotations["catalyst.openova.io/seed-phase"], "phase-1-mothership-relay")
	}
}

// TestSeedSovereignSMTPCredentials_AlreadyExistsPreCreate — pre-existing
// Secret bytes are NOT overwritten; outcome is AlreadyExists.
func TestSeedSovereignSMTPCredentials_AlreadyExistsPreCreate(t *testing.T) {
	setSeedSMTPEnv(t, "mothership-user", "mothership-pass")

	preExisting := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sovereignSMTPSeedSecretName,
			Namespace: sovereignSMTPSeedNamespace,
		},
		Data: map[string][]byte{
			"smtp-user": []byte("operator-supplied-user"),
			"smtp-pass": []byte("operator-supplied-pass"),
		},
	}
	preNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: sovereignSMTPSeedNamespace}}
	core := kfake.NewSimpleClientset(preNS, preExisting)

	h := &Handler{log: silentLogger()}
	h.SetSovereignSMTPSeedClientFactory(func(string) (kubernetes.Interface, error) {
		return core, nil
	})

	dep := seedTestDeployment("dep-exists")
	outcome := h.seedSovereignSMTPCredentials(context.Background(), dep, "kubeconfig")

	if outcome != SovereignSMTPSeedOutcomeAlreadyExists {
		t.Errorf("outcome = %q, want %q", outcome, SovereignSMTPSeedOutcomeAlreadyExists)
	}

	got, err := core.CoreV1().Secrets(sovereignSMTPSeedNamespace).Get(context.Background(), sovereignSMTPSeedSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Secret unexpectedly missing: %v", err)
	}
	if string(got.Data["smtp-user"]) != "operator-supplied-user" {
		t.Errorf("smtp-user was overwritten, length = %d", len(got.Data["smtp-user"]))
	}
}

// TestSeedSovereignSMTPCredentials_RaceOnCreate — Get returns
// NotFound but Create races a parallel writer and returns
// AlreadyExists. Outcome is AlreadyExists; no error surfaced.
func TestSeedSovereignSMTPCredentials_RaceOnCreate(t *testing.T) {
	setSeedSMTPEnv(t, "u", "p")

	core := kfake.NewSimpleClientset()
	// Inject a reactor on Secrets create that returns AlreadyExists
	// the first time. The fake client's default Get returns
	// NotFound when no object exists, so we hit the create branch.
	core.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		gr := schema.GroupResource{Group: "", Resource: "secrets"}
		return true, nil, apierrors.NewAlreadyExists(gr, sovereignSMTPSeedSecretName)
	})

	h := &Handler{log: silentLogger()}
	h.SetSovereignSMTPSeedClientFactory(func(string) (kubernetes.Interface, error) {
		return core, nil
	})

	dep := seedTestDeployment("dep-race")
	outcome := h.seedSovereignSMTPCredentials(context.Background(), dep, "kubeconfig")

	if outcome != SovereignSMTPSeedOutcomeAlreadyExists {
		t.Errorf("outcome = %q, want %q", outcome, SovereignSMTPSeedOutcomeAlreadyExists)
	}
}

// TestSeedSovereignSMTPCredentials_ClientFailure — the kubeconfig
// parser returns an error → ClientFailure outcome, no Secret create
// attempted on any clientset.
func TestSeedSovereignSMTPCredentials_ClientFailure(t *testing.T) {
	setSeedSMTPEnv(t, "u", "p")

	h := &Handler{log: silentLogger()}
	h.SetSovereignSMTPSeedClientFactory(func(string) (kubernetes.Interface, error) {
		return nil, errors.New("malformed kubeconfig YAML")
	})

	dep := seedTestDeployment("dep-client-fail")
	outcome := h.seedSovereignSMTPCredentials(context.Background(), dep, "garbage")

	if outcome != SovereignSMTPSeedOutcomeClientFailure {
		t.Errorf("outcome = %q, want %q", outcome, SovereignSMTPSeedOutcomeClientFailure)
	}
}

// TestSeedSovereignSMTPCredentials_APIFailureOnGet — Get returns a
// non-NotFound error (e.g. RBAC drift, network blip) → APIFailure
// outcome, no overwrite of any pre-existing Secret.
func TestSeedSovereignSMTPCredentials_APIFailureOnGet(t *testing.T) {
	setSeedSMTPEnv(t, "u", "p")

	core := kfake.NewSimpleClientset()
	core.PrependReactor("get", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: secrets.get")
	})

	h := &Handler{log: silentLogger()}
	h.SetSovereignSMTPSeedClientFactory(func(string) (kubernetes.Interface, error) {
		return core, nil
	})

	dep := seedTestDeployment("dep-api-fail")
	outcome := h.seedSovereignSMTPCredentials(context.Background(), dep, "kubeconfig")

	if outcome != SovereignSMTPSeedOutcomeAPIFailure {
		t.Errorf("outcome = %q, want %q", outcome, SovereignSMTPSeedOutcomeAPIFailure)
	}
}

// TestEmitSovereignSMTPSeedEvent_MessageMatrix — every outcome maps
// to a non-empty SSE message with the right level. The message
// contract is what the wizard's reducer keys off.
func TestEmitSovereignSMTPSeedEvent_MessageMatrix(t *testing.T) {
	cases := []struct {
		outcome    SovereignSMTPSeedOutcome
		wantLevel  string
		wantSubstr string
	}{
		{SovereignSMTPSeedOutcomeCreated, "info", "created Secret catalyst-system/sovereign-smtp-credentials"},
		{SovereignSMTPSeedOutcomeAlreadyExists, "info", "already present"},
		{SovereignSMTPSeedOutcomeSkippedNoEnv, "warn", "SKIPPED"},
		{SovereignSMTPSeedOutcomeClientFailure, "warn", "build a Kubernetes client"},
		{SovereignSMTPSeedOutcomeAPIFailure, "warn", "talking to the new Sovereign"},
	}

	for _, tc := range cases {
		t.Run(string(tc.outcome), func(t *testing.T) {
			h := &Handler{log: silentLogger()}
			dep := seedTestDeployment("emit-" + string(tc.outcome))
			h.emitSovereignSMTPSeedEvent(dep, tc.outcome)

			dep.mu.Lock()
			defer dep.mu.Unlock()
			var found *provisioner.Event
			for i := range dep.eventsBuf {
				if dep.eventsBuf[i].Phase == sovereignSMTPSeedPhase {
					found = &dep.eventsBuf[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("no %s event in eventsBuf; got=%+v", sovereignSMTPSeedPhase, dep.eventsBuf)
			}
			if found.Level != tc.wantLevel {
				t.Errorf("level = %q, want %q", found.Level, tc.wantLevel)
			}
			if !strings.Contains(found.Message, tc.wantSubstr) {
				t.Errorf("message %q missing substring %q", found.Message, tc.wantSubstr)
			}
		})
	}
}
