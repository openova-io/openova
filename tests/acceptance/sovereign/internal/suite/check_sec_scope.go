package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check SEC-G07, External Secrets scope. The admission rule
// external-secret-remote-key-scope of bp-external-secrets-stores limits the
// OpenBao key that an ExternalSecret or a PushSecret names, and the OpenBao
// policy of the cluster-wide store refuses the hand-over archive. In every
// region the check records five steps:
//
//  1. a server-side dry run of an ExternalSecret in namespace default with
//     the remote key sso/sec-g07-probe is refused by the rule;
//  2. one in namespace gitea with the key catalyst/tofu-phase0-archive is
//     refused by the rule;
//  3. one in namespace gitea with the key sso/sec-g07-probe is admitted, so
//     the rule reads its legacy map and does not refuse everything;
//  4. every ExternalSecret and PushSecret outside the namespaces of an
//     Organization (label openova.io/organization) passes the rule again on a
//     server-side dry-run update; with none to replay, nothing was measured;
//  5. inside an OpenBao server pod, a token of the identity of the store
//     vault-region1 gets HTTP 403 on <kv>/data/catalyst/tofu-phase0-archive
//     and no 403 on <kv>/data/sso/sec-g07-probe.
//
// A dry run stores nothing. Step 5 asks the API server for a token of the
// store's service account that lives 10 minutes, logs in to OpenBao with it
// in the pod (`kubectl exec`, the script on standard input), reads the two
// paths with their output thrown away, and revokes its OpenBao token.
const secScopeID = "SEC-G07"

const (
	secScopePolicy    = "external-secret-remote-key-scope" // the ValidatingAdmissionPolicy and its binding
	secScopeStore     = "vault-region1"                    // the cluster-wide store of the region
	secScopeProbe     = "sec-g07-probe"                    // name of the dry-run objects, last segment of the probe keys
	secScopeArchive   = "catalyst/tofu-phase0-archive"     // the hand-over archive
	secScopeOrgLabel  = "openova.io/organization"          // namespaces the rule does not judge
	secScopeBaoNS     = "openbao"                          // namespace of bp-openbao (bootstrap-kit slot 08)
	secScopeBaoPods   = "app.kubernetes.io/name=openbao,component=server"
	secScopeBaoTarget = "openbao" // the server container
	secScopeManager   = "sec-g07" // field manager of the dry-run updates
)

// secScopeScript runs in an OpenBao server pod as `sh -s`, after four
// assignments that the check puts in front of it: jwt, mount, role and kv.
// It prints name=value words and never a token or a value.
const secScopeScript = `class() {
	case "$1" in
	*"Code: 403"*) echo denied ;;
	*"No value found"*) echo absent ;;
	"") echo read ;;
	*) echo error ;;
	esac
}
token=$(bao write -field=token "auth/$mount/login" role="$role" jwt="$jwt" 2>/dev/null) || token=""
if [ -z "$token" ]; then
	echo login=failed
	exit 0
fi
echo login=ok
export BAO_TOKEN="$token"
echo "archive=$(class "$(bao read "$kv/data/catalyst/tofu-phase0-archive" 2>&1 >/dev/null)")"
echo "sso=$(class "$(bao read "$kv/data/sso/sec-g07-probe" 2>&1 >/dev/null)")"
if bao token revoke -self >/dev/null 2>&1; then echo revoked=yes; else echo revoked=no; fi
`

type secScopeCheck struct{ kubectl kubectlFunc }

func init() { RegisterCheck(secScopeCheck{kubectl: kube.Region.Run}) }

func (secScopeCheck) ID() string { return secScopeID }

