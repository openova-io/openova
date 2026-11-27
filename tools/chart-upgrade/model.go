// Package chartupgrade computes, object by object, what Helm 3 sends to the
// API server when it upgrades a release, and what the API server makes of it.
// A chart case (one _test.go file per chart, run by a chart test of that
// chart) renders the chart under test and charts of earlier releases taken
// from the history of the repository, and judges every state a Sovereign can
// hold. It is a module of its own because it needs the Kubernetes libraries
// that tools/guards (standard library only) does not take.
//
// What Helm v3.18.4 does, read in its source:
//   - pkg/action/upgrade.go, prepareUpgrade and performUpgrade: the manifest an
//     upgrade diffs against is the one of the last DEPLOYED release
//     (Releases.Deployed), not the one of the last release tried. Hooks are not
//     in that manifest (pkg/releaseutil, SortManifests).
//   - pkg/kube/client.go, update: an object of the new manifest that is not live
//     is created; a live object that the deployed release does not hold is the
//     error "no <kind> with the name <name> found".
//   - pkg/kube/client.go, createPatch: any other object of a kind Kubernetes
//     defines is sent strategicpatch.CreateThreeWayMergePatch(deployed, new,
//     live, the patch meta of its Go type, overwrite true). A kind Kubernetes
//     does not define gets a JSON merge patch, which this package does not
//     model: it refuses to judge such a kind.
//
// The API server applies the patch to the live object and validates the result;
// Validate holds the rule of that validation the probes of a chart can break.
package chartupgrade

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// Object is one object of a render.
type Object struct {
	GVK  schema.GroupVersionKind
	Name string
	JSON []byte
}

// Key names the object as Helm's messages do: kind/name.
func (o Object) Key() string { return o.GVK.Kind + "/" + o.Name }

// Parse reads the output of helm template and keeps what Helm keeps in the
// manifest of a release: no hook (an object with the annotation helm.sh/hook)
// and no empty document.
func Parse(render []byte) (map[string]Object, error) {
	objs := map[string]Object{}
	r := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(render)))
	for {
		doc, err := r.Read()
		if errors.Is(err, io.EOF) {
			return objs, nil
		}
		if err != nil {
			return nil, err
		}
		j, err := yaml.YAMLToJSON(doc)
		if err != nil {
			return nil, err
		}
		var head struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Metadata   struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		if string(bytes.TrimSpace(j)) == "null" {
			continue
		}
		if err := json.Unmarshal(j, &head); err != nil {
			return nil, err
		}
		if _, hook := head.Metadata.Annotations["helm.sh/hook"]; hook {
			continue
		}
		gv, err := schema.ParseGroupVersion(head.APIVersion)
		if err != nil {
			return nil, err
		}
		o := Object{GVK: gv.WithKind(head.Kind), Name: head.Metadata.Name, JSON: j}
		if _, twice := objs[o.Key()]; twice {
			return nil, fmt.Errorf("%s is rendered twice", o.Key())
		}
		objs[o.Key()] = o
	}
}

// kinds holds the Go types of the groups this package judges.
var kinds = func() *runtime.Scheme {
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme, appsv1.AddToScheme, batchv1.AddToScheme, rbacv1.AddToScheme,
		storagev1.AddToScheme, policyv1.AddToScheme, networkingv1.AddToScheme, schedulingv1.AddToScheme,
	} {
		if err := add(s); err != nil {
			panic(err)
		}
	}
	return s
}()

func typed(gvk schema.GroupVersionKind) (runtime.Object, error) {
	obj, err := kinds.New(gvk)
	if err != nil {
		return nil, fmt.Errorf("%s: not a kind of the groups core, apps, batch, rbac, storage, policy, networking or scheduling; Helm sends a kind Kubernetes does not define a JSON merge patch, which this test does not model (add the group if Kubernetes defines it)", gvk)
	}
	return obj, nil
}

// Outcome is what an upgrade does to one object.
type Outcome struct {
	// Object is the live object after the upgrade: the one sent when accepted,
	// the one that was live when refused.
	Object Object
	// Refusal is the error of Helm or of the API server; empty when accepted.
	Refusal string
}

