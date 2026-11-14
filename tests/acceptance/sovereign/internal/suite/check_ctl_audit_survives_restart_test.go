package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// auditWorld is a console API with an audit listing, and the cluster that
// runs it. fault names what is wrong with it; "" is a platform that keeps its
// audit events on the stream.
type auditWorld struct {
	sync.Mutex
	fault      string
	stream     []map[string]string // what the stream holds: it survives a restart
	ring       []map[string]string // what the API lists
	generation int                 // the pods of the API are api-<generation>
	deleted    bool                // the pods are deleted and the rollout has not run
	closed     int                 // listing requests the gateway still answers with 503
	calls      []string            // the writes, in order: POST, kubectl delete, kubectl rollout, DELETE
}

func (w *auditWorld) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	w.Lock()
	defer w.Unlock()
	answer := func(status int, body any) {
		rw.WriteHeader(status)
		_ = json.NewEncoder(rw).Encode(body)
	}
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/dep/rbac/assign"):
		w.calls = append(w.calls, "POST")
		var body struct {
			User  struct{ Email, KeycloakSubject string }
			Tier  string
			Scope []struct{ Key, Value string }
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Tier != "viewer" || len(body.Scope) != 1 ||
			body.Scope[0].Key != "openova.io/application" || body.Scope[0].Value != body.User.KeycloakSubject ||
			!strings.HasPrefix(body.User.KeycloakSubject, "ctl-g07-") || body.User.Email != body.User.KeycloakSubject+"@gate.invalid" {
			answer(http.StatusBadRequest, map[string]string{"error": "invalid"})
			return
		}
		switch w.fault {
		case "forbidden":
			answer(http.StatusForbidden, map[string]string{"error": "403"})
			return
		case "no-op":
			answer(http.StatusOK, map[string]any{"applied": "no-op", "userAccess": map[string]string{"name": "rbac-x"}})
			return
		}
		ev := map[string]string{"id": "01JCDZ8A0000000000000000AB", "auditType": "rbac-grant-created", "userAccessRef": "rbac-x"}
		if w.fault == "no-id" {
			ev["id"] = ""
		}
		w.ring = append(w.ring, ev)
		if w.fault != "volatile" && w.fault != "in-process" {
			w.stream = append(w.stream, ev)
		}
		answer(http.StatusCreated, map[string]any{"applied": "created", "userAccess": map[string]string{"name": "rbac-x"}})
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/sovereigns/dep/audit/rbac"):
		if w.closed > 0 || w.deleted {
			w.closed = max(w.closed-1, 0)
			answer(http.StatusServiceUnavailable, map[string]string{"error": "no healthy upstream"})
			return
		}
		transport := "jetstream:CATALYST_ORG"
		if w.fault == "in-process" {
			transport = "in-process"
		}
		items := []map[string]string{{"id": "", "auditType": "rbac-grant-created", "userAccessRef": "rbac-other"}}
		answer(http.StatusOK, map[string]any{"items": append(items, w.ring...), "transport": transport})
	case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/deployments/dep/admin/user-access/rbac-x"):
		w.calls = append(w.calls, "DELETE")
		if w.fault == "kept" {
			answer(http.StatusInternalServerError, map[string]string{"error": "delete-failed"})
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	default:
		answer(http.StatusNotFound, map[string]string{"error": "no such route: " + r.Method + " " + r.URL.Path})
	}
}

// kubectl answers for the cluster. Only region-a runs the API.
func (w *auditWorld) kubectl(r kube.Region, _ context.Context, _ io.Reader, args ...string) ([]byte, error) {
	w.Lock()
	defer w.Unlock()
	if !slices.Contains(args, "catalyst-system") {
		return nil, fmt.Errorf("unexpected kubectl %v", args)
	}
	runs := r.ID == "region-a" && w.fault != "nowhere"
	switch args[0] + " " + args[1] {
	case "get pods":
		if !slices.Contains(args, "app.kubernetes.io/name=catalyst-api") {
			return nil, fmt.Errorf("unexpected kubectl %v", args)
		}
		if !runs || w.deleted {
			return []byte(kubeList()), nil
		}
		ready := "True"
		if w.fault == "not-ready" && w.generation > 0 {
			ready = "False"
		}
		name := fmt.Sprintf("api-%d", w.generation)
		return []byte(kubeList(obj{"metadata": obj{"name": name, "uid": "uid-" + name},
			"status": obj{"conditions": kubeConditions("PodScheduled", "True", "Ready", ready)}})), nil
	case "delete pod":
		w.calls = append(w.calls, "kubectl delete")
		if !runs || !slices.Contains(args, "app.kubernetes.io/name=catalyst-api") || !slices.Contains(args, "--wait=true") {
			return nil, fmt.Errorf("unexpected kubectl %v", args)
		}
		if w.fault == "survives" {
			return []byte("pod deleted"), nil // the same pod is still there
		}
		w.deleted = true
		return []byte("pod deleted"), nil
	case "rollout status":
		w.calls = append(w.calls, "kubectl rollout")
		if !runs || !slices.Contains(args, "deployment/catalyst-api") {
			return nil, fmt.Errorf("unexpected kubectl %v", args)
		}
		if w.fault == "stuck" {
			return nil, errors.New("error: timed out waiting for the condition")
		}
		if w.deleted { // the new pod starts: its ring holds what the stream holds
			w.deleted = false
			w.generation++
			w.ring = append([]map[string]string(nil), w.stream...)
			switch w.fault {
			case "slow-gateway":
				w.closed = 2
			case "gateway-down":
				w.closed = 1000
			}
		}
		return []byte("deployment successfully rolled out"), nil
	}
	return nil, fmt.Errorf("unexpected kubectl %v", args)
}