func (c secScopeCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	n := 0
	step := func(name string, fn func(ev Evidence) error) {
		n++
		checkStep(rec, secScopeID, n, name, fn)
	}
	for _, region := range cc.Regions {
		step("the rule refuses remote key sso/"+secScopeProbe+" in namespace default of region "+region.ID, func(ev Evidence) error {
			return c.refused(ctx, region, ev, "default", "sso/"+secScopeProbe)
		})
		step("the rule refuses remote key "+secScopeArchive+" in namespace gitea of region "+region.ID, func(ev Evidence) error {
			return c.refused(ctx, region, ev, "gitea", secScopeArchive)
		})
		step("the rule admits remote key sso/"+secScopeProbe+" in namespace gitea of region "+region.ID, func(ev Evidence) error {
			ev["namespace"], ev["key"] = "gitea", "sso/"+secScopeProbe
			_, err := c.dryRunCreate(ctx, region, "gitea", "sso/"+secScopeProbe)
			return err
		})
		step("every ExternalSecret and PushSecret outside the Organizations passes the rule on update in region "+region.ID, func(ev Evidence) error {
			return c.replay(ctx, region, ev)
		})
		step("a token of the store "+secScopeStore+" gets 403 on the hand-over archive in region "+region.ID, func(ev Evidence) error {
			return c.openbao(ctx, region, ev)
		})
	}
	return nil
}

// probeExternalSecret is an ExternalSecret named secScopeProbe in ns that reads key through the store.
func probeExternalSecret(ns, key string) string {
	return fmt.Sprintf(`{"apiVersion": "external-secrets.io/v1beta1", "kind": "ExternalSecret",
  "metadata": {"name": %q, "namespace": %q},
  "spec": {"secretStoreRef": {"kind": "ClusterSecretStore", "name": %q}, "target": {"name": %q},
    "data": [{"secretKey": "probe", "remoteRef": {"key": %q}}]}}`, secScopeProbe, ns, secScopeStore, secScopeProbe, key)
}

func (c secScopeCheck) dryRunCreate(ctx context.Context, region kube.Region, ns, key string) ([]byte, error) {
	return c.kubectl(region, ctx, strings.NewReader(probeExternalSecret(ns, key)), "create", "--dry-run=server", "-f", "-")
}

// refused passes when the dry run of a probe ExternalSecret is refused by the rule, and only then.
func (c secScopeCheck) refused(ctx context.Context, region kube.Region, ev Evidence, ns, key string) error {
	ev["namespace"], ev["key"] = ns, key
	_, err := c.dryRunCreate(ctx, region, ns, key)
	if err == nil {
		return fmt.Errorf("the API server admitted an ExternalSecret in namespace %s with remote key %s", ns, key)
	}
	if !strings.Contains(err.Error(), "ValidatingAdmissionPolicy '"+secScopePolicy+"'") {
		return fmt.Errorf("the dry run failed, but not by the rule %s: %v", secScopePolicy, err)
	}
	ev["refused_by"] = secScopePolicy
	return nil
}

// replay sends every ExternalSecret and PushSecret outside the Organization
// namespaces back to the API server as a server-side dry-run update.
func (c secScopeCheck) replay(ctx context.Context, region kube.Region, ev Evidence) error {
	out, err := c.kubectl(region, ctx, nil, "get", "namespaces", "-l", secScopeOrgLabel, "-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return err
	}
	orgs := map[string]bool{}
	for _, name := range strings.Fields(string(out)) {
		orgs[name] = true
	}
	out, err = c.kubectl(region, ctx, nil, "get", "externalsecrets.external-secrets.io,pushsecrets.external-secrets.io", "--all-namespaces", "-o", "json")
	if err != nil {
		return err
	}
	var list kube.List
	if err := json.Unmarshal(out, &list); err != nil {
		return fmt.Errorf("the list of ExternalSecrets and PushSecrets is not JSON: %v", err)
	}
	replayed, skipped, refused := 0, 0, []string{}
	for _, o := range list.Items {
		if orgs[o.Namespace()] || o.Get("metadata", "deletionTimestamp") != nil {
			skipped++
			continue
		}
		replayed++
		body, err := json.Marshal(map[string]any{"apiVersion": o.Str("apiVersion"), "kind": o.Str("kind"),
			"metadata": map[string]any{"name": o.Name(), "namespace": o.Namespace()}, "spec": o.Get("spec")})
		if err != nil {
			return err
		}
		if _, err := c.kubectl(region, ctx, strings.NewReader(string(body)), "apply", "--server-side", "--dry-run=server",
			"--force-conflicts", "--field-manager="+secScopeManager, "-f", "-"); err != nil {
			refused = append(refused, fmt.Sprintf("%s %s/%s: %v", o.Str("kind"), o.Namespace(), o.Name(), err))
		}
	}
	sort.Strings(refused)
	ev["replayed"], ev["skipped_organization"], ev["refused"] = replayed, skipped, refused
	switch {
	case replayed == 0:
		return fmt.Errorf("no ExternalSecret or PushSecret outside the Organizations in region %s: nothing was measured", region.ID)
	case len(refused) > 0:
		return fmt.Errorf("%d of %d objects would be refused on their next update: %s", len(refused), replayed, strings.Join(refused, "; "))
	}
	return nil
}