// Upgrade is Helm's step for one object of the new render. deployed is the
// object in the last deployed release, live the live object; each is nil when
// there is none.
func Upgrade(deployed, live *Object, modified Object) (Outcome, error) {
	obj, err := typed(modified.GVK)
	if err != nil {
		return Outcome{}, err
	}
	if live == nil {
		return judge(nil, modified, modified)
	}
	if deployed == nil {
		return Outcome{Object: *live, Refusal: fmt.Sprintf("no %s with the name %q found", modified.GVK.Kind, modified.Name)}, nil
	}
	meta, err := strategicpatch.NewPatchMetaFromStruct(obj)
	if err != nil {
		return Outcome{}, err
	}
	patch, err := strategicpatch.CreateThreeWayMergePatch(deployed.JSON, modified.JSON, live.JSON, meta, true)
	if err != nil {
		return Outcome{}, fmt.Errorf("%s: %w", modified.Key(), err)
	}
	if string(patch) == "{}" {
		return Outcome{Object: *live}, nil
	}
	merged, err := strategicpatch.StrategicMergePatch(live.JSON, patch, obj)
	if err != nil {
		return Outcome{}, fmt.Errorf("%s: %w", modified.Key(), err)
	}
	return judge(live, Object{GVK: modified.GVK, Name: modified.Name, JSON: merged}, *live)
}

// judge validates next as the API server does; on a refusal the object stays
// as it was (kept).
func judge(live *Object, next, kept Object) (Outcome, error) {
	errs, err := Validate(next)
	if err != nil {
		return Outcome{}, err
	}
	if live != nil {
		same, err := sameSelector(*live, next)
		if err != nil {
			return Outcome{}, err
		}
		if !same {
			errs = append(errs, "spec.selector: Invalid value: field is immutable")
		}
	}
	if len(errs) == 0 {
		return Outcome{Object: next}, nil
	}
	msg := strings.Join(errs, ", ")
	if len(errs) > 1 {
		msg = "[" + msg + "]"
	}
	return Outcome{Object: kept, Refusal: fmt.Sprintf("%s %q is invalid: %s", next.GVK.GroupKind(), next.Name, msg)}, nil
}

// PodSpec returns the Pod template of a workload and the path of its spec,
// or nil for an object that has none.
func PodSpec(o Object) (*corev1.PodSpec, string, error) {
	var path string
	var tmpl struct {
		Spec struct {
			Template corev1.PodTemplateSpec `json:"template"`
			Job      struct {
				Spec struct {
					Template corev1.PodTemplateSpec `json:"template"`
				} `json:"spec"`
			} `json:"jobTemplate"`
		} `json:"spec"`
	}
	switch o.GVK.GroupKind() {
	case schema.GroupKind{Group: "apps", Kind: "Deployment"}, schema.GroupKind{Group: "apps", Kind: "DaemonSet"},
		schema.GroupKind{Group: "apps", Kind: "StatefulSet"}, schema.GroupKind{Group: "apps", Kind: "ReplicaSet"},
		schema.GroupKind{Group: "batch", Kind: "Job"}, schema.GroupKind{Kind: "ReplicationController"}:
		path = "spec.template.spec"
	case schema.GroupKind{Group: "batch", Kind: "CronJob"}:
		path = "spec.jobTemplate.spec.template.spec"
	case schema.GroupKind{Kind: "Pod"}:
		var p corev1.Pod
		if err := json.Unmarshal(o.JSON, &p); err != nil {
			return nil, "", fmt.Errorf("%s: %w", o.Key(), err)
		}
		return &p.Spec, "spec", nil
	default:
		return nil, "", nil
	}
	if err := json.Unmarshal(o.JSON, &tmpl); err != nil {
		return nil, "", fmt.Errorf("%s: %w", o.Key(), err)
	}
	if path == "spec.template.spec" {
		return &tmpl.Spec.Template.Spec, path, nil
	}
	return &tmpl.Spec.Job.Spec.Template.Spec, path, nil
}

