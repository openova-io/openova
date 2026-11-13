package handler

// Epic #6703 P4b — ad-hoc CronJob schedules ("New schedule") from the console.
//
// The contract under test:
//   - POST clones the SOURCE CronJob's jobTemplate verbatim, carries the
//     source's release-attribution labels (the keys Kyverno's flux-managed
//     policy admits on), is honest about who manages it (catalyst-api, never
//     flux), marks itself schedule-origin=console, and own-references the
//     source so a Blueprint uninstall GCs it;
//   - PATCH / DELETE work ONLY on console-origin schedules — a chart-declared
//     CronJob answers 409 not-console-schedule, because it changes in Git;
//   - a bad name / schedule / zone is a 400 with a readable reason, not an
//     apiserver Invalid.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

type scheduleRig struct {
	h    *Handler
	dyn  *dynamicfake.FakeDynamicClient
	stop func()
}

// newScheduleRig is a k8scache rig with batch/v1 CronJob + Job registered
// (the resource rig in k8s_resource_test.go knows only pods/configmaps).
func newScheduleRig(t *testing.T, objs ...runtime.Object) *scheduleRig {
	t.Helper()
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJobList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJob"}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "JobList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}, &unstructured.Unstructured{})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		helmwatch.CronJobGVR: "CronJobList",
		helmwatch.JobGVR:     "JobList",
	}, objs...)
	reg := k8scache.NewRegistry()
	_ = reg.Add(k8scache.Kind{Name: "cronjob", GVR: helmwatch.CronJobGVR, Namespaced: true})
	_ = reg.Add(k8scache.Kind{Name: "job", GVR: helmwatch.JobGVR, Namespaced: true})
	factory, err := k8scache.NewFactory(k8scache.Config{
		Logger:   quietK8sResourceLogger(),
		Registry: reg,
		Clusters: []k8scache.ClusterRef{{ID: "alpha", DynamicClient: dyn, CoreClient: kfake.NewSimpleClientset()}},
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	if err := factory.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, err := factory.List("alpha", "cronjob", labels.Everything()); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	h := NewWithPDM(quietK8sResourceLogger(), &fakePDM{})
	h.SetK8sCache(factory, k8scache.NewSARCache(), "X-Forwarded-User")
	return &scheduleRig{h: h, dyn: dyn, stop: factory.Stop}
}

func (rig *scheduleRig) router() chi.Router {
	rt := chi.NewRouter()
	rt.Post("/api/v1/sovereigns/{id}/schedules", rig.h.HandleScheduleCreate)
	rt.Patch("/api/v1/sovereigns/{id}/schedules/{ns}/{name}", rig.h.HandleScheduleUpdate)
	rt.Delete("/api/v1/sovereigns/{id}/schedules/{ns}/{name}", rig.h.HandleScheduleDelete)
	return rt
}

func jsonReq(method, url string, body any) *http.Request {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Email", "owner@hw307.omani.works")
	return req
}

// A Blueprint CronJob as the chart ships it (shape measured on hw307
// openbao/openbao-snapshot-save, 2026-11-05).
func chartCronJob(ns, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "CronJob",
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
			"uid":       "uid-" + name,
			"labels": map[string]any{
				"app.kubernetes.io/managed-by":     "Helm",
				"catalyst.openova.io/blueprint":    "bp-openbao",
				"catalyst.openova.io/component":    "openbao-snapshot-replication",
				"helm.toolkit.fluxcd.io/name":      "bp-openbao",
				"helm.toolkit.fluxcd.io/namespace": "flux-system",
			},
			"annotations": map[string]any{
				"meta.helm.sh/release-name":      "bp-openbao",
				"meta.helm.sh/release-namespace": "openbao",
			},
		},
		"spec": map[string]any{
			"schedule":                   "*/10 * * * *",
			"concurrencyPolicy":          "Forbid",
			"startingDeadlineSeconds":    int64(120),
			"successfulJobsHistoryLimit": int64(3),
			"failedJobsHistoryLimit":     int64(3),
			"jobTemplate": map[string]any{
				"metadata": map[string]any{
					"labels": map[string]any{"app.kubernetes.io/managed-by": "flux", "catalyst.openova.io/blueprint": "bp-openbao"},
				},
				"spec": map[string]any{
					"template": map[string]any{
						"spec": map[string]any{
							"serviceAccountName": "openbao-snapshot",
							"restartPolicy":      "Never",
							"containers":         []any{map[string]any{"name": "snap", "image": "registry.hw307.omani.works/proxy/library/busybox:1.36"}},
						},
					},
				},
			},
		},
	}}
}