// shellQuote quotes s for sh with single quotes.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// openbao logs in to OpenBao as the store's identity and reads the two paths.
func (c secScopeCheck) openbao(ctx context.Context, region kube.Region, ev Evidence) error {
	out, err := c.kubectl(region, ctx, nil, "get", "clustersecretstore.external-secrets.io", secScopeStore, "-o", "json")
	if err != nil {
		return err
	}
	var store kube.Object
	if err := json.Unmarshal(out, &store); err != nil {
		return fmt.Errorf("the store %s is not JSON: %v", secScopeStore, err)
	}
	kv := store.Str("spec", "provider", "vault", "path")
	mount := store.Str("spec", "provider", "vault", "auth", "kubernetes", "mountPath")
	role := store.Str("spec", "provider", "vault", "auth", "kubernetes", "role")
	sa := store.Str("spec", "provider", "vault", "auth", "kubernetes", "serviceAccountRef", "name")
	saNS := store.Str("spec", "provider", "vault", "auth", "kubernetes", "serviceAccountRef", "namespace")
	ev["kv"], ev["mount"], ev["role"], ev["service_account"] = kv, mount, role, saNS+"/"+sa
	if kv == "" || mount == "" || role == "" || sa == "" || saNS == "" {
		return fmt.Errorf("the store %s names no kv path, mount, role or service account with its namespace", secScopeStore)
	}
	out, err = c.kubectl(region, ctx, nil, "-n", secScopeBaoNS, "get", "pods", "-l", secScopeBaoPods,
		"--field-selector=status.phase=Running", "-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return err
	}
	pods := strings.Fields(string(out))
	if len(pods) == 0 {
		return fmt.Errorf("no running OpenBao server pod in namespace %s", secScopeBaoNS)
	}
	ev["pod"] = pods[0]
	out, err = c.kubectl(region, ctx, nil, "-n", saNS, "create", "token", sa, "--duration=10m")
	if err != nil {
		return err
	}
	jwt := strings.TrimSpace(string(out))
	if strings.Count(jwt, ".") != 2 {
		return fmt.Errorf("the API server returned no token for %s/%s", saNS, sa)
	}
	script := "jwt=" + shellQuote(jwt) + "\nmount=" + shellQuote(mount) + "\nrole=" + shellQuote(role) + "\nkv=" + shellQuote(kv) + "\n" + secScopeScript
	out, err = c.kubectl(region, ctx, strings.NewReader(script), "-n", secScopeBaoNS, "exec", "-i", pods[0], "-c", secScopeBaoTarget, "--", "sh", "-s")
	if err != nil {
		return err
	}
	v := parseProbe(out)
	ev["login"], ev["archive"], ev["sso"], ev["revoked"] = v.one("login"), v.one("archive"), v.one("sso"), v.one("revoked")
	switch {
	case v.one("login") != "ok":
		return fmt.Errorf("the token of %s/%s could not log in to OpenBao with role %s on mount %s", saNS, sa, role, mount)
	case v.one("archive") != "denied":
		return fmt.Errorf("reading %s/data/%s gave %q, not a 403", kv, secScopeArchive, v.one("archive"))
	case v.one("sso") != "absent" && v.one("sso") != "read":
		return fmt.Errorf("reading %s/data/sso/%s gave %q: the store cannot reach the prefix sso/", kv, secScopeProbe, v.one("sso"))
	}
	return nil
}
