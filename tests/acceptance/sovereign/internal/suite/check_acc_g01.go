package suite

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check ACC-G01, Gateway closed by default. In every region: (1) the Deployment oidc-gate/oidc-gate-guacamole is
// absent, or its container oauth2-proxy has accessGateUpstreamArg, or the groups it admits (a member of any one of its
// --allowed-group and OAUTH2_PROXY_ALLOWED_GROUPS entries; none admits every realm user) are exactly accessGateGroup;
// (2) namespace guacamole holds no Deployment guacamole-server and no CloudNativePG Cluster: the web application and
// its database are removed (ACC-102, ADR-0033 decision A3); (3) the DaemonSet catalyst-system/k8s-ws-proxy has
// ALLOWED_NAMESPACES=catalyst-system or no SHARED_SECRET_FILE. A part that was not measured fails. The check only
// reads (kubectl get).
const accessGateID = "ACC-G01"

const (
	accessGateGroup       = "sovereign-admins"
	accessGateUpstreamArg = "--upstream=http://access-controller.guacamole.svc.cluster.local:8080"
	accessGateWebapp      = "guacamole-server"
	accessGateAllowed     = "catalyst-system"
)

type accessGateCheck struct{}

func init() { RegisterCheck(accessGateCheck{}) }

func (accessGateCheck) ID() string { return accessGateID }

func (accessGateCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	parts := []struct {
		key   string
		judge func(context.Context, kube.Region) (string, error)
	}{{"gate", accessGateGate}, {"store", accessGateStore}, {"proxy", accessGateProxy}}
	for i, region := range cc.Regions {
		rec.Step(accessGateID+"."+strconv.Itoa(i+1), "the access gateway is closed by default in region "+region.ID, func(ev Evidence) error {
			var fails []string
			for _, p := range parts {
				seen, err := p.judge(ctx, region)
				ev[p.key] = seen
				if err != nil {
					fails = append(fails, err.Error())
				}
			}
			if len(fails) > 0 {
				return fmt.Errorf("region %s: %s", region.ID, strings.Join(fails, "; "))
			}
			return nil
		})
	}
	return nil
}

// accessGateWorkload returns the named object of the list, or nil.
func accessGateWorkload(ctx context.Context, region kube.Region, resource, ns, name string) (kube.Object, error) {
	list, err := region.GetList(ctx, resource, ns)
	if err != nil {
		return nil, err
	}
	for _, w := range list {
		if w.Name() == name {
			return w, nil
		}
	}
	return nil, nil
}

// accessGateContainer returns the named container of a workload.
func accessGateContainer(w kube.Object, name string) (kube.Object, error) {
	for _, c := range w.Objects("spec", "template", "spec", "containers") {
		if c.Str("name") == name {
			return c, nil
		}
	}
	return nil, fmt.Errorf("%s/%s has no container %s", w.Namespace(), w.Name(), name)
}

func accessGateGate(ctx context.Context, region kube.Region) (string, error) {
	w, err := accessGateWorkload(ctx, region, "deployments", "oidc-gate", "oidc-gate-guacamole")
	if err != nil {
		return "", err
	}
	if w == nil {
		return "absent", nil
	}
	c, err := accessGateContainer(w, "oauth2-proxy")
	if err != nil {
		return "no container", err
	}
	args, _ := c.Get("args").([]any)
	for _, e := range c.Objects("env") {
		if e.Str("name") == "OAUTH2_PROXY_ALLOWED_GROUPS" {
			args = append(args, "--allowed-group="+e.Str("value"))
		}
	}
	groups, upstream := []string{}, "none"
	for _, a := range args {
		s, _ := a.(string)
		switch {
		case s == accessGateUpstreamArg:
			return "upstream is the access controller", nil
		case s == "--allowed-group" || strings.HasPrefix(s, "--allowed-group="): // a bare flag is an unread group: it fails
			groups = append(groups, strings.Split(strings.TrimPrefix(s, "--allowed-group="), ",")...)
		case strings.HasPrefix(s, "--upstream="):
			upstream = strings.TrimPrefix(s, "--upstream=")
		}
	}
	if got := strings.Join(groups, ","); len(groups) == 0 || slices.ContainsFunc(groups, func(g string) bool { return g != accessGateGroup }) {
		return "groups [" + got + "]", fmt.Errorf("oidc-gate/oidc-gate-guacamole admits the groups [%s] (none: every user of the realm), want exactly [%s] or the access controller as upstream (upstream: %s)", got, accessGateGroup, upstream)
	}
	return "allowed-group sovereign-admins", nil
}

// accessGateStore passes when namespace guacamole holds no Deployment guacamole-server and no CloudNativePG Cluster.
func accessGateStore(ctx context.Context, region kube.Region) (string, error) {
	clusters, err := region.GetList(ctx, cnpgResource, "guacamole")
	if err != nil {
		return "", err
	}
	w, err := accessGateWorkload(ctx, region, "deployments", "guacamole", accessGateWebapp)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(clusters))
	for _, c := range clusters {
		names = append(names, c.Name())
	}
	if len(clusters) == 0 && w == nil {
		return "no web application, no database", nil
	}
	return fmt.Sprintf("Deployment %s present=%v, Clusters [%s]", accessGateWebapp, w != nil, strings.Join(names, ",")),
		fmt.Errorf("namespace guacamole holds the Deployment %s: %v, and the CloudNativePG Clusters [%s]; want neither: the web application and its database are not removed",
			accessGateWebapp, w != nil, strings.Join(names, ","))
}

func accessGateProxy(ctx context.Context, region kube.Region) (string, error) {
	w, err := accessGateWorkload(ctx, region, "daemonsets", "catalyst-system", "k8s-ws-proxy")
	if err != nil {
		return "", err
	}
	if w == nil {
		return "absent", errors.New("no DaemonSet k8s-ws-proxy in namespace catalyst-system: nothing was measured")
	}
	c, err := accessGateContainer(w, "k8s-ws-proxy")
	if err != nil {
		return "no container", err
	}
	allowed, shared := "", false
	for _, e := range c.Objects("env") {
		switch e.Str("name") {
		case "ALLOWED_NAMESPACES":
			allowed = e.Str("value")
		case "SHARED_SECRET_FILE":
			shared = true
		}
	}
	switch {
	case allowed == accessGateAllowed:
		return "ALLOWED_NAMESPACES=" + allowed, nil
	case !shared:
		return "no shared-secret leg", nil
	}
	return "ALLOWED_NAMESPACES=" + allowed, fmt.Errorf("catalyst-system/k8s-ws-proxy execs into namespaces %q, want %q", allowed, accessGateAllowed)
}
