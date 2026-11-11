package handler

import (
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Each Primary move is its own Jobs group (Refs #7067: the move back reused
// the first move's ids and never appeared in Jobs).
func TestJobsProjection_EveryTransitionIsItsOwnGroup(t *testing.T) {
	store, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cont := func(tid, ts string) unstructured.Unstructured {
		return unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "dr.openova.io/v1", "kind": "Continuum",
			"metadata": map[string]interface{}{"name": "dr-blog", "namespace": "acme"},
			"status": map[string]interface{}{"activity": []interface{}{
				map[string]interface{}{"id": "traffic-off-acme-blog", "phase": "traffic-off", "member": "blog", "transition": tid,
					"state": "succeeded", "startedAt": ts, "finishedAt": ts},
			}},
		}}
	}
	for _, c := range []unstructured.Unstructured{
		cont("a-to-b-1794390000", "2026-11-11T10:00:00Z"),
		cont("b-to-a-1794391000", "2026-11-11T10:20:00Z"),
	} {
		if err := projectApplicationObjects(store, "dep", nil, []unstructured.Unstructured{c}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := store.ListJobs("dep")
	groups := map[string]bool{}
	for _, j := range all {
		if j.Type == jobs.JobTypeGroup && strings.HasPrefix(j.JobName, jobs.TopologyTransitionGroupPrefix) {
			groups[j.JobName] = true
		}
	}
	for _, want := range []string{
		jobs.TopologyTransitionGroupPrefix + "acme-dr-blog-a-to-b-1794390000",
		jobs.TopologyTransitionGroupPrefix + "acme-dr-blog-b-to-a-1794391000",
	} {
		if !groups[want] {
			t.Errorf("missing group %q; have %v", want, groups)
		}
	}
}
