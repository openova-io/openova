// Command blueprint-controller — slice C3 of EPIC-0 (#1095).
//
// Watches Blueprint.catalyst.openova.io/v1 + v1alpha1 CRs cluster-wide,
// validates them against the business-logic checks not expressible in
// the CRD's openAPIV3Schema, mirrors them to the Sovereign-local
// `catalog` Gitea Org per docs/NAMING-CONVENTION.md §11.2, and updates
// each CR's status with phase + conditions.
//
// Wire-up at deploy time:
//
//   - Runs on the management cluster (`hz-nbg-mgt-prod` post-Phase-0;
//     `ct-eu-mgt-prod` until then) per
//     docs/EPICS-1-6-unified-design.md §3.3 ("Where it runs: mgmt
//     cluster").
//   - Reads kubeconfig from in-cluster ServiceAccount; ClusterRole
//     scope is get/list/watch on Blueprints + update on
//     Blueprint.status (subresource).
//   - Gitea endpoint configured via CATALYST_GITEA_URL and
//     CATALYST_GITEA_TOKEN env vars.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 every value is runtime-
// configurable; the binary hard-codes nothing region-specific.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/openova-io/openova/core/controllers/blueprint/internal/controller"
	"github.com/openova-io/openova/core/controllers/internal/gitea"
)

func main() {
	logLevel := slog.LevelInfo
	if strings.EqualFold(os.Getenv("LOG_LEVEL"), "debug") {
		logLevel = slog.LevelDebug
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(log)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, log); err != nil {
		log.Error("blueprint-controller exited with error", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger) error {
	cfg, err := loadKubeConfig()
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}

	giteaURL := os.Getenv("CATALYST_GITEA_URL")
	giteaToken := os.Getenv("CATALYST_GITEA_TOKEN")
	var giteaClient *gitea.Client
	switch {
	case giteaURL == "":
		log.Warn("CATALYST_GITEA_URL is empty; mirror writes are DISABLED — controller will validate + update status only")
	case giteaToken == "":
		log.Warn("CATALYST_GITEA_TOKEN is empty; mirror writes are DISABLED — controller will validate + update status only")
	default:
		giteaClient = gitea.New(giteaURL, giteaToken)
		log.Info("Gitea mirror enabled", "url", giteaURL)
	}

	resync := durationFromEnv("RESYNC_PERIOD", 5*time.Minute)

	r := controller.New(controller.Config{
		DynamicClient: dyn,
		Gitea:         giteaClient,
		Log:           log,
		ResyncPeriod:  resync,
	})

	log.Info("blueprint-controller starting",
		"blueprint_gvr", controller.BlueprintGVR.String(),
		"resync", resync,
	)
	return r.Run(ctx)
}

// loadKubeConfig prefers in-cluster config; falls back to KUBECONFIG
// or ~/.kube/config for local dev runs (rare — production deploys to
// a Pod with a ServiceAccount).
func loadKubeConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path := os.Getenv("KUBECONFIG"); path != "" {
		rules.ExplicitPath = path
	}
	overrides := &clientcmd.ConfigOverrides{}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
}

// durationFromEnv parses a Go duration string from env; returns def
// on parse failure or empty value.
func durationFromEnv(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
