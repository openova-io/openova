package crossregion

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	kfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
)

const testRegion = "hel1-1"

func newSync(primary kubernetes.Interface, follower kubernetes.Interface, dyn dynamic.Interface, d *Deliveries) InputSync {
	return InputSync{
		DeploymentID: "dep-7902",
		Primary:      primary,
		Followers: []Follower{{Key: testRegion, Client: follower,
			Dynamic: func() (dynamic.Interface, error) { return dyn, nil }}},
		Copy:        plainCopy,
		Deliveries:  d,
		CallTimeout: testTimeout,
		Log:         quietLog(),
	}
}

// TestInputSync_CopiesEveryFollowerInput: each Secret of the table reaches
// the follower with the primary's value, a missing source is skipped, the
// primary is never written, and a pass that changes nothing records no
// delivery. These are the #4158/#4915 cases of the Keycloak admin copy,
// which moved here, plus the two Secrets #7902 adds.
func TestInputSync_CopiesEveryFollowerInput(t *testing.T) {
	ctx := context.Background()
	primary, follower := kfake.NewSimpleClientset(), kfake.NewSimpleClientset()
	seedSecret(t, primary, "keycloak", "keycloak-admin", map[string]string{"admin-password": "primary"})
	seedSecret(t, primary, "keycloak", "catalyst-kc-master-admin-credentials", map[string]string{"master-realm-admin-password": "primary"})
	seedSecret(t, primary, helmwatch.FluxNamespace, "openova-org-tenants-git-auth", map[string]string{"username": "gitea_admin", "password": "primary"})
	// harbor-admin is not made on the primary yet: skipped, not an error.
	seedSecret(t, follower, "keycloak", "keycloak-admin", map[string]string{"admin-password": "follower-own"})
	seedSecret(t, follower, "keycloak", "catalyst-kc-master-admin-credentials", map[string]string{"master-realm-admin-password": "follower-own"})
	seedSecret(t, follower, "harbor", "harbor-admin", map[string]string{"HARBOR_ADMIN_PASSWORD": "follower-own"})
	_, _ = follower.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: helmwatch.FluxNamespace}}, metav1.CreateOptions{})

	d := &Deliveries{}
	sync := newSync(primary, follower, fakeRegion(t), d)
	sync.Run(ctx)

	for _, c := range []struct{ ns, name, key, want string }{
		{"keycloak", "keycloak-admin", "admin-password", "primary"},
		{"keycloak", "catalyst-kc-master-admin-credentials", "master-realm-admin-password", "primary"},
		{helmwatch.FluxNamespace, "openova-org-tenants-git-auth", "password", "primary"},
		{"harbor", "harbor-admin", "HARBOR_ADMIN_PASSWORD", "follower-own"},
	} {
		if got, _ := secretValue(t, follower, c.ns, c.name, c.key); got != c.want {
			t.Errorf("follower %s/%s %s = %q, want %q", c.ns, c.name, c.key, got, c.want)
		}
	}
	if got, _ := secretValue(t, primary, "keycloak", "keycloak-admin", "admin-password"); got != "primary" {
		t.Errorf("the primary's keycloak-admin changed to %q: the primary is the source and is never written", got)
	}
	first := d.At("dep-7902", testRegion, "")
	if first.IsZero() {
		t.Fatalf("no delivery recorded although the follower's bytes changed")
	}
	if !d.At("dep-7902", testRegion, "harbor/harbor-admin").IsZero() {
		t.Errorf("a delivery is recorded for a Secret that was not copied")
	}

	// The same pass again: nothing changes, the delivery time stays.
	sync.Now = func() time.Time { return first.Add(time.Hour) }
	sync.Run(ctx)
	if got := d.At("dep-7902", testRegion, ""); !got.Equal(first) {
		t.Errorf("a pass that changed nothing moved the delivery time from %v to %v", first, got)
	}

	// One region: nothing to do, no panic.
	InputSync{DeploymentID: "one", Primary: primary, Copy: plainCopy, Deliveries: d, CallTimeout: testTimeout, Log: quietLog()}.Run(ctx)
}

func failedJob(name string, began time.Time, mutate func(*batchv1.Job)) *batchv1.Job {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "harbor", CreationTimestamp: metav1.NewTime(began),
			Labels: map[string]string{labelReleaseName: "bp-harbor", labelReleaseNamespace: helmwatch.FluxNamespace},
		},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{Name: "admin", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "harbor-admin"}}}},
		}}},
		Status: batchv1.JobStatus{Failed: 4, Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}},
	}
	if mutate != nil {
		mutate(job)
	}
	return job
}

