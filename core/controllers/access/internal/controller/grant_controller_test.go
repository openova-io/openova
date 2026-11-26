package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	accessv1 "github.com/openova-io/openova/core/controllers/access/api/v1alpha1"
	"github.com/openova-io/openova/core/controllers/pkg/natsbus"
)

var t0 = time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)

const (
	subject  = "jane.roe@example.org"
	approver = "omar.said@example.org"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := accessv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func target(required bool, count int32, maxDuration string) *accessv1.AccessTarget {
	return &accessv1.AccessTarget{
		ObjectMeta: metav1.ObjectMeta{Name: "shell", Namespace: "acme"},
		Spec: accessv1.AccessTargetSpec{Kind: accessv1.KindPod, Pod: &accessv1.PodTarget{Selector: map[string]string{"app": "api"}},
			Policy: accessv1.TargetPolicy{Approval: accessv1.ApprovalPolicy{Required: &required, Count: count}, MaxDuration: maxDuration}},
	}
}

// grant is a grant on target shell with the window [from, to] and approvals by the given people.
func grant(name string, from, to time.Time, by ...string) *accessv1.AccessGrant {
	g := &accessv1.AccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "acme", Generation: 1},
		Spec: accessv1.AccessGrantSpec{TargetRef: accessv1.LocalRef{Name: "shell"}, Subject: accessv1.Subject{Email: subject},
			Reason: "Investigate failed settlement batch 4417", Ticket: "INC-20417",
			Window: accessv1.Window{NotBefore: metav1.NewTime(from), NotAfter: metav1.NewTime(to)}},
	}
	for _, b := range by {
		g.Spec.Approvals = append(g.Spec.Approvals, accessv1.Approval{By: b, At: metav1.NewTime(from.Add(-time.Minute))})
	}
	return g
}

func TestPhaseRules(t *testing.T) {
	in, out := t0.Add(-10*time.Minute), t0.Add(35*time.Minute) // a window of 45 minutes around t0
	suspended := grant("g", in, out, approver)
	suspended.Spec.Suspended, suspended.Spec.SuspendReason = true, "under review"
	withAccount := grant("g", in, out, approver)
	withAccount.Spec.Account = "root"
	for name, c := range map[string]struct {
		g             *accessv1.AccessGrant
		t             *accessv1.AccessTarget
		now           time.Time
		phase, reason string
	}{
		"no target":                           {grant("g", in, out, approver), nil, t0, accessv1.PhaseInvalid, accessv1.ReasonTargetNotFound},
		"window longer than maxDuration":      {grant("g", in, in.Add(61*time.Minute), approver), target(true, 1, "1h"), t0, accessv1.PhaseInvalid, accessv1.ReasonWindowTooLong},
		"window of exactly maxDuration":       {grant("g", in, in.Add(time.Hour), approver), target(true, 1, "1h"), t0, accessv1.PhaseActive, ""},
		"approved by its own subject":         {grant("g", in, out, subject), target(false, 1, "1h"), t0, accessv1.PhaseInvalid, accessv1.ReasonSelfApproval},
		"the same approver twice":             {grant("g", in, out, approver, approver), target(true, 2, "1h"), t0, accessv1.PhaseInvalid, accessv1.ReasonDuplicateApprover},
		"an account on a target of kind pod":  {withAccount, target(true, 1, "1h"), t0, accessv1.PhaseInvalid, accessv1.ReasonAccountNotAllowed},
		"suspended, also after its window":    {suspended, target(true, 1, "1h"), out.Add(time.Hour), accessv1.PhaseSuspended, ""},
		"at notAfter":                         {grant("g", in, out, approver), target(true, 1, "1h"), out, accessv1.PhaseExpired, ""},
		"unapproved after its window":         {grant("g", in, out), target(true, 1, "1h"), out, accessv1.PhaseExpired, ""},
		"no approval where one is required":   {grant("g", in, out), target(true, 1, "1h"), t0, accessv1.PhasePending, ""},
		"one of two approvals":                {grant("g", in, out, approver), target(true, 2, "1h"), t0, accessv1.PhasePending, ""},
		"unapproved, before its window":       {grant("g", out, out.Add(time.Minute)), target(true, 1, "1h"), t0, accessv1.PhasePending, ""},
		"approved, before notBefore":          {grant("g", t0.Add(time.Second), out, approver), target(true, 1, "1h"), t0, accessv1.PhaseScheduled, ""},
		"approved, at notBefore":              {grant("g", t0, out, approver), target(true, 1, "1h"), t0, accessv1.PhaseActive, ""},
		"no approval where none is required":  {grant("g", in, out), target(false, 1, "1h"), t0, accessv1.PhaseActive, ""},
		"maxDuration absent: one hour":        {grant("g", in, in.Add(61*time.Minute), approver), target(true, 1, ""), t0, accessv1.PhaseInvalid, accessv1.ReasonWindowTooLong},
		"approval count absent: one approval": {grant("g", in, out, approver), target(true, 0, "1h"), t0, accessv1.PhaseActive, ""},
	} {
		phase, reason, err := Phase(c.g, c.t, c.now)
		if err != nil || phase != c.phase || reason != c.reason {
			t.Errorf("%s: got %q %q %v, want %q %q", name, phase, reason, err, c.phase, c.reason)
		}
	}
	if _, _, err := Phase(grant("g", in, out), target(true, 1, "an hour"), t0); err == nil {
		t.Error("a maxDuration that is no duration gives no error")
	}
}

