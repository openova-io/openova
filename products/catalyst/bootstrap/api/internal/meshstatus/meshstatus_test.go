package meshstatus

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kfake "k8s.io/client-go/kubernetes/fake"
)

var testNow = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

func ciliumConfig(name, id string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: ConfigMapName, Namespace: Namespace},
		Data:       map[string]string{KeyClusterName: name, KeyClusterID: id},
	}
}

func peerConfig(keys ...string) *corev1.Secret {
	data := map[string][]byte{}
	for _, k := range keys {
		data[k] = []byte("x")
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: PeerConfigName, Namespace: Namespace},
		Data:       data,
	}
}

// apiserver returns the Deployment clustermesh-apiserver. replicas < 0
// leaves spec.replicas unset (the default of one).
func apiserver(replicas, ready int32) *appsv1.Deployment {
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: APIServerName, Namespace: Namespace}}
	if replicas >= 0 {
		d.Spec.Replicas = &replicas
	}
	d.Status.ReadyReplicas = ready
	return d
}

func cluster(objs ...runtime.Object) *kfake.Clientset {
	return kfake.NewSimpleClientset(objs...)
}

func TestParseMode(t *testing.T) {
	cases := []struct {
		in      string
		want    Mode
		wantErr bool
	}{
		{"", ModeEstablish, false},
		{"establish", ModeEstablish, false},
		{"observe", ModeObserve, false},
		{"Observe", "", true},
		{" observe", "", true},
		{"off", "", true},
	}
	for _, c := range cases {
		got, err := ParseMode(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseMode(%q) error = %v, want error %v", c.in, err, c.wantErr)
			continue
		}
		if got != c.want {
			t.Errorf("ParseMode(%q) = %q, want %q", c.in, got, c.want)
		}
		if err != nil && !strings.Contains(err.Error(), ModeEnv) {
			t.Errorf("ParseMode(%q) error %q does not name %s", c.in, err, ModeEnv)
		}
	}
}

func TestModeFromEnv(t *testing.T) {
	t.Setenv(ModeEnv, "")
	if got := MustModeFromEnv(); got != ModeEstablish {
		t.Errorf("empty %s: mode %q, want %q", ModeEnv, got, ModeEstablish)
	}
	t.Setenv(ModeEnv, "observe")
	if got := MustModeFromEnv(); got != ModeObserve {
		t.Errorf("%s=observe: mode %q, want %q", ModeEnv, got, ModeObserve)
	}
	t.Setenv(ModeEnv, "bogus")
	if _, err := ModeFromEnv(); err == nil {
		t.Errorf("%s=bogus: no error", ModeEnv)
	}
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("%s=bogus: MustModeFromEnv did not stop the start-up", ModeEnv)
		}
	}()
	MustModeFromEnv()
}

// TestObserve_Fixture: three regions. The primary configures both peers
// and its API server is ready; region b configures the primary, but its API
// server is not ready; region c cannot be reached.
func TestObserve_Fixture(t *testing.T) {
	regions := []Region{
		{Key: "", Client: cluster(ciliumConfig("mesh-a", "1"), peerConfig("mesh-b", "mesh-b-ca.crt", "mesh-c"), apiserver(2, 2))},
		{Key: "b", Client: cluster(ciliumConfig("mesh-b", " 2 "), peerConfig("mesh-a"), apiserver(-1, 0))},
		{Key: "c", Err: errors.New("kubeconfig missing")},
	}
	want := []ClusterMeshStatus{
		{RegionKey: "", ClusterName: "mesh-a", ClusterID: 1, Peers: []PeerStatus{
			{Name: "mesh-b", Error: "peer Deployment kube-system/clustermesh-apiserver not ready: 0 of 1 replicas ready"},
			{Name: "", Error: "peer region unreachable: kubeconfig missing"},
		}},
		{RegionKey: "b", ClusterName: "mesh-b", ClusterID: 2, Peers: []PeerStatus{
			{Name: "mesh-a", Connected: true},
			{Name: "", Error: "peer region unreachable: kubeconfig missing"},
		}},
		{RegionKey: "c", Peers: []PeerStatus{
			{Name: "mesh-a", Error: "local region unreachable: kubeconfig missing"},
			{Name: "mesh-b", Error: "local region unreachable: kubeconfig missing"},
		}},
	}
	got := Observe(context.Background(), regions, testNow)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Observe:\n got  %+v\n want %+v", got, want)
	}
}

