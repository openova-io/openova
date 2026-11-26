package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check ACC-G13, the grant life cycle from objects (ADR-0033 decisions A4, A6
// and A21). In the primary region, in a namespace of its own (acc-g13-<run>),
// it applies the target and the grants of tests/acceptance/sovereign/fixtures/access/g13
// as the Flux account and reads their status once a second; each step names
// what it asserts. The check deletes the namespace, and with it every object it
// applied; the service account access-session that the controller makes in
// namespace guacamole for the targets of kind pod is the controller's own.
const (
	accG13ID       = "ACC-G13"
	accG13Fixtures = "tests/acceptance/sovereign/fixtures/access/g13"
	accG13Flux     = "system:serviceaccount:flux-system:kustomize-controller"
	accG13Lock     = "access-kinds-writers"
	accG13LockMsg  = "AccessGrant may be written only by Git through the Catalyst API"
	accG13SelfMsg  = "an approval may not be given by the grant's own subject"
	accG13Within   = 10 * time.Second
	accG13Ready    = 30 * time.Second
)

type accG13Check struct {
	kubectl kubectlFunc
	now     func() time.Time
	sleep   func(ctx context.Context, d time.Duration) error
}

func init() {
	RegisterCheck(accG13Check{kubectl: kube.Region.Run, now: time.Now, sleep: func(ctx context.Context, d time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
			return nil
		}
	}})
}

func (accG13Check) ID() string { return accG13ID }

// accG13Grant is one grant of the check: its fixture, its window and the
// phases read, each when it first appeared.
type accG13Grant struct {
	name, fixture string
	from, to      time.Time
	seen          []accG13Phase
}

type accG13Phase struct {
	Phase string  `json:"phase"`
	At    float64 `json:"secondsAfterApply"`
	at    time.Time
}

func (c accG13Check) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	primary, fixtures := cc.Regions[0], map[string]string{}
	for _, f := range []string{"target", "grant-approved", "grant-unapproved", "grant-self-approved"} {
		b, err := os.ReadFile(filepath.Clean(filepath.Join(cc.RepoRoot, filepath.FromSlash(accG13Fixtures), f+".yaml")))
		if err != nil {
			return fmt.Errorf("read the fixture %s: %w", f, err)
		}
		fixtures[f] = string(b)
	}
	ns := "acc-g13-" + strings.ToLower(NewRunID(c.now()))
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	defer func() {
		_, _ = c.kubectl(primary, cleanup, nil, "delete", "namespace", ns, "--wait=false", "--ignore-not-found")
	}()
	if _, err := c.kubectl(primary, ctx, nil, "create", "namespace", ns); err != nil {
		return err
	}
	fill := func(g *accG13Grant) (string, error) {
		return accG13Fill(fixtures[g.fixture], map[string]string{"NAMESPACE": ns, "NAME": g.name, "NOT_BEFORE": g.from.Format(time.RFC3339),
			"NOT_AFTER": g.to.Format(time.RFC3339), "APPROVED_AT": g.from.Add(-time.Minute).Format(time.RFC3339)})
	}
	if !checkStep(rec, accG13ID, 1, "a target of kind pod applied as the Flux account is Ready", func(ev Evidence) error {
		return c.targetReady(ctx, primary, ns, fixtures["target"], ev)
	}) {
		return nil
	}
	t := c.now().UTC().Truncate(time.Second)
	grant := func(name, fixture string, from, to time.Duration) *accG13Grant {
		return &accG13Grant{name: name, fixture: fixture, from: t.Add(from), to: t.Add(to)}
	}
	refused := func(g *accG13Grant, verb []string, want []string, ev Evidence) error {
		manifest, err := fill(g)
		if err != nil {
			return err
		}
		if _, err = c.kubectl(primary, ctx, strings.NewReader(manifest), append(verb, "-f", "-")...); err == nil {
			return fmt.Errorf("grant %s was accepted", g.name) // the check deletes it with the namespace
		}
		ev["refusal"] = lastLine(err.Error())
		for _, w := range want {
			if !strings.Contains(err.Error(), w) {
				return fmt.Errorf("grant %s was refused, but not with %q: %s", g.name, w, lastLine(err.Error()))
			}
		}
		return nil
	}
	checkStep(rec, accG13ID, 2, "a grant approved by its own subject is refused by the API server", func(ev Evidence) error {
		return refused(grant("g13-self", "grant-self-approved", -time.Minute, 10*time.Minute), accG13Apply, []string{accG13SelfMsg}, ev)
	})
	checkStep(rec, accG13ID, 3, "a grant created as the cluster administrator is refused by the writer lock", func(ev Evidence) error {
		return refused(grant("g13-admin", "grant-approved", -time.Minute, 10*time.Minute), []string{"create"}, []string{accG13Lock, accG13LockMsg}, ev)
	})
	active := grant("g13-active", "grant-approved", -time.Minute, 10*time.Minute)
	pending := grant("g13-pending", "grant-unapproved", -time.Minute, 10*time.Minute)
	scheduled := grant("g13-scheduled", "grant-approved", 60*time.Second, 10*time.Minute)
	expiring := grant("g13-expiring", "grant-approved", -5*time.Second, 85*time.Second)
	var applied time.Time
	watchErr := errors.New("the grants were not applied")
	checkStep(rec, accG13ID, 4, "an approved grant inside its window is Active within 10 seconds", func(ev Evidence) error {
		if applied, watchErr = c.watch(ctx, primary, ns, []*accG13Grant{active, pending, scheduled, expiring}, fill, ev); watchErr != nil {
			return watchErr
		}
		return accG13Order(active, "", "Active", applied, ev)
	})
	watched := func(fn func(ev Evidence) error) func(ev Evidence) error {
		return func(ev Evidence) error {
			if watchErr != nil {
				return fmt.Errorf("the grants were not watched: %w", watchErr)
			}
			return fn(ev)
		}
	}
	checkStep(rec, accG13ID, 5, "a grant without approval on a target that requires one is Pending", watched(func(ev Evidence) error {
		if len(pending.seen) != 1 {
			ev[pending.name] = pending.seen
			return fmt.Errorf("grant %s went through %v, want Pending alone", pending.name, accG13Phases(pending))
		}
		return accG13Order(pending, "", "Pending", applied, ev)
	}))
	checkStep(rec, accG13ID, 6, "an approved grant whose window opens in 60 seconds is Scheduled, then Active within 10 seconds of notBefore",
		watched(func(ev Evidence) error { return accG13Order(scheduled, "Scheduled", "Active", scheduled.from, ev) }))
	checkStep(rec, accG13ID, 7, "an approved grant with a window of 90 seconds is Expired within 10 seconds of notAfter",
		watched(func(ev Evidence) error { return accG13Order(expiring, "Active", "Expired", expiring.to, ev) }))
	checkStep(rec, accG13ID, 8, "the controller runs in the primary region only", func(ev Evidence) error {
		return c.placement(ctx, cc.Regions, ev)
	})
	return nil
}