// sender records the envelopes it is given; it refuses while fail is set.
type sender struct {
	fail error
	got  []natsbus.CloudEvent
}

func (s *sender) SendCloudEvent(_ context.Context, ce natsbus.CloudEvent) error {
	if s.fail != nil {
		return s.fail
	}
	s.got = append(s.got, ce)
	return nil
}

type grantCase struct {
	r   *GrantReconciler
	c   client.Client
	s   *sender
	now time.Time
}

func newGrantCase(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) *grantCase {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).
		WithStatusSubresource(&accessv1.AccessGrant{}, &accessv1.AccessTarget{}).WithInterceptorFuncs(funcs).Build()
	gc := &grantCase{c: c, s: &sender{}, now: t0}
	gc.r = &GrantReconciler{Client: c, Events: gc.s, Source: "access-controller/t1.omani.works/region-a", Now: func() time.Time { return gc.now }}
	return gc
}

func (gc *grantCase) reconcile(t *testing.T, name string) (ctrl.Result, error) {
	t.Helper()
	return gc.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "acme", Name: name}})
}

func (gc *grantCase) status(t *testing.T, name string) accessv1.AccessGrantStatus {
	t.Helper()
	var g accessv1.AccessGrant
	if err := gc.c.Get(context.Background(), types.NamespacedName{Namespace: "acme", Name: name}, &g); err != nil {
		t.Fatal(err)
	}
	return g.Status
}

// endedData is the data of grant_ended for grant acme/g.
func endedData(reason string) string {
	return `{"namespace":"acme","grant":"g","subject":"` + subject + `","reason":"` + reason + `"}`
}