func TestScheduleCreate_ClonesSourceTemplateWithHonestOwnershipLabels_6703(t *testing.T) {
	rig := newScheduleRig(t, chartCronJob("openbao", "openbao-snapshot-save"))
	defer rig.stop()

	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, jsonReq(http.MethodPost, "/api/v1/sovereigns/alpha/schedules", map[string]any{
		"sourceNamespace": "openbao",
		"sourceName":      "openbao-snapshot-save",
		"name":            "openbao-snapshot-noon",
		"schedule":        "0 12 * * 1-5",
		"timeZone":        "Asia/Muscat",
	}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d; body=%s", rec.Code, rec.Body.String())
	}
	var resp scheduleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Origin != "console" || resp.SourceName != "openbao-snapshot-save" || resp.SourceNamespace != "openbao" || resp.Schedule != "0 12 * * 1-5" || resp.TimeZone != "Asia/Muscat" {
		t.Fatalf("response = %+v", resp)
	}

	got, err := rig.dyn.Resource(helmwatch.CronJobGVR).Namespace("openbao").Get(context.Background(), "openbao-snapshot-noon", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("clone not created: %v", err)
	}
	lbl := got.GetLabels()
	// Honest ownership: the console's, never flux's.
	if lbl["app.kubernetes.io/managed-by"] != scheduleManagedBy {
		t.Fatalf("managed-by = %q, want %q (a console schedule must not claim to be Flux's)", lbl["app.kubernetes.io/managed-by"], scheduleManagedBy)
	}
	if lbl[scheduleOriginLabel] != scheduleOriginConsole || lbl[scheduleSourceLabel] != "openbao-snapshot-save" {
		t.Fatalf("origin/source labels missing: %v", lbl)
	}
	// Release attribution carried (what the Kyverno flux-managed policy admits on).
	if lbl["helm.toolkit.fluxcd.io/name"] != "bp-openbao" || lbl["catalyst.openova.io/blueprint"] != "bp-openbao" {
		t.Fatalf("attribution labels not carried: %v", lbl)
	}
	// Helm's adoption annotations are NOT carried — the clone is not Helm's.
	if _, leaked := got.GetAnnotations()["meta.helm.sh/release-name"]; leaked {
		t.Fatalf("meta.helm.sh/* must not be copied onto the clone: %v", got.GetAnnotations())
	}
	if got.GetAnnotations()[scheduleCreatedByAnnotation] != "owner@hw307.omani.works" {
		t.Fatalf("created-by audit annotation missing: %v", got.GetAnnotations())
	}
	// Template verbatim.
	sa, _, _ := unstructured.NestedString(got.Object, "spec", "jobTemplate", "spec", "template", "spec", "serviceAccountName")
	if sa != "openbao-snapshot" {
		t.Fatalf("jobTemplate not cloned verbatim: serviceAccountName = %q", sa)
	}
	cp, _, _ := unstructured.NestedString(got.Object, "spec", "concurrencyPolicy")
	if cp != "Forbid" {
		t.Fatalf("concurrencyPolicy not inherited: %q", cp)
	}
	sus, _, _ := unstructured.NestedBool(got.Object, "spec", "suspend")
	if sus {
		t.Fatalf("suspend must default to false")
	}
	// Owner-referenced to the source so a Blueprint uninstall GCs it.
	refs := got.GetOwnerReferences()
	if len(refs) != 1 || refs[0].Kind != "CronJob" || refs[0].Name != "openbao-snapshot-save" || string(refs[0].UID) != "uid-openbao-snapshot-save" {
		t.Fatalf("ownerReference to the source missing: %+v", refs)
	}
}

