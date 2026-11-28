package suite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

const (
	g15Guacd = "ghcr.io/openova-io/openova/guacd@sha256:8974eaa9ba32f713daf311e7cc8cd7e4cdfba1edea39eed75524e78ef4b08f4f"
	g15Agent = "ghcr.io/openova-io/openova/access-session-agent@sha256:0011223344556677001122334455667700112233445566770011223344556677"
)

// g15Cluster is a cluster with the access controller and its session images
// for check ACC-G15; defect names what it does wrong.
type g15Cluster struct {
	now     time.Time
	defect  string
	created time.Time
	pod     string // the manifest of the session pod the check created
	calls   []string
}

func (f *g15Cluster) kubectl(r kube.Region, _ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	f.calls = append(f.calls, r.ID+": "+strings.Join(args, " "))
	in := ""
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		in = string(b)
	}
	cmd := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(cmd, "get deployment access-controller"):
		guacd, agent := g15Guacd, `,{"name": "ACCESS_IMAGE_AGENT", "value": "`+g15Agent+`"}`
		switch f.defect {
		case "no agent image":
			agent = ""
		case "guacd by tag":
			guacd = "ghcr.io/openova-io/openova/guacd:1.6.0"
		}
		return []byte(`{"spec": {"template": {"spec": {"containers": [{"name": "controller", "env": [{"name": "ACCESS_IMAGE_GUACD", "value": "` + guacd + `"}` + agent + `]}]}}}}`), nil
	case strings.HasPrefix(cmd, "get accesstarget"):
		status := map[bool]string{true: "False", false: "True"}[f.defect == "target not ready"]
		return []byte(`{"status": {"conditions": [{"type": "Ready", "status": "` + status + `", "reason": "Present", "message": "m"}]}}`), nil
	case strings.HasPrefix(cmd, "get serviceaccount access-session"):
		return []byte(`{"metadata": {"name": "access-session"}}`), nil
	case cmd == "-n guacamole create -f -" && strings.Contains(in, "access.openova.io/config"):
		f.pod, f.created = in, f.now
		return nil, nil
	case strings.HasPrefix(cmd, "get pod acc-g15-"):
		return []byte(f.podJSON()), nil
	case strings.Contains(cmd, " exec -i pol-dial-"):
		port, rc, out := args[len(args)-1], "0", "open"
		if (port == "4822" && f.defect != "guacd on every address") || (port == "8443" && f.defect == "agent not listening") {
			rc, out = "1", "10.42.0.17 (10.42.0.17:"+port+"): Connection refused"
		}
		return []byte("control_rc=0\ncontrol_out=kube-dns (10.96.0.10:53) open\ntarget_rc=" + rc + "\ntarget_out=" + out + "\n"), nil
	case strings.HasPrefix(cmd, "--as="+accG13Flux+" apply"), strings.HasPrefix(cmd, "create namespace"), strings.Contains(cmd, " delete "),
		strings.HasPrefix(cmd, "delete "), strings.Contains(cmd, " wait --for=condition=Ready pod/pol-dial-"), cmd == "-n guacamole create -f -":
		return nil, nil
	}
	return nil, errors.New("the fake cluster does not know: " + cmd)
}

// podJSON is the session pod at the cluster's time: its containers start 5
// seconds after the create, unless a defect says otherwise.
func (f *g15Cluster) podJSON() string {
	up := f.now.Sub(f.created)
	status := func(name string) string {
		state, started, restarts := "", f.created.Add(5*time.Second), 0
		switch {
		case up < 5*time.Second:
			state = `{"waiting": {"reason": "ContainerCreating"}}`
		case name == "agent" && f.defect == "agent exits":
			state = `{"terminated": {"reason": "Error", "exitCode": 78}}`
		case name == "agent" && f.defect == "agent restarts" && up >= 20*time.Second:
			started, restarts = f.created.Add(20*time.Second), 1
		}
		if state == "" {
			state = fmt.Sprintf(`{"running": {"startedAt": %q}}`, started.Format(time.RFC3339))
		}
		return fmt.Sprintf(`{"name": %q, "state": %s, "restartCount": %d}`, name, state, restarts)
	}
	return `{"spec": {"containers": [{"name": "guacd", "image": "` + g15Guacd + `"}, {"name": "agent", "image": "` + g15Agent + `"}]},
		"status": {"phase": "Running", "podIP": "10.42.0.17", "containerStatuses": [` + status("guacd") + `, ` + status("agent") + `]}}`
}

