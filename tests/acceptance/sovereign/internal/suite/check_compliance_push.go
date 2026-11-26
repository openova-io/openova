package suite

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check CMP-G01, the score moves by push. The check applies one failing
// workload, the seed, in the first region: the region the API runs in. The
// change must reach an open compliance stream as a frame, and the score must
// count it. When the seed is deleted the violations must drop it.
// The check makes no request on a timer: while it waits it only reads the
// stream, and it reads a route again only after a frame arrived.
//
// The score counts the seed when the seed's own subject, the Deployment
// cmp-bad of the seed's namespace, enters the denominator and fails in it. The
// Sovereign score is the sum of the scores of its subjects, so the check reads
// that one subject by its name (GET .../compliance/resource), before the seed
// and after. It does not compare two readings of the whole Sovereign: other
// resources of the cluster come and go between them: on 24 November the
// Sovereign denominator fell from 618 to 615 while the violations listed the
// seed five times.
const (
	compliancePushID = "CMP-G01"
	complianceSeed   = "cmp-bad"
	complianceEngine = "kyverno" // the seed borrows the image of the first Deployment of this namespace
	complianceWait   = 120 * time.Second
)

// seedManifest is the seed as JSON. The first %q is its namespace, which is
// not the namespace of an Organization. The second is its image. The seed has
// zero replicas, so no Pod starts and the image never runs. It satisfies the
// policies that enforce after hand-over and fails the rule on two replicas.
const seedManifest = `{"apiVersion": "apps/v1", "kind": "Deployment",
  "metadata": {"name": "cmp-bad", "namespace": %q, "labels": {
    "app.kubernetes.io/managed-by": "flux", "kustomize.toolkit.fluxcd.io/name": "cmp-gate"}},
  "spec": {"replicas": 0, "selector": {"matchLabels": {"app": "cmp-bad"}}, "template": {
    "metadata": {"labels": {"app": "cmp-bad", "policy.cilium.io/enforced": "true"}},
    "spec": {"containers": [{"name": "main", "image": %q,
      "livenessProbe": {"tcpSocket": {"port": 8080}}, "readinessProbe": {"tcpSocket": {"port": 8080}},
      "resources": {"requests": {"cpu": "10m", "memory": "16Mi"}}}]}}}}`

// kubectlFunc runs kubectl against one region. It is kube.Region.Run, and a
// fake in the tests.
type kubectlFunc func(r kube.Region, ctx context.Context, stdin io.Reader, args ...string) ([]byte, error)

type compliancePushCheck struct {
	kubectl kubectlFunc
	wait    time.Duration // the longest wait for a frame
}

func init() { RegisterCheck(compliancePushCheck{kubectl: kube.Region.Run, wait: complianceWait}) }

func (compliancePushCheck) ID() string { return compliancePushID }

// complianceBase is the path of the compliance routes of the deployment under test.
func complianceBase(cc *CheckContext) (string, error) {
	if cc.API == nil || cc.Deploy == nil || cc.Deploy.ID == "" || len(cc.Regions) == 0 {
		return "", errors.New("the check needs a console session, the deployment identifier and one region")
	}
	return "/api/v1/sovereigns/" + url.PathEscape(cc.Deploy.ID) + "/compliance", nil
}

// checkStep records step <check>.<n> and reports whether it passed.
func checkStep(rec StepRecorder, check string, n int, name string, fn func(ev Evidence) error) bool {
	passed := false
	rec.Step(fmt.Sprintf("%s.%d", check, n), name, func(ev Evidence) error {
		err := fn(ev)
		passed = err == nil
		return err
	})
	return passed
}