func TestScheduleCreate_RejectsBadInputWithReadableReasons_6703(t *testing.T) {
	rig := newScheduleRig(t, chartCronJob("openbao", "openbao-snapshot-save"))
	defer rig.stop()
	base := map[string]any{"sourceNamespace": "openbao", "sourceName": "openbao-snapshot-save"}
	cases := []struct {
		name     string
		over     map[string]any
		wantCode int
		wantErr  string
	}{
		{"bad cron field count", map[string]any{"name": "x", "schedule": "0 12 * *"}, 400, "invalid-schedule"},
		{"out of range", map[string]any{"name": "x", "schedule": "0 25 * * *"}, 400, "invalid-schedule"},
		{"reboot macro", map[string]any{"name": "x", "schedule": "@reboot"}, 400, "invalid-schedule"},
		{"uppercase name", map[string]any{"name": "Noon", "schedule": "0 12 * * *"}, 400, "invalid-name"},
		{"too long name", map[string]any{"name": strings.Repeat("a", 53), "schedule": "0 12 * * *"}, 400, "invalid-name"},
		{"same name as source", map[string]any{"name": "openbao-snapshot-save", "schedule": "0 12 * * *"}, 400, "invalid-name"},
		{"unknown zone", map[string]any{"name": "x", "schedule": "0 12 * * *", "timeZone": "Mars/Olympus"}, 400, "invalid-timezone"},
		{"unknown source", map[string]any{"sourceName": "ghost", "name": "x", "schedule": "0 12 * * *"}, 404, "source-not-found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{}
			for k, v := range base {
				body[k] = v
			}
			for k, v := range tc.over {
				body[k] = v
			}
			rec := httptest.NewRecorder()
			rig.router().ServeHTTP(rec, jsonReq(http.MethodPost, "/api/v1/sovereigns/alpha/schedules", body))
			if rec.Code != tc.wantCode || !strings.Contains(rec.Body.String(), tc.wantErr) {
				t.Fatalf("want %d %s, got %d %s", tc.wantCode, tc.wantErr, rec.Code, rec.Body.String())
			}
		})
	}
	// Valid shapes the validator must NOT refuse (names + steps + macro).
	for _, ok := range []string{"@daily", "30 2 * * *", "*/10 * * * *", "0 0 1 jan,jul *", "0 9-17/2 * * mon-fri", "0,30 * * * 0"} {
		if err := validateCronSchedule(ok); err != nil {
			t.Fatalf("validateCronSchedule(%q) must accept: %v", ok, err)
		}
	}
}

