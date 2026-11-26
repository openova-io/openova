package suite

import (
	"context"
	"strings"
	"testing"
)

// accGateFixture is a list of one Deployment or DaemonSet with one container;
// env holds pairs of name and value.
func accGateFixture(namespace, name, container string, args []string, env ...string) string {
	vars := []obj{}
	for i := 0; i+1 < len(env); i += 2 {
		vars = append(vars, obj{"name": env[i], "value": env[i+1]})
	}
	c := obj{"name": container, "image": "busybox:1.36", "args": args, "env": vars}
	return kubeList(obj{"metadata": kubeMeta(namespace, name), "spec": obj{"template": obj{"spec": obj{"containers": []obj{c}}}}})
}

// accGateGate is the gate Deployment with the given extra arguments.
func accGateGate(extra ...string) string {
	return accGateFixture("oidc-gate", "oidc-gate-guacamole", "oauth2-proxy", append([]string{"--provider=keycloak-oidc", "--email-domain=*"}, extra...))
}

// accGateCluster is the CNPG Cluster list of namespace guacamole.
func accGateCluster(name, primary string) string {
	return kubeList(obj{"metadata": kubeMeta("guacamole", name), "status": obj{"currentPrimary": primary}})
}

const accGateOldUpstream = "--upstream=http://guacamole-server.guacamole.svc.cluster.local:80"

func init() {
	secret := []string{"SHARED_SECRET_FILE", "/etc/k8s-ws-proxy/secret/key"}
	proxy := func(env ...string) string {
		return accGateFixture("catalyst-system", "k8s-ws-proxy", "k8s-ws-proxy", nil, env...)
	}
	// Neither region runs the web application or a database (ACC-102).
	with := func(changes ...string) map[string]string {
		m := map[string]string{
			"* deployments oidc-gate":                 accGateGate(accGateOldUpstream, "--allowed-group=sovereign-admins"),
			"* clusters.postgresql.cnpg.io guacamole": kubeList(),
			"* deployments guacamole":                 kubeList(),
			"* daemonsets catalyst-system":            proxy(append(secret, "ALLOWED_NAMESPACES", "catalyst-system")...),
		}
		for i := 0; i+1 < len(changes); i += 2 {
			if m[changes[i]] = changes[i+1]; changes[i+1] == "" {
				delete(m, changes[i])
			}
		}
		return m
	}
	fakeAnswers["accgate-closed"] = with()
	fakeAnswers["accgate-gate-open"] = with("* deployments oidc-gate", accGateGate(accGateOldUpstream))
	fakeAnswers["accgate-gate-absent"] = with("* deployments oidc-gate", kubeList())
	fakeAnswers["accgate-gate-controller"] = with("* deployments oidc-gate", accGateGate("--upstream=http://access-controller.guacamole.svc.cluster.local:8080"))
	fakeAnswers["accgate-gate-other-group"] = with("* deployments oidc-gate", accGateGate(accGateOldUpstream, "--allowed-group=sovereign-viewers"))
	fakeAnswers["accgate-gate-admins-and-viewers"] = with("* deployments oidc-gate", accGateGate(accGateOldUpstream, "--allowed-group=sovereign-admins", "--allowed-group=sovereign-viewers"))
	fakeAnswers["accgate-gate-env-group"] = with("* deployments oidc-gate", accGateFixture("oidc-gate", "oidc-gate-guacamole", "oauth2-proxy", []string{"--allowed-group=sovereign-admins"}, "OAUTH2_PROXY_ALLOWED_GROUPS", "sovereign-admins,sovereign-viewers"))
	fakeAnswers["accgate-gate-no-container"] = with("* deployments oidc-gate", accGateFixture("oidc-gate", "oidc-gate-guacamole", "sidecar", []string{"--allowed-group=sovereign-admins"}))
	fakeAnswers["accgate-store-webapp"] = with("a deployments guacamole", accGateFixture("guacamole", "guacamole-server", "guacamole", nil))
	fakeAnswers["accgate-store-database"] = with("a clusters.postgresql.cnpg.io guacamole", accGateCluster("guacamole-pg", "guacamole-pg-1"))
	fakeAnswers["accgate-store-other-cluster"] = with("b clusters.postgresql.cnpg.io guacamole", accGateCluster("other-pg", "other-pg-1"))
	fakeAnswers["accgate-store-other-deployment"] = with("a deployments guacamole", accGateFixture("guacamole", "access-controller", "controller", nil))
	fakeAnswers["accgate-store-unlisted"] = with("* clusters.postgresql.cnpg.io guacamole", "", "a clusters.postgresql.cnpg.io guacamole", kubeList())
	fakeAnswers["accgate-store-not-served"] = with("* clusters.postgresql.cnpg.io guacamole", fakeNotServed)
	fakeAnswers["accgate-proxy-open"] = with("* daemonsets catalyst-system", proxy(secret...))
	fakeAnswers["accgate-proxy-wide"] = with("* daemonsets catalyst-system", proxy(append(secret, "ALLOWED_NAMESPACES", "catalyst-system,kube-system")...))
	fakeAnswers["accgate-proxy-no-shared-secret"] = with("* daemonsets catalyst-system", proxy())
	fakeAnswers["accgate-proxy-absent"] = with("* daemonsets catalyst-system", kubeList())
	fakeAnswers["accgate-proxy-no-container"] = with("* daemonsets catalyst-system", accGateFixture("catalyst-system", "k8s-ws-proxy", "other", nil))
	fakeAnswers["accgate-b-unreachable"] = with("* deployments oidc-gate", "", "a deployments oidc-gate", accGateGate("--allowed-group=sovereign-admins"))
}

