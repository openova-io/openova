package main

import (
	"context"
	"log/slog"
	"os"
	"sync"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/lifecycle"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// defaultDeploymentsDir is the deployment store's directory when
// CATALYST_DEPLOYMENTS_DIR is unset: the default of handler.New, which
// restores the records from it (the chart sets the variable).
const defaultDeploymentsDir = "/var/lib/catalyst/deployments"

// storeRecords reads the deployment store for the network registry (#7938).
type storeRecords struct{ st *store.Store }

func registryRecord(rec store.Record) lifecycle.DeploymentRecord {
	return lifecycle.DeploymentRecord{ID: rec.ID, Status: rec.Status, OwnerEmail: rec.OwnerEmail}
}

func (s storeRecords) Load(id string) (lifecycle.DeploymentRecord, error) {
	rec, err := s.st.Load(id)
	return registryRecord(rec), err
}

func (s storeRecords) LoadAll() ([]lifecycle.DeploymentRecord, error) {
	recs, err := s.st.LoadAll(nil)
	out := make([]lifecycle.DeploymentRecord, 0, len(recs))
	for _, rec := range recs {
		out = append(out, registryRecord(rec))
	}
	return out, err
}

// newNetworkRegistry gives the network registry the parent's deployment
// store (opened once, at first use), the cluster cache of h and the OpenTofu
// work directory of the provisioner.
func newNetworkRegistry(log *slog.Logger, h *handler.Handler) *lifecycle.NetworkRegistry {
	var once sync.Once
	var records lifecycle.DeploymentRecords
	var openErr error
	return &lifecycle.NetworkRegistry{
		Log: log,
		Records: func() (lifecycle.DeploymentRecords, error) {
			once.Do(func() {
				dir := os.Getenv("CATALYST_DEPLOYMENTS_DIR")
				if dir == "" {
					dir = defaultDeploymentsDir
				}
				st, err := store.New(dir)
				records, openErr = storeRecords{st: st}, err
			})
			return records, openErr
		},
		Clusters: func() lifecycle.Clusters {
			if f := h.K8sCache(); f != nil {
				return f
			}
			return nil
		},
		StateDir: func() string { return provisioner.New().WorkDir },
	}
}

// setK8sCache wires the cluster cache into the handler, then starts the
// network registry's start pass: from here on the parent holds both its
// restored records and the cache (#7938).
func setK8sCache(log *slog.Logger, h *handler.Handler, f *k8scache.Factory, sar *k8scache.SARCache, userHeader string) {
	h.SetK8sCache(f, sar, userHeader)
	go newNetworkRegistry(log, h).ReconcileReady(context.Background())
}
