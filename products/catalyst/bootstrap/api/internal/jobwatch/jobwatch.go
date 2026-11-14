// Package jobwatch closes the runs of the Jobs page from the Job watch of
// the shared informer cache (internal/k8scache).
//
// A run of a Kubernetes Job is recorded by the seed as an Execution that
// carries the Job's metadata.uid. When the watch reports that Job with a
// terminal condition (Complete, SuccessCriteriaMet or Failed set to True),
// in an added, modified or deleted event, the run is closed with that
// outcome. The deleted event carries the last object the watch held, so a
// Job that finished and was then deleted (by its TTL or by a Helm hook
// policy) still closes its run.
//
// The rules:
//
//  1. The key is the Job's metadata.uid. Two Jobs with the same name in two
//     namespaces, or a Job deleted and created again under one name, are
//     different runs.
//  2. The outcome is the Job's own terminal condition, nothing else. A Job
//     that disappears without a terminal condition gives no verdict: its run
//     stays as it is.
//  3. The cluster id of the event is the deployment id of the jobs store.
//     An event of a cluster with no store index changes nothing.
package jobwatch

import (
	"context"
	"log/slog"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// kindJob is the k8scache kind name of batch/v1 Jobs.
const kindJob = "job"

// Source is the part of k8scache.Factory the watch uses.
type Source interface {
	Subscribe(user string, kinds map[string]struct{}) (<-chan k8scache.Event, func())
}

// Closer is the part of jobs.Store the watch uses.
type Closer interface {
	CloseJobRun(deploymentID, jobUID, status string, finishedAt time.Time) (bool, error)
}

// Run subscribes to the Job events of src and closes runs in st until ctx
// ends or the subscription channel closes.
func Run(ctx context.Context, src Source, st Closer, log *slog.Logger) {
	ch, cancel := src.Subscribe("jobwatch", map[string]struct{}{kindJob: {}})
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			Handle(ev, st, log)
		}
	}
}

// Handle applies one event. It reports whether a run was closed.
func Handle(ev k8scache.Event, st Closer, log *slog.Logger) bool {
	if ev.Kind != kindJob || ev.Object == nil {
		return false
	}
	uid := string(ev.Object.GetUID())
	if uid == "" {
		return false
	}
	status, at, ok := Outcome(ev)
	if !ok {
		return false
	}
	closed, err := st.CloseJobRun(ev.Cluster, uid, status, at)
	if err != nil {
		if log != nil {
			log.Warn("jobwatch: close run failed",
				"cluster", ev.Cluster, "namespace", ev.Object.GetNamespace(), "job", ev.Object.GetName(), "err", err)
		}
		return false
	}
	if closed && log != nil {
		log.Info("jobwatch: run closed from the Job watch",
			"cluster", ev.Cluster, "namespace", ev.Object.GetNamespace(), "job", ev.Object.GetName(),
			"status", status, "event", string(ev.Type))
	}
	return closed
}

// Outcome is the terminal outcome the event's Job object records: the jobs
// status (jobs.StatusSucceeded or jobs.StatusFailed) and the time of the
// terminal condition, or of the event when the condition has no time.
// ok is false when the Job has no terminal condition.
func Outcome(ev k8scache.Event) (status string, at time.Time, ok bool) {
	st, _, finished := helmwatch.JobRunStatus(ev.Object)
	switch st {
	case helmwatch.ObsStatusSucceeded:
		status = jobs.StatusSucceeded
	case helmwatch.ObsStatusFailed:
		status = jobs.StatusFailed
	default:
		return "", time.Time{}, false
	}
	if finished.IsZero() {
		finished = ev.At
	}
	return status, finished.UTC(), true
}
