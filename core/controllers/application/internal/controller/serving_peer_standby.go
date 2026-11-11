package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultPeerObserverURLTemplate — each region's Continuum serves its view of
// its own CNPG Clusters at GET /v1/clusters (ADR-0015 D8 observer), reachable
// from peer regions through the per-region ClusterMesh Service
// continuum-<region-id>.
const DefaultPeerObserverURLTemplate = "http://continuum-{region}.catalyst-system.svc.cluster.local:8082"

// peerClusterStatus is the observer's answer for one Cluster.
type peerClusterStatus struct {
	Region  string `json:"region"`
	Found   bool   `json:"found"`
	Ready   bool   `json:"ready"`
	Primary bool   `json:"primary"`
	Phase   string `json:"phase"`
}

var peerObserverHTTP = &http.Client{Timeout: 3 * time.Second}

// observeStandbyInPeers asks every peer region's Continuum observer for the
// named Cluster. A synchronous standby of a two-region pair lives in the
// OTHER region, which this region's API server cannot see (D1). Returns the
// first region that has it (ok=true) or ok=false when no peer answered with
// the Cluster.
func (r *Reconciler) observeStandbyInPeers(ctx context.Context, namespace, name string) (peerClusterStatus, bool) {
	tmpl := strings.TrimSpace(r.Cfg.PeerObserverURLTemplate)
	if tmpl == "" {
		tmpl = DefaultPeerObserverURLTemplate
	}
	for _, region := range r.Cfg.Regions.IDs() {
		if region == r.Cfg.LocalRegionID {
			continue
		}
		u := strings.ReplaceAll(tmpl, "{region}", region) + "/v1/clusters?namespace=" +
			url.QueryEscape(namespace) + "&name=" + url.QueryEscape(name)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			continue
		}
		resp, err := peerObserverHTTP.Do(req)
		if err != nil {
			continue
		}
		var st peerClusterStatus
		decErr := json.NewDecoder(resp.Body).Decode(&st)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || decErr != nil || !st.Found {
			continue
		}
		if st.Region == "" {
			st.Region = region
		}
		return st, true
	}
	return peerClusterStatus{}, false
}

func (s peerClusterStatus) String() string {
	return fmt.Sprintf("%s (region %s, ready=%v, primary=%v)", s.Phase, s.Region, s.Ready, s.Primary)
}