// Validate returns the errors the API server's validation gives for the
// handlers of the probes and lifecycle hooks of a Pod template
// (k8s.io/kubernetes pkg/apis/core/validation, validateHandler and
// validateLifecycle): each names exactly one handler. Paths and texts are the
// API server's.
func Validate(o Object) ([]string, error) {
	spec, path, err := PodSpec(o)
	if err != nil || spec == nil {
		return nil, err
	}
	var errs []string
	for _, list := range []struct {
		field      string
		containers []corev1.Container
	}{{"initContainers", spec.InitContainers}, {"containers", spec.Containers}} {
		for i, c := range list.containers {
			at := fmt.Sprintf("%s.%s[%d]", path, list.field, i)
			for _, p := range []struct {
				field string
				probe *corev1.Probe
			}{{"livenessProbe", c.LivenessProbe}, {"readinessProbe", c.ReadinessProbe}, {"startupProbe", c.StartupProbe}} {
				if p.probe == nil {
					continue
				}
				h := p.probe.ProbeHandler
				errs = append(errs, oneHandler(at+"."+p.field,
					handler{"exec", h.Exec != nil}, handler{"httpGet", h.HTTPGet != nil},
					handler{"tcpSocket", h.TCPSocket != nil}, handler{"grpc", h.GRPC != nil})...)
			}
			if c.Lifecycle == nil {
				continue
			}
			for _, hook := range []struct {
				field string
				h     *corev1.LifecycleHandler
			}{{"postStart", c.Lifecycle.PostStart}, {"preStop", c.Lifecycle.PreStop}} {
				if hook.h == nil {
					continue
				}
				errs = append(errs, oneHandler(at+".lifecycle."+hook.field,
					handler{"exec", hook.h.Exec != nil}, handler{"httpGet", hook.h.HTTPGet != nil},
					handler{"tcpSocket", hook.h.TCPSocket != nil}, handler{"sleep", hook.h.Sleep != nil})...)
			}
		}
	}
	return errs, nil
}

type handler struct {
	name string
	set  bool
}

func oneHandler(path string, hs ...handler) []string {
	var errs []string
	n := 0
	for _, h := range hs {
		if !h.set {
			continue
		}
		if n > 0 {
			errs = append(errs, path+"."+h.name+": Forbidden: may not specify more than 1 handler type")
		}
		n++
	}
	if n == 0 {
		errs = append(errs, path+": Required value: must specify a handler type")
	}
	return errs
}

// sameSelector is false when an upgrade changes the selector of a workload of
// apps/v1, which the API server refuses (the field is immutable).
func sameSelector(live, next Object) (bool, error) {
	if next.GVK.Group != "apps" {
		return true, nil
	}
	var a, b struct {
		Spec struct {
			Selector any `json:"selector"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(live.JSON, &a); err != nil {
		return false, err
	}
	if err := json.Unmarshal(next.JSON, &b); err != nil {
		return false, err
	}
	return reflect.DeepEqual(a.Spec.Selector, b.Spec.Selector), nil
}

// Drift lists the fields where got differs from want, metadata and status
// aside: after an upgrade, what an earlier upgrade left that this one did not
// take away, or what this one did not set.
func Drift(got, want Object) ([]string, error) {
	var g, w map[string]any
	if err := json.Unmarshal(got.JSON, &g); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(want.JSON, &w); err != nil {
		return nil, err
	}
	for _, m := range []map[string]any{g, w} {
		delete(m, "metadata")
		delete(m, "status")
	}
	var out []string
	diff("", g, w, &out)
	return out, nil
}

func diff(path string, got, want any, out *[]string) {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: %v, want an object", path, got))
			return
		}
		keys := map[string]bool{}
		for k := range g {
			keys[k] = true
		}
		for k := range w {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			p := strings.TrimPrefix(path+"."+k, ".")
			gv, inGot := g[k]
			wv, inWant := w[k]
			switch {
			case !inWant:
				*out = append(*out, fmt.Sprintf("%s: %s, which the new render does not hold", p, compact(gv)))
			case !inGot:
				*out = append(*out, fmt.Sprintf("%s: missing, the new render holds %s", p, compact(wv)))
			default:
				diff(p, gv, wv, out)
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			*out = append(*out, fmt.Sprintf("%s: %s, want %s", path, names(got), names(want)))
			return
		}
		for i := range w {
			diff(fmt.Sprintf("%s[%d]", path, i), g[i], w[i], out)
		}
	default:
		if !reflect.DeepEqual(got, want) {
			*out = append(*out, fmt.Sprintf("%s: %s, want %s", path, compact(got), compact(want)))
		}
	}
}

func compact(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// names describes a list by the names of its items when they have one.
func names(v any) string {
	list, ok := v.([]any)
	if !ok {
		return compact(v)
	}
	var ns []string
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok || m["name"] == nil {
			return fmt.Sprintf("%d items", len(list))
		}
		ns = append(ns, fmt.Sprint(m["name"]))
	}
	return fmt.Sprintf("%d items %v", len(list), ns)
}
