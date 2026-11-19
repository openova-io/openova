package suite

// Helpers of the model checks AIP-G02 to AIP-G05 (docs/contracts/model-contract.md): a forward to
// the Service model-gateway of the first region and one to the admin port of each meter pod (the
// ring of entries is per pod), and an agent token minted as check AIP-G07 does. Each check is a file.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/modelcontract"
)

const (
	modelNS          = "model-gateway"
	modelGatewayPort = 8080
	modelMeterLabel  = "app.kubernetes.io/name=model-meter"
	modelMeterAdmin  = 9090
	modelEntryWait   = 30 * time.Second // the longest wait for the entry of an answered call
)

type modelStep = func(name string, fn func(ev Evidence) error) bool

// modelSession is what the steps of a model check work with.
type modelSession struct {
	client modelcontract.Client
	meters []string // the admin address of each meter pod
	stops  []func()
}

// modelCheck is one model check. Its first step opens the session; its steps run after it.
type modelCheck struct {
	id    string
	steps func(ctx context.Context, cc *CheckContext, s *modelSession, step modelStep)
}

func (c modelCheck) ID() string { return c.id }

func (c modelCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	n := 0
	step := func(name string, fn func(ev Evidence) error) bool {
		n++
		return checkStep(rec, c.id, n, name, fn)
	}
	var s *modelSession
	if step("the model gateway and every meter pod are forwarded, and an agent token is minted", func(ev Evidence) error {
		var err error
		s, err = openModelSession(ctx, cc, ev)
		return err
	}) {
		defer s.close()
		c.steps(ctx, cc, s, step)
	}
	return nil
}

func openModelSession(ctx context.Context, cc *CheckContext, ev Evidence) (*modelSession, error) {
	if cc.API == nil || cc.API.Base == "" || len(cc.Regions) == 0 {
		return nil, errors.New("the run has no console address or no region")
	}
	s, region := &modelSession{}, cc.Regions[0]
	err := func() error {
		gw, err := s.forward(ctx, region, "service/model-gateway", modelGatewayPort)
		if err != nil {
			return err
		}
		out, err := region.Run(ctx, nil, "-n", modelNS, "get", "pods", "-l", modelMeterLabel, "-o", "jsonpath={.items[*].metadata.name}")
		if err != nil {
			return err
		}
		pods := strings.Fields(string(out))
		if ev["meter_pods"] = pods; len(pods) == 0 {
			return fmt.Errorf("no pod in namespace %s has the label %s", modelNS, modelMeterLabel)
		}
		for _, p := range pods {
			addr, err := s.forward(ctx, region, "pod/"+p, modelMeterAdmin)
			if err != nil {
				return err
			}
			s.meters = append(s.meters, "http://"+addr)
		}
		token, err := mintAgentToken(ctx, cc, region)
		s.client = modelcontract.Client{BaseURL: "http://" + gw + "/v1", Token: token}
		return err
	}()
	if err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *modelSession) close() {
	for _, stop := range s.stops {
		stop()
	}
}

// forward starts `kubectl port-forward` to port of target (service/<name> or pod/<name>) in the
// namespace model-gateway and returns the local address that kubectl prints.
func (s *modelSession) forward(ctx context.Context, region kube.Region, target string, port int) (string, error) {
	args, bin := []string{"-n", modelNS, "port-forward", target, ":" + strconv.Itoa(port)}, region.Kubectl
	if region.Kubeconfig != "" {
		args = append([]string{"--kubeconfig", region.Kubeconfig}, args...)
	}
	if bin == "" {
		bin = "kubectl"
	}
	fctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	cmd := exec.CommandContext(fctx, bin, args...) //nolint:gosec // G204: the kubectl of the region, with arguments of the check
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		cancel()
		return "", fmt.Errorf("[%s] kubectl port-forward %s: %v", region.ID, target, err)
	}
	s.stops = append(s.stops, func() { cancel(); _ = cmd.Wait() })
	found := make(chan string, 1)
	go func() { // reads stdout to its end, so that kubectl never blocks on a full pipe
		addr, sc := "", bufio.NewScanner(out)
		for sc.Scan() {
			if m := forwardRE.FindStringSubmatch(sc.Text()); m != nil && addr == "" {
				addr = m[1]
				found <- addr
			}
		}
		if addr == "" {
			found <- ""
		}
	}()
	select {
	case addr := <-found:
		if addr != "" {
			return addr, nil
		}
	case <-time.After(isoForwardWait):
	}
	return "", fmt.Errorf("[%s] kubectl port-forward %s/%s printed no local address: %s", region.ID, modelNS, target, strings.TrimSpace(stderr.String()))
}