func TestAccessGateCheck(t *testing.T) {
	if _, ok := registry[accessGateID]; !ok || accessGateID != "ACC-G01" {
		t.Fatalf("check %s is not registered", accessGateID)
	}
	// want: "" = PASS, else a part of the reason of the FAIL.
	for name, c := range map[string]struct{ scenario, want string }{
		"closed in both regions":                {"accgate-closed", ""},
		"the gate admits every realm user":      {"accgate-gate-open", "region region-a: oidc-gate/oidc-gate-guacamole admits the groups [] (none: every user of the realm), want exactly [sovereign-admins] or the access controller as upstream (upstream: http://guacamole-server.guacamole.svc.cluster.local:80)"},
		"the gate instance is absent":           {"accgate-gate-absent", ""},
		"the gate's upstream is the controller": {"accgate-gate-controller", ""},
		"the gate admits another group":         {"accgate-gate-other-group", "admits the groups [sovereign-viewers]"},
		"the gate admits a second group":        {"accgate-gate-admins-and-viewers", "admits the groups [sovereign-admins,sovereign-viewers]"},
		"the env var adds a group":              {"accgate-gate-env-group", "admits the groups [sovereign-admins,sovereign-admins,sovereign-viewers]"},
		"the gate has no proxy container":       {"accgate-gate-no-container", "oidc-gate/oidc-gate-guacamole has no container oauth2-proxy"},
		"the web application is still there":    {"accgate-store-webapp", "region region-a: namespace guacamole holds the Deployment guacamole-server: true, and the CloudNativePG Clusters []; want neither"},
		"the database is still there":           {"accgate-store-database", "region region-a: namespace guacamole holds the Deployment guacamole-server: false, and the CloudNativePG Clusters [guacamole-pg]; want neither"},
		"another Cluster is there":              {"accgate-store-other-cluster", "region region-b: namespace guacamole holds the Deployment guacamole-server: false, and the CloudNativePG Clusters [other-pg]; want neither"},
		"another Deployment is there":           {"accgate-store-other-deployment", ""},
		"the Clusters cannot be listed":         {"accgate-store-unlisted", "[region-b] kubectl get clusters.postgresql.cnpg.io"},
		"the Cluster kind is not served":        {"accgate-store-not-served", "doesn't have a resource type"},
		"the proxy execs anywhere":              {"accgate-proxy-open", "catalyst-system/k8s-ws-proxy execs into namespaces \"\", want \"catalyst-system\""},
		"the proxy allows a second namespace":   {"accgate-proxy-wide", "\"catalyst-system,kube-system\""},
		"the proxy has no shared-secret leg":    {"accgate-proxy-no-shared-secret", ""},
		"the proxy is absent":                   {"accgate-proxy-absent", "no DaemonSet k8s-ws-proxy in namespace catalyst-system: nothing was measured"},
		"the proxy has no proxy container":      {"accgate-proxy-no-container", "catalyst-system/k8s-ws-proxy has no container k8s-ws-proxy"},
		"a region cannot be reached":            {"accgate-b-unreachable", "[region-b] kubectl get deployments"},
	} {
		r := runCheck(t, c.scenario, accessGateCheck{}, &CheckContext{})
		got := r.run.Checks[0]
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", name, got)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
	}
	// One step per region, and the evidence holds what each part saw.
	r := runCheck(t, "accgate-closed", accessGateCheck{}, &CheckContext{})
	if len(r.run.Steps) != 2 {
		t.Fatalf("want one step per region, got %d", len(r.run.Steps))
	}
	a, b := r.run.Steps[0], r.run.Steps[1]
	if a.Journey != accessGateID || a.ID != "ACC-G01.1" || a.Status != Pass || a.Evidence["gate"] != "allowed-group sovereign-admins" ||
		a.Evidence["store"] != "no web application, no database" || a.Evidence["proxy"] != "ALLOWED_NAMESPACES=catalyst-system" {
		t.Errorf("the step of region-a does not hold the evidence: %+v", a)
	}
	if b.ID != "ACC-G01.2" || b.Status != Pass || b.Evidence["store"] != "no web application, no database" {
		t.Errorf("the step of region-b does not hold the evidence: %+v", b)
	}
	// The evidence of a FAIL names what is still there.
	r = runCheck(t, "accgate-store-database", accessGateCheck{}, &CheckContext{})
	if got := r.run.Steps[0].Evidence["store"]; got != "Deployment guacamole-server present=false, Clusters [guacamole-pg]" {
		t.Errorf("the evidence of region-a with the database left: %q", got)
	}
	// Without a region the check fails; it never passes on nothing.
	r.run.Steps, r.run.Checks = nil, nil
	r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(accessGateID, StateRequired)}}, []Check{accessGateCheck{}}, &CheckContext{}, true)
	if got := r.run.Checks[0]; got.Status != Fail || !strings.Contains(got.Reason, "the run has no region") {
		t.Errorf("without a region: want FAIL, got %+v", got)
	}
}
