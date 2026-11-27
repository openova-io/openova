package chartupgrade

// The case of bp-huawei-evs-csi (run by platform/huawei-evs-csi/chart/tests/
// upgrade-over-failed.sh). The chart under test applies over every state a
// Sovereign can hold, as Helm 3 upgrades it: a fresh install, and each pair
// (the last deployed release, the live objects) of evsStates.
//
// The known answer: 1.0.13 over 1.0.11 deployed and 1.0.12 live must give the
// refusal both regions of the Sovereign reported on 27 Nov 2026, word for word,
// and no other. A model that does not reproduce the measured failure proves
// nothing about a repair.
//
// What it cannot see: whether the exec probe of the registrar exits 0 on a
// node (that needs the kubelet to call the registrar), the defaults the API
// server adds to a live object (a field neither render names is not in the
// patch), and an upgrade by another Helm (Helm 4 applies on the server side).

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	evsChart     = "platform/huawei-evs-csi/chart"
	evsRelease   = "huawei-evs-csi"
	evsPluginDir = "/var/lib/kubelet/plugins/evs.csi.huaweicloud.com"
	// The HelmRelease bp-huawei-evs-csi in both regions, 27 Nov 2026 13:25 UTC:
	// "cannot patch "csi-evs-node" with kind DaemonSet: <this>".
	evsMeasured = `DaemonSet.apps "csi-evs-node" is invalid: [spec.template.spec.containers[0].livenessProbe.httpGet: Forbidden: may not specify more than 1 handler type, spec.template.spec.containers[0].readinessProbe.httpGet: Forbidden: may not specify more than 1 handler type]`
)

// The values of kit slot 55b that shape these objects
// (clusters/_template/bootstrap-kit/55b-bp-huawei-evs-csi.yaml).
var evsValues = []string{"--set", "enabled=true", "--set", "defaultStorageClass=true", "--set", "volumeSnapshotClass.enabled=false"}

var evsStates = []struct{ name, deployed, live string }{
	{"1.0.11 deployed, the failed 1.0.12 live (the Sovereign of 27 Nov 2026)", "1.0.11", "1.0.12"},
	{"1.0.11 deployed and live", "1.0.11", "1.0.11"},
	{"1.0.12 deployed and live", "1.0.12", "1.0.12"},
	{"1.0.13 deployed and live", "1.0.13", "1.0.13"},
}

func TestHuaweiEVSCSIAppliesOverEveryState(t *testing.T) {
	root := needTools(t)
	renders := map[string]map[string]Object{}
	for _, v := range []string{"1.0.11", "1.0.12", "1.0.13"} {
		c := releaseCommit(t, root, evsChart, v)
		t.Logf("%s: the chart at commit %s", v, c)
		renders[v] = render(t, extract(t, root, evsChart, c), evsRelease, evsValues)
	}
	chart := chartUnderTest(root, evsChart)
	t.Logf("the chart under test: %s", chart)
	next := render(t, chart, evsRelease, evsValues)

	t.Run("known answer: 1.0.13 over 1.0.11 deployed and 1.0.12 live is the refusal measured", func(t *testing.T) {
		refused := map[string]string{}
		for k, o := range upgradeAll(t, renders["1.0.11"], renders["1.0.12"], renders["1.0.13"]) {
			if o.Refusal != "" {
				refused[k] = o.Refusal
			}
		}
		if len(refused) != 1 || refused["DaemonSet/csi-evs-node"] != evsMeasured {
			t.Fatalf("the model gives the refusals %q, the API server gave DaemonSet/csi-evs-node: %s", refused, evsMeasured)
		}
	})
	t.Run("fresh install", func(t *testing.T) {
		evsCheck(t, upgradeAll(t, nil, nil, next), nil, next)
	})
	for _, s := range evsStates {
		t.Run(s.name, func(t *testing.T) {
			evsCheck(t, upgradeAll(t, renders[s.deployed], renders[s.live], next), renders[s.live], next)
		})
	}
}

// evsCheck: the upgrade converges, it replaces no node plugin Pod, and the
// registrar's probes are the exec form on the plugin directory it mounts.
func evsCheck(t *testing.T, outcomes map[string]Outcome, live, next map[string]Object) {
	t.Helper()
	converges(t, outcomes, next)
	ds, ok := outcomes["DaemonSet/csi-evs-node"]
	if !ok {
		t.Fatal("the render has no DaemonSet/csi-evs-node")
	}
	if ds.Refusal == "" {
		var before *Object
		if o, ok := live["DaemonSet/csi-evs-node"]; ok {
			before = &o
		}
		evsNoRoll(t, before, ds.Object)
		evsRegistrar(t, ds.Object)
	}
}