func (c compliancePushCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	base, err := complianceBase(cc)
	if err != nil {
		return err
	}
	region := cc.Regions[0]
	ns := "cmp-gate-" + strings.ToLower(NewRunID(time.Now()))
	seedKey := "/" + ns + "/" + complianceSeed + `"` // how the resource key of the seed ends inside a frame
	streamCtx, closeStream := context.WithCancel(ctx)
	defer closeStream()
	var num0, den0, seedNum0, seedDen0 int
	var frames <-chan string

	if !checkStep(rec, compliancePushID, 1, "the Sovereign has a score and the seed has none yet (GET /api/v1/sovereigns/{id}/compliance/scorecard and .../resource)", func(ev Evidence) error {
		var err error
		num0, den0, err = sovereignScore(ctx, cc.API, base)
		ev["numerator"], ev["denominator"] = num0, den0
		if err == nil && den0 <= 0 {
			err = errors.New("scorecard empty: the Sovereign score has denominator 0")
		}
		if err == nil {
			seedNum0, seedDen0, err = seedScore(ctx, cc.API, base, ns)
			ev["seed_numerator"], ev["seed_denominator"] = seedNum0, seedDen0
		}
		if err == nil && seedDen0 != 0 {
			err = fmt.Errorf("the seed's subject deployment/%s/%s has denominator %d before the seed exists: the route does not read one subject", ns, complianceSeed, seedDen0)
		}
		return err
	}) {
		return nil
	}
	if !checkStep(rec, compliancePushID, 2, "the compliance stream is open (GET /api/v1/sovereigns/{id}/compliance/stream)", func(Evidence) error {
		var err error
		frames, err = openStream(streamCtx, cc.API, base+"/stream")
		return err
	}) {
		return nil
	}
	created := false
	ok := checkStep(rec, compliancePushID, 3, "the seed "+complianceSeed+" is applied in region "+region.ID, func(ev Evidence) error {
		out, err := c.kubectl(region, ctx, nil, "-n", complianceEngine, "get", "deploy",
			"-o", "jsonpath={.items[0].spec.template.spec.containers[0].image}")
		image := strings.TrimSpace(string(out))
		if err == nil && image == "" {
			err = errors.New("namespace " + complianceEngine + " has no Deployment: the seed has no image")
		}
		if err != nil {
			return err
		}
		ev["namespace"], ev["image"] = ns, image
		if _, err := c.kubectl(region, ctx, nil, "create", "namespace", ns); err != nil {
			return err
		}
		created = true
		_, err = c.kubectl(region, ctx, strings.NewReader(fmt.Sprintf(seedManifest, ns, image)), "-n", ns, "apply", "-f", "-")
		return err
	})
	ok = ok && checkStep(rec, compliancePushID, 4, "a frame names the seed, with no request made while waiting", func(ev Evidence) error {
		start := time.Now()
		n, err := onFrames(frames, c.wait, false, func(frame string) (bool, string, error) {
			return strings.Contains(frame, seedKey), "no frame named the seed", nil
		})
		ev["frames_read"], ev["waited_seconds"] = n, math.Round(time.Since(start).Seconds()*10)/10
		return err
	})
	ok = ok && checkStep(rec, compliancePushID, 5, "the score counts the seed's own subject, and the seed fails", func(ev Evidence) error {
		_, err := onFrames(frames, c.wait, true, func(string) (bool, string, error) {
			num, den, err := sovereignScore(ctx, cc.API, base)
			if err != nil {
				return false, "", err
			}
			seedNum, seedDen, err := seedScore(ctx, cc.API, base, ns)
			if err != nil {
				return false, "", err
			}
			fails, err := seedViolations(ctx, cc.API, base, ns)
			ev["numerator"], ev["denominator"], ev["seed_fails"] = num, den, fails
			ev["seed_numerator"], ev["seed_denominator"] = seedNum, seedDen
			counted := seedDen > seedDen0 && seedDen-seedNum > seedDen0-seedNum0 // the seed enters the denominator, and fails in it
			held := den >= seedDen && den-num >= seedDen-seedNum                 // the Sovereign score holds at least the seed's share
			return counted && held && len(fails) > 0,
				fmt.Sprintf("the seed's own score numerator %d and denominator %d (before the seed %d and %d), the Sovereign score numerator %d and denominator %d (before the seed %d and %d), %d violations of the seed",
					seedNum, seedDen, seedNum0, seedDen0, num, den, num0, den0, len(fails)), err
		})
		return err
	})
	if !created {
		return nil
	}
	for len(frames) > 0 { // a frame that counts below must arrive after the delete
		<-frames
	}
	deleted := checkStep(rec, compliancePushID, 6, "the namespace of the seed is deleted", func(Evidence) error {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		_, err := c.kubectl(region, cleanup, nil, "delete", "namespace", ns, "--wait=false", "--ignore-not-found")
		return err
	})
	if ok && deleted {
		checkStep(rec, compliancePushID, 7, "after a frame the violations no longer list the seed", func(ev Evidence) error {
			n, err := onFrames(frames, c.wait, false, func(string) (bool, string, error) {
				fails, err := seedViolations(ctx, cc.API, base, ns)
				return len(fails) == 0, fmt.Sprintf("the violations still list the seed %d times", len(fails)), err
			})
			ev["frames_read"] = n
			return err
		})
	}
	return nil
}

