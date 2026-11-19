// The two triggers of the network registry after the hand-over (#7938).
//
// The hand-over's adoption step writes the registry once. A Sovereign handed
// over by a parent that predates the registry (hw307, 15 Nov) never gets the
// ConfigMap: restoreFromStore resumes the secondary-kubeconfig delivery and
// the spine on every start of the parent, not the registry. NetworkRegistry
// runs ReconcileNetworkRegistry for a ready deployment, from two places:
//
//  1. Start: ReconcileReady, one pass over every ready record of the parent's
//     deployment store, started by cmd/api right after it wires the cluster
//     cache into the handler (network_registry_wire.go there). restoreFromStore
//     runs inside handler.New, before the cache exists, so it has no client.
//  2. POST /api/v1/deployments/{id}/network-registry: Serve, for one
//     deployment and nothing else.
//
// The records are the files of the deployment store, the ones restoreFromStore
// loads; the handler persists every status change there.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
)

// DeploymentRecord is what the registry reads of one deployment record.
type DeploymentRecord struct {
	ID         string
	Status     string
	OwnerEmail string
}

// DeploymentRecords reads the parent's deployment store. Load returns an
// error that wraps os.ErrNotExist for an unknown id.
type DeploymentRecords interface {
	Load(id string) (DeploymentRecord, error)
	LoadAll() ([]DeploymentRecord, error)
}

// NetworkRegistry holds what both triggers read. Records and Clusters are
// asked at each use: either may be absent (nil, or an error) on a parent that
// runs without its store or its cluster cache. StateDir is the OpenTofu work
// directory; the state of a deployment is <StateDir>/<id>/terraform.tfstate,
// the file the adoption step reads.
type NetworkRegistry struct {
	Log      *slog.Logger
	Records  func() (DeploymentRecords, error)
	Clusters func() Clusters
	StateDir func() string
	// Accepted, when set, is told of every request of the route whose
	// reconcile ran (200 or 502, not a refusal): the parent records it as a
	// retry call of the deployment (gate check FND-G02).
	Accepted func(r *http.Request, id string)
}

// deploymentID is the shape of a deployment id the route accepts; any other
// path segment is an unknown deployment, and never reaches a file path.
var deploymentID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// errNoOpenTofuState: the parent holds no OpenTofu state for the deployment.
var errNoOpenTofuState = errors.New("the parent holds no OpenTofu state for the deployment")

func (n *NetworkRegistry) log() *slog.Logger {
	if n.Log != nil {
		return n.Log
	}
	return slog.Default()
}

// reconcile reads the deployment's state and runs the reconcile.
func (n *NetworkRegistry) reconcile(ctx context.Context, id string, clusters Clusters) (string, []RegionResult, error) {
	state, err := os.ReadFile(filepath.Clean(filepath.Join(n.StateDir(), id, "terraform.tfstate")))
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", errNoOpenTofuState, err)
	}
	return ReconcileNetworkRegistry(ctx, state, id, clusters)
}

// ReconcileReady is the start pass: every ready record, in id order, one log
// line each. It returns the ids it judged. A Sovereign's own catalyst-api
// (SOVEREIGN_FQDN set) is not the writer and holds no state: it judges none.
func (n *NetworkRegistry) ReconcileReady(ctx context.Context) []string {
	if strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN")) != "" {
		return nil
	}
	records, err := n.Records()
	var all []DeploymentRecord
	if err == nil {
		all, err = records.LoadAll()
	}
	if err != nil {
		n.log().Warn("network-registry", "trigger", "start", "err", "read the deployment store: "+err.Error())
		return nil
	}
	var ids []string
	for _, rec := range all {
		if rec.Status == "ready" {
			ids = append(ids, rec.ID)
		}
	}
	sort.Strings(ids)
	clusters := n.Clusters()
	for _, id := range ids {
		rev, regions, err := n.reconcile(ctx, id, clusters)
		if err != nil {
			n.log().Warn("network-registry", "id", id, "trigger", "start", "err", err.Error())
			continue
		}
		n.log().Info("network-registry", "id", id, "trigger", "start", "revision", rev, "regions", regions)
	}
	return ids
}

// networkRegistryResponse is the body of a judged request.
type networkRegistryResponse struct {
	DeploymentID string         `json:"deploymentId"`
	Revision     string         `json:"revision"`
	Regions      []RegionResult `json:"regions"`
}

// owns applies the ownership rule of the neighbouring deployment routes
// (handler/ownership.go, the mothership arm; this route is mothership-only):
// a record without an owner and a request without a session email pass;
// otherwise the session email must be the owner's, ignoring case.
func owns(r *http.Request, rec DeploymentRecord) bool {
	owner := strings.TrimSpace(rec.OwnerEmail)
	session := strings.TrimSpace(r.Header.Get("X-User-Email"))
	return owner == "" || session == "" || strings.EqualFold(owner, session)
}

// Serve handles POST /api/v1/deployments/{id}/network-registry: the
// reconcile for one ready deployment, nothing else.
//
//	200 every region was read; regions[].written says where it wrote
//	404 unknown id, or a session that does not own the deployment
//	409 the deployment is not ready
//	422 the parent holds no OpenTofu state or no registry in it, or its
//	    cluster cache holds no cluster of the deployment
//	502 a region could not be read or written; regions[].error says why
//	503 the parent runs without its deployment store or its cluster cache
func (n *NetworkRegistry) Serve(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !deploymentID.MatchString(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "deployment not found"})
		return
	}
	records, err := n.Records()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "deployment-store-unavailable", "detail": err.Error()})
		return
	}
	rec, err := records.Load(id)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !owns(r, rec)) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "deployment not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "deployment-store-unavailable", "detail": err.Error()})
		return
	}
	if rec.Status != "ready" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "deployment-not-ready",
			"status": rec.Status,
			"detail": "the network registry is written into the regions of a ready deployment only",
		})
		return
	}
	clusters := n.Clusters()
	if _, err := deploymentClusters(clusters, rec.ID); errors.Is(err, ErrNoClusterCache) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cluster-cache-not-wired", "detail": err.Error()})
		return
	}
	rev, regions, err := n.reconcile(r.Context(), rec.ID, clusters)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "network-registry-not-delivered", "detail": err.Error()})
		return
	}
	if n.Accepted != nil {
		n.Accepted(r, rec.ID)
	}
	n.log().Info("network-registry", "id", rec.ID, "trigger", "route", "revision", rev, "regions", regions)
	code := http.StatusOK
	for _, region := range regions {
		if region.Error != "" {
			code = http.StatusBadGateway
		}
	}
	writeJSON(w, code, networkRegistryResponse{DeploymentID: rec.ID, Revision: rev, Regions: regions})
}