// TestInputSync_RerunsJobsThatFailedBeforeTheirSecretArrived is #7902
// defect 5: the follower's Harbor setup Jobs failed on the follower's own
// admin password. After the primary's password arrives they are deleted and
// their release is asked for a fresh attempt. Everything else is left alone.
func TestInputSync_RerunsJobsThatFailedBeforeTheirSecretArrived(t *testing.T) {
	ctx := context.Background()
	primary, follower := kfake.NewSimpleClientset(), kfake.NewSimpleClientset()
	seedSecret(t, primary, "harbor", "harbor-admin", map[string]string{"HARBOR_ADMIN_PASSWORD": "primary"})
	seedSecret(t, follower, "harbor", "harbor-admin", map[string]string{"HARBOR_ADMIN_PASSWORD": "follower-own"})
	before := time.Now().Add(-time.Hour)

	jobs := []*batchv1.Job{
		failedJob("harbor-pull-robot-62d6ba9f", before, nil),
		failedJob("harbor-registry-projects-2cb40aa0", before, nil),
		// Reads another Secret.
		failedJob("other-secret", before, func(j *batchv1.Job) { j.Spec.Template.Spec.Volumes[0].Secret.SecretName = "something-else" }),
		// Not failed.
		failedJob("succeeded", before, func(j *batchv1.Job) {
			j.Status = batchv1.JobStatus{Succeeded: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}}
		}),
		// A Helm hook belongs to an attempt of its release.
		failedJob("hook", before, func(j *batchv1.Job) { j.Annotations = map[string]string{annotationHelmHook: "post-install"} }),
		// No release to create it again.
		failedJob("no-release", before, func(j *batchv1.Job) { j.Labels = nil }),
		// Reads the Secret through an env reference.
		failedJob("env-reader", before, func(j *batchv1.Job) {
			j.Spec.Template.Spec.Volumes = nil
			j.Spec.Template.Spec.Containers = []corev1.Container{{Name: "c", Env: []corev1.EnvVar{{Name: "P", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "harbor-admin"}, Key: "HARBOR_ADMIN_PASSWORD"}}}}}}
		}),
	}
	for _, j := range jobs {
		if _, err := follower.BatchV1().Jobs("harbor").Create(ctx, j, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed Job %s: %v", j.Name, err)
		}
	}
	dyn := fakeRegion(t, readyRelease("bp-harbor"))
	d := &Deliveries{}
	sync := newSync(primary, follower, dyn, d)
	sync.Run(ctx)

	left := map[string]bool{}
	list, err := follower.BatchV1().Jobs("harbor").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	for _, j := range list.Items {
		left[j.Name] = true
	}
	for _, gone := range []string{"harbor-pull-robot-62d6ba9f", "harbor-registry-projects-2cb40aa0", "env-reader"} {
		if left[gone] {
			t.Errorf("Job %s failed on the old password and is still there: nothing runs it again", gone)
		}
	}
	for _, kept := range []string{"other-secret", "succeeded", "hook", "no-release"} {
		if !left[kept] {
			t.Errorf("Job %s was deleted; it must be left alone", kept)
		}
	}
	ann := getRelease(t, dyn, "bp-harbor").GetAnnotations()
	token := ann[helmwatch.AnnotationReconcileRequestedAt]
	if token == "" || ann[helmwatch.AnnotationReconcileForceAt] != token || ann[helmwatch.AnnotationReconcileResetAt] != token {
		t.Fatalf("bp-harbor annotations = %v, want one retry token on all three: the release must create the deleted Jobs again", ann)
	}

	// A Job the release creates AFTER the delivery and that fails is a real
	// failure: it ran with the right password. It stays.
	if _, err := follower.BatchV1().Jobs("harbor").Create(ctx, failedJob("harbor-pull-robot-62d6ba9f", time.Now().Add(time.Minute), nil), metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed new Job: %v", err)
	}
	sync.Run(ctx)
	if _, err := follower.BatchV1().Jobs("harbor").Get(ctx, "harbor-pull-robot-62d6ba9f", metav1.GetOptions{}); err != nil {
		t.Errorf("a Job that began after the Secret arrived was deleted (%v): its failure must stay visible", err)
	}
	if got := getRelease(t, dyn, "bp-harbor").GetAnnotations()[helmwatch.AnnotationReconcileRequestedAt]; got != token {
		t.Errorf("bp-harbor was asked for another attempt (%q → %q) by a pass that had nothing to run again", token, got)
	}
}

