// machine-controller runs the controllers of the machine plane in one process.
//
// Flag --controllers names the controllers to run, comma-separated. "inventory" reads
// every MachineSource and keeps one Machine object per machine the source lists.
// "operations" creates the MachineRuns of every ScanPolicy's schedule and brings each
// run to its end with one machine-runner Job per target; it needs the flags
// --job-namespace, --runner-image, --evidence-endpoint, --evidence-region and
// --evidence-bucket, and reads the evidence bucket with the object-storage binding
// mounted at /var/run/secrets/evidence-store. At each time of --rematch-schedule it
// creates one rematch run per ScanPolicy with the profile vulnerability; the match Jobs
// fetch the vulnerability database from --vulnerability-db-repository, or from Trivy's
// own repositories when that flag is empty. Leader election uses the Lease
// machine-controller-<names> in the namespace of the pod, so that one replica works at a
// time. Health is served on :8081 (/healthz and /readyz); metrics are off.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/inventory"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/operations"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runner"
)

// known lists the controllers this binary can run.
var known = map[string]bool{"inventory": true, "operations": true}

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
			return nil, fmt.Errorf("--controllers %q: %q is not a controller of this binary (known: inventory, operations)", value, name)
		case seen[name]:
			return nil, fmt.Errorf("--controllers %q: %q is named twice", value, name)
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// checkSettings refuses the settings of the operations controller when a required flag
// is empty, the endpoint is not https or the retention is not above zero.
func checkSettings(s operations.Settings) error {
	for _, f := range [][2]string{
		{"--job-namespace", s.JobNamespace}, {"--runner-image", s.RunnerImage}, {"--evidence-endpoint", s.EvidenceEndpoint},
		{"--evidence-region", s.EvidenceRegion}, {"--evidence-bucket", s.EvidenceBucket},
	} {
		if f[1] == "" {
			return fmt.Errorf("the controller operations needs %s", f[0])
		}
	}
	if !strings.HasPrefix(s.EvidenceEndpoint, "https://") {
		return fmt.Errorf("--evidence-endpoint %q must start with https://", s.EvidenceEndpoint)
	}
	if s.Retention <= 0 {
		return fmt.Errorf("--runs-retention must be above zero")
	}
	if s.VulnerabilityDB != "" && !runner.ValidRepository(s.VulnerabilityDB) {
		return fmt.Errorf("--vulnerability-db-repository %q is not an OCI repository such as registry.example/trivy-db:2", s.VulnerabilityDB)
	}
	return nil
}

// parseRematch reads --rematch-schedule: a five-field cron expression in UTC that has a
// time in the next five years.
func parseRematch(text string, now time.Time) (operations.Schedule, error) {
	sched, err := operations.ParseSchedule(text)
	if err == nil && sched.Next(now).IsZero() {
		err = fmt.Errorf("schedule %q has no time in the next five years", text)
	}
	if err != nil {
		return operations.Schedule{}, fmt.Errorf("--rematch-schedule: %w", err)
	}
	return sched, nil
}

func run(args []string) error {
	flags := flag.NewFlagSet("machine-controller", flag.ContinueOnError)
	value := flags.String("controllers", "", "controllers to run, comma-separated; known: inventory, operations")
	var s operations.Settings
	flags.StringVar(&s.JobNamespace, "job-namespace", "", "operations: the namespace of the runner Jobs, the controller's own")
	flags.StringVar(&s.RunnerImage, "runner-image", "", "operations: the machine-runner image, by digest")
	flags.StringVar(&s.EvidenceEndpoint, "evidence-endpoint", "", "operations: the S3 endpoint of the evidence bucket, https://host[:port]")
	flags.StringVar(&s.EvidenceRegion, "evidence-region", "", "operations: the region of the evidence endpoint")
	flags.StringVar(&s.EvidenceBucket, "evidence-bucket", "", "operations: the evidence bucket")
	flags.DurationVar(&s.Retention, "runs-retention", 720*time.Hour, "operations: how long a finished MachineRun is kept")
	flags.StringVar(&s.VulnerabilityDB, "vulnerability-db-repository", "", "operations: the OCI repository of the vulnerability database; empty: Trivy's own repositories")
	rematchText := flags.String("rematch-schedule", operations.DefaultRematchSchedule, "operations: when the daily rematch runs are created, five-field cron in UTC")
	if err := flags.Parse(args); err != nil {
		return err
	}
	names, err := parseControllers(*value)
	if err != nil {
		return err
	}
	withOperations := false
	for _, name := range names {
		withOperations = withOperations || name == "operations"
	}
	var rematch operations.Schedule
	if withOperations {
		if err := checkSettings(s); err != nil {
			return err
		}
		if rematch, err = parseRematch(*rematchText, time.Now()); err != nil {
			return err
		}
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
	options := ctrl.Options{
		Scheme:                 scheme,
		LeaderElection:         true,
		LeaderElectionID:       "machine-controller-" + strings.Join(names, "-"),
		HealthProbeBindAddress: ":8081",
		Metrics:                metricsserver.Options{BindAddress: "0"},
	}
	if withOperations {
		// The controller may read Jobs in its own namespace only, so the Job watch is held to it.
		options.Cache = cache.Options{ByObject: map[client.Object]cache.ByObject{
			&batchv1.Job{}: {Namespaces: map[string]cache.Config{s.JobNamespace: {}}},
		}}
	}
	mgr, err := ctrl.NewManager(cfg, options)
	if err != nil {
		return err
	}
	for _, name := range names {
		switch name {
		case "inventory":
			r := &inventory.Reconciler{Client: mgr.GetClient(), Reader: mgr.GetClient()}
			if err := r.SetupWithManager(mgr); err != nil {
				return fmt.Errorf("set up the inventory controller: %w", err)
			}
		case "operations":
			scheduler := &operations.Scheduler{Client: mgr.GetClient()}
			if err := scheduler.SetupWithManager(mgr); err != nil {
				return fmt.Errorf("set up the scan schedule controller: %w", err)
			}
			store := operations.BucketStore{Endpoint: s.EvidenceEndpoint, Region: s.EvidenceRegion, Bucket: s.EvidenceBucket, BindingDir: operations.EvidenceMountPath}
			runs := &operations.RunReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Store: store, Settings: s}
			if err := runs.SetupWithManager(mgr); err != nil {
				return fmt.Errorf("set up the machine run controller: %w", err)
			}
			if err := mgr.Add(&operations.Rematcher{Client: mgr.GetClient(), Schedule: rematch, Text: *rematchText}); err != nil {
				return fmt.Errorf("set up the rematch timer: %w", err)
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