func TestScheduleUpdateAndDelete_OnlyConsoleSchedules_6703(t *testing.T) {
	src := chartCronJob("openbao", "openbao-snapshot-save")
	rig := newScheduleRig(t, src)
	defer rig.stop()
	rt := rig.router()

	// A chart-declared CronJob cannot be edited or deleted here.
	for _, m := range []string{http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, jsonReq(m, "/api/v1/sovereigns/alpha/schedules/openbao/openbao-snapshot-save", map[string]any{"suspend": true}))
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not-console-schedule") {
			t.Fatalf("%s on a chart CronJob: want 409 not-console-schedule, got %d %s", m, rec.Code, rec.Body.String())
		}
	}
	if _, err := rig.dyn.Resource(helmwatch.CronJobGVR).Namespace("openbao").Get(context.Background(), "openbao-snapshot-save", metav1.GetOptions{}); err != nil {
		t.Fatalf("chart CronJob must survive the refused delete: %v", err)
	}

	// Create a console schedule, then edit + delete it.
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, jsonReq(http.MethodPost, "/api/v1/sovereigns/alpha/schedules", map[string]any{
		"sourceNamespace": "openbao", "sourceName": "openbao-snapshot-save", "name": "snap-noon", "schedule": "0 12 * * *",
	}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	rt.ServeHTTP(rec, jsonReq(http.MethodPatch, "/api/v1/sovereigns/alpha/schedules/openbao/snap-noon", map[string]any{
		"schedule": "0 13 * * *", "suspend": true, "timeZone": "Europe/Istanbul",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := rig.dyn.Resource(helmwatch.CronJobGVR).Namespace("openbao").Get(context.Background(), "snap-noon", metav1.GetOptions{})
	sched, _, _ := unstructured.NestedString(got.Object, "spec", "schedule")
	sus, _, _ := unstructured.NestedBool(got.Object, "spec", "suspend")
	tz, _, _ := unstructured.NestedString(got.Object, "spec", "timeZone")
	if sched != "0 13 * * *" || !sus || tz != "Europe/Istanbul" {
		t.Fatalf("patch not applied: schedule=%q suspend=%v tz=%q", sched, sus, tz)
	}

	// An invalid schedule on PATCH is refused and the object is untouched.
	rec = httptest.NewRecorder()
	rt.ServeHTTP(rec, jsonReq(http.MethodPatch, "/api/v1/sovereigns/alpha/schedules/openbao/snap-noon", map[string]any{"schedule": "every day"}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad patch: want 400, got %d %s", rec.Code, rec.Body.String())
	}
	// An empty PATCH is a 400 too.
	rec = httptest.NewRecorder()
	rt.ServeHTTP(rec, jsonReq(http.MethodPatch, "/api/v1/sovereigns/alpha/schedules/openbao/snap-noon", map[string]any{}))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "empty-update") {
		t.Fatalf("empty patch: want 400 empty-update, got %d %s", rec.Code, rec.Body.String())
	}
	// clearTimeZone removes the zone.
	rec = httptest.NewRecorder()
	rt.ServeHTTP(rec, jsonReq(http.MethodPatch, "/api/v1/sovereigns/alpha/schedules/openbao/snap-noon", map[string]any{"clearTimeZone": true}))
	if rec.Code != http.StatusOK {
		t.Fatalf("clearTimeZone: %d %s", rec.Code, rec.Body.String())
	}
	got, _ = rig.dyn.Resource(helmwatch.CronJobGVR).Namespace("openbao").Get(context.Background(), "snap-noon", metav1.GetOptions{})
	if _, has, _ := unstructured.NestedString(got.Object, "spec", "timeZone"); has {
		t.Fatalf("timeZone must be removed by clearTimeZone")
	}

	rec = httptest.NewRecorder()
	rt.ServeHTTP(rec, jsonReq(http.MethodDelete, "/api/v1/sovereigns/alpha/schedules/openbao/snap-noon", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deleted":true`) {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := rig.dyn.Resource(helmwatch.CronJobGVR).Namespace("openbao").Get(context.Background(), "snap-noon", metav1.GetOptions{}); err == nil {
		t.Fatalf("console schedule must be gone after DELETE")
	}
	// Deleting again → 404, not a silent 200.
	rec = httptest.NewRecorder()
	rt.ServeHTTP(rec, jsonReq(http.MethodDelete, "/api/v1/sovereigns/alpha/schedules/openbao/snap-noon", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second delete: want 404, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestScheduleCreate_DuplicateNameIs409_6703(t *testing.T) {
	rig := newScheduleRig(t, chartCronJob("openbao", "openbao-snapshot-save"))
	defer rig.stop()
	rt := rig.router()
	body := map[string]any{"sourceNamespace": "openbao", "sourceName": "openbao-snapshot-save", "name": "snap-noon", "schedule": "0 12 * * *"}
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, jsonReq(http.MethodPost, "/api/v1/sovereigns/alpha/schedules", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("first create: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	rt.ServeHTTP(rec, jsonReq(http.MethodPost, "/api/v1/sovereigns/alpha/schedules", body))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "schedule-exists") {
		t.Fatalf("duplicate create: want 409 schedule-exists, got %d %s", rec.Code, rec.Body.String())
	}
}
