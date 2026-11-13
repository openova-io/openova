// Package meshstatus reads the state of the Cilium ClusterMesh of a
// Sovereign, one status per region. It only reads: it makes no create,
// update, patch or delete call on any cluster.
//
// It also holds the mode switch of catalyst-api's mesh step, the
// environment variable CATALYST_CLUSTERMESH_MODE:
//
//	establish  (the default, also when the variable is unset or empty)
//	           catalyst-api writes the mesh trust material itself.
//	observe    catalyst-api only reads the mesh with Observe; the mesh is
//	           declared elsewhere.
//
// Any other value is a start-up error.
//
// What Observe reads, in namespace kube-system of every region:
//
//	ConfigMap  cilium-config          keys cluster-name and cluster-id
//	Secret     cilium-clustermesh     one key per configured peer cluster name
//	Deployment clustermesh-apiserver  desired and ready replicas
//
// Region A's peer B is Connected when A's Secret cilium-clustermesh has a
// key equal to B's cluster name and B's Deployment clustermesh-apiserver
// has all its replicas ready. Observe leaves LoadBalancerIP empty: the
// address a peer dials is not read from the cluster.
package meshstatus

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ModeEnv is the environment variable that selects the mode.
const ModeEnv = "CATALYST_CLUSTERMESH_MODE"

// Mode is the mode of catalyst-api's mesh step.
type Mode string

const (
	// ModeEstablish: catalyst-api writes the mesh trust material itself.
	ModeEstablish Mode = "establish"
	// ModeObserve: catalyst-api only reads the mesh.
	ModeObserve Mode = "observe"
)

// The objects Observe reads. PeerConfigName names the Secret cilium-clustermesh.
const (
	Namespace          = "kube-system"
	ConfigMapName      = "cilium-config"
	KeyClusterName     = "cluster-name"
	KeyClusterID       = "cluster-id"
	PeerConfigName     = "cilium-clustermesh"
	APIServerName      = "clustermesh-apiserver"
	defaultCallTimeout = 30 * time.Second
)

// ParseMode returns the mode for a value of ModeEnv. The empty value is
// the default, ModeEstablish. Any value other than "", "establish" and
// "observe" is an error.
func ParseMode(value string) (Mode, error) {
	switch value {
	case "", string(ModeEstablish):
		return ModeEstablish, nil
	case string(ModeObserve):
		return ModeObserve, nil
	}
	return "", fmt.Errorf("%s=%q: the value must be %q or %q", ModeEnv, value, ModeEstablish, ModeObserve)
}

// ModeFromEnv reads ModeEnv from the environment of the process.
func ModeFromEnv() (Mode, error) {
	return ParseMode(os.Getenv(ModeEnv))
}

// MustModeFromEnv is ModeFromEnv for start-up: an invalid value stops the
// process with a panic that names the variable and the value.
func MustModeFromEnv() Mode {
	mode, err := ModeFromEnv()
	if err != nil {
		panic(err)
	}
	return mode
}

// ClusterMeshStatus is the per-region summary of the mesh step. One entry
// per region; Peers describes the link to each OTHER region.
type ClusterMeshStatus struct {
	RegionKey      string       `json:"regionKey"`
	ClusterName    string       `json:"clusterName"`
	ClusterID      int          `json:"clusterID"`
	LoadBalancerIP string       `json:"loadBalancerIP"`
	Peers          []PeerStatus `json:"peers"`
	ReadyAt        time.Time    `json:"readyAt,omitempty"`
}

// PeerStatus describes the link from one region to another. In mode
// establish, Connected=true means the peer entries were written. In mode
// observe, it means the peer is configured and its mesh API server is
// ready (see the package comment).
type PeerStatus struct {
	Name           string `json:"name"`
	LoadBalancerIP string `json:"loadBalancerIP"`
	Connected      bool   `json:"connected"`
	Error          string `json:"error,omitempty"`
}

// Region is one region to read.
type Region struct {
	// Key is the region key; "" for the primary region.
	Key string
	// Client is the typed client of the region's cluster.
	Client kubernetes.Interface
	// Err is set when the region could not be reached before the read.
	// Observe then reads nothing from it.
	Err error
}

