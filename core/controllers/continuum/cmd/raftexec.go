// Production Pod-exec backend for the raft-transition promotion (#3492).
//
// This is the kube-client implementation of switchover.PodExecutor +
// switchover.PodLister the controller wires into the RaftExecPromoter so
// bp-continuum can run `bao operator raft snapshot restore` +
// `transition-to-primary` inside the surviving-region openbao standby Pod
// on a region-kill.
//
// The exec uses client-go remotecommand SPDY against the kube-apiserver —
// the same mechanism core/cmd/k8s-ws-proxy/internal/proxy/exec.go uses.
// It lives in the cmd package (not internal/switchover) so the switchover
// package keeps a minimal interface seam and does not grow a hard
// dependency on the remotecommand SDK (which keeps its unit tests
// SDK-free).

package main

import (
	"bytes"
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	kubescheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	remotecommand "k8s.io/client-go/tools/remotecommand"
)

// podGVR — core/v1 Pods, for the PodLister.
var podGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}

// spdyPodExecutor implements switchover.PodExecutor via remotecommand SPDY.
type spdyPodExecutor struct {
	cfg       *rest.Config
	clientset kubernetes.Interface
}

// newSPDYPodExecutor builds the executor from the in-cluster REST config.
func newSPDYPodExecutor(cfg *rest.Config) (*spdyPodExecutor, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("raftexec: clientset: %w", err)
	}
	return &spdyPodExecutor{cfg: cfg, clientset: cs}, nil
}

// Exec runs `command` in (namespace/pod/container) and returns combined
// stdout+stderr. A non-zero exit surfaces as a non-nil error (the SDK
// returns a CodeExitError) — the caller's wrapper includes the output so
// the operator sees exactly what the bao binary rejected.
func (e *spdyPodExecutor) Exec(ctx context.Context, namespace, pod, container string, command []string) (string, error) {
	req := e.clientset.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Namespace(namespace).
		Name(pod).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, kubescheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(e.cfg, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("raftexec: NewSPDYExecutor: %w", err)
	}
	var stdout, stderr bytes.Buffer
	streamErr := exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
		Tty:    false,
	})
	out := joinExecOutput(&stdout, &stderr)
	if streamErr != nil {
		return out, streamErr
	}
	return out, nil
}

// RestartPod implements switchover.PodRestarter — deletes (namespace/pod); the
// owning StatefulSet recreates it with the same name + PVC. Used by the
// raft-transition peers.json recovery (#3492): after the peers.json write the
// survivor Pod must restart so openbao re-reads peers.json on boot and
// self-elects as the sole leader (peers.json is consumed at process start, not
// live). A not-found delete is treated as success (already gone → will be
// recreated). bp-continuum's ServiceAccount needs pods/delete RBAC in the
// target namespace.
func (e *spdyPodExecutor) RestartPod(ctx context.Context, namespace, pod string) error {
	err := e.clientset.CoreV1().Pods(namespace).Delete(ctx, pod, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("raftexec: delete pod %s/%s: %w", namespace, pod, err)
	}
	return nil
}

// dynamicPodLister implements switchover.PodLister via the dynamic client.
type dynamicPodLister struct {
	dyn dynamic.Interface
}

// newDynamicPodLister builds the lister from the dynamic client.
func newDynamicPodLister(dyn dynamic.Interface) *dynamicPodLister {
	return &dynamicPodLister{dyn: dyn}
}

// ReadyPods returns the names of Ready Pods in `namespace` matching
// `selector`, lexically sorted so promotion is deterministic.
func (l *dynamicPodLister) ReadyPods(ctx context.Context, namespace, selector string) ([]string, error) {
	list, err := l.dyn.Resource(podGVR).Namespace(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return nil, fmt.Errorf("raftexec: list pods (%s/%s): %w", namespace, selector, err)
	}
	var ready []string
	for i := range list.Items {
		pod := &corev1.Pod{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, pod); err != nil {
			continue
		}
		if isPodReady(pod) {
			ready = append(ready, pod.Name)
		}
	}
	sort.Strings(ready)
	return ready, nil
}

// isPodReady reports whether the Pod has condition Ready=True.
func isPodReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// joinExecOutput merges stdout+stderr (stderr after stdout, newline-joined).
func joinExecOutput(stdout, stderr *bytes.Buffer) string {
	var b bytes.Buffer
	if stdout != nil {
		b.Write(stdout.Bytes())
	}
	if stderr != nil && stderr.Len() > 0 {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.Write(stderr.Bytes())
	}
	return b.String()
}
