// helm-controller log tailer.
//
// Tails the helm-controller Pod's logs in flux-system, parses each
// line for "<helmrelease-name> <release-namespace>" hints (the
// helm-controller logger always tags messages with the release it's
// working on), and emits one phase: "component-log" Event per line
// with Component set to the matched bp-* name.
//
// Why a stream not a one-shot Get: helm-controller's logs flow as
// long as it has work to do. The Sovereign Admin's Logs tab needs
// live tailing — a snapshot would miss every line emitted after the
// page loaded.
//
// Why one tailer for all components instead of per-component
// streams: helm-controller is a single Deployment with a single Pod.
// Asking the apiserver for N parallel log streams against the same
// Pod just multiplies the bytes off the wire. We attach once, parse
// each line for the bp-* token, and route in-process.
package helmwatch

import (
	"bufio"
	"context"
	"io"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// helmControllerNameRe — extracts the bp-<name> token from a
// helm-controller log line. helm-controller's log shape varies by
// version + the configured logger; we observe THREE stable formats
// in production against flux v2.4 (the version Catalyst-Zero pins):
//
//   - flat-string structured JSON:
//       `... "helmrelease":"flux-system/bp-cilium" ...`
//   - flat-string logr/klog text:
//       `... helmrelease="flux-system/bp-cilium" ...`
//   - NESTED-OBJECT structured JSON (live shape on otech.omani.works):
//       `... "HelmRelease":{"name":"bp-cilium","namespace":"flux-system"}, ...`
//
// The third shape is what flux v2.4's helm-controller actually emits
// in production. The first two are still observed in legacy / debug
// modes and on older fluxes. The regex alternates across all three so
// every line is parsed correctly. A future helm-controller release
// that switches to a fourth shape lands here as a test failure on the
// fixtures in logtailer_test.go.
//
// Issue #305: without the nested-object alternation, every raw
// helm-controller line was silently dropped — the GitLab-style log
// viewer only ever rendered synthetic [seeded] / [<state>] anchor
// lines.
var helmControllerNameRe = regexp.MustCompile(
	// Alternative 1+2 — flat string `helmrelease[":=]flux-system/bp-X`
	`(?:` +
		`(?:helmrelease|HelmRelease)["']?\s*[:=]\s*["']?` +
		regexp.QuoteMeta(FluxNamespace) + `/(bp-[a-z0-9-]+)` +
		`)|(?:` +
		// Alternative 3 — nested object `"HelmRelease":{...,"name":"bp-X",...}`
		`["']?HelmRelease["']?\s*:\s*\{[^}]*?["']?name["']?\s*:\s*["'](bp-[a-z0-9-]+)["']` +
		`)`,
)

type logTailer struct {
	client kubernetes.Interface
	emit   Emit
	now    func() time.Time
}

func newLogTailer(client kubernetes.Interface, emit Emit, now func() time.Time) *logTailer {
	return &logTailer{
		client: client,
		emit:   emit,
		now:    now,
	}
}

// run finds the helm-controller Pod and follows its logs until ctx
// fires. On Pod restart we re-discover and re-attach (helm-controller
// is a single-replica Deployment by default — restarts during Phase 1
// are rare but possible).
func (t *logTailer) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		pod, err := t.findHelmControllerPod(ctx)
		if err != nil {
			// Backoff and retry. helm-controller is part of bp-flux,
			// so it's possible we attach before bp-flux installed —
			// the watch loop's other path (the dynamic informer) is
			// still running, this side just waits.
			t.sleep(ctx, 5*time.Second)
			continue
		}

		if err := t.tailPod(ctx, pod); err != nil && ctx.Err() == nil {
			// Tail closed but ctx is still live — Pod likely got
			// rescheduled. Reattach.
			t.sleep(ctx, 2*time.Second)
		}
	}
}

func (t *logTailer) findHelmControllerPod(ctx context.Context) (*corev1.Pod, error) {
	pods, err := t.client.CoreV1().Pods(FluxNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: HelmControllerSelector,
	})
	if err != nil {
		return nil, err
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.Phase == corev1.PodRunning {
			return p, nil
		}
	}
	return nil, errorPodNotReady
}

// tailPod opens a follow=true log stream against the pod and pumps
// each line through the emit callback as a phase: "component-log"
// Event keyed by the bp-* token in the line.
func (t *logTailer) tailPod(ctx context.Context, pod *corev1.Pod) error {
	req := t.client.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
		Follow:    true,
		TailLines: ptrInt64(0),
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()
	return t.pumpLines(ctx, stream)
}

// pumpLines is split out so tests can drive the parser on a raw
// io.Reader without standing up a Pod.
//
// Context handling: bufio.Scanner.Scan() blocks on the underlying
// reader, so an in-flight scanner cannot poll ctx.Done() between
// lines. We spawn a watchdog that closes the stream when ctx fires,
// which causes Scan to return false at the next read. If the stream
// is already an io.ReadCloser (the kubernetes log stream is — see
// CoreV1().Pods().GetLogs().Stream), we close that side; if not (the
// test passes a strings.Reader for example), we still respect ctx
// best-effort by checking ctx.Done() between lines, accepting that a
// quiet stream will lag behind ctx by one read.
func (t *logTailer) pumpLines(ctx context.Context, stream io.Reader) error {
	if closer, ok := stream.(io.Closer); ok {
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			select {
			case <-ctx.Done():
				_ = closer.Close()
			case <-stop:
			}
		}()
	}

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		match := helmControllerNameRe.FindStringSubmatch(line)
		if len(match) < 3 {
			// Not associated with a bp-* HelmRelease — skip. The
			// Sovereign Admin's Logs tab filters by component, so
			// noise from the helm-controller's leader-election or
			// startup chatter would render as "logs for no
			// component," which is not useful.
			continue
		}
		// The regex has two alternatives, each with its own capture
		// group; exactly one is populated per match. Pick whichever
		// fired so the downstream emit doesn't see an empty string.
		bpName := match[1]
		if bpName == "" {
			bpName = match[2]
		}
		if bpName == "" {
			continue
		}
		componentID := ComponentIDFromHelmRelease(bpName)
		t.emit(provisioner.Event{
			Time:      t.now().UTC().Format(time.RFC3339),
			Phase:     PhaseComponentLog,
			Level:     levelFromLogLine(line),
			Component: componentID,
			Message:   line,
		})
	}
	// scanner.Err() returns nil on EOF or a closed stream (the
	// watchdog's path); a non-nil error is a real read failure.
	return scanner.Err()
}

// levelFromLogLine — coarse classifier. helm-controller uses
// logr-style level tags `level=info` / `level=error`; we surface
// error and warn explicitly, default to info.
func levelFromLogLine(line string) string {
	low := strings.ToLower(line)
	switch {
	case strings.Contains(low, "level=error"), strings.Contains(low, `"level":"error"`):
		return "error"
	case strings.Contains(low, "level=warn"), strings.Contains(low, `"level":"warn"`):
		return "warn"
	default:
		return "info"
	}
}

func (t *logTailer) sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func ptrInt64(v int64) *int64 { return &v }

// errorPodNotReady — sentinel returned when no Running helm-controller
// Pod is in flux-system yet (early in Phase 1, before bp-flux installs).
// The tailer's outer loop treats this as retryable.
type podNotReadyError struct{}

func (podNotReadyError) Error() string {
	return "helm-controller pod not yet running in flux-system"
}

var errorPodNotReady = podNotReadyError{}