// mintAgentToken mints a viewer token for agent conformance-probe of Organization conformance,
// as check AIP-G07 does. No error holds a token.
func mintAgentToken(ctx context.Context, cc *CheckContext, region kube.Region) (string, error) {
	out, err := region.Run(ctx, nil, "-n", aipG07NS, "create", "token", aipG07SA, "--duration=10m")
	if err != nil {
		return "", err
	}
	client := cc.API.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	a, err := agentCall(ctx, client, cc.API.Base, http.MethodPost, aipG07Issue, strings.TrimSpace(string(out)),
		map[string]any{"organization": aipG07Org, "agent": aipG07Agent, "tier": "viewer"})
	if err != nil {
		return "", err
	}
	if token, _ := a.body["token"].(string); a.status == http.StatusOK && token != "" {
		return token, nil
	}
	return "", fmt.Errorf("POST %s answered %d %q, want 200 with a token", aipG07Issue, a.status, a.code)
}

// entries asks every meter pod for the entry of requestID.
func (s *modelSession) entries(ctx context.Context, requestID string) ([]modelcontract.Entry, error) {
	var out []modelcontract.Entry
	for _, m := range s.meters {
		e, found, err := modelcontract.ReadEntry(ctx, m, requestID)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, e)
		}
	}
	return out, nil
}

// entry waits until the meter has the entry of an answered call: exactly one pod returns it.
func (s *modelSession) entry(ctx context.Context, requestID string) (modelcontract.Entry, error) {
	var got []modelcontract.Entry
	_, err := poll(ctx, modelEntryWait, time.Second, func() (bool, string, error) {
		var err error
		got, err = s.entries(ctx, requestID)
		if len(got) > 1 {
			err = fmt.Errorf("%d meter pods have the entry of request %s, want exactly one", len(got), requestID)
		}
		return len(got) == 1, fmt.Sprintf("%d meter pods have the entry of request %s", len(got), requestID), err
	})
	if err != nil {
		return modelcontract.Entry{}, err
	}
	return got[0], nil
}

type modelRunKey struct{}

// withModelRun returns ctx carrying the run cc, which ModelUsageProbe reads.
func withModelRun(ctx context.Context, cc *CheckContext) context.Context {
	return context.WithValue(ctx, modelRunKey{}, cc)
}

type modelProbeMemo struct {
	once                    sync.Once
	organization, id, alias string
	err                     error
}

var (
	modelProbeMu sync.Mutex
	modelProbes  = map[*CheckContext]*modelProbeMemo{}
)

// ModelUsageProbe makes one metered call on conformance-echo for the run that ctx carries
// (withModelRun) and returns the Organization of its usage event, the alias and the request
// identifier. The call also sends x-openova-organization: someone-else, a header the gateway
// removes. The meter's entry must be published with the measures of the answer's usage. The
// call is made once per run; every later caller gets the stored result. Check AIP-G04 reads it,
// and so do BSS-G09 and AIP-G09, which need a model call with no real model.
func ModelUsageProbe(ctx context.Context) (organization, alias, requestID string, err error) {
	cc, _ := ctx.Value(modelRunKey{}).(*CheckContext)
	if cc == nil {
		return "", "", "", errors.New("ModelUsageProbe: the context carries no run")
	}
	modelProbeMu.Lock()
	m := modelProbes[cc]
	if m == nil {
		m = &modelProbeMemo{alias: modelcontract.ConformanceAlias}
		modelProbes[cc] = m
	}
	modelProbeMu.Unlock()
	m.once.Do(func() {
		s, err := openModelSession(ctx, cc, Evidence{})
		if m.err = err; err != nil {
			return
		}
		defer s.close()
		r, err := s.client.Chat(ctx, m.alias, "probe", http.Header{"X-Openova-Organization": {"someone-else"}})
		if m.id, m.err = r.RequestID, err; err != nil {
			return
		}
		e, err := s.entry(ctx, r.RequestID)
		if m.err = err; err == nil {
			m.organization, m.err = e.Organization, e.Metered(m.alias, false, *r.Usage)
		}
	})
	return m.organization, m.alias, m.id, m.err
}
