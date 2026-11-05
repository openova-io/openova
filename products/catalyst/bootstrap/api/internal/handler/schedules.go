// Package handler — schedules.go: ad-hoc CronJob schedules from the
// sovereign-admin console (epic #6703 P4b, "New schedule").
//
// REST surface:
//
//	POST   /api/v1/sovereigns/{id}/schedules                — create an ad-hoc schedule
//	PATCH  /api/v1/sovereigns/{id}/schedules/{ns}/{name}    — change schedule / timeZone / suspend
//	DELETE /api/v1/sovereigns/{id}/schedules/{ns}/{name}    — delete an ad-hoc schedule
//
// # What an ad-hoc schedule IS
//
// A real batch/v1 CronJob, nothing synthetic (GLOSSARY: real engine names
// throughout). It is CLONED from an existing CronJob's jobTemplate — the
// operator picks the work (a Blueprint's cron: openbao-snapshot-save,
// syft-grype, …) and gives it a second cadence ("also at 12:00 on
// weekdays"). The console never authors a pod spec: the template, its
// images, service account and secrets are the source's, verbatim, so the
// clone can only ever do what the source already does.
//
// # Why these labels, exactly
//
// The platform's Kyverno `flux-managed` ClusterPolicy is Enforce on
// CronJob and admits an object only when it carries
// `app.kubernetes.io/managed-by: flux` OR a `helm.toolkit.fluxcd.io/name`
// / `kustomize.toolkit.fluxcd.io/name` label. A console-created CronJob is
// NOT Flux-managed — saying so would be a lie that also routes every later
// edit through the Gitea-PR path (isFluxManaged) — so it carries
// `app.kubernetes.io/managed-by: catalyst-api` plus the source's RELEASE
// ATTRIBUTION labels (helm.toolkit.fluxcd.io/{name,namespace},
// catalyst.openova.io/{blueprint,component}, …): true statements about
// where the work comes from, and the ones the policy admits on. Two
// origin labels mark it as the console's own:
//
//	catalyst.openova.io/schedule-origin: console
//	catalyst.openova.io/schedule-source: <source CronJob name>
//
// Only a CronJob carrying schedule-origin=console may be edited or deleted
// here. A chart-declared CronJob is owned by its Blueprint and changes in
// Git (the HelmRelease values), never from this surface — the edit/delete
// handlers answer 409 not-console-schedule for it.
//
// # Lifecycle
//
// The clone own-references its source (non-controller, like `kubectl
// create job --from`), so uninstalling the Blueprint GCs every ad-hoc
// schedule derived from it — a clone of a template whose images and
// secrets are gone could only fail on every fire.
package handler

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
)

// ── Contract constants ──────────────────────────────────────────────

const (
	// scheduleOriginLabel marks a CronJob the console created; only these
	// are editable/deletable through this surface.
	scheduleOriginLabel   = "catalyst.openova.io/schedule-origin"
	scheduleOriginConsole = "console"
	// scheduleSourceLabel names the CronJob whose jobTemplate was cloned.
	scheduleSourceLabel = "catalyst.openova.io/schedule-source"
	// scheduleManagedBy is the honest managed-by for a console-owned
	// CronJob (never "flux" — see the package comment).
	scheduleManagedBy = "catalyst-api"
	// scheduleCreatedByAnnotation / scheduleSourceAnnotation — audit.
	scheduleCreatedByAnnotation = "catalyst.openova.io/schedule-created-by"
	scheduleSourceAnnotation    = "catalyst.openova.io/schedule-source"

	// maxCronJobNameLen — the apiserver's own limit for a CronJob name
	// (52), so the controller's "<name>-<10-digit minute>" child Job
	// names fit the 63-char label limit.
	maxCronJobNameLen = 52
)

// scheduleAttributionLabels are the source CronJob's metadata labels that
// are copied onto a clone: release attribution (what Blueprint/Helm
// release the work belongs to) — true of the clone too, and the keys the
// flux-managed policy admits on. Ownership markers (`app.kubernetes.io/
// managed-by`, the `meta.helm.sh/*` annotations) are NOT copied: Helm's
// adoption logic keys on those, and the clone is not Helm's.
var scheduleAttributionLabels = []string{
	"helm.toolkit.fluxcd.io/name",
	"helm.toolkit.fluxcd.io/namespace",
	"kustomize.toolkit.fluxcd.io/name",
	"kustomize.toolkit.fluxcd.io/namespace",
	"catalyst.openova.io/blueprint",
	"catalyst.openova.io/component",
	"app.kubernetes.io/name",
	"app.kubernetes.io/instance",
	"app.kubernetes.io/part-of",
}

