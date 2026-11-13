// Peer kubeconfigs of a multi-region Sovereign (SOV-141).
//
// Every region that is not the primary creates, at first boot, the
// ServiceAccount kube-system/handover-peer, bound to cluster-admin, with a
// token Secret that has no expiry. Its post-back to the parent carries the
// kubeconfig of that account under the key peerKubeconfig, beside the
// region's parent-bootstrap kubeconfig. The parent keeps the peer kubeconfig
// next to the region's own kubeconfig and gives it, not the parent-bootstrap
// kubeconfig, to the Sovereign's primary region. So the primary still reaches
// every peer region after the parent-bootstrap account is deleted.
package lifecycle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openova-io/openova/core/controllers/pkg/kubeconfig"
)

// PeerKubeconfigPath is the file that holds the peer kubeconfig of one region:
// <dir>/<clusterID>.peer, where clusterID is <deploymentID>-<regionKey>. The
// suffix is not .yaml, .yml or .kubeconfig, so no reader of the kubeconfigs
// directory takes the file for a cluster of its own.
func PeerKubeconfigPath(dir, clusterID string) string {
	return filepath.Join(dir, clusterID+".peer")
}

// NodeIPPath is the file that holds the private node address of one region:
// <dir>/<clusterID>.nodeip. The receiver of a region's post-back writes it;
// ForwardBody replays it to the Sovereign so that the Sovereign can reach the
// region over the peered private network.
func NodeIPPath(dir, clusterID string) string {
	return filepath.Join(dir, clusterID+".nodeip")
}

// PeerKubeconfigDefects reports why a peer kubeconfig cannot produce a client.
// A blank document is no peer kubeconfig and has no defect. Each defect is
// named "peerKubeconfig.<section>" so that it is told apart from a defect of
// the region's own kubeconfig in the same answer.
func PeerKubeconfigDefects(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, d := range kubeconfig.Defects(raw) {
		out = append(out, "peerKubeconfig."+d)
	}
	return out
}

// StorePeerKubeconfig writes raw to PeerKubeconfigPath(dir, clusterID) with
// mode 0600, without leading and trailing white space. The write is atomic: a
// reader sees the previous document or the new one, never a part of it. A
// blank raw writes nothing.
func StorePeerKubeconfig(dir, clusterID, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	path := PeerKubeconfigPath(dir, clusterID)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("peer kubeconfig: create temp: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("peer kubeconfig: chmod 0600: %w", err)
	}
	if _, err := tmp.WriteString(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("peer kubeconfig: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("peer kubeconfig: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("peer kubeconfig: close: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("peer kubeconfig: rename: %w", err)
	}
	return nil
}

// ForwardBody is the JSON body that the parent posts to the Sovereign's
// /api/v1/sovereign/secondary-kubeconfig for one region: deploymentId,
// regionKey, kubeconfigYaml and, when dir holds the region's node address,
// nodeInternalIp. kubeconfigYaml is the region's peer kubeconfig when dir
// holds one. Only a region whose post-back carried no peer kubeconfig (a
// Sovereign provisioned before SOV-141) gets stored, the kubeconfig the
// parent itself uses for that region.
func ForwardBody(dir, deploymentID, regionKey string, stored []byte) []byte {
	clusterID := deploymentID + "-" + regionKey
	doc := string(stored)
	if peer, err := os.ReadFile(PeerKubeconfigPath(dir, clusterID)); err == nil && strings.TrimSpace(string(peer)) != "" {
		doc = string(peer)
	}
	payload := map[string]string{
		"deploymentId":   deploymentID,
		"regionKey":      regionKey,
		"kubeconfigYaml": doc,
	}
	if ip, err := os.ReadFile(NodeIPPath(dir, clusterID)); err == nil && strings.TrimSpace(string(ip)) != "" {
		payload["nodeInternalIp"] = strings.TrimSpace(string(ip))
	}
	body, _ := json.Marshal(payload)
	return body
}