// accG13Apply applies a manifest as the Flux account, as Flux does: server-side.
var accG13Apply = []string{"--as=" + accG13Flux, "apply", "--server-side", "--field-manager=acc-g13"}

// accG13Fill fills in a fixture; a name the check does not fill in is an error.
func accG13Fill(text string, vars map[string]string) (string, error) {
	missing := []string{}
	out := os.Expand(text, func(k string) string {
		if _, ok := vars[k]; !ok {
			missing = append(missing, k)
		}
		return vars[k]
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("the fixture names %v, which the check does not fill in", missing)
	}
	return out, nil
}

// get reads one object or list as JSON into out.
func (c accG13Check) get(ctx context.Context, region kube.Region, out any, args ...string) error {
	b, err := c.kubectl(region, ctx, nil, append(append([]string{"get"}, args...), "-o", "json")...)
	if err == nil {
		err = json.Unmarshal(b, out)
	}
	if err != nil {
		return fmt.Errorf("read %s in region %s: %w", strings.Join(args, " "), region.ID, err)
	}
	return nil
}

func (c accG13Check) targetReady(ctx context.Context, region kube.Region, ns, fixture string, ev Evidence) error {
	manifest, err := accG13Fill(fixture, map[string]string{"NAMESPACE": ns})
	if err == nil {
		_, err = c.kubectl(region, ctx, strings.NewReader(manifest), append(accG13Apply, "-f", "-")...)
	}
	if err != nil {
		return fmt.Errorf("apply the target as %s: %w", accG13Flux, err)
	}
	for deadline := c.now().Add(accG13Ready); ; {
		var o kube.Object
		if err := c.get(ctx, region, &o, "accesstarget", "g13-shell", "-n", ns); err != nil {
			return err
		}
		status, reason, message, _ := o.Condition("Ready")
		ev["ready"] = map[string]string{"status": status, "reason": reason, "message": message}
		if status == "True" {
			return nil
		}
		if !c.now().Before(deadline) {
			return fmt.Errorf("the target is not Ready after %s: Ready is %q (%s: %s)", accG13Ready, status, reason, message)
		}
		if err := c.sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// watch applies the grants as the Flux account and reads their phases once a
// second until the scheduled one is Active and the expiring one Expired, or
// until 12 seconds after the later of those two window edges.
func (c accG13Check) watch(ctx context.Context, region kube.Region, ns string, grants []*accG13Grant, fill func(*accG13Grant) (string, error), ev Evidence) (time.Time, error) {
	docs, windows := []string{}, map[string]string{}
	for _, g := range grants {
		m, err := fill(g)
		if err != nil {
			return time.Time{}, err
		}
		docs, windows[g.name] = append(docs, m), g.from.Format(time.RFC3339)+" to "+g.to.Format(time.RFC3339)
	}
	applied := c.now()
	ev["windows"], ev["applied"] = windows, applied.UTC().Format(time.RFC3339)
	if _, err := c.kubectl(region, ctx, strings.NewReader(strings.Join(docs, "\n---\n")), append(accG13Apply, "-f", "-")...); err != nil {
		return applied, fmt.Errorf("apply the grants as %s: %w", accG13Flux, err)
	}
	scheduled, expiring := grants[2], grants[3]
	end := expiring.to
	if scheduled.from.After(end) {
		end = scheduled.from
	}
	end = end.Add(accG13Within + 2*time.Second)
	seen := map[string][]accG13Phase{}
	ev["seen"] = seen
	for {
		var list kube.List
		if err := c.get(ctx, region, &list, "accessgrants", "-n", ns); err != nil {
			return applied, err
		}
		now := c.now()
		for _, o := range list.Items {
			for _, g := range grants {
				if p := o.Str("status", "phase"); o.Name() == g.name && p != "" && (len(g.seen) == 0 || g.seen[len(g.seen)-1].Phase != p) {
					g.seen = append(g.seen, accG13Phase{Phase: p, At: now.Sub(applied).Seconds(), at: now})
					seen[g.name] = g.seen
				}
			}
		}
		if slices.Contains(accG13Phases(scheduled), "Active") && slices.Contains(accG13Phases(expiring), "Expired") || !now.Before(end) {
			return applied, nil
		}
		if err := c.sleep(ctx, time.Second); err != nil {
			return applied, err
		}
	}
}

func accG13Phases(g *accG13Grant) []string {
	out := []string{}
	for _, p := range g.seen {
		out = append(out, p.Phase)
	}
	return out
}

// accG13Order wants phase a read (when a is not empty) and then phase b, and b
// read first no later than 10 seconds after edge.
func accG13Order(g *accG13Grant, a, b string, edge time.Time, ev Evidence) error {
	ev[g.name] = g.seen
	names := accG13Phases(g)
	ia, ib := slices.Index(names, a), slices.Index(names, b)
	switch {
	case ib < 0 || (a != "" && (ia < 0 || ib < ia)):
		return fmt.Errorf("grant %s went through %v, want %s", g.name, names, strings.TrimPrefix(a+" and then "+b, " and then "))
	case g.seen[ib].at.After(edge.Add(accG13Within)):
		return fmt.Errorf("grant %s was %s %.0f seconds after %s, more than %s", g.name, b, g.seen[ib].at.Sub(edge).Seconds(), edge.UTC().Format(time.RFC3339), accG13Within)
	}
	return nil
}

// placement wants the Deployment of the controller ready in the primary region
// and no pod of the controller in any other.
func (c accG13Check) placement(ctx context.Context, regions []kube.Region, ev Evidence) error {
	var d kube.Object
	if err := c.get(ctx, regions[0], &d, "deployment", "access-controller", "-n", "guacamole"); err != nil {
		return err
	}
	ready, others := d.Int("status", "readyReplicas"), map[string][]string{}
	ev["primary"], ev["others"] = map[string]any{"region": regions[0].ID, "readyReplicas": ready}, others
	if ready < 1 {
		return fmt.Errorf("the primary region %s has no ready replica of Deployment guacamole/access-controller", regions[0].ID)
	}
	for _, r := range regions[1:] {
		var list kube.List
		if err := c.get(ctx, r, &list, "pods", "-n", "guacamole", "-l", "app.kubernetes.io/name=access-controller"); err != nil {
			return err
		}
		others[r.ID] = []string{}
		for _, p := range list.Items {
			others[r.ID] = append(others[r.ID], p.Name())
		}
		if len(others[r.ID]) > 0 {
			return fmt.Errorf("region %s, not the primary, runs pods of the controller: %v", r.ID, others[r.ID])
		}
	}
	return nil
}
