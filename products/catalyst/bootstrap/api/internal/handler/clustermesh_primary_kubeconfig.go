// clustermesh_primary_kubeconfig.go — #5131 primary (local) kubeconfig
// self-materialization for the in-cluster (chroot) Sovereign.
//
// Root cause (#5131, root-caused live on hw259, a healthy fresh 2-region
// Huawei Sovereign):
//
//	The mothership writes each provisioned Sovereign's PRIMARY (region-a)
//	kubeconfig to its OWN PVC at <kubeconfigsDir>/<id>.yaml during Phase 0,
//	and forwards only the SECONDARY region kubeconfigs to the chroot via
//	POST /api/v1/sovereign/secondary-kubeconfig at handover. The chroot —
//	the in-cluster Sovereign catalyst-api — therefore has the SECONDARY
//	kubeconfig on disk (resolves fine) but NEVER receives the PRIMARY: the
//	local region-a cluster is the one the chroot itself runs on, reachable
//	via the mounted ServiceAccount, so no file was ever posted for it.
//
//	resolvePrimaryKubeconfigPath insists on a stat-readable FILE at
//	<kubeconfigsDir>/<id>.yaml (or dep.Result.KubeconfigPath). On a chroot
//	that file is absent, so os.Stat missed forever, shouldStartupCluster-
//	MeshReconcile returned false on every attempt, and the retry loop
//	exhausted its budget with "primary kubeconfig unresolved — next trigger
//	is a catalyst-api restart". The ClusterMesh establish never ran, so the
//	slot-16b SOVEREIGN_ENABLE_CNPG_PAIR flip + the multi-region convergence
//	that arms self-sovereign-cutover never fired: the keystone stalled on a
//	perfectly healthy 2-region mesh.
//
// Fix: on the chroot, MATERIALIZE the primary (local) kubeconfig from the
// in-cluster ServiceAccount — NO network round-trip — and write it to the
// canonical <kubeconfigsDir>/<id>.yaml path so every existing file-based
// consumer (buildRegionSlots, clusterMeshDynamicClient, the jobs informer)
// resolves the local cluster exactly as it does on the mothership.

package handler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// inClusterConfigForMaterialize is the seam the production path wires to
// rest.InClusterConfig. Tests override it to inject a synthetic rest.Config
// (or a forced error) without needing a real ServiceAccount mount.
var inClusterConfigForMaterialize = rest.InClusterConfig

// chrootServesDeployment reports whether THIS catalyst-api is the in-cluster
// (chroot) Sovereign that OWNS dep — i.e. SOVEREIGN_FQDN is set on the process
// AND equals dep.Request.SovereignFQDN. Mirrors sovereignDynamicClient /
// tryDynamicClientLocked exactly so the primary-kubeconfig materialization uses
// the identical discriminator: a chroot only ever self-materializes for the
// single deployment it runs on, and the mothership (SOVEREIGN_FQDN unset) never
// materializes at all.
func (h *Handler) chrootServesDeployment(dep *Deployment) bool {
	selfFQDN := strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN"))
	if selfFQDN == "" {
		return false
	}
	dep.mu.Lock()
	depFQDN := strings.TrimSpace(dep.Request.SovereignFQDN)
	dep.mu.Unlock()
	return depFQDN != "" && depFQDN == selfFQDN
}

// inClusterKubeconfigYAML serializes the in-cluster rest.Config into a
// kubeconfig YAML pointing at the LOCAL apiserver with the mounted
// ServiceAccount bearer token + CA. This is a NO-NETWORK resolution of the
// local cluster: rest.InClusterConfig reads only the KUBERNETES_SERVICE_*
// env and the mounted token/CA files — it never dials the apiserver.
func inClusterKubeconfigYAML() (string, error) {
	cfg, err := inClusterConfigForMaterialize()
	if err != nil {
		return "", fmt.Errorf("in-cluster config: %w", err)
	}
	if strings.TrimSpace(cfg.Host) == "" {
		return "", fmt.Errorf("in-cluster config has empty host")
	}

	caData := cfg.TLSClientConfig.CAData
	if len(caData) == 0 && cfg.TLSClientConfig.CAFile != "" {
		if b, rerr := os.ReadFile(cfg.TLSClientConfig.CAFile); rerr == nil {
			caData = b
		}
	}
	token := cfg.BearerToken
	if token == "" && cfg.BearerTokenFile != "" {
		if b, rerr := os.ReadFile(cfg.BearerTokenFile); rerr == nil {
			token = strings.TrimSpace(string(b))
		}
	}

	const name = "in-cluster"
	apiCfg := clientcmdapi.NewConfig()
	cluster := &clientcmdapi.Cluster{Server: cfg.Host}
	if len(caData) > 0 {
		cluster.CertificateAuthorityData = caData
	} else {
		// No CA available (unusual) — the helmwatch client path forces
		// TLS-insecure anyway (issue #923), but a serialized kubeconfig
		// must be self-consistent, so mark it insecure explicitly.
		cluster.InsecureSkipTLSVerify = true
	}
	apiCfg.Clusters[name] = cluster
	apiCfg.AuthInfos[name] = &clientcmdapi.AuthInfo{Token: token}
	apiCfg.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name}
	apiCfg.CurrentContext = name

	out, err := clientcmd.Write(*apiCfg)
	if err != nil {
		return "", fmt.Errorf("serialize in-cluster kubeconfig: %w", err)
	}
	return string(out), nil
}

// materializeChrootPrimaryKubeconfig writes the in-cluster (local, region-a)
// kubeconfig for a chroot's OWN deployment to the canonical primary path when
// that file is absent, and reports whether the file is now present. See the
// package-doc (#5131) for the root cause. Best-effort + idempotent: a failure
// (not a chroot, in-cluster config unavailable, write error) returns false and
// the caller keeps its honest "primary unresolved" warn-and-skip, so a
// genuinely-lost MOTHERSHIP kubeconfig is never masked.
func (h *Handler) materializeChrootPrimaryKubeconfig(dep *Deployment, path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	if !h.chrootServesDeployment(dep) {
		return false
	}
	raw, err := inClusterKubeconfigYAML()
	if err != nil {
		h.log.Warn("clustermesh: chroot primary kubeconfig materialize skipped — in-cluster config unavailable (#5131)",
			"id", dep.ID, "err", err)
		return false
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		h.log.Warn("clustermesh: chroot primary kubeconfig mkdir failed (#5131)",
			"id", dep.ID, "dir", filepath.Dir(path), "err", err)
		return false
	}
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		h.log.Warn("clustermesh: chroot primary kubeconfig write failed (#5131)",
			"id", dep.ID, "path", path, "err", err)
		return false
	}
	h.log.Info("clustermesh: materialized chroot primary (local) kubeconfig from in-cluster ServiceAccount (#5131)",
		"id", dep.ID, "path", path)
	return true
}