// auditRun runs the check against a world the way the runner does and returns
// the result of the check and the recorded steps.
func auditRun(t *testing.T, w *auditWorld) (CheckResult, []*Step) {
	t.Helper()
	srv := httptest.NewServer(w)
	defer srv.Close()
	c := ctlAuditCheck{kubectl: w.kubectl, pause: time.Millisecond}
	cc := &CheckContext{API: api.New(srv.URL, "session"), Deploy: &DeploymentInfo{ID: "dep"},
		Regions: []kube.Region{{ID: "region-a"}, {ID: "region-b"}}}
	r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
	r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(c.ID(), StateRequired)}}, []Check{c}, cc, true)
	return r.run.Checks[0], r.run.Steps
}

func TestAuditSurvivesRestartCheck(t *testing.T) {
	const all = "POST,kubectl delete,kubectl rollout,DELETE"
	for fault, want := range map[string]struct{ reason, calls string }{
		"":             {"", all}, // the event is on the stream and is listed again
		"slow-gateway": {"", all}, // two 503 answers after the restart, then the listing
		"volatile":     {"was listed before the restart and is not listed after it", all},
		"in-process":   {`the transport of the audit is "in-process"`, "POST,DELETE"},
		"no-id":        {"holds no rbac-grant-created event of UserAccess rbac-x with an id", "POST,DELETE"},
		"forbidden":    {"HTTP 403", "POST"},
		"no-op":        {`applied "no-op"`, "POST"}, // a grant the run did not create is not removed
		"nowhere":      {"no region runs a pod of Deployment catalyst-system/catalyst-api", "POST,DELETE"},
		"survives":     {"pod api-0 was not replaced", all},
		"stuck":        {"timed out waiting for the condition", all},
		"not-ready":    {"these are not Ready: api-1", all},
		"gateway-down": {"HTTP 503", all},
		"kept":         {"the UserAccess rbac-x may be left behind", all},
	} {
		t.Run("fault="+fault, func(t *testing.T) {
			w := &auditWorld{fault: fault}
			res, steps := auditRun(t, w)
			if (want.reason == "") != (res.Status == Pass) || !strings.Contains(res.Reason, want.reason) {
				t.Fatalf("status %s, reason %q; want a reason that holds %q (empty: PASS)", res.Status, res.Reason, want.reason)
			}
			if got := strings.Join(w.calls, ","); got != want.calls {
				t.Errorf("the check wrote %q, want %q", got, want.calls)
			}
			if res.Status != Pass {
				return
			}
			last := steps[len(steps)-2] // step 4; step 5 is the clean-up
			if last.ID != "CTL-G07.4" || last.Evidence["event_id_after"] != "01JCDZ8A0000000000000000AB" {
				t.Errorf("step %s has evidence %v", last.ID, last.Evidence)
			}
			if attempts := map[string]int{"": 1, "slow-gateway": 3}[fault]; last.Evidence["attempts"] != attempts {
				t.Errorf("step 4 asked %v times, want %d", last.Evidence["attempts"], attempts)
			}
			if steps[1].Evidence["event_id_before"] != "01JCDZ8A0000000000000000AB" || steps[1].Evidence["transport"] != "jetstream:CATALYST_ORG" {
				t.Errorf("step 2 has evidence %v", steps[1].Evidence)
			}
			if pods := fmt.Sprint(steps[2].Evidence["pods"]); pods != "[region-a: api-0 -> api-1]" {
				t.Errorf("step 3 has pods %s", pods)
			}
		})
	}
}

func TestAuditSurvivesRestartCheckNeedsASessionAndADeployment(t *testing.T) {
	c := ctlAuditCheck{kubectl: (&auditWorld{}).kubectl}
	for name, cc := range map[string]*CheckContext{
		"no session":    {Deploy: &DeploymentInfo{ID: "dep"}, Regions: []kube.Region{{ID: "region-a"}}},
		"no deployment": {API: api.New("http://127.0.0.1:1", "s"), Regions: []kube.Region{{ID: "region-a"}}},
		"no region":     {API: api.New("http://127.0.0.1:1", "s"), Deploy: &DeploymentInfo{ID: "dep"}},
	} {
		r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
		r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(c.ID(), StateRequired)}}, []Check{c}, cc, true)
		if res := r.run.Checks[0]; res.Status != Fail || !strings.Contains(res.Reason, "needs a console session") {
			t.Errorf("%s: status %s, reason %q", name, res.Status, res.Reason)
		}
	}
	if _, registered := registry["CTL-G07"]; !registered {
		t.Error("check CTL-G07 is not registered")
	}
}