// stalledWithAttempt builds a stalled release whose failed attempt began at
// `began` and failed at `failed`.
func stalledWithAttempt(name string, began, failed time.Time) *unstructured.Unstructured {
	hr := release(name,
		cond("Ready", "False", "InstallFailed", failed),
		cond("Stalled", "True", "RetriesExceeded", failed),
	)
	_ = unstructured.SetNestedSlice(hr.Object, []any{
		map[string]any{"name": name, "status": "failed", "lastDeployed": began.UTC().Format(time.RFC3339)},
	}, "status", "history")
	return hr
}

func forceToken(t *testing.T, dyn dynamic.Interface, name string) string {
	t.Helper()
	return getRelease(t, dyn, name).GetAnnotations()[helmwatch.AnnotationReconcileForceAt]
}

// TestHealReleases covers the restart rule of #7902 defect 3.
func TestHealReleases(t *testing.T) {
	now := time.Now().UTC()
	ctx := context.Background()
	setup := func(t *testing.T, objs ...*unstructured.Unstructured) (InputSync, *Deliveries, dynamic.Interface) {
		t.Helper()
		dyn := fakeRegion(t, objs...)
		d := &Deliveries{}
		return newSync(nil, nil, dyn, d), d, dyn
	}

	t.Run("failed-after-the-database-secrets-and-before-the-admin-secret", func(t *testing.T) {
		// The fresh installation of 15 November: the database Secrets were
		// delivered, the follower's bp-keycloak then failed on HTTP 401,
		// and the admin Secret came three minutes later. The former rule
		// (failure time against the database-Secret delivery, on that pass
		// only) left it stalled for good.
		hubAt, failedAt, adminAt := now.Add(-40*time.Minute), now.Add(-10*time.Minute), now.Add(-7*time.Minute)
		sync, d, dyn := setup(t, stalledWithAttempt("bp-keycloak", failedAt.Add(-30*time.Minute), failedAt))
		d.Note("dep-7902", testRegion, "", hubAt)
		d.Note("dep-7902", testRegion, "keycloak/keycloak-admin", adminAt)
		sync.HealReleases(ctx, testRegion, dyn)
		first := forceToken(t, dyn, "bp-keycloak")
		if first == "" {
			t.Fatalf("the stalled release was not restarted after the admin Secret arrived: Flux never retries it, and seven releases depend on it")
		}
		ann := getRelease(t, dyn, "bp-keycloak").GetAnnotations()
		if ann[helmwatch.AnnotationReconcileRequestedAt] != first || ann[helmwatch.AnnotationReconcileResetAt] != first {
			t.Errorf("annotations = %v, want one token on requestedAt, resetAt and forceAt", ann)
		}
		// One restart per delivery.
		sync.HealReleases(ctx, testRegion, dyn)
		sync.HealReleases(ctx, testRegion, dyn)
		if got := forceToken(t, dyn, "bp-keycloak"); got != first {
			t.Errorf("the release was restarted again without a new delivery (%q → %q)", first, got)
		}
	})

	t.Run("attempt-began-before-the-delivery-and-failed-after-it", func(t *testing.T) {
		// A hook pod that started before the Secret arrived keeps the old
		// value and fails after the delivery.
		deliveredAt := now.Add(-20 * time.Minute)
		sync, d, dyn := setup(t, stalledWithAttempt("bp-keycloak", deliveredAt.Add(-10*time.Minute), deliveredAt.Add(15*time.Minute)))
		d.Note("dep-7902", testRegion, "", deliveredAt)
		sync.HealReleases(ctx, testRegion, dyn)
		if forceToken(t, dyn, "bp-keycloak") == "" {
			t.Errorf("a release whose attempt began before the delivery was not restarted")
		}
	})

	t.Run("attempt-began-after-the-delivery-is-a-real-failure", func(t *testing.T) {
		deliveredAt := now.Add(-60 * time.Minute)
		sync, d, dyn := setup(t, stalledWithAttempt("bp-newapi", deliveredAt.Add(10*time.Minute), deliveredAt.Add(40*time.Minute)))
		d.Note("dep-7902", testRegion, "", deliveredAt)
		sync.HealReleases(ctx, testRegion, dyn)
		if ann := getRelease(t, dyn, "bp-newapi").GetAnnotations(); len(ann) != 0 {
			t.Errorf("a release that failed with every input present was restarted (%v): its failure must stay visible", ann)
		}
	})

	t.Run("no-delivery-no-restart", func(t *testing.T) {
		sync, _, dyn := setup(t, stalledWithAttempt("bp-keycloak", now.Add(-time.Hour), now.Add(-30*time.Minute)))
		sync.HealReleases(ctx, testRegion, dyn)
		if ann := getRelease(t, dyn, "bp-keycloak").GetAnnotations(); len(ann) != 0 {
			t.Errorf("a stalled release was restarted although nothing was delivered (%v)", ann)
		}
	})

	t.Run("healthy-and-suspended-releases-untouched", func(t *testing.T) {
		suspended := stalledWithAttempt("bp-velero", now.Add(-time.Hour), now.Add(-50*time.Minute))
		_ = unstructured.SetNestedField(suspended.Object, true, "spec", "suspend")
		sync, d, dyn := setup(t, readyRelease("bp-grafana"), suspended)
		d.Note("dep-7902", testRegion, "", now.Add(-time.Minute))
		sync.HealReleases(ctx, testRegion, dyn)
		for _, name := range []string{"bp-grafana", "bp-velero"} {
			if ann := getRelease(t, dyn, name).GetAnnotations(); len(ann) != 0 {
				t.Errorf("%s gained annotations %v", name, ann)
			}
		}
	})

	t.Run("dropped-request-is-sent-again-when-the-dependency-is-ready", func(t *testing.T) {
		// helm-controller consumed the restart while a dependency was
		// re-rendering: lastHandledReconcileAt has the token, the reset
		// and the force were never handled.
		deliveredAt := now.Add(-20 * time.Minute)
		sync, d, dyn := setup(t, stalledWithAttempt("bp-keycloak", deliveredAt.Add(-10*time.Minute), deliveredAt.Add(-time.Minute)))
		d.Note("dep-7902", testRegion, "", deliveredAt)
		sync.HealReleases(ctx, testRegion, dyn)
		first := forceToken(t, dyn, "bp-keycloak")
		if first == "" {
			t.Fatalf("no first restart")
		}
		setStatus := func(reason, message string, stalled bool) {
			cur := getRelease(t, dyn, "bp-keycloak")
			_ = unstructured.SetNestedField(cur.Object, first, "status", "lastHandledReconcileAt")
			conds := []any{map[string]any{"type": "Ready", "status": "False", "reason": reason, "message": message,
				"lastTransitionTime": time.Now().UTC().Format(time.RFC3339)}}
			if stalled {
				conds = append(conds, cond("Stalled", "True", "RetriesExceeded", time.Now()))
			}
			_ = unstructured.SetNestedSlice(cur.Object, conds, "status", "conditions")
			updateRelease(t, dyn, cur)
		}

		setStatus("DependencyNotReady", "dependency 'flux-system/bp-postgres-shared' is not ready", false)
		sync.HealReleases(ctx, testRegion, dyn)
		if got := forceToken(t, dyn, "bp-keycloak"); got != first {
			t.Fatalf("a request was sent while the dependency was not ready (%q → %q): it would be dropped again", first, got)
		}

		setStatus("InstallFailed", "Failed to install after 2 attempt(s)", true)
		sync.HealReleases(ctx, testRegion, dyn)
		second := forceToken(t, dyn, "bp-keycloak")
		if second == first {
			t.Fatalf("the dropped request was not sent again once the dependency was ready: the release stays stalled for good")
		}

		cur := getRelease(t, dyn, "bp-keycloak")
		for _, f := range []string{"lastHandledReconcileAt", "lastHandledResetAt", "lastHandledForceAt"} {
			_ = unstructured.SetNestedField(cur.Object, second, "status", f)
		}
		updateRelease(t, dyn, cur)
		sync.HealReleases(ctx, testRegion, dyn)
		if got := forceToken(t, dyn, "bp-keycloak"); got != second {
			t.Errorf("a handled request was sent again (%q → %q)", second, got)
		}
	})
}
