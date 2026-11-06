package handler

import (
	"bufio"
	"context"
	"io"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// cutoverStepLogSink receives the live output lines of a cutover step's
// batch/v1 Job. The activity bridge implements it; tests substitute a
// recorder.
type cutoverStepLogSink interface {
	AppendStepLog(slug string, lines []jobs.LogLine) error
}

// tailCutoverStepJobLogs follows the logs of the Pod(s) the step's Job
// runs and appends each line to the step's projected Execution, so the
// /jobs log panel shows what the step is actually doing. It returns when
// ctx ends. Best-effort: a missing bridge, an unscheduled Pod or a
// broken stream never fails the step — the Job's own condition decides
// that.
func (h *Handler) tailCutoverStepJobLogs(ctx context.Context, deps *cutoverDeps, stepSlug, jobName string) {
	ab := h.cutoverActivityBridge()
	if ab == nil || deps == nil || deps.core == nil {
		return
	}
	tailCutoverJobLogsTo(ctx, deps, ab, stepSlug, jobName)
}

func tailCutoverJobLogsTo(ctx context.Context, deps *cutoverDeps, sink cutoverStepLogSink, stepSlug, jobName string) {
	followed := map[string]bool{}
	for {
		pods, err := deps.core.CoreV1().Pods(deps.ns).List(ctx, metav1.ListOptions{
			LabelSelector: "job-name=" + jobName,
		})
		if err == nil {
			for _, p := range pods.Items {
				if followed[p.Name] || !podHasStarted(&p) {
					continue
				}
				followed[p.Name] = true
				streamPodLogLines(ctx, deps, sink, stepSlug, p.Name)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func podHasStarted(p *corev1.Pod) bool {
	return p.Status.Phase == corev1.PodRunning || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
}

// streamPodLogLines follows one Pod's log until it ends or ctx is done,
// flushing lines to the sink in small batches.
func streamPodLogLines(ctx context.Context, deps *cutoverDeps, sink cutoverStepLogSink, stepSlug, podName string) {
	rc, err := deps.core.CoreV1().Pods(deps.ns).GetLogs(podName, &corev1.PodLogOptions{Follow: true}).Stream(ctx)
	if err != nil {
		return
	}
	defer rc.Close()
	pumpLogLines(rc, sink, stepSlug)
}

func pumpLogLines(r io.Reader, sink cutoverStepLogSink, stepSlug string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		_ = sink.AppendStepLog(stepSlug, []jobs.LogLine{{
			Timestamp: time.Now().UTC(),
			Level:     cutoverLogLevel(line),
			Message:   line,
		}})
	}
}

// cutoverLogLevel maps the step scripts' plain-text markers onto the
// panel's level filter so FATAL/ERROR/WARN lines are findable.
func cutoverLogLevel(line string) string {
	u := strings.ToUpper(line)
	switch {
	case strings.Contains(u, "FATAL"), strings.Contains(u, "ERROR"):
		return jobs.LevelError
	case strings.Contains(u, "WARN"):
		return jobs.LevelWarn
	default:
		return jobs.LevelInfo
	}
}