// scheduleSpecFieldsToCopy are the source CronJob spec fields a clone
// inherits besides jobTemplate. schedule / timeZone / suspend are the
// clone's own.
var scheduleSpecFieldsToCopy = []string{
	"concurrencyPolicy",
	"startingDeadlineSeconds",
	"successfulJobsHistoryLimit",
	"failedJobsHistoryLimit",
}

// ── Wire shapes ─────────────────────────────────────────────────────

// scheduleCreateRequest is the body of POST /schedules.
type scheduleCreateRequest struct {
	// SourceNamespace / SourceName — the CronJob whose jobTemplate is
	// cloned. The clone lands in the SAME namespace (the template's
	// service account, secrets and config live there).
	SourceNamespace string `json:"sourceNamespace"`
	SourceName      string `json:"sourceName"`
	// Name — the new CronJob's name (DNS-1123 label, ≤52 chars).
	Name string `json:"name"`
	// Schedule — standard 5-field cron or a @macro.
	Schedule string `json:"schedule"`
	// TimeZone — optional IANA zone for the schedule (batch/v1 timeZone).
	TimeZone string `json:"timeZone,omitempty"`
	// Suspend — create suspended (defaults to false).
	Suspend *bool `json:"suspend,omitempty"`
}

// scheduleUpdateRequest is the body of PATCH /schedules/{ns}/{name}. Every
// field is optional; absent fields are left as they are. TimeZone "" with
// ClearTimeZone=true removes the zone.
type scheduleUpdateRequest struct {
	Schedule      *string `json:"schedule,omitempty"`
	TimeZone      *string `json:"timeZone,omitempty"`
	Suspend       *bool   `json:"suspend,omitempty"`
	ClearTimeZone bool    `json:"clearTimeZone,omitempty"`
}