func TestGrantLifeCycle(t *testing.T) {
	from, to := t0.Add(time.Minute), t0.Add(31*time.Minute)
	gc := newGrantCase(t, interceptor.Funcs{}, target(true, 1, "1h"), grant("g", from, to, approver))

	res, err := gc.reconcile(t, "g")
	if st := gc.status(t, "g"); err != nil || st.Phase != accessv1.PhaseScheduled || res.RequeueAfter != time.Minute || len(gc.s.got) != 0 {
		t.Fatalf("before notBefore: phase %q, requeue %s, events %d, err %v; want Scheduled, 1m0s, 0", st.Phase, res.RequeueAfter, len(gc.s.got), err)
	}

	gc.now = from
	res, err = gc.reconcile(t, "g")
	st := gc.status(t, "g")
	if err != nil || st.Phase != accessv1.PhaseActive || res.RequeueAfter != 30*time.Minute || st.ActivatedAt == nil || !st.ActivatedAt.Time.Equal(from) {
		t.Fatalf("at notBefore: phase %q, requeue %s, activatedAt %v, err %v; want Active, 30m0s, %s", st.Phase, res.RequeueAfter, st.ActivatedAt, err, from)
	}
	if len(gc.s.got) != 1 || gc.s.got[0].Type != "io.openova.catalyst.access.grant_activated.v1" || gc.s.got[0].Source != gc.r.Source || gc.s.got[0].Subject != "" {
		t.Fatalf("at notBefore: want one grant_activated envelope from %s without an Organization, got %+v", gc.r.Source, gc.s.got)
	}
	want := `{"namespace":"acme","grant":"g","target":"shell","subject":"` + subject + `","account":"","reason":"Investigate failed ` +
		`settlement batch 4417","ticket":"INC-20417","notBefore":"2026-11-20T14:01:00Z","notAfter":"2026-11-20T14:31:00Z","approvers":["` + approver + `"]}`
	if got := string(gc.s.got[0].Data); got != want {
		t.Fatalf("grant_activated data:\n got %s\nwant %s", got, want)
	}

	// A second reconcile in the same phase sends nothing.
	if _, err = gc.reconcile(t, "g"); err != nil || len(gc.s.got) != 1 {
		t.Fatalf("again Active: %d events, err %v; want 1", len(gc.s.got), err)
	}

	gc.now = to
	res, err = gc.reconcile(t, "g")
	st = gc.status(t, "g")
	if err != nil || st.Phase != accessv1.PhaseExpired || res.RequeueAfter != 0 || st.EndedAt == nil || !st.EndedAt.Time.Equal(to) {
		t.Fatalf("at notAfter: phase %q, requeue %s, endedAt %v, err %v; want Expired, 0s, %s", st.Phase, res.RequeueAfter, st.EndedAt, err, to)
	}
	if len(gc.s.got) != 2 || gc.s.got[1].Type != "io.openova.catalyst.access.grant_ended.v1" || string(gc.s.got[1].Data) != endedData("expired") {
		t.Fatalf("at notAfter: want grant_ended with reason expired, got %+v", gc.s.got)
	}
}

func TestGrantPendingRequeuesAtNotAfter(t *testing.T) {
	gc := newGrantCase(t, interceptor.Funcs{}, target(true, 1, "1h"), grant("g", t0.Add(-time.Minute), t0.Add(20*time.Minute)))
	res, err := gc.reconcile(t, "g")
	if st := gc.status(t, "g"); err != nil || st.Phase != accessv1.PhasePending || res.RequeueAfter != 20*time.Minute {
		t.Fatalf("phase %q, requeue %s, err %v; want Pending, 20m0s", st.Phase, res.RequeueAfter, err)
	}
	gc2 := newGrantCase(t, interceptor.Funcs{}, grant("g", t0.Add(-time.Minute), t0.Add(20*time.Minute), approver))
	res, err = gc2.reconcile(t, "g")
	if st := gc2.status(t, "g"); err != nil || st.Phase != accessv1.PhaseInvalid || st.Reason != accessv1.ReasonTargetNotFound || res.RequeueAfter != 0 {
		t.Fatalf("no target: phase %q reason %q, requeue %s, err %v; want Invalid TargetNotFound, 0s", st.Phase, st.Reason, res.RequeueAfter, err)
	}
}

