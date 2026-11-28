package suite

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check ACC-G15, the session images start hardened (ADR-0033). In the primary
// region it reads the images the access controller names in its environment,
// ACCESS_IMAGE_GUACD and ACCESS_IMAGE_AGENT, and creates in namespace guacamole
// one pod of the fixed shape of a session pod with those two images, no
// credential volume and an agent configuration for protocol ssh with chunkBytes
// 0 (fixtures/access/g15/pod.yaml). The pod runs as the service account
// access-session, which the controller makes for a Ready target of kind pod;
// the check applies one as the Flux account (the target fixture of ACC-G13) in
// a namespace of its own. Then both containers must be Running for 30 seconds
// with restart count 0, and from a dial pod of namespace guacamole (the Dialer
// of NET-G06; namespace guacamole admits its own pods) a dial of the pod's
// address on port 4822 must be refused (guacd listens on the loopback address
// only) and one on port 8443 must connect (the agent). Every object the check
// creates carries the label acc-check: acc-g15 and is deleted at its end.
const (
	accG15ID      = "ACC-G15"
	accG15Fixture = "tests/acceptance/sovereign/fixtures/access/g15/pod.yaml"
	accG15NS      = "guacamole"
	accG15Hold    = 30 * time.Second
	accG15Start   = 3 * time.Minute // a cold node pulls both images first
)

var accG15Digest = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

type accG15Check struct {
	kubectl kubectlFunc
	now     func() time.Time
	sleep   func(ctx context.Context, d time.Duration) error
	uid     func() (string, error)
}

func init() {
	RegisterCheck(accG15Check{kubectl: kube.Region.Run, now: time.Now, uid: accG15UID, sleep: func(ctx context.Context, d time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
			return nil
		}
	}})
}

func (accG15Check) ID() string { return accG15ID }

// accG15UID is a random UUID (version 4), the UID of the pod's configuration.
func accG15UID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

func (c accG15Check) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	region, fixtures := cc.Regions[0], map[string]string{}
	for name, path := range map[string]string{"pod": accG15Fixture, "target": accG13Fixtures + "/target.yaml"} {
		b, err := os.ReadFile(filepath.Clean(filepath.Join(cc.RepoRoot, filepath.FromSlash(path))))
		if err != nil {
			return fmt.Errorf("read the fixture %s: %w", path, err)
		}
		fixtures[name] = string(b)
	}
	uid, err := c.uid()
	if err != nil {
		return err
	}
	run := strings.ToLower(NewRunID(c.now()))
	ns, pod := "acc-g15-"+run, "acc-g15-"+run
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	defer func() {
		_, _ = c.kubectl(region, cleanup, nil, "-n", accG15NS, "delete", "pod", pod, "--wait=false", "--ignore-not-found")
		_, _ = c.kubectl(region, cleanup, nil, "delete", "namespace", ns, "--wait=false", "--ignore-not-found")
	}()
	var images map[string]string
	if !checkStep(rec, accG15ID, 1, "the access controller names the guacd and agent images by digest (ACCESS_IMAGE_GUACD, ACCESS_IMAGE_AGENT)", func(ev Evidence) error {
		images, err = c.images(ctx, region, ev)
		return err
	}) {
		return nil
	}
	if !checkStep(rec, accG15ID, 2, "a target of kind pod applied as the Flux account is Ready, so the service account access-session exists", func(ev Evidence) error {
		return c.account(ctx, region, ns, fixtures["target"], ev)
	}) {
		return nil
	}
	if !checkStep(rec, accG15ID, 3, "the session pod with the two images, no credential volume and an ssh configuration with chunkBytes 0 has both containers Running", func(ev Evidence) error {
		manifest, err := accG13Fill(fixtures["pod"], map[string]string{"NAME": pod, "UID": uid, "GUACD_IMAGE": images["guacd"], "AGENT_IMAGE": images["agent"]})
		if err != nil {
			return err
		}
		if _, err := c.kubectl(region, ctx, strings.NewReader(manifest), "-n", accG15NS, "create", "-f", "-"); err != nil {
			return fmt.Errorf("create the pod %s/%s: %w", accG15NS, pod, err)
		}
		return c.running(ctx, region, pod, ev)
	}) {
		return nil
	}
	var podIP string
	checkStep(rec, accG15ID, 4, "both containers stay Running for 30 seconds with restart count 0, each image referenced by digest", func(ev Evidence) error {
		podIP, err = c.hold(ctx, region, pod, images, ev)
		return err
	})
	checkStep(rec, accG15ID, 5, "from a dial pod of namespace guacamole the pod's port 4822 is refused and its port 8443 connects", func(ev Evidence) error {
		return c.dials(ctx, region, run, podIP, ev)
	})
	return nil
}