// sovereignScore reads the numerator and the denominator of the Sovereign score.
func sovereignScore(ctx context.Context, c *api.Client, base string) (num, den int, err error) {
	var card struct {
		Sovereign struct{ Numerator, Denominator int } `json:"sovereign"`
	}
	_, err = c.Get(ctx, base+"/scorecard", &card)
	return card.Sovereign.Numerator, card.Sovereign.Denominator, err
}

// seedScore reads the numerator and the denominator of the seed's own subject,
// the Deployment cmp-bad of the namespace ns. A subject the API has no verdict
// for answers zero and zero. No other resource of the cluster moves this score.
func seedScore(ctx context.Context, c *api.Client, base, ns string) (num, den int, err error) {
	var res struct{ Numerator, Denominator int }
	q := url.Values{"kind": {"deployment"}, "ns": {ns}, "name": {complianceSeed}}
	_, err = c.Get(ctx, base+"/resource?"+q.Encode(), &res)
	return res.Numerator, res.Denominator, err
}

// seedViolations returns the policies that the violations route lists for the
// seed. It reads every page of the route.
func seedViolations(ctx context.Context, c *api.Client, base, ns string) ([]string, error) {
	out := []string{}
	for offset := 0; ; {
		var page struct {
			Items []struct{ Resource, Namespace, Policy string } `json:"items"`
			Total int                                            `json:"total"`
		}
		if _, err := c.Get(ctx, fmt.Sprintf("%s/violations?limit=500&offset=%d", base, offset), &page); err != nil {
			return nil, err
		}
		for _, v := range page.Items {
			if v.Namespace == ns && strings.HasSuffix(v.Resource, "/"+complianceSeed) {
				out = append(out, v.Policy)
			}
		}
		if offset += len(page.Items); len(page.Items) == 0 || offset >= page.Total {
			sort.Strings(out)
			return out, nil
		}
	}
}

// readFrames calls emit with the data of every event of a text/event-stream
// body. A comment line, which starts with a colon, is not an event.
func readFrames(body io.Reader, emit func(data string)) error {
	lines := bufio.NewScanner(body)
	lines.Buffer(make([]byte, 0, 64<<10), 4<<20)
	var data []string
	for lines.Scan() {
		line := strings.TrimSuffix(lines.Text(), "\r")
		if line == "" && data != nil {
			emit(strings.Join(data, "\n"))
			data = nil
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(line[len("data:"):], " "))
		}
	}
	return lines.Err()
}

// openStream opens a stream route with the session of the console client and
// sends the data of its frames to a channel. The request has no time limit:
// it lives until ctx ends. The channel is closed when the body ends.
func openStream(ctx context.Context, c *api.Client, path string) (<-chan string, error) {
	client := &http.Client{} // no Timeout: it would end the stream
	if c.HTTP != nil {
		client.Transport = c.HTTP.Transport
	}
	res, err := c.Stream(ctx, client, path) // an answer HTTP 401 renews the session once
	if err != nil {
		return nil, err
	}
	if kind := res.Header.Get("Content-Type"); res.StatusCode != http.StatusOK || !strings.HasPrefix(kind, "text/event-stream") {
		_ = res.Body.Close()
		return nil, fmt.Errorf("GET %s: HTTP %d with content type %q, want 200 and text/event-stream", path, res.StatusCode, kind)
	}
	frames := make(chan string, 1024)
	go func() {
		defer close(frames)
		defer func() { _ = res.Body.Close() }()
		_ = readFrames(res.Body, func(data string) {
			select {
			case frames <- data:
			case <-ctx.Done():
			}
		})
	}()
	return frames, nil
}

// onFrames calls done with every frame that arrives, until done says true, the
// stream ends or the wait is over. With first set it calls done once before it
// waits for a frame. It returns the number of frames it read. When it gives up,
// the error holds the last text that done returned.
func onFrames(frames <-chan string, wait time.Duration, first bool, done func(frame string) (bool, string, error)) (int, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	n, last, frame := 0, "no frame arrived", ""
	for ; ; first = false {
		if !first {
			select {
			case data, open := <-frames:
				if !open {
					return n, fmt.Errorf("the stream ended after %d frames: %s", n, last)
				}
				n, frame = n+1, data
			case <-timer.C:
				return n, fmt.Errorf("not within %s (%d frames read): %s", wait, n, last)
			}
		}
		ok, text, err := done(frame)
		if err != nil || ok {
			return n, err
		}
		last = text
	}
}