// A refused send leaves the status as it was; the retry sends the same envelope.
func TestGrantSendsBeforeItWritesStatus(t *testing.T) {
	statusWrites, failWrite := 0, true
	funcs := interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
		statusWrites++
		if failWrite {
			return errors.New("status write refused")
		}
		return c.SubResource(sub).Update(ctx, obj, opts...)
	}}
	gc := newGrantCase(t, funcs, target(true, 1, "1h"), grant("g", t0.Add(-time.Minute), t0.Add(20*time.Minute), approver))
	gc.s.fail = errors.New("no acknowledgement")
	if _, err := gc.reconcile(t, "g"); err == nil || statusWrites != 0 || gc.status(t, "g").Phase != "" {
		t.Fatalf("refused send: err %v, status writes %d, phase %q; want an error, 0 writes, no phase", err, statusWrites, gc.status(t, "g").Phase)
	}
	gc.s.fail = nil
	if _, err := gc.reconcile(t, "g"); err == nil || statusWrites != 1 || len(gc.s.got) != 1 {
		t.Fatalf("refused status write: err %v, status writes %d, events %d; want an error, 1, 1", err, statusWrites, len(gc.s.got))
	}
	failWrite = false
	if _, err := gc.reconcile(t, "g"); err != nil || gc.status(t, "g").Phase != accessv1.PhaseActive || len(gc.s.got) != 2 {
		t.Fatalf("retry: err %v, phase %q, events %d; want Active and a second send", err, gc.status(t, "g").Phase, len(gc.s.got))
	}
	if gc.s.got[0].ID != gc.s.got[1].ID {
		t.Fatalf("the retry sent a new id %s, want the id %s of the unsettled send", gc.s.got[1].ID, gc.s.got[0].ID)
	}
	// The phase changes after an acknowledged send and before its status write, then the grant is deleted.
	for i, fail := range []bool{true, false, false, true, false, true} { // suspended on every even step
		p := client.RawPatch(types.MergePatchType, []byte(`{"spec":{"suspended":`+[2]string{"true", "false"}[i%2]+`}}`))
		if failWrite = fail; gc.c.Patch(context.Background(), grant("g", t0, t0), p) != nil {
			t.Fatalf("step %d: the patch was refused", i)
		}
		if _, err := gc.reconcile(t, "g"); (err != nil) != fail {
			t.Fatalf("step %d: err %v", i, err)
		}
	}
	derr := gc.c.Delete(context.Background(), grant("g", t0, t0))
	_, err := gc.reconcile(t, "g")
	got := ""
	for _, ce := range gc.s.got {
		got += map[bool]string{true: "a", false: "e"}[ce.Type == gc.s.got[0].Type]
	}
	if derr != nil || err != nil || got != "aaeeaeaaeaae" || gc.s.got[3].ID != gc.s.got[2].ID || gc.s.got[7].ID != gc.s.got[6].ID || gc.s.got[10].ID != gc.s.got[9].ID || string(gc.s.got[11].Data) != endedData("deleted") {
		t.Fatalf("delete %v, err %v, events %s; want aaeeaeaaeaae: each envelope of a refused status write sent again, each activation ended", derr, err, got)
	}
}

func TestGrantDeletedWhileActive(t *testing.T) {
	g := grant("g", t0.Add(-time.Minute), t0.Add(20*time.Minute), approver)
	gc := newGrantCase(t, interceptor.Funcs{}, target(true, 1, "1h"), g, grant("other", t0.Add(-time.Minute), t0.Add(20*time.Minute)))
	if _, err := gc.reconcile(t, "g"); err != nil {
		t.Fatal(err)
	}
	if err := gc.c.Delete(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	if _, err := gc.reconcile(t, "g"); err != nil || len(gc.s.got) != 2 ||
		string(gc.s.got[1].Data) != endedData("deleted") {
		t.Fatalf("deleted: err %v, events %+v; want grant_ended with reason deleted", err, gc.s.got)
	}
	// A grant that was never Active sends nothing when it is gone.
	if _, err := gc.reconcile(t, "never-seen"); err != nil || len(gc.s.got) != 2 {
		t.Fatalf("an unknown grant: err %v, events %d; want 2", err, len(gc.s.got))
	}
}

func TestGrantsOfTarget(t *testing.T) {
	other := grant("other", t0, t0.Add(time.Minute))
	other.Spec.TargetRef.Name = "db"
	elsewhere := grant("elsewhere", t0, t0.Add(time.Minute))
	elsewhere.Namespace = "beta"
	gc := newGrantCase(t, interceptor.Funcs{}, target(true, 1, "1h"), grant("g", t0, t0.Add(time.Minute)), other, elsewhere)
	got := gc.r.grantsOf(context.Background(), target(true, 1, "1h"))
	if len(got) != 1 || got[0].Name != "g" || got[0].Namespace != "acme" {
		t.Fatalf("the grants of target acme/shell: got %v, want [acme/g]", got)
	}
}