// evsNoRoll: after the patch the DaemonSet is OnDelete, so the DaemonSet
// controller replaces no Pod whatever the patch did to the template. The plugin
// stages each EVS volume in its own mount namespace, so a replaced Pod drops
// the globalmount of every volume attached to its node and an EVS-backed Pod
// that restarts there stays ContainerCreating (exit 32; hw251, hw271). It
// reports whether the patch changed the pod template, which under RollingUpdate
// would replace the node plugin on every node.
func evsNoRoll(t *testing.T, live *Object, got Object) {
	t.Helper()
	var after appsv1.DaemonSet
	if err := json.Unmarshal(got.JSON, &after); err != nil {
		t.Fatal(err)
	}
	changed := false
	if live != nil {
		var before appsv1.DaemonSet
		if err := json.Unmarshal(live.JSON, &before); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(before.Spec.Template)
		if err != nil {
			t.Fatal(err)
		}
		a, err := json.Marshal(after.Spec.Template)
		if err != nil {
			t.Fatal(err)
		}
		changed = !bytes.Equal(a, b)
	}
	t.Logf("DaemonSet/csi-evs-node: the patch changes the pod template: %t; updateStrategy after it: %q", changed, after.Spec.UpdateStrategy.Type)
	if after.Spec.UpdateStrategy.Type != appsv1.OnDeleteDaemonSetStrategyType {
		t.Errorf("DaemonSet/csi-evs-node after the upgrade has updateStrategy %q, want %q: the controller replaces the node plugin Pods (template changed: %t) and each replaced Pod drops the stage mounts of the EVS volumes on its node",
			after.Spec.UpdateStrategy.Type, appsv1.OnDeleteDaemonSetStrategyType, changed)
	}
}

func evsRegistrar(t *testing.T, o Object) {
	t.Helper()
	var d appsv1.DaemonSet
	if err := json.Unmarshal(o.JSON, &d); err != nil {
		t.Fatal(err)
	}
	spec := d.Spec.Template.Spec
	var found []int
	for i, c := range spec.Containers {
		for _, a := range c.Args {
			if strings.HasPrefix(a, "--http-endpoint") {
				t.Errorf("container %s of DaemonSet/csi-evs-node runs with %s", c.Name, a)
			}
		}
		if strings.Contains(c.Image, "csi-node-driver-registrar") {
			found = append(found, i)
		}
	}
	if len(found) != 1 {
		t.Fatalf("DaemonSet/csi-evs-node has %d registrar containers (image csi-node-driver-registrar), want 1", len(found))
	}
	r := spec.Containers[found[0]]
	for _, p := range []struct {
		name  string
		probe *corev1.Probe
	}{{"livenessProbe", r.LivenessProbe}, {"readinessProbe", r.ReadinessProbe}} {
		if p.probe == nil || p.probe.Exec == nil || p.probe.HTTPGet != nil || p.probe.TCPSocket != nil || p.probe.GRPC != nil {
			t.Errorf("the registrar %s: its %s is not the exec form alone: %+v", r.Name, p.name, p.probe)
			continue
		}
		cmd := p.probe.Exec.Command
		if len(cmd) == 0 || cmd[0] != "/csi-node-driver-registrar" || !slices.Contains(cmd, "--mode=kubelet-registration-probe") ||
			!slices.Contains(cmd, "--kubelet-registration-path="+evsPluginDir+"/csi.sock") {
			t.Errorf("the registrar %s: its %s runs %q, want /csi-node-driver-registrar --mode=kubelet-registration-probe --kubelet-registration-path=%s/csi.sock", r.Name, p.name, cmd, evsPluginDir)
		}
	}
	mounted := false
	for _, m := range r.VolumeMounts {
		if strings.TrimSuffix(m.MountPath, "/") != evsPluginDir || m.ReadOnly {
			continue
		}
		for _, v := range spec.Volumes {
			if v.Name == m.Name && v.HostPath != nil && strings.TrimSuffix(v.HostPath.Path, "/") == evsPluginDir {
				mounted = true
			}
		}
	}
	if !mounted {
		t.Errorf("the registrar %s does not mount the node's %s writable at that path, where its probe reads the file registration", r.Name, evsPluginDir)
	}
	if sc := r.SecurityContext; sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Errorf("the registrar %s: its root file system is not read-only", r.Name)
	}
}