func TestAccG15Check(t *testing.T) {
	if _, ok := registry[accG15ID]; !ok || accG15ID != "ACC-G15" {
		t.Fatalf("check %s is not registered", accG15ID)
	}
	root := filepath.Join("..", "..", "..", "..", "..") // the fixtures of this repository
	for defect, want := range map[string]string{
		"":                       "",
		"no agent image":         `ACC-G15.1: the controller's ACCESS_IMAGE_AGENT is "", not an image referenced by digest`,
		"guacd by tag":           `ACC-G15.1: the controller's ACCESS_IMAGE_GUACD is "ghcr.io/openova-io/openova/guacd:1.6.0", not an image`,
		"target not ready":       "ACC-G15.2: the target is not Ready after 30s",
		"agent exits":            "ACC-G15.3: container agent ended (Error) before both were Running",
		"agent restarts":         "ACC-G15.4: after 15 seconds container agent is running=true with restart count 1",
		"guacd on every address": "ACC-G15.5: the dial of 10.42.0.17:4822 connected",
		"agent not listening":    "ACC-G15.5: the dial of 10.42.0.17:8443 refused",
	} {
		f := &g15Cluster{now: time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC), defect: defect}
		c := accG15Check{kubectl: f.kubectl, now: func() time.Time { return f.now },
			sleep: func(_ context.Context, d time.Duration) error { f.now = f.now.Add(d); return nil },
			uid:   func() (string, error) { return "7c1d0e3a-5b2f-4c1e-9a0d-3f6b8e2c4d10", nil }}
		r := runCheck(t, "none", c, &CheckContext{RepoRoot: root})
		got, failed := r.run.Checks[0], []string{}
		for _, s := range r.run.Steps {
			if s.Status == Fail {
				failed = append(failed, s.ID+": "+s.Error)
			}
		}
		calls := strings.Join(f.calls, "\n")
		switch {
		case want == "" && (got.Status != Pass || len(r.run.Steps) != 5):
			t.Errorf("a healthy cluster: want PASS with 5 steps, got %s with %d steps: %v", got.Status, len(r.run.Steps), failed)
		case want != "" && (got.Status != Fail || !slices.ContainsFunc(failed, func(s string) bool { return strings.HasPrefix(s, want) })):
			t.Errorf("%s: want a failed step %q, got %s: %v", defect, want, got.Status, failed)
		case !strings.Contains(calls, "region-a: delete namespace acc-g15-"):
			t.Errorf("%s: the namespace of the check is not deleted: %s", defect, calls)
		case f.pod != "" && !strings.Contains(calls, "region-a: -n guacamole delete pod acc-g15-"):
			t.Errorf("%s: the session pod is not deleted: %s", defect, calls)
		case strings.Contains(calls, "exec -i pol-dial-") && !strings.Contains(calls, "-n guacamole delete pod pol-dial-"):
			t.Errorf("%s: the dial pod is not deleted: %s", defect, calls)
		}
		if defect != "" {
			continue
		}
		for _, part := range []string{"image: " + g15Guacd, "image: " + g15Agent, "access.openova.io/session-uid: 7c1d0e3a-5b2f-4c1e-9a0d-3f6b8e2c4d10",
			`"sessionUID": "7c1d0e3a-5b2f-4c1e-9a0d-3f6b8e2c4d10"`, `"protocol": "ssh"`, `"chunkBytes": 0`, "serviceAccountName: access-session",
			"automountServiceAccountToken: false", "acc-check: acc-g15", `"-b", "127.0.0.1", "-l", "4822"`} {
			if !strings.Contains(f.pod, part) {
				t.Errorf("the session pod lacks %q:\n%s", part, f.pod)
			}
		}
		if strings.Contains(f.pod, "${") || strings.Contains(f.pod, "secrets-store") || strings.Contains(f.pod, "/var/run/secrets/openova") {
			t.Errorf("the session pod is not filled in, or has a credential volume:\n%s", f.pod)
		}
		if !strings.Contains(calls, "region-a: -n guacamole create -f -") || !strings.Contains(calls, "--as="+accG13Flux+" apply --server-side --field-manager=acc-g15") {
			t.Errorf("the target is not applied as the Flux account, or the dial pod is not in namespace guacamole: %s", calls)
		}
	}
}