// view is what Observe read from one region.
type view struct {
	name     string
	id       int
	peers    map[string]bool // keys of the Secret cilium-clustermesh
	notReady string          // why the mesh API server is not ready; "" when it is
	err      error           // the region could not be read
}

// Observe reads every region and returns one status per region, in the
// order of regions. A region that cannot be read gives Connected=false on
// each of its peers, with the reason in Error. now is stamped as ReadyAt
// on a region whose peers are all Connected.
func Observe(ctx context.Context, regions []Region, now time.Time) []ClusterMeshStatus {
	views := make([]view, len(regions))
	for i := range regions {
		views[i] = read(ctx, regions[i])
	}
	statuses := make([]ClusterMeshStatus, 0, len(regions))
	for i := range regions {
		a := views[i]
		st := ClusterMeshStatus{
			RegionKey:   regions[i].Key,
			ClusterName: a.name,
			ClusterID:   a.id,
			Peers:       make([]PeerStatus, 0, len(regions)-1),
		}
		connected := 0
		for j := range regions {
			if i == j {
				continue
			}
			b := views[j]
			peer := PeerStatus{Name: b.name}
			switch {
			case a.err != nil:
				peer.Error = fmt.Sprintf("local region unreachable: %v", a.err)
			case b.err != nil:
				peer.Error = fmt.Sprintf("peer region unreachable: %v", b.err)
			case !a.peers[b.name]:
				peer.Error = fmt.Sprintf("Secret %s/%s has no key %q", Namespace, PeerConfigName, b.name)
			case b.notReady != "":
				peer.Error = fmt.Sprintf("peer Deployment %s/%s not ready: %s", Namespace, APIServerName, b.notReady)
			default:
				peer.Connected = true
				connected++
			}
			st.Peers = append(st.Peers, peer)
		}
		if connected == len(regions)-1 {
			st.ReadyAt = now
		}
		statuses = append(statuses, st)
	}
	return statuses
}

// read gets the three objects of one region. Every call is a Get.
func read(ctx context.Context, r Region) view {
	if r.Err != nil {
		return view{err: r.Err}
	}
	if r.Client == nil {
		return view{err: fmt.Errorf("no client")}
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	cm, err := r.Client.CoreV1().ConfigMaps(Namespace).Get(callCtx, ConfigMapName, metav1.GetOptions{})
	if err != nil {
		return view{err: fmt.Errorf("get ConfigMap %s/%s: %w", Namespace, ConfigMapName, err)}
	}
	name := strings.TrimSpace(cm.Data[KeyClusterName])
	if name == "" {
		return view{err: fmt.Errorf("ConfigMap %s/%s has no %s", Namespace, ConfigMapName, KeyClusterName)}
	}
	id, err := strconv.Atoi(strings.TrimSpace(cm.Data[KeyClusterID]))
	if err != nil {
		return view{name: name, err: fmt.Errorf("ConfigMap %s/%s: %s: %w", Namespace, ConfigMapName, KeyClusterID, err)}
	}
	v := view{name: name, id: id, peers: map[string]bool{}}

	obj, err := r.Client.CoreV1().Secrets(Namespace).Get(callCtx, PeerConfigName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		// No peer is configured in this region.
	case err != nil:
		v.err = fmt.Errorf("get Secret %s/%s: %w", Namespace, PeerConfigName, err)
		return v
	default:
		for key := range obj.Data {
			v.peers[key] = true
		}
		for key := range obj.StringData {
			v.peers[key] = true
		}
	}

	deploy, err := r.Client.AppsV1().Deployments(Namespace).Get(callCtx, APIServerName, metav1.GetOptions{})
	if err != nil {
		v.notReady = fmt.Sprintf("get: %v", err)
		return v
	}
	desired := int32(1)
	if deploy.Spec.Replicas != nil {
		desired = *deploy.Spec.Replicas
	}
	if desired < 1 || deploy.Status.ReadyReplicas < desired {
		v.notReady = fmt.Sprintf("%d of %d replicas ready", deploy.Status.ReadyReplicas, desired)
	}
	return v
}