// TestObserve_ReadyAt: a region whose peers are all connected gets ReadyAt;
// a region without the Secret has no peer configured.
func TestObserve_ReadyAt(t *testing.T) {
	regions := []Region{
		{Key: "", Client: cluster(ciliumConfig("mesh-a", "1"), peerConfig("mesh-b"), apiserver(1, 1))},
		{Key: "b", Client: cluster(ciliumConfig("mesh-b", "2"), apiserver(1, 1))},
	}
	want := []ClusterMeshStatus{
		{RegionKey: "", ClusterName: "mesh-a", ClusterID: 1, ReadyAt: testNow, Peers: []PeerStatus{
			{Name: "mesh-b", Connected: true},
		}},
		{RegionKey: "b", ClusterName: "mesh-b", ClusterID: 2, Peers: []PeerStatus{
			{Name: "mesh-a", Error: `Secret kube-system/cilium-clustermesh has no key "mesh-a"`},
		}},
	}
	got := Observe(context.Background(), regions, testNow)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Observe:\n got  %+v\n want %+v", got, want)
	}
}

// TestObserve_UnreadableConfig: a region without a usable cilium-config is
// a region that cannot be read.
func TestObserve_UnreadableConfig(t *testing.T) {
	cases := map[string]*kfake.Clientset{
		"configmaps \"cilium-config\" not found": cluster(peerConfig("mesh-b"), apiserver(1, 1)),
		"has no cluster-name":                    cluster(ciliumConfig("", "1"), apiserver(1, 1)),
		"cluster-id: strconv.Atoi":               cluster(ciliumConfig("mesh-a", "one"), apiserver(1, 1)),
	}
	for wantText, client := range cases {
		regions := []Region{
			{Key: "", Client: client},
			{Key: "b", Client: cluster(ciliumConfig("mesh-b", "2"), peerConfig("mesh-a"), apiserver(1, 1))},
		}
		got := Observe(context.Background(), regions, testNow)
		if got[0].Peers[0].Connected || !strings.HasPrefix(got[0].Peers[0].Error, "local region unreachable: ") ||
			!strings.Contains(got[0].Peers[0].Error, wantText) {
			t.Errorf("%s: primary peer = %+v", wantText, got[0].Peers[0])
		}
		if got[1].Peers[0].Connected || !strings.Contains(got[1].Peers[0].Error, wantText) {
			t.Errorf("%s: region b peer = %+v", wantText, got[1].Peers[0])
		}
		if !got[0].ReadyAt.IsZero() || !got[1].ReadyAt.IsZero() {
			t.Errorf("%s: ReadyAt stamped on a region that cannot be read", wantText)
		}
	}
}

// TestObserve_OnlyReads: every call Observe makes is a get.
func TestObserve_OnlyReads(t *testing.T) {
	a := cluster(ciliumConfig("mesh-a", "1"), peerConfig("mesh-b"), apiserver(1, 1))
	b := cluster(ciliumConfig("mesh-b", "2"), peerConfig("mesh-a"), apiserver(1, 1))
	Observe(context.Background(), []Region{{Key: "", Client: a}, {Key: "b", Client: b}}, testNow)
	for name, c := range map[string]*kfake.Clientset{"primary": a, "b": b} {
		actions := c.Actions()
		if len(actions) != 3 {
			t.Errorf("%s: %d calls, want 3 (ConfigMap, Secret, Deployment)", name, len(actions))
		}
		for _, act := range actions {
			if act.GetVerb() != "get" || act.GetNamespace() != Namespace {
				t.Errorf("%s: call %s %s in namespace %q, want only get in %s",
					name, act.GetVerb(), act.GetResource().Resource, act.GetNamespace(), Namespace)
			}
		}
	}
}
