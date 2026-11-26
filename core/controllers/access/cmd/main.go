// access-controller — the controller of the access kinds AccessTarget and
// AccessGrant (ADR-0033), installed by bp-guacamole in the primary region
// only (decision A21). The flags are rendered by
// platform/guacamole/chart/templates/controller-deployment.yaml.
//
// Port 8080 serves /healthz and /metrics, whether or not this copy holds the
// lease; the broker of a later package serves its routes on the same port.
package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	accessv1 "github.com/openova-io/openova/core/controllers/access/api/v1alpha1"
	"github.com/openova-io/openova/core/controllers/access/internal/controller"
	"github.com/openova-io/openova/core/controllers/access/internal/events"
	"github.com/openova-io/openova/core/controllers/pkg/natsbus"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(accessv1.AddToScheme(scheme))
}

// httpServer serves /healthz and /metrics on every copy, leader or not.
type httpServer struct{ srv *http.Server }

func (h httpServer) NeedLeaderElection() bool { return false }

func (h httpServer) Start(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.srv.Shutdown(shut)
	}()
	if err := h.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func main() {
	var (
		httpAddr, fqdn, region, natsURL, sessionNS, denied string
		leaderElect                                        bool
	)
	flag.StringVar(&httpAddr, "http-address", ":8080", "Address of /healthz and /metrics.")
	flag.StringVar(&fqdn, "sovereign-fqdn", "", "FQDN of the Sovereign (chart value access.sovereignFQDN).")
	flag.StringVar(&region, "region-id", "", "Id of this region (chart value access.regionID).")
	flag.StringVar(&natsURL, "nats-url", "", "NATS URL of the stream CATALYST_ORG (chart value access.nats.url).")
	flag.StringVar(&sessionNS, "session-namespace", "", "Namespace of the session pods of kind pod and of the lease: the release namespace.")
	flag.StringVar(&denied, "denied-target-namespaces", "", "Comma-separated namespaces a target of kind pod may not be in.")
	flag.BoolVar(&leaderElect, "leader-elect", true, "Hold a lease, so one copy reconciles.")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	source, err := events.Source(fqdn, region)
	if err == nil && (natsURL == "" || sessionNS == "") {
		err = errors.New("--nats-url and --session-namespace are required")
	}
	fatal(err, "invalid flags", 2)
	// Events are written ahead (decision A16): without the stream no grant
	// may change phase, so the controller does not start without it.
	pub, err := natsbus.NewPublisher(natsURL)
	fatal(err, "connect to NATS", 1)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                        scheme,
		Metrics:                       metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress:        "0",
		LeaderElection:                leaderElect,
		LeaderElectionID:              "access-controller.access.openova.io",
		LeaderElectionNamespace:       sessionNS,
		LeaderElectionReleaseOnCancel: true,
	})
	fatal(err, "create the manager", 1)
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	fatal(mgr.Add(httpServer{srv: &http.Server{Addr: httpAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}}), "add the HTTP server", 1)
	deniedList := []string{}
	for _, ns := range strings.Split(denied, ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			deniedList = append(deniedList, ns)
		}
	}
	fatal((&controller.TargetReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), SessionNamespace: sessionNS,
		DeniedNamespaces: deniedList}).SetupWithManager(mgr), "set up the target reconciler", 1)
	fatal((&controller.GrantReconciler{Client: mgr.GetClient(), Events: pub, Source: source, Now: time.Now}).SetupWithManager(mgr),
		"set up the grant reconciler", 1)
	setupLog.Info("starting access-controller", "source", source, "sessionNamespace", sessionNS, "deniedTargetNamespaces", deniedList)
	err = mgr.Start(ctrl.SetupSignalHandler())
	pub.Close()
	fatal(err, "manager exited", 1)
}

// fatal logs err and ends the process with code when err is not nil.
func fatal(err error, msg string, code int) {
	if err != nil {
		setupLog.Error(err, msg)
		os.Exit(code)
	}
}