// images reads the two images from the environment of the controller's
// container.
func (c accG15Check) images(ctx context.Context, region kube.Region, ev Evidence) (map[string]string, error) {
	var d kube.Object
	if err := (accG13Check{kubectl: c.kubectl}).get(ctx, region, &d, "deployment", "access-controller", "-n", accG15NS); err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, ct := range d.Objects("spec", "template", "spec", "containers") {
		if ct.Str("name") != "controller" {
			continue
		}
		for _, e := range ct.Objects("env") {
			env[e.Str("name")] = e.Str("value")
		}
	}
	out := map[string]string{"guacd": env["ACCESS_IMAGE_GUACD"], "agent": env["ACCESS_IMAGE_AGENT"]}
	ev["images"] = out
	for k, name := range map[string]string{"guacd": "ACCESS_IMAGE_GUACD", "agent": "ACCESS_IMAGE_AGENT"} {
		if !accG15Digest.MatchString(out[k]) {
			return nil, fmt.Errorf("the controller's %s is %q, not an image referenced by digest", name, out[k])
		}
	}
	return out, nil
}

// account applies the target and waits until it is Ready and the service
// account access-session exists in namespace guacamole.
func (c accG15Check) account(ctx context.Context, region kube.Region, ns, fixture string, ev Evidence) error {
	if _, err := c.kubectl(region, ctx, nil, "create", "namespace", ns); err != nil {
		return err
	}
	manifest, err := accG13Fill(fixture, map[string]string{"NAMESPACE": ns})
	if err == nil {
		_, err = c.kubectl(region, ctx, strings.NewReader(manifest), "--as="+accG13Flux, "apply", "--server-side", "--field-manager=acc-g15", "-f", "-")
	}
	if err != nil {
		return fmt.Errorf("apply the target as %s: %w", accG13Flux, err)
	}
	g := accG13Check{kubectl: c.kubectl}
	for deadline := c.now().Add(accG13Ready); ; {
		var o kube.Object
		if err := g.get(ctx, region, &o, "accesstarget", "g13-shell", "-n", ns); err != nil {
			return err
		}
		status, reason, message, _ := o.Condition("Ready")
		ev["ready"] = map[string]string{"status": status, "reason": reason, "message": message}
		if status == "True" {
			break
		}
		if !c.now().Before(deadline) {
			return fmt.Errorf("the target is not Ready after %s: Ready is %q (%s: %s)", accG13Ready, status, reason, message)
		}
		if err := c.sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	var sa kube.Object
	if err := g.get(ctx, region, &sa, "serviceaccount", "access-session", "-n", accG15NS); err != nil {
		return err
	}
	ev["serviceAccount"] = accG15NS + "/" + sa.Name()
	return nil
}

// accG15State is what one read of the pod gave for one container.
type accG15State struct {
	Running   bool   `json:"running"`
	StartedAt string `json:"startedAt"`
	Restarts  int    `json:"restartCount"`
	Image     string `json:"image"`
	Ended     string `json:"terminated,omitempty"`
}

// read reads the pod: its phase, its address, and the state of each container
// by name, with the image of its spec.
func (c accG15Check) read(ctx context.Context, region kube.Region, pod string) (kube.Object, map[string]accG15State, error) {
	var o kube.Object
	if err := (accG13Check{kubectl: c.kubectl}).get(ctx, region, &o, "pod", pod, "-n", accG15NS); err != nil {
		return o, nil, err
	}
	states := map[string]accG15State{}
	for _, ct := range o.Objects("spec", "containers") {
		states[ct.Str("name")] = accG15State{Image: ct.Str("image")}
	}
	for _, s := range o.Objects("status", "containerStatuses") {
		st := states[s.Str("name")]
		st.Running, st.StartedAt, st.Restarts = s.Get("state", "running") != nil, s.Str("state", "running", "startedAt"), s.Int("restartCount")
		if s.Get("restartCount") == nil {
			st.Restarts = -1 // not reported: never read as no restart
		}
		st.Ended = s.Str("state", "terminated", "reason")
		states[s.Str("name")] = st
	}
	return o, states, nil
}

// running waits until both containers are Running.
func (c accG15Check) running(ctx context.Context, region kube.Region, pod string, ev Evidence) error {
	for deadline := c.now().Add(accG15Start); ; {
		o, states, err := c.read(ctx, region, pod)
		if err != nil {
			return err
		}
		ev["phase"], ev["containers"] = o.Str("status", "phase"), states
		if states["guacd"].Running && states["agent"].Running {
			return nil
		}
		for name, st := range states {
			if st.Ended != "" {
				return fmt.Errorf("container %s ended (%s) before both were Running", name, st.Ended)
			}
		}
		if !c.now().Before(deadline) {
			return fmt.Errorf("the containers are not both Running after %s: %+v", accG15Start, states)
		}
		if err := c.sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// hold reads the pod once a second for 30 seconds: both containers Running,
// started at the same instant as at the first read, restart count 0, the
// images the controller names. It returns the pod's address.
func (c accG15Check) hold(ctx context.Context, region kube.Region, pod string, images map[string]string, ev Evidence) (string, error) {
	var first map[string]accG15State
	var ip string
	reads := 0
	for start := c.now(); ; reads++ {
		o, states, err := c.read(ctx, region, pod)
		if err != nil {
			return "", err
		}
		if first == nil {
			first, ip = states, o.Str("status", "podIP")
			ev["first"], ev["podIP"] = first, ip
		}
		ev["reads"], ev["last"] = reads+1, states
		for _, name := range []string{"guacd", "agent"} {
			st := states[name]
			switch {
			case st.Image != images[name] || !accG15Digest.MatchString(st.Image):
				return "", fmt.Errorf("container %s runs the image %q, want %q by digest", name, st.Image, images[name])
			case !st.Running || st.Restarts != 0 || st.StartedAt != first[name].StartedAt:
				return "", fmt.Errorf("after %.0f seconds container %s is running=%t with restart count %d, started at %q (first read: %q)",
					c.now().Sub(start).Seconds(), name, st.Running, st.Restarts, st.StartedAt, first[name].StartedAt)
			}
		}
		if !c.now().Before(start.Add(accG15Hold)) {
			if ip == "" {
				return "", errors.New("the pod has no address")
			}
			return ip, nil
		}
		if err := c.sleep(ctx, time.Second); err != nil {
			return "", err
		}
	}
}

// dials dials the pod's two ports from a dial pod of namespace guacamole.
func (c accG15Check) dials(ctx context.Context, region kube.Region, run, ip string, ev Evidence) error {
	if ip == "" {
		return errors.New("the pod did not stay Running, so it was not dialed")
	}
	d := &Dialer{Kubectl: c.kubectl, Region: region, Run: run}
	from := DialSource{Namespace: accG15NS, Labels: map[string]string{"acc-check": "acc-g15"}}
	defer func() { _ = d.Stop(ctx) }()
	if err := d.Start(ctx, []DialSource{from}); err != nil {
		return err
	}
	ev["dialPods"] = d.Pods()
	for _, want := range []struct {
		port    int
		outcome string
	}{{4822, DialRefused}, {8443, DialConnected}} {
		res, err := d.Dial(ctx, from, DialTarget{Address: ip, Port: want.port})
		if err != nil {
			return err
		}
		ev[fmt.Sprintf("port%d", want.port)] = res
		if res.Control != DialConnected {
			return fmt.Errorf("the dial pod did not reach %s:%d (%s), so port %d was not measured", dialControlHost, dialControlPort, res.Control, want.port)
		}
		if res.Outcome != want.outcome {
			return fmt.Errorf("the dial of %s:%d %s (%s), want %s", ip, want.port, res.Outcome, res.Output, want.outcome)
		}
	}
	return nil
}
