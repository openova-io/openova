package main

// placement_webhook.go — #3969: serves the placement ValidatingWebhook.
//
// The validating webhook (admission.PlacementWebhook) rejects an invalid
// desired-state placement (multi-primary on a primary+standby Blueprint, a
// Standby with no type, no Primary…) at the apiserver — synchronously at
// `kubectl apply` / `POST /apps`, not silently at reconcile. This file wires
// the pure handler to a real Blueprint capability resolver (a dynamic GET) +
// TLS serving inside the application-controller process.
//
// It is GATED behind PLACEMENT_WEBHOOK_ADDR: empty (the default) means the
// webhook server is NOT started, so existing deployments are byte-unchanged
// until a Sovereign opts in by setting the addr + cert paths and applying the
// ValidatingWebhookConfiguration. This keeps the change additive.
//
// Ref #3969 §7.3.

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/core/controllers/application/admission"
	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// blueprintCapabilityResolver resolves a Blueprint's spec.placementCapability
// via a dynamic GET, trying the v1 GVR then the v1alpha1 fallback (mirroring
// the controller's dual-version Blueprint lookup). It implements
// admission.CapabilityResolver.
type blueprintCapabilityResolver struct {
	dyn dynamic.Interface
}

var (
	bpGVRv1 = schema.GroupVersionResource{
		Group:    "catalyst.openova.io",
		Version:  "v1",
		Resource: "blueprints",
	}
	bpGVRv1alpha1 = schema.GroupVersionResource{
		Group:    "catalyst.openova.io",
		Version:  "v1alpha1",
		Resource: "blueprints",
	}
)

// CapabilityFor reads spec.placementCapability off the named Blueprint. A
// missing field / unrecognised value folds to the safe primary+standby default
// (NormalizeCapability). A Blueprint that cannot be fetched at all returns an
// error so the webhook fails CLOSED (an un-validatable placement is rejected),
// which is the correct conservative behaviour for the capability gate.
func (b blueprintCapabilityResolver) CapabilityFor(ctx context.Context, blueprintRef string) (bpv1alpha1.PlacementCapability, error) {
	bp, err := b.dyn.Resource(bpGVRv1).Namespace("").Get(ctx, blueprintRef, metav1.GetOptions{})
	if err != nil {
		bp, err = b.dyn.Resource(bpGVRv1alpha1).Namespace("").Get(ctx, blueprintRef, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
	}
	s, _, _ := unstructured.NestedString(bp.Object, "spec", "placementCapability")
	return bpv1alpha1.NormalizeCapability(bpv1alpha1.PlacementCapability(s)), nil
}

// maybeRunPlacementWebhook starts the placement ValidatingWebhook server when
// PLACEMENT_WEBHOOK_ADDR is set. It serves `/validate-placement` over TLS using
// PLACEMENT_WEBHOOK_CERT + PLACEMENT_WEBHOOK_KEY. When the addr is empty the
// webhook is disabled (no-op) — the additive default.
func maybeRunPlacementWebhook(ctx context.Context, dyn dynamic.Interface, logger *slog.Logger) {
	addr := os.Getenv("PLACEMENT_WEBHOOK_ADDR")
	if addr == "" {
		logger.Info("placement webhook disabled (set PLACEMENT_WEBHOOK_ADDR to enable)")
		return
	}
	certFile := envOr("PLACEMENT_WEBHOOK_CERT", "/etc/webhook/tls/tls.crt")
	keyFile := envOr("PLACEMENT_WEBHOOK_KEY", "/etc/webhook/tls/tls.key")

	wh := &admission.PlacementWebhook{
		Resolver: blueprintCapabilityResolver{dyn: dyn},
	}
	mux := http.NewServeMux()
	mux.Handle("/validate-placement", wh)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		logger.Info("placement webhook listening", "addr", addr, "path", "/validate-placement")
		if err := srv.ListenAndServeTLS(certFile, keyFile); err != nil && err != http.ErrServerClosed {
			logger.Error("placement webhook server exited", "err", err)
		}
	}()
}

// envOr returns the env value or a default.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