// scheduleResponse is the wire shape every handler here returns.
type scheduleResponse struct {
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	Schedule        string `json:"schedule"`
	TimeZone        string `json:"timeZone,omitempty"`
	Suspend         bool   `json:"suspend"`
	Origin          string `json:"origin"`
	SourceNamespace string `json:"sourceNamespace,omitempty"`
	SourceName      string `json:"sourceName,omitempty"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
	UID             string `json:"uid,omitempty"`
}

// ── Validation ──────────────────────────────────────────────────────

var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// validateScheduleName enforces the apiserver's CronJob name rules so the
// operator gets a 400 with a readable reason instead of an apiserver
// Invalid they cannot act on.
func validateScheduleName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if len(name) > maxCronJobNameLen {
		return fmt.Errorf("name %q is longer than %d characters (a CronJob name must leave room for its child Job suffix)", name, maxCronJobNameLen)
	}
	if !dns1123Label.MatchString(name) {
		return fmt.Errorf("name %q must be a DNS-1123 label: lowercase letters, digits and '-', starting and ending alphanumeric", name)
	}
	return nil
}

var cronMacros = map[string]struct{}{
	"@yearly": {}, "@annually": {}, "@monthly": {}, "@weekly": {},
	"@daily": {}, "@midnight": {}, "@hourly": {},
}

var cronMonthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}
var cronDowNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// validateCronSchedule checks a 5-field cron expression (or a supported
// @macro) the way the CronJob controller's parser will, so a typo is a 400
// here rather than an opaque apiserver Invalid. Grammar per field: `*`,
// a value, a range `a-b`, each optionally `/step`, comma-joined; month
// and day-of-week also accept their three-letter names. `@reboot` has no
// wall-clock schedule and is refused.
func validateCronSchedule(expr string) error {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return fmt.Errorf("schedule is required")
	}
	if strings.HasPrefix(expr, "@") {
		if _, ok := cronMacros[strings.ToLower(expr)]; ok {
			return nil
		}
		return fmt.Errorf("unsupported schedule macro %q", expr)
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fmt.Errorf("schedule %q must have 5 fields (minute hour day-of-month month day-of-week), got %d", expr, len(fields))
	}
	specs := []struct {
		label    string
		min, max int
		names    map[string]int
	}{
		{"minute", 0, 59, nil},
		{"hour", 0, 23, nil},
		{"day-of-month", 1, 31, nil},
		{"month", 1, 12, cronMonthNames},
		{"day-of-week", 0, 7, cronDowNames},
	}
	for i, f := range fields {
		if err := validateCronField(f, specs[i].min, specs[i].max, specs[i].names); err != nil {
			return fmt.Errorf("schedule %q: %s field: %w", expr, specs[i].label, err)
		}
	}
	return nil
}

func validateCronField(raw string, min, max int, names map[string]int) error {
	atoi := func(s string) (int, error) {
		if names != nil {
			if v, ok := names[strings.ToLower(s)]; ok {
				return v, nil
			}
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("%q is not a number", s)
		}
		return n, nil
	}
	for _, item := range strings.Split(raw, ",") {
		if item == "" {
			return fmt.Errorf("empty list item in %q", raw)
		}
		rangePart := item
		if slash := strings.IndexByte(item, '/'); slash >= 0 {
			rangePart = item[:slash]
			step, err := strconv.Atoi(item[slash+1:])
			if err != nil || step <= 0 {
				return fmt.Errorf("bad step in %q", item)
			}
		}
		if rangePart == "*" || rangePart == "?" {
			continue
		}
		lo, hi := rangePart, rangePart
		if dash := strings.IndexByte(rangePart, '-'); dash >= 0 {
			lo, hi = rangePart[:dash], rangePart[dash+1:]
		}
		a, err := atoi(lo)
		if err != nil {
			return err
		}
		b, err := atoi(hi)
		if err != nil {
			return err
		}
		if a < min || a > max || b < min || b > max {
			return fmt.Errorf("%q is outside %d-%d", item, min, max)
		}
		if a > b {
			return fmt.Errorf("inverted range %q", item)
		}
	}
	return nil
}

// validateTimeZone accepts "" (controller clock) or an IANA zone name.
func validateTimeZone(tz string) error {
	if tz == "" {
		return nil
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return fmt.Errorf("timeZone %q is not a known IANA zone", tz)
	}
	return nil
}

// ── Handlers ────────────────────────────────────────────────────────

// HandleScheduleCreate — POST /api/v1/sovereigns/{id}/schedules
func (h *Handler) HandleScheduleCreate(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		http.Error(w, "k8scache disabled", http.StatusServiceUnavailable)
		return
	}
	clusterID := strings.TrimSpace(chi.URLParam(r, "id"))
	if clusterID == "" {
		writeBadRequest(w, "missing-cluster", "id path parameter is required")
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)
	if !h.requireResourceMutationAuth(w, r) {
		return
	}
	var body scheduleCreateRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	body.SourceNamespace = strings.TrimSpace(body.SourceNamespace)
	body.SourceName = strings.TrimSpace(body.SourceName)
	body.Name = strings.TrimSpace(body.Name)
	body.Schedule = strings.TrimSpace(body.Schedule)
	body.TimeZone = strings.TrimSpace(body.TimeZone)
	if body.SourceNamespace == "" || body.SourceName == "" {
		writeBadRequest(w, "missing-source", "sourceNamespace and sourceName are required — an ad-hoc schedule clones an existing CronJob's jobTemplate")
		return
	}
	if err := validateScheduleName(body.Name); err != nil {
		writeBadRequest(w, "invalid-name", err.Error())
		return
	}
	if body.Name == body.SourceName {
		writeBadRequest(w, "invalid-name", "the new schedule needs a name different from its source CronJob")
		return
	}
	if err := validateCronSchedule(body.Schedule); err != nil {
		writeBadRequest(w, "invalid-schedule", err.Error())
		return
	}
	if err := validateTimeZone(body.TimeZone); err != nil {
		writeBadRequest(w, "invalid-timezone", err.Error())
		return
	}

	dyn, err := h.k8sCache.DynamicClientFor(clusterID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cluster-unavailable", "detail": err.Error()})
		return
	}
	source, err := dyn.Resource(helmwatch.CronJobGVR).Namespace(body.SourceNamespace).Get(r.Context(), body.SourceName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "source-not-found", "detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "source-get-failed", "detail": err.Error()})
		return
	}

	clone, err := buildScheduleClone(source, body, retryOperatorIdentity(auth.ClaimsFromContext(r.Context()), r))
	if err != nil {
		writeBadRequest(w, "invalid-source", err.Error())
		return
	}
	created, err := dyn.Resource(helmwatch.CronJobGVR).Namespace(body.SourceNamespace).Create(r.Context(), clone, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "schedule-exists", "detail": err.Error()})
			return
		}
		writeK8sUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, scheduleResponseFrom(created))
}

// buildScheduleClone assembles the ad-hoc CronJob from the source and the
// request. Pure — unit-tested without a client.
func buildScheduleClone(source *unstructured.Unstructured, req scheduleCreateRequest, createdBy string) (*unstructured.Unstructured, error) {
	jobTemplate, found, err := unstructured.NestedMap(source.Object, "spec", "jobTemplate")
	if err != nil || !found {
		return nil, fmt.Errorf("source CronJob %s/%s has no spec.jobTemplate", source.GetNamespace(), source.GetName())
	}
	spec := map[string]any{
		"schedule":    req.Schedule,
		"jobTemplate": jobTemplate,
		"suspend":     req.Suspend != nil && *req.Suspend,
	}
	if req.TimeZone != "" {
		spec["timeZone"] = req.TimeZone
	}
	for _, f := range scheduleSpecFieldsToCopy {
		if v, ok, _ := unstructured.NestedFieldCopy(source.Object, "spec", f); ok {
			spec[f] = v
		}
	}

	labels := map[string]any{}
	srcLabels := source.GetLabels()
	for _, k := range scheduleAttributionLabels {
		if v, ok := srcLabels[k]; ok && v != "" {
			labels[k] = v
		}
	}
	labels["app.kubernetes.io/managed-by"] = scheduleManagedBy
	labels[scheduleOriginLabel] = scheduleOriginConsole
	labels[scheduleSourceLabel] = source.GetName()

	annotations := map[string]any{
		scheduleSourceAnnotation: source.GetNamespace() + "/" + source.GetName(),
	}
	if createdBy != "" {
		annotations[scheduleCreatedByAnnotation] = createdBy
	}

	metadata := map[string]any{
		"name":        req.Name,
		"namespace":   source.GetNamespace(),
		"labels":      labels,
		"annotations": annotations,
	}
	if uid := string(source.GetUID()); uid != "" {
		metadata["ownerReferences"] = []any{map[string]any{
			"apiVersion": "batch/v1",
			"kind":       "CronJob",
			"name":       source.GetName(),
			"uid":        uid,
		}}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "CronJob",
		"metadata":   metadata,
		"spec":       spec,
	}}, nil
}

// isConsoleSchedule reports whether a CronJob was created through this
// surface (and so may be edited/deleted here).
func isConsoleSchedule(cj *unstructured.Unstructured) bool {
	return cj != nil && cj.GetLabels()[scheduleOriginLabel] == scheduleOriginConsole
}

// loadConsoleSchedule resolves the path params, auth, client and the
// target CronJob for the edit/delete handlers, enforcing the console-
// origin gate. Returns ok=false after writing the response.
func (h *Handler) loadConsoleSchedule(w http.ResponseWriter, r *http.Request) (dynamic.Interface, *unstructured.Unstructured, string, string, bool) {
	if h.k8sCache == nil {
		http.Error(w, "k8scache disabled", http.StatusServiceUnavailable)
		return nil, nil, "", "", false
	}
	clusterID := strings.TrimSpace(chi.URLParam(r, "id"))
	ns := strings.TrimSpace(chi.URLParam(r, "ns"))
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if clusterID == "" || ns == "" || name == "" {
		writeBadRequest(w, "missing-path-params", "id, ns and name are required")
		return nil, nil, "", "", false
	}
	clusterID = h.resolveChrootClusterID(clusterID)
	if !h.requireResourceMutationAuth(w, r) {
		return nil, nil, "", "", false
	}
	dyn, err := h.k8sCache.DynamicClientFor(clusterID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cluster-unavailable", "detail": err.Error()})
		return nil, nil, "", "", false
	}
	cj, err := dyn.Resource(helmwatch.CronJobGVR).Namespace(ns).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "schedule-not-found", "detail": err.Error()})
			return nil, nil, "", "", false
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "schedule-get-failed", "detail": err.Error()})
		return nil, nil, "", "", false
	}
	if !isConsoleSchedule(cj) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "not-console-schedule",
			"detail": fmt.Sprintf("CronJob %s/%s is declared by its Blueprint chart, not by the console; change it in Git (the HelmRelease values), or create an ad-hoc schedule from it", ns, name),
		})
		return nil, nil, "", "", false
	}
	return dyn, cj, ns, name, true
}

// HandleScheduleUpdate — PATCH /api/v1/sovereigns/{id}/schedules/{ns}/{name}
func (h *Handler) HandleScheduleUpdate(w http.ResponseWriter, r *http.Request) {
	dyn, cj, ns, name, ok := h.loadConsoleSchedule(w, r)
	if !ok {
		return
	}
	var body scheduleUpdateRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	if body.Schedule == nil && body.TimeZone == nil && body.Suspend == nil && !body.ClearTimeZone {
		writeBadRequest(w, "empty-update", "nothing to change: give schedule, timeZone, suspend or clearTimeZone")
		return
	}
	if body.Schedule != nil {
		s := strings.TrimSpace(*body.Schedule)
		if err := validateCronSchedule(s); err != nil {
			writeBadRequest(w, "invalid-schedule", err.Error())
			return
		}
		_ = unstructured.SetNestedField(cj.Object, s, "spec", "schedule")
	}
	if body.ClearTimeZone {
		unstructured.RemoveNestedField(cj.Object, "spec", "timeZone")
	} else if body.TimeZone != nil {
		tz := strings.TrimSpace(*body.TimeZone)
		if err := validateTimeZone(tz); err != nil {
			writeBadRequest(w, "invalid-timezone", err.Error())
			return
		}
		if tz == "" {
			unstructured.RemoveNestedField(cj.Object, "spec", "timeZone")
		} else {
			_ = unstructured.SetNestedField(cj.Object, tz, "spec", "timeZone")
		}
	}
	if body.Suspend != nil {
		_ = unstructured.SetNestedField(cj.Object, *body.Suspend, "spec", "suspend")
	}
	updated, err := dyn.Resource(helmwatch.CronJobGVR).Namespace(ns).Update(r.Context(), cj, metav1.UpdateOptions{})
	if err != nil {
		writeK8sUpdateError(w, err)
		return
	}
	_ = name
	writeJSON(w, http.StatusOK, scheduleResponseFrom(updated))
}

// HandleScheduleDelete — DELETE /api/v1/sovereigns/{id}/schedules/{ns}/{name}
func (h *Handler) HandleScheduleDelete(w http.ResponseWriter, r *http.Request) {
	dyn, cj, ns, name, ok := h.loadConsoleSchedule(w, r)
	if !ok {
		return
	}
	// Background propagation so the schedule's child Jobs are reaped with
	// it rather than orphaned.
	policy := metav1.DeletePropagationBackground
	if err := dyn.Resource(helmwatch.CronJobGVR).Namespace(ns).Delete(r.Context(), name, metav1.DeleteOptions{PropagationPolicy: &policy}); err != nil {
		writeK8sUpdateError(w, err)
		return
	}
	resp := scheduleResponseFrom(cj)
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted":   true,
		"namespace": resp.Namespace,
		"name":      resp.Name,
		"schedule":  resp.Schedule,
	})
}

// scheduleResponseFrom projects a CronJob onto the wire shape.
func scheduleResponseFrom(cj *unstructured.Unstructured) scheduleResponse {
	out := scheduleResponse{
		Namespace:       cj.GetNamespace(),
		Name:            cj.GetName(),
		ResourceVersion: cj.GetResourceVersion(),
		UID:             string(cj.GetUID()),
		Origin:          "chart",
	}
	out.Schedule, _, _ = unstructured.NestedString(cj.Object, "spec", "schedule")
	out.TimeZone, _, _ = unstructured.NestedString(cj.Object, "spec", "timeZone")
	out.Suspend, _, _ = unstructured.NestedBool(cj.Object, "spec", "suspend")
	if isConsoleSchedule(cj) {
		out.Origin = scheduleOriginConsole
		out.SourceName = cj.GetLabels()[scheduleSourceLabel]
		if src := cj.GetAnnotations()[scheduleSourceAnnotation]; src != "" {
			if i := strings.IndexByte(src, '/'); i > 0 {
				out.SourceNamespace = src[:i]
				out.SourceName = src[i+1:]
			}
		}
	}
	return out
}
