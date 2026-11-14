// machine-controller runs the controllers of the machine plane in one process.
//
// Flag --controllers names the controllers to run, comma-separated. This version
// knows one controller, "inventory": it reads every MachineSource and keeps one
// Machine object per machine the source lists. Leader election uses the Lease
// machine-controller-<names> in the namespace of the pod, so that one replica works
// at a time. Health is served on :8081 (/healthz and /readyz); metrics are off.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/inventory"
)

// known lists the controllers this binary can run.
var known = map[string]bool{"inventory": true}

// parseControllers reads the value of --controllers: one or more known names,
// comma-separated, each at most once. It returns them sorted.
func parseControllers(value string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, name := range strings.Split(value, ",") {
		name = strings.TrimSpace(name)
		switch {
		case name == "":
			return nil, fmt.Errorf("--controllers %q: a name is empty", value)
		case !known[name]:
			return nil, fmt.Errorf("--controllers %q: %q is not a controller of this binary (known: inventory)", value, name)
		case seen[name]:
			return nil, fmt.Errorf("--controllers %q: %q is named twice", value, name)
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func run(args []string) error {
	flags := flag.NewFlagSet("machine-controller", flag.ContinueOnError)
	value := flags.String("controllers", "", "controllers to run, comma-separated; known: inventory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	names, err := parseControllers(*value)
	if err != nil {
		return err
	}
	ctrl.SetLogger(logr.FromSlogHandler(slog.NewJSONHandler(os.Stderr, nil)))

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		LeaderElection:         true,
		LeaderElectionID:       "machine-controller-" + strings.Join(names, "-"),
		HealthProbeBindAddress: ":8081",
		Metrics:                metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == "inventory" {
			r := &inventory.Reconciler{Client: mgr.GetClient(), Reader: mgr.GetClient()}
			if err := r.SetupWithManager(mgr); err != nil {
				return fmt.Errorf("set up the inventory controller: %w", err)
			}
		}
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	return mgr.Start(ctrl.SetupSignalHandler())
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "machine-controller:", err)
		os.Exit(1)
	}
}
