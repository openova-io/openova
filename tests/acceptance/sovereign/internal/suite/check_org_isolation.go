package suite

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/wordpress"
)

// Check NET-G07, Organization isolation: journey ISO. It proves on a running
// Sovereign the Organization fence that the organization controller writes
// into the host namespace of every Organization (org-fence and org-fence-api,
// package NET-170), with two test Organizations A and B in both regions.
//
// The Organizations. The check creates A (iso-a-<tag>) and B (iso-b-<tag>)
// through the console API, POST /api/v1/organizations with kind internal (no
// voucher step), A on plan isoPlanA and B on plan isoPlanB (see the constants
// below), and the free subdomain of the first ready parent domain, and waits
// until GET /api/v1/organizations/{id} says done for both
// (failed ends the wait). Before it ends, also when a step failed, it deletes
// every Organization it created with DELETE /api/v1/organizations/{id} and
// waits until their host namespaces are gone in every region, and the
// namespace of its positive control in the regions that created one; it fails
// naming each namespace that is left and its region.
//
// The kubeconfig of an Organization. In every region the vCluster of an
// Organization writes its kubeconfig into the Secrets vc-vcluster and
// tenant-<slug>-kubeconfig (key config) of the host namespace, and
// bp-reflector copies the second into flux-system. Its server is
// the Service vcluster.<slug>:443, which only the cluster resolves. The check
// reads the Secret with the kubeconfig of the region, starts `kubectl
// port-forward` to that Service, and calls kubectl with the kubeconfig of the
// Organization, --server set to the local address and --tls-server-name set
// to the host of the server of the kubeconfig, so the certificate of the
// vCluster is checked as for every other client. No Service of type NodePort
// is created or used.
//
// The dial Pods. In every region the check creates the namespace iso-<tag> in
// the vCluster of A and of B, each with the kubeconfig of its Organization,
// and one dial Pod in it with the Dialer of check NET-G06 (image secProbeImage,
// busybox `nc -z -vv -w 3`: exit code 0 when it connects, exit code 1 with
// "Connection refused" or "timed out" in its output when not). The syncer
// writes each Pod into the host namespace of its Organization with the label
// vcluster.loft.sh/managed-by, which the fence selects; the check reads that
// host Pod. A Pod created on the host would carry no such label and prove
// nothing. A row that expects deny passes on a timeout only: the fence drops
// the packets, and a refused connection reached its target. A row that
// expects allow passes when nc connects. Every dial first dials the cluster
// DNS Service of the vCluster on TCP port 53; when that does not connect, the
// Pod has no working network and the row fails, so a Pod without a network
// never passes.
//
// The assertions, in every region r with the peer region p:
//
//	ISO-1 the three kubeconfig Secrets of A exist in r and hold one kubeconfig,
//	      which reaches the vCluster of A; that kubeconfig lists its own
//	      namespace and dial Pod, no namespace of the host (by its UID: no
//	      namespace that the kubeconfig of r lists, which holds catalyst-system,
//	      openbao, gitea and the host namespaces of A and B; and by name: none
//	      of these but A's own) and no dial Pod of B.
//	ISO-2 the Pod of A times out on the DNS Service of B in r, on a DNS Pod of
//	      B in r and on a DNS Pod of B in p (the selector of the fence across
//	      the mesh). Before that the Pod of B in r connects to the DNS Service
//	      and the DNS Pod of B in r, so these targets answer.
//	ISO-3 it times out on the ClusterIP of openbao/openbao:8200,
//	      gitea/gitea-http:3000 and catalyst-system/catalyst-api:8080, each a
//	      Service that is shown to listen in r (or the row fails): it has a
//	      ready endpoint in r, or it has none there and is a Cilium global and
//	      shared Service (its annotations service.cilium.io/global and
//	      service.cilium.io/shared in r), whose backends answer from another
//	      region over the mesh, and then a positive control connects to its
//	      ClusterIP from a dial Pod on the host network of r. That Pod dials as
//	      the node, which every platform target admits as it admits the
//	      kubelet; the Pod of an Organization cannot be the control, the fence
//	      denies it. A secondary region runs no catalyst-api: there the Service
//	      is the global stub of bp-catalyst-edge-routes (slot 13e) and its
//	      backends are the Pods of the primary region. A headless Service
//	      (gitea/gitea-http has clusterIP None) has no ClusterIP: it is dialed
//	      at every ready endpoint address on its port, one row each, and has
//	      at least one such endpoint (or the row fails).
//	ISO-4 it times out on the ClusterIP of the host API server
//	      (default/kubernetes:443, with a ready endpoint), on port 10250 of the
//	      InternalIP of every Ready node of r and on 169.254.169.254:80.
//	ISO-5 it times out on the internet address 1.1.1.1:443, which no
//	      Application of A declares. Measured before ISO-6.
//	ISO-6 A creates the NetworkPolicy iso-allow-all (every Pod, all ingress,
//	      all egress) in iso-<tag> of its vCluster. The Pod of A then connects
//	      to the internet address within three minutes: the policy was synced
//	      and took effect, and the timeout of ISO-5 was a deny. ISO-2 to ISO-4
//	      are dialed again and must still time out. The fence denies no
//	      internet egress, so ISO-5 is not dialed again.
//	ISO-9 an Application of bp-wordpress-tenant (topology singleton, the first
//	      region, the first Environment of A) becomes Ready and answers
//	      https://<its endpoint>/ through the gateway with HTTP 200 and a
//	      WordPress page. TLS is not verified there: the path through the
//	      gateway is asserted, not the certificate. The Application is deleted
//	      before the Organizations.
//
// ISO-7 is check NET-G11 and ISO-8 is reported by the compliance area; neither
// is part of this check. A run with fewer than two regions or without a
// console session fails.
const (
	orgIsolationID = "NET-G07"
	// isoBlueprint is the Blueprint of ISO-9: the Blueprint of the journeys,
	// whose HTTPRoute inside the vCluster the syncer reflects to the host,
	// where the gateway routes it.
	isoBlueprint = "bp-wordpress-tenant"
	// isoPlanA is the plan of A, the Organization that installs ISO-9's
	// Application: the smallest plan whose customer part holds it. Installed
	// with the chart's defaults it needs 250m of requests.cpu while its
	// post-install hooks run (WordPress 100m + its database 100m + a hook
	// 50m, platform/wordpress-tenant/chart/values.yaml and templates), and
	// plan s sells a guaranteed share of 167m (planQuotaTable in
	// core/controllers/organization/internal/gitops/manifests.go), so no plan
	// s Organization can hold it, whatever else runs. The organization
	// controller's test TestPlanQuota_AcceptanceApplicationFitsItsPlan reads
	// this constant and fails when the plan does not hold the Application or
	// a smaller plan would. isoPlanB is the plan of B, which installs nothing:
	// the smallest plan.
	isoPlanA           = "m"
	isoPlanB           = "s"
	isoInternet        = "1.1.1.1" // ISO-5: a public anycast address that answers on TCP port 443
	isoInternetPort    = 443
	isoAllowAll        = "iso-allow-all" // ISO-6: the NetworkPolicy that A creates in its vCluster
	isoVClusterName    = "vcluster"      // the vCluster of an Organization, and its Service in the host namespace (port 443)
	isoVClusterSecret  = "vc-vcluster"   // the kubeconfig Secret that the vCluster writes into the host namespace
	isoManagedBy       = "vcluster.loft.sh/managed-by"
	isoKubeletPort     = 10250
	isoForwardWait     = 30 * time.Second // the longest wait for kubectl port-forward to print its local address
	isoForwardRetries  = 3                // how often a call that the port forward refused is sent again
	isoVClusterAPIPort = 443
	// isoGlobal and isoShared are the Cilium annotations of a ClusterMesh
	// global Service: Cilium merges the Services of that name and namespace
	// of every region into one, whose backends are those of every region that
	// shares them (shared is true when it is not set).
	isoGlobal = "service.cilium.io/global"
	isoShared = "service.cilium.io/shared"
)

// isoPlatform are the Services of ISO-3: OpenBao, the Git service and catalyst-system.
var isoPlatform = []DialTarget{
	{Namespace: "openbao", Service: "openbao", Port: 8200},
	{Namespace: "gitea", Service: "gitea-http", Port: 3000},
	{Namespace: "catalyst-system", Service: "catalyst-api", Port: 8080},
}

// isoHostNamespaces are namespaces of the host that the kubeconfig of A must
// not list, besides the host namespaces of A and B. ISO-1 judges every host
// namespace by its UID; these must be among those the host lists, or the
// comparison proves nothing.
var isoHostNamespaces = []string{"catalyst-system", "openbao", "gitea"}

// isoNamespaceUIDs is the output of ISO-1's namespace list: name=uid of each.
const isoNamespaceUIDs = `jsonpath={range .items[*]}{.metadata.name}={.metadata.uid}{" "}{end}`

// isoServerRE finds the host of the https server of a kubeconfig.
var isoServerRE = regexp.MustCompile(`(?m)^\s*server:\s*"?https://([^:/\s"]+)`)

// forwardRE is the line of kubectl port-forward that names its local address.
var forwardRE = regexp.MustCompile(`^Forwarding from (127\.0\.0\.1:[0-9]+) -> `)

// isoWaits are the longest waits of the check and the pause between two reads.
type isoWaits struct{ org, app, gone, policy, every time.Duration }

// forwardFunc forwards a local port to port of the Service in namespace,
// through the kubeconfig of region. It returns the local address and a
// function that ends the forward.
type forwardFunc func(ctx context.Context, region kube.Region, namespace, service string, port int) (string, func(), error)

type orgIsolationCheck struct {
	kubectl kubectlFunc
	forward forwardFunc
	fetch   func(ctx context.Context, host string) (*wordpress.Page, error)
	waits   isoWaits
}

func init() {
	RegisterCheck(orgIsolationCheck{kubectl: kube.Region.Run, forward: portForward, fetch: fetchHome,
		waits: isoWaits{org: 30 * time.Minute, app: 25 * time.Minute, gone: 15 * time.Minute, policy: 3 * time.Minute, every: 10 * time.Second}})
}

func (orgIsolationCheck) ID() string { return orgIsolationID }

// isoOrg is one test Organization.
type isoOrg struct {
	label, slug, plan, id, namespace string
	created                          bool
	vc                               map[string]*isoVC // by region ID
}

// isoVC is the vCluster of an Organization in one region, reached through a port forward.
type isoVC struct {
	region      kube.Region // ID <slug>@<region>, the kubeconfig file of the Organization
	server, sni string
	stop        func()
	dialer      *Dialer
}

// isoRow is one dial: the assertion, what the target is and where it is.
type isoRow struct {
	iso, what string
	to        DialTarget
}

// isoTargets are the targets of one region: the DNS Service and a DNS Pod of
// B, and the rows of ISO-3 and ISO-4.
type isoTargets struct {
	bService, bPod DialTarget
	rest           []isoRow
}

type isoJourney struct {
	c       orgIsolationCheck
	cc      *CheckContext
	rec     StepRecorder
	n       int
	tag, ns string
	dir     string
	a, b    *isoOrg
	targets map[string]*isoTargets
	// controls are the dialers of the positive control of a global Service,
	// by region ID: one Pod on the host network in the namespace iso-ctl-<tag>
	// of the host, created only in a region that needs it.
	controls map[string]*Dialer
	app      struct {
		name, namespace, uid, fqdn string
		created                    bool
	}
}

func (c orgIsolationCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) < 2 {
		return fmt.Errorf("journey ISO runs in two regions, and the run has %d", len(cc.Regions))
	}
	if cc.API == nil {
		return errors.New("journey ISO creates its Organizations through the console API, and the run has no console session")
	}
	dir, err := os.MkdirTemp("", "net-g07-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	run := strings.ToLower(NewRunID(time.Now())) // 20261119t101500z-abcd
	tag := run[9:15] + run[len(run)-4:]
	j := &isoJourney{c: c, cc: cc, rec: rec, dir: dir, tag: tag, ns: "iso-" + tag, targets: map[string]*isoTargets{}, controls: map[string]*Dialer{},
		a: &isoOrg{label: "A", slug: "iso-a-" + tag, plan: isoPlanA, vc: map[string]*isoVC{}},
		b: &isoOrg{label: "B", slug: "iso-b-" + tag, plan: isoPlanB, vc: map[string]*isoVC{}}}
	j.run(ctx)
	return nil
}

func (j *isoJourney) orgs() []*isoOrg { return []*isoOrg{j.a, j.b} }

func (j *isoJourney) step(name string, fn func(ev Evidence) error) bool {
	j.n++
	return checkStep(j.rec, orgIsolationID, j.n, name, fn)
}

func (j *isoJourney) run(ctx context.Context) {
	if j.createOrgs(ctx) {
		j.install(ctx)
		if j.connect(ctx) && j.startPods(ctx) {
			j.listOwn(ctx)
			if j.readTargets(ctx) {
				j.dials(ctx)
				j.allowAll(ctx)
			}
		}
		j.answers(ctx)
	}
	j.cleanup(ctx)
}

// createOrgs creates A and B and waits until both are done.
func (j *isoJourney) createOrgs(ctx context.Context) bool {
	ok := true
	for _, o := range j.orgs() {
		ok = j.step("ISO: Organization "+o.label+" ("+o.slug+") is created through the console API (POST /api/v1/organizations)", func(ev Evidence) error {
			body := map[string]any{"subdomain": o.slug, "admin_email": o.slug + "@acceptance.invalid", "kind": "internal", "plan_slug": o.plan}
			ev["organization"], ev["request"] = o.label, body
			res, err := j.cc.API.Do(ctx, "POST", "/api/v1/organizations", body)
			if err != nil {
				return err
			}
			ev["http"] = res.Status
			o.created = res.Status >= 200 && res.Status < 300
			var v struct {
				ID        string `json:"org_tenant_id"`
				Namespace string `json:"tenant_namespace"`
				State     string `json:"state"`
			}
			if o.created {
				if err := res.JSON(&v); err != nil {
					return err
				}
			}
			o.id, o.namespace = v.ID, v.Namespace
			ev["id"], ev["namespace"], ev["state"] = v.ID, o.namespace, v.State
			switch {
			case res.Status != 202:
				return fmt.Errorf("HTTP %d (want 202): %s", res.Status, trunc(string(res.Body), 300))
			case v.ID == "":
				return errors.New("the answer names no org_tenant_id")
			case v.Namespace == "":
				return errors.New("the answer names no tenant_namespace")
			}
			return nil
		}) && ok
	}
	if !ok {
		return false
	}
	return j.step("ISO: Organizations A and B reach the state done (GET /api/v1/organizations/{id})", func(ev Evidence) error {
		states := map[string]string{}
		ev["states"] = states
		_, err := poll(ctx, j.c.waits.org, j.c.waits.every, func() (bool, string, error) {
			done := true
			for _, o := range j.orgs() {
				var v struct {
					State     string `json:"state"`
					LastError string `json:"last_error"`
				}
				if _, err := j.cc.API.Get(ctx, "/api/v1/organizations/"+url.PathEscape(o.id), &v); err != nil {
					return false, err.Error(), nil
				}
				states[o.label] = v.State
				if v.State == "failed" {
					return false, "", fmt.Errorf("the Organization %s is failed: %s", o.slug, v.LastError)
				}
				done = done && v.State == "done"
			}
			return done, fmt.Sprintf("A=%s B=%s", states["A"], states["B"]), nil
		})
		return err
	})
}

// connect reads the kubeconfig of each Organization in each region and reaches its vCluster.
func (j *isoJourney) connect(ctx context.Context) bool {
	ok := true
	for _, region := range j.cc.Regions {
		for _, o := range j.orgs() {
			prefix := "ISO: "
			if o == j.a {
				prefix = "ISO-1: "
			}
			name := prefix + "the kubeconfig Secret of Organization " + o.label + " and its two mirrors exist in region " + region.ID +
				" with one kubeconfig, and it reaches the vCluster of " + o.label
			ok = j.step(name, func(ev Evidence) error { return j.reach(ctx, o, region, ev) }) && ok
		}
	}
	return ok
}

func (j *isoJourney) reach(ctx context.Context, o *isoOrg, region kube.Region, ev Evidence) error {
	mirror := "tenant-" + o.slug + "-kubeconfig"
	secrets := []string{o.namespace + "/" + isoVClusterSecret, o.namespace + "/" + mirror, "flux-system/" + mirror}
	ev["organization"], ev["region"], ev["secrets"] = o.label, region.ID, secrets
	config := ""
	_, err := poll(ctx, j.c.waits.org, j.c.waits.every, func() (bool, string, error) {
		var missing []string
		seen := map[string]bool{}
		for i, s := range secrets {
			ns, name, _ := strings.Cut(s, "/")
			out, err := j.c.kubectl(region, ctx, nil, "-n", ns, "get", "secret", name, "--ignore-not-found", "-o", "jsonpath={.data.config}")
			if err != nil {
				return false, err.Error(), nil
			}
			v := strings.TrimSpace(string(out))
			if v == "" {
				missing = append(missing, s)
				continue
			}
			if i == 0 {
				config = v
			}
			seen[v] = true
		}
		switch {
		case len(missing) > 0:
			return false, "no kubeconfig in " + strings.Join(missing, ", "), nil
		case len(seen) > 1:
			return false, "the three Secrets hold different kubeconfigs", nil
		}
		return true, "", nil
	})
	if err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(config)
	if err != nil {
		return fmt.Errorf("the key config of %s is not base64: %v", secrets[0], err)
	}
	m := isoServerRE.FindSubmatch(raw)
	if m == nil {
		return fmt.Errorf("the kubeconfig in %s names no https server", secrets[0])
	}
	path := filepath.Join(j.dir, o.slug+"."+region.ID+".kubeconfig")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	addr, stop, err := j.c.forward(ctx, region, o.namespace, isoVClusterName, isoVClusterAPIPort)
	if err != nil {
		return err
	}
	v := &isoVC{region: kube.Region{ID: o.slug + "@" + region.ID, Kubeconfig: path, Kubectl: region.Kubectl},
		server: "https://" + addr, sni: string(m[1]), stop: stop}
	o.vc[region.ID] = v
	ev["tls_server_name"] = v.sni
	out, err := j.vcKubectl(v)(v.region, ctx, nil, "get", "namespace", "default", "-o", "name")
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(string(out)); got != "namespace/default" {
		return fmt.Errorf("the vCluster of %s in region %s answered %q for its namespace default", o.label, region.ID, got)
	}
	return nil
}

// vcKubectl is kubectl of the vCluster v: the kubeconfig of its Organization,
// the server of the port forward, and the certificate checked for the host of
// the kubeconfig. A call that the port forward refused never reached the
// vCluster: once the forward listens again (portForward starts a forward
// that ended again), the call is sent again with the same input, at most
// isoForwardRetries times.
func (j *isoJourney) vcKubectl(v *isoVC) kubectlFunc {
	return func(r kube.Region, ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
		var in []byte
		if stdin != nil {
			b, err := io.ReadAll(stdin)
			if err != nil {
				return nil, err
			}
			in = b
		}
		full := append([]string{"--server", v.server, "--tls-server-name", v.sni}, args...)
		local := strings.TrimPrefix(v.server, "https://")
		for attempt := 0; ; attempt++ {
			var body io.Reader
			if stdin != nil {
				body = bytes.NewReader(in)
			}
			out, err := j.c.kubectl(r, ctx, body, full...)
			if err == nil || attempt == isoForwardRetries || !forwardRefused(err, local) || waitListening(ctx, local) != nil {
				return out, err
			}
		}
	}
}

// forwardRefused says whether err is kubectl's own answer to a refused
// connection to the local address of a port forward. Only kubectl's
// client-side messages for that address count: every error of a vCluster
// call carries the address in its arguments (--server, kube.Region.once), and
// a refusal on the API path while the forward is up ("error dialing backend:
// dial tcp 10.42.x:10250: connect: connection refused", a webhook) reached the
// vCluster and is returned at once.
func forwardRefused(err error, local string) bool {
	msg := err.Error()
	return strings.Contains(msg, "The connection to the server "+local+" was refused") ||
		strings.Contains(msg, "dial tcp "+local+": connect: connection refused")
}

// waitListening waits until addr accepts a TCP connection, for at most
// isoForwardWait.
func waitListening(ctx context.Context, addr string) error {
	deadline := time.Now().Add(isoForwardWait)
	for {
		c, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", addr)
		if err == nil {
			return c.Close()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the port forward on %s did not listen again in %s: %v", addr, isoForwardWait, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// startPods starts one dial Pod of A and one of B in every region.
func (j *isoJourney) startPods(ctx context.Context) bool {
	ok := true
	for _, region := range j.cc.Regions {
		ok = j.step("ISO: a dial Pod of A and one of B are Ready in the namespace "+j.ns+" of their vCluster in region "+region.ID+
			", created through the kubeconfig of each and synced into its host namespace with the label "+isoManagedBy, func(ev Evidence) error {
			ev["region"], ev["namespace"] = region.ID, j.ns
			for _, o := range j.orgs() {
				v := o.vc[region.ID]
				k := j.vcKubectl(v)
				if _, err := k(v.region, ctx, nil, "create", "namespace", j.ns); err != nil {
					return err
				}
				v.dialer = &Dialer{Kubectl: k, Region: v.region, Run: j.tag + strings.ToLower(o.label)}
				err := v.dialer.Start(ctx, []DialSource{{Namespace: j.ns}})
				ev[o.label] = v.dialer.Pods()
				if err != nil {
					return err
				}
				// The syncer names the host Pod <name>-x-<namespace>-x-<vCluster>.
				want := v.dialer.pods[DialSource{Namespace: j.ns}.key()].name + "-x-"
				out, err := j.c.kubectl(region, ctx, nil, "-n", o.namespace, "get", "pods", "-l", isoManagedBy, "-o", "jsonpath={.items[*].metadata.name}")
				if err != nil {
					return err
				}
				found := ""
				for _, name := range strings.Fields(string(out)) {
					if strings.HasPrefix(name, want) {
						found = name
					}
				}
				if found == "" {
					return fmt.Errorf("the host namespace %s of region %s holds no Pod %s... with the label %s: the fence would not select the dial Pod of %s",
						o.namespace, region.ID, want, isoManagedBy, o.label)
				}
				ev[o.label+"_host_pod"] = o.namespace + "/" + found
			}
			return nil
		}) && ok
	}
	return ok
}

// listOwn is the second half of ISO-1: what the kubeconfig of A lists.
//
// A namespace is judged by its identity (metadata.uid), not by its name: a
// name collides by design. The organization controller creates inside the
// vCluster of every Organization a namespace with exactly the name of the
// Organization's host namespace, the target namespace of its Applications
// (appsNamespaceTemplate, core/controllers/organization/internal/gitops/
// manifests.go), and the vCluster holds a namespace keycloak for the host
// Service keycloak/keycloak it replicates. So the kubeconfig of A lists
// iso-a-<tag> as the vCluster's own object, and its name proves nothing about
// the host. A host namespace seen through the kubeconfig of A carries the UID
// that the kubeconfig of the region reads for it, so every namespace A lists
// must carry a UID and none may carry a host's. The collision covers only
// those two names: the host namespace of B, catalyst-system, openbao and gitea
// still fail by name whatever their UID (B's Kustomization applied through the
// kubeconfig of A, or a mapping from the host, would create them). Any other
// namespace named like a host namespace with another UID is the vCluster's own
// and is recorded, not failed.
//
// The Pods of B are judged by name, and that name cannot collide: the dial Pod
// of B is <ns>/pol-dial-<tag>b-1, a name that carries the run of B, nothing in
// the vCluster of A creates it, and the syncer writes no Pod from the host into
// a vCluster (on the host the Pod is <name>-x-<ns>-x-vcluster anyway).
func (j *isoJourney) listOwn(ctx context.Context) {
	for _, region := range j.cc.Regions {
		j.step("ISO-1: the kubeconfig of A in region "+region.ID+" lists its namespace "+j.ns+" and its dial Pod, and no namespace of the host (by UID) and no dial Pod of B", func(ev Evidence) error {
			v := j.a.vc[region.ID]
			k := j.vcKubectl(v)
			namespaces, uids, err := isoNamespaces(ctx, k, v.region)
			if err != nil {
				return err
			}
			out, err := k(v.region, ctx, nil, "get", "pods", "-A", "-o", `jsonpath={range .items[*]}{.metadata.namespace}/{.metadata.name}{" "}{end}`)
			if err != nil {
				return err
			}
			pods := strings.Fields(string(out))
			hostNames, hostUIDs, err := isoNamespaces(ctx, j.c.kubectl, region)
			if err != nil {
				return err
			}
			ev["region"], ev["namespaces"], ev["pods"], ev["host_namespaces"] = region.ID, namespaces, pods, len(hostNames)
			own := v.dialer.Pods()
			switch {
			case !contains(namespaces, j.ns):
				return fmt.Errorf("the kubeconfig of A lists no namespace %s, so the list proves nothing", j.ns)
			case len(own) != 1 || !contains(pods, own[0]):
				return fmt.Errorf("the kubeconfig of A lists no Pod %v of its own, so the list proves nothing", own)
			}
			for _, ns := range append([]string{j.a.namespace, j.b.namespace}, isoHostNamespaces...) {
				if hostUIDs[ns] == "" {
					return fmt.Errorf("the kubeconfig of region %s lists no namespace %s with a UID, so the identity of the namespaces of A proves nothing", region.ID, ns)
				}
			}
			host := map[string]string{} // UID -> name of every host namespace
			for _, ns := range hostNames {
				if hostUIDs[ns] != "" {
					host[hostUIDs[ns]] = ns
				}
			}
			named := append([]string{j.b.namespace}, isoHostNamespaces...) // never the vCluster's own name
			twins := []string{}
			for _, ns := range namespaces {
				if uids[ns] == "" {
					return fmt.Errorf("the kubeconfig of A lists the namespace %s without a UID, so the identity proves nothing", ns)
				}
				if name, ok := host[uids[ns]]; ok {
					return fmt.Errorf("the kubeconfig of A lists the namespace %s of the host (it lists %s with the host's UID %s)", name, ns, uids[ns])
				}
				if contains(named, ns) {
					return fmt.Errorf("the kubeconfig of A lists the namespace %s of the host by name (with the UID %s)", ns, uids[ns])
				}
				if hostUIDs[ns] != "" {
					twins = append(twins, ns+"="+uids[ns]+" (host "+hostUIDs[ns]+")")
				}
			}
			ev["own_namespaces_named_like_host_namespaces"] = twins
			for _, p := range j.b.vc[region.ID].dialer.Pods() {
				if contains(pods, p) {
					return fmt.Errorf("the kubeconfig of A lists the Pod %s of B", p)
				}
			}
			return nil
		})
	}
}

// isoNamespaces lists the namespaces that k reaches in r, by name, and the UID of each.
func isoNamespaces(ctx context.Context, k kubectlFunc, r kube.Region) ([]string, map[string]string, error) {
	out, err := k(r, ctx, nil, "get", "namespaces", "-o", isoNamespaceUIDs)
	if err != nil {
		return nil, nil, err
	}
	names, uids := []string{}, map[string]string{}
	for _, f := range strings.Fields(string(out)) {
		name, uid, _ := strings.Cut(f, "=")
		names, uids[name] = append(names, name), uid
	}
	return names, uids, nil
}

// readTargets reads the targets of every region.
func (j *isoJourney) readTargets(ctx context.Context) bool {
	ok := true
	for _, region := range j.cc.Regions {
		ok = j.step("ISO: the targets of region "+region.ID+" are read: the DNS Service and a DNS Pod of B, the Services of ISO-3 and the API server, each with a ready endpoint "+
			"or, a Cilium global Service, a positive control that reaches it, and the Ready nodes", func(ev Evidence) error {
			ev["region"] = region.ID
			listeners := map[string]string{}
			ev["listeners"] = listeners
			v := j.b.vc[region.ID]
			k := j.vcKubectl(v)
			out, err := k(v.region, ctx, nil, "-n", "kube-system", "get", "service", "kube-dns", "-o", "jsonpath={.spec.clusterIP}")
			if err != nil {
				return err
			}
			ip := strings.TrimSpace(string(out))
			if net.ParseIP(ip) == nil {
				return fmt.Errorf("the DNS Service of B in region %s has no ClusterIP (%q)", region.ID, ip)
			}
			t := &isoTargets{bService: DialTarget{Address: ip, Port: dialControlPort}}
			if t.bPod, err = isoDNSPod(ctx, k, v.region); err != nil {
				return err
			}
			host := &Dialer{Kubectl: j.c.kubectl, Region: region}
			for _, s := range isoPlatform {
				l, err := j.listener(ctx, host, s, listeners)
				if err != nil {
					return err
				}
				what := "the Service " + s.Namespace + "/" + s.Service
				switch {
				case l.headless:
					what = "a ready endpoint of the headless Service " + s.Namespace + "/" + s.Service
				case l.global:
					what = "the Cilium global Service " + s.Namespace + "/" + s.Service + " (its backends answer from another region over the mesh)"
				}
				for _, a := range l.addrs {
					t.rest = append(t.rest, isoRow{"ISO-3", what, DialTarget{Address: a, Port: s.Port}})
				}
			}
			api := DialTarget{Namespace: "default", Service: "kubernetes", Port: 443}
			l, err := j.listener(ctx, host, api, listeners)
			if err != nil {
				return err
			}
			for _, a := range l.addrs {
				t.rest = append(t.rest, isoRow{"ISO-4", "the host API server (default/kubernetes)", DialTarget{Address: a, Port: api.Port}})
			}
			nodes, err := isoNodes(ctx, j.c.kubectl, region)
			if err != nil {
				return err
			}
			t.rest = append(t.rest, nodes...)
			t.rest = append(t.rest, isoRow{"ISO-4", "the instance metadata address", DialTarget{Address: metadataAddress, Port: metadataPort}})
			j.targets[region.ID] = t
			rows := []string{"B DNS Service " + t.bService.String(), "B DNS Pod " + t.bPod.String()}
			for _, r := range t.rest {
				rows = append(rows, r.what+" "+r.to.String())
			}
			ev["targets"] = rows
			return nil
		}) && ok
	}
	return ok
}

// isoListener is how a Service of ISO-3 or ISO-4 is dialed in one region: at
// addrs on its port. headless: it has no ClusterIP and addrs are its ready
// endpoints. global: it has no ready endpoint in the region and is a Cilium
// global and shared Service, dialed at its ClusterIP once a positive control
// reached it there.
type isoListener struct {
	addrs            []string
	headless, global bool
}

// isoAddresses are the addresses at which the Service t of the region of host
// is dialed on t.Port. A Service with a ClusterIP is dialed at its ClusterIP
// and must be shown to listen: by a ready endpoint in the region, or, when it
// has none there, by being a Cilium global and shared Service (read from the
// Service itself), whose backends are in another region; then global is set
// and the caller proves the listener with a positive control. Anything else
// has no listener, and a timeout would prove nothing. A headless Service
// (clusterIP None, as gitea/gitea-http of the upstream gitea chart) has no
// address of its own: its clients reach its ready endpoints directly, so those
// are its listeners, and every ready endpoint address whose EndpointSlice
// serves TCP port t.Port is dialed; at least one must exist.
func isoAddresses(ctx context.Context, host *Dialer, t DialTarget) (isoListener, error) {
	out, err := host.Kubectl(host.Region, ctx, nil, "-n", t.Namespace, "get", "service", t.Service, "-o", "jsonpath={.spec.clusterIP}")
	if err != nil {
		return isoListener{}, err
	}
	ip := strings.TrimSpace(string(out))
	if ip == "None" {
		addrs, err := isoReadyAddresses(ctx, host, t)
		if err == nil && len(addrs) == 0 {
			err = fmt.Errorf("the headless Service %s/%s of region %s has no ready endpoint on TCP port %d: nothing listens, so a timeout would prove nothing",
				t.Namespace, t.Service, host.Region.ID, t.Port)
		}
		return isoListener{addrs: addrs, headless: true}, err
	}
	if net.ParseIP(ip) == nil {
		return isoListener{}, fmt.Errorf("the Service %s/%s of region %s has no ClusterIP (%q)", t.Namespace, t.Service, host.Region.ID, ip)
	}
	n, err := host.ReadyEndpoints(ctx, t)
	if err != nil {
		return isoListener{}, err
	}
	if n > 0 {
		return isoListener{addrs: []string{ip}}, nil
	}
	out, err = host.Kubectl(host.Region, ctx, nil, "-n", t.Namespace, "get", "service", t.Service, "-o", "json")
	if err != nil {
		return isoListener{}, err
	}
	var svc kube.Object
	if err := json.Unmarshal(out, &svc); err != nil {
		return isoListener{}, fmt.Errorf("the Service %s/%s of region %s is not JSON: %v", t.Namespace, t.Service, host.Region.ID, err)
	}
	global, shared := svc.Str("metadata", "annotations", isoGlobal), svc.Str("metadata", "annotations", isoShared)
	if global != "true" || (shared != "true" && shared != "") {
		return isoListener{}, fmt.Errorf("the Service %s/%s of region %s has no ready endpoint and is no Cilium global and shared Service (%s %q, %s %q): nothing listens, so a timeout would prove nothing",
			t.Namespace, t.Service, host.Region.ID, isoGlobal, global, isoShared, shared)
	}
	return isoListener{addrs: []string{ip}, global: true}, nil
}

// listener reads how the Service t of the region of host is dialed, and
// proves the listener of a global Service with a positive control from that
// region. It records in listeners what shows that t listens.
func (j *isoJourney) listener(ctx context.Context, host *Dialer, t DialTarget, listeners map[string]string) (isoListener, error) {
	key := t.Namespace + "/" + t.Service
	l, err := isoAddresses(ctx, host, t)
	switch {
	case err != nil:
		return l, err
	case l.headless:
		listeners[key] = fmt.Sprintf("%d ready endpoints in the region", len(l.addrs))
		return l, nil
	case !l.global:
		listeners[key] = "a ready endpoint in the region"
		return l, nil
	}
	to := DialTarget{Address: l.addrs[0], Port: t.Port}
	res, err := j.control(ctx, host.Region, to)
	if err != nil {
		return l, fmt.Errorf("the Service %s of region %s has no ready endpoint, and the positive control from a Pod on the host network of the region did not run: %v", key, host.Region.ID, err)
	}
	listeners[key] = "no ready endpoint in the region; a global Service: the positive control from a Pod on the host network of the region dialed " +
		to.String() + ": control " + res.Control + ", target " + res.Outcome + " (" + res.Output + ")"
	switch {
	case res.Control != DialConnected:
		return l, fmt.Errorf("the Service %s of region %s has no ready endpoint, and the positive control from a Pod on the host network of the region has no working network (%s:%d %s: %s): nothing is shown to listen, so a timeout would prove nothing",
			key, host.Region.ID, dialControlHost, dialControlPort, res.Control, res.ControlOutput)
	case res.Outcome != DialConnected:
		return l, fmt.Errorf("the Service %s of region %s has no ready endpoint, and the dial of its ClusterIP %s from a Pod on the host network of region %s %s (%s): nothing is shown to listen, so a timeout would prove nothing",
			key, host.Region.ID, to, host.Region.ID, res.Outcome, res.Output)
	}
	return l, nil
}

// control dials to from the Pod of the positive control in region, which it
// starts on the first call: the namespace iso-ctl-<tag> of the host and in it
// one dial Pod on the host network. It dials as the node (the identity host,
// remote-node in another region of the mesh), which the platform targets
// admit as they admit the kubelet: catalyst-system/baseline-default-deny
// (fromEntities host, remote-node) and the allow-gateway-ingress policies of
// openbao and gitea.
func (j *isoJourney) control(ctx context.Context, region kube.Region, to DialTarget) (DialResult, error) {
	src := DialSource{Namespace: "iso-ctl-" + j.tag}
	d := j.controls[region.ID]
	if d == nil {
		if _, err := j.c.kubectl(region, ctx, nil, "create", "namespace", src.Namespace); err != nil {
			return DialResult{}, err
		}
		d = &Dialer{Kubectl: j.c.kubectl, Region: region, Run: j.tag + "ctl", HostNetwork: true}
		j.controls[region.ID] = d
	}
	if err := d.Start(ctx, []DialSource{src}); err != nil {
		return DialResult{}, err
	}
	return d.Dial(ctx, src, to)
}

// isoSlices is what the check reads of a list of EndpointSlices.
type isoSlices struct {
	Items []struct {
		Endpoints []struct {
			Addresses  []string `json:"addresses"`
			Conditions struct {
				Ready *bool `json:"ready"`
			} `json:"conditions"`
		} `json:"endpoints"`
		Ports []struct {
			Name     string `json:"name"`
			Port     *int   `json:"port"`
			Protocol string `json:"protocol"`
		} `json:"ports"`
	} `json:"items"`
}

// isoReadyAddresses are the addresses of the ready endpoints of the Service t
// in the region of host, from its EndpointSlices that serve TCP port t.Port;
// an endpoint whose ready condition is not set counts as ready.
func isoReadyAddresses(ctx context.Context, host *Dialer, t DialTarget) ([]string, error) {
	out, err := host.Kubectl(host.Region, ctx, nil, "-n", t.Namespace, "get", "endpointslices", "-l", "kubernetes.io/service-name="+t.Service, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("read the EndpointSlices of the Service %s/%s: %v", t.Namespace, t.Service, err)
	}
	var list isoSlices
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("the EndpointSlices of the Service %s/%s are not JSON: %v", t.Namespace, t.Service, err)
	}
	var addrs []string
	for _, s := range list.Items {
		serves := false
		for _, p := range s.Ports {
			serves = serves || (p.Port != nil && *p.Port == t.Port && (p.Protocol == "TCP" || p.Protocol == ""))
		}
		for _, e := range s.Endpoints {
			for _, a := range e.Addresses {
				if serves && (e.Conditions.Ready == nil || *e.Conditions.Ready) && net.ParseIP(a) != nil && !contains(addrs, a) {
					addrs = append(addrs, a)
				}
			}
		}
	}
	return addrs, nil
}

// isoDNSPod is the address and the TCP port of a ready endpoint of the DNS
// Service of a vCluster, read through its kubeconfig.
func isoDNSPod(ctx context.Context, k kubectlFunc, r kube.Region) (DialTarget, error) {
	out, err := k(r, ctx, nil, "-n", "kube-system", "get", "endpointslices", "-l", "kubernetes.io/service-name=kube-dns", "-o", "json")
	if err != nil {
		return DialTarget{}, err
	}
	var list isoSlices
	if err := json.Unmarshal(out, &list); err != nil {
		return DialTarget{}, fmt.Errorf("the EndpointSlices of the DNS Service of %s are not JSON: %v", r.ID, err)
	}
	for _, s := range list.Items {
		// The port named dns-tcp; else the first TCP port (the Service also has a metrics port).
		port := 0
		for _, p := range s.Ports {
			if p.Port != nil && (p.Protocol == "TCP" || p.Protocol == "") && (port == 0 || p.Name == "dns-tcp") {
				port = *p.Port
			}
		}
		for _, e := range s.Endpoints {
			if port > 0 && len(e.Addresses) > 0 && (e.Conditions.Ready == nil || *e.Conditions.Ready) {
				return DialTarget{Address: e.Addresses[0], Port: port}, nil
			}
		}
	}
	return DialTarget{}, fmt.Errorf("the DNS Service of %s has no ready endpoint on a TCP port", r.ID)
}

// isoNodes are the rows of port 10250 of every Ready node of a region.
func isoNodes(ctx context.Context, k kubectlFunc, region kube.Region) ([]isoRow, error) {
	out, err := k(region, ctx, nil, "get", "nodes", "-o", "json")
	if err != nil {
		return nil, err
	}
	var list kube.List
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("the nodes of region %s are not JSON: %v", region.ID, err)
	}
	var rows []isoRow
	for _, n := range list.Items {
		if st, _, _, _ := n.Condition("Ready"); st != "True" {
			continue
		}
		for _, a := range n.Objects("status", "addresses") {
			if a.Str("type") == "InternalIP" && net.ParseIP(a.Str("address")) != nil {
				rows = append(rows, isoRow{"ISO-4", "the kubelet of the node " + n.Name(), DialTarget{Address: a.Str("address"), Port: isoKubeletPort}})
				break
			}
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("region %s has no Ready node with an InternalIP", region.ID)
	}
	return rows, nil
}

// rowsFor are the deny rows of ISO-2 to ISO-4 of the Pod of A in region i.
func (j *isoJourney) rowsFor(i int) []isoRow {
	region, peer := j.cc.Regions[i], j.cc.Regions[(i+1)%len(j.cc.Regions)]
	t := j.targets[region.ID]
	rows := []isoRow{
		{"ISO-2", "the DNS Service of B in region " + region.ID, t.bService},
		{"ISO-2", "a DNS Pod of B in region " + region.ID, t.bPod},
		{"ISO-2", "a DNS Pod of B in the peer region " + peer.ID, j.targets[peer.ID].bPod},
	}
	return append(rows, t.rest...)
}

// dial records one row from the dial Pod of o in region.
func (j *isoJourney) dial(ctx context.Context, region kube.Region, o *isoOrg, r isoRow, expect string) bool {
	src := DialSource{Namespace: j.ns}
	return j.step(fmt.Sprintf("%s in region %s: the Pod of %s to %s (%s) is %s", r.iso, region.ID, o.label, r.what, r.to, expect), func(ev Evidence) error {
		ev["iso"], ev["region"], ev["from"], ev["to"], ev["target"], ev["expect"] = r.iso, region.ID, o.label+" "+j.ns, r.to.String(), r.what, expect
		res, err := o.vc[region.ID].dialer.Dial(ctx, src, r.to)
		if err != nil {
			return err
		}
		ev["control"], ev["observed"], ev["output"] = res.Control, res.Outcome, res.Output
		return judgeDial(DialRow{Name: r.iso, From: src, To: r.to, Expect: expect}, res)
	})
}

// dials are ISO-2 to ISO-5: first the rows of B that must connect, then the rows of A that must time out.
func (j *isoJourney) dials(ctx context.Context) {
	for _, region := range j.cc.Regions {
		t := j.targets[region.ID]
		j.dial(ctx, region, j.b, isoRow{"ISO-2", "its own DNS Service", t.bService}, "allow")
		j.dial(ctx, region, j.b, isoRow{"ISO-2", "its own DNS Pod", t.bPod}, "allow")
	}
	internet := DialTarget{Address: isoInternet, Port: isoInternetPort}
	for i, region := range j.cc.Regions {
		for _, r := range j.rowsFor(i) {
			j.dial(ctx, region, j.a, r, "deny")
		}
		j.dial(ctx, region, j.a, isoRow{"ISO-5", "an internet address that no Application of A declares", internet}, "deny")
	}
}

// isoAllowAllPolicy is the NetworkPolicy of ISO-6.
func isoAllowAllPolicy(ns string) map[string]any {
	return map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": isoAllowAll, "namespace": ns},
		"spec": map[string]any{"podSelector": map[string]any{}, "policyTypes": []string{"Ingress", "Egress"},
			"ingress": []any{map[string]any{}}, "egress": []any{map[string]any{}}}}
}

// allowAll is ISO-6.
func (j *isoJourney) allowAll(ctx context.Context) {
	internet := DialTarget{Address: isoInternet, Port: isoInternetPort}
	for i, region := range j.cc.Regions {
		v := j.a.vc[region.ID]
		if !j.step("ISO-6: A creates the NetworkPolicy "+isoAllowAll+" (every Pod, all ingress, all egress) in "+j.ns+" of its vCluster in region "+region.ID, func(ev Evidence) error {
			b, err := json.Marshal(isoAllowAllPolicy(j.ns))
			if err != nil {
				return err
			}
			ev["region"], ev["policy"] = region.ID, string(b)
			_, err = j.vcKubectl(v)(v.region, ctx, bytes.NewReader(b), "-n", j.ns, "create", "-f", "-")
			return err
		}) {
			continue
		}
		if !j.step("ISO-6 in region "+region.ID+": the Pod of A reaches "+internet.String()+" once the policy is synced (allow): the policy took effect", func(ev Evidence) error {
			src := DialSource{Namespace: j.ns}
			var res DialResult
			_, err := poll(ctx, j.c.waits.policy, j.c.waits.every, func() (bool, string, error) {
				var err error
				if res, err = v.dialer.Dial(ctx, src, internet); err != nil {
					return false, "", err
				}
				return res.Control == DialConnected && res.Outcome == DialConnected, "control " + res.Control + ", target " + res.Outcome, nil
			})
			ev["region"], ev["to"], ev["control"], ev["observed"], ev["output"] = region.ID, internet.String(), res.Control, res.Outcome, res.Output
			if err != nil {
				return fmt.Errorf("the policy did not take effect, or the Sovereign has no internet egress, so ISO-6 would measure nothing: %v", err)
			}
			return nil
		}) {
			continue
		}
		for _, r := range j.rowsFor(i) {
			r.iso = "ISO-6 (" + r.iso + ")"
			j.dial(ctx, region, j.a, r, "deny")
		}
	}
}

// install starts ISO-9: an Application in the first Environment of A.
func (j *isoJourney) install(ctx context.Context) {
	region := j.cc.Regions[0]
	j.app.name = "iso-app-" + j.tag
	j.step("ISO-9: the Application "+j.app.name+" of "+isoBlueprint+" (topology singleton, region "+region.ID+") is installed in the first Environment of A (POST /catalyst/v1/apps/instances)", func(ev Evidence) error {
		env := ""
		_, err := poll(ctx, j.c.waits.org, j.c.waits.every, func() (bool, string, error) {
			var v struct {
				Environments []struct {
					Name string `json:"name"`
				} `json:"environments"`
			}
			if _, err := j.cc.API.Get(ctx, "/api/v1/refs/organizations/"+url.PathEscape(j.a.slug)+"/environments", &v); err != nil {
				return false, err.Error(), nil
			}
			if len(v.Environments) == 0 {
				return false, "A has no Environment yet", nil
			}
			env = v.Environments[0].Name
			return true, "", nil
		})
		if err != nil {
			return err
		}
		body := map[string]any{"blueprint": isoBlueprint, "org": j.a.slug, "environment": env, "name": j.app.name,
			"topology": "singleton", "placement": map[string]any{"regions": []string{region.ID}}}
		ev["request"] = body
		res, err := j.cc.API.Do(ctx, "POST", "/catalyst/v1/apps/instances", body)
		if err != nil {
			return err
		}
		ev["http"] = res.Status
		j.app.created = res.Status >= 200 && res.Status < 300
		if res.Status != 201 {
			return fmt.Errorf("HTTP %d (want 201): %s", res.Status, trunc(string(res.Body), 300))
		}
		return nil
	})
}

func (j *isoJourney) appsPath() string {
	dep := ""
	if j.cc.Deploy != nil {
		dep = j.cc.Deploy.ID
	}
	return "/api/v1/sovereigns/" + url.PathEscape(dep) + "/applications"
}

// answers ends ISO-9: the Application is Ready and answers through the gateway.
func (j *isoJourney) answers(ctx context.Context) {
	if !j.app.created {
		return
	}
	if !j.step("ISO-9: the Application "+j.app.name+" is Ready and has an endpoint (GET /api/v1/sovereigns/{id}/applications/{name}, GET /catalyst/v1/apps/{uid}/endpoints)", func(ev Evidence) error {
		if j.cc.Deploy == nil || j.cc.Deploy.ID == "" {
			return errors.New("the run names no deployment, so the Application cannot be read")
		}
		_, err := poll(ctx, j.c.waits.app, j.c.waits.every, func() (bool, string, error) {
			if j.app.namespace == "" {
				var list struct {
					Items []struct{ Name, Namespace string } `json:"items"`
				}
				if _, err := j.cc.API.Get(ctx, j.appsPath(), &list); err != nil {
					return false, err.Error(), nil
				}
				for _, a := range list.Items {
					if a.Name == j.app.name {
						j.app.namespace = a.Namespace
					}
				}
				if j.app.namespace == "" {
					return false, "the Application is not listed yet", nil
				}
			}
			var a appView
			if _, err := j.cc.API.Get(ctx, j.appsPath()+"/"+url.PathEscape(j.app.name)+"?namespace="+url.QueryEscape(j.app.namespace), &a); err != nil {
				return false, err.Error(), nil
			}
			j.app.uid = a.UID
			if st, why := a.ready(); st != "True" {
				return false, "Ready=" + st + " " + why, nil
			}
			var eps struct {
				Items []struct {
					Hostname      string `json:"hostname"`
					LaunchDefault bool   `json:"launchDefault"`
				} `json:"items"`
			}
			if _, err := j.cc.API.Get(ctx, "/catalyst/v1/apps/"+url.PathEscape(j.app.uid)+"/endpoints", &eps); err != nil {
				return false, err.Error(), nil
			}
			for _, e := range eps.Items {
				if e.Hostname != "" && (e.LaunchDefault || j.app.fqdn == "") {
					j.app.fqdn = e.Hostname
				}
			}
			return j.app.fqdn != "", fmt.Sprintf("Ready, %d endpoints", len(eps.Items)), nil
		})
		ev["namespace"], ev["uid"], ev["fqdn"] = j.app.namespace, j.app.uid, j.app.fqdn
		return err
	}) {
		return
	}
	j.step("ISO-9: the Application of A answers https://"+j.app.fqdn+"/ through the gateway with HTTP 200 and a WordPress page", func(ev Evidence) error {
		_, err := poll(ctx, j.c.waits.app, j.c.waits.every, func() (bool, string, error) {
			p, err := j.c.fetch(ctx, j.app.fqdn)
			if err != nil {
				return false, err.Error(), nil
			}
			ev["status"], ev["final_url"] = p.Status, p.FinalURL
			configured, _ := wordpress.IsConfiguredHome(p)
			return p.Status == 200 && (configured || wordpress.NeedsInstall(p)), fmt.Sprintf("HTTP %d at %s", p.Status, p.FinalURL), nil
		})
		return err
	})
}

// cleanup ends the port forwards and deletes the Application and the Organizations that the check created.
func (j *isoJourney) cleanup(ctx context.Context) {
	for _, o := range j.orgs() {
		for _, v := range o.vc {
			v.stop()
		}
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), j.c.waits.gone+5*time.Minute)
	defer cancel()
	for _, region := range j.cc.Regions {
		if j.controls[region.ID] == nil {
			continue
		}
		ns := "iso-ctl-" + j.tag
		j.step("ISO: the namespace "+ns+" of the positive control and its Pod on the host network are deleted in region "+region.ID, func(ev Evidence) error {
			ev["region"], ev["namespace"], ev["pods"] = region.ID, ns, j.controls[region.ID].Pods()
			_, err := j.c.kubectl(region, cctx, nil, "delete", "namespace", ns, "--wait=false", "--ignore-not-found")
			return err
		})
	}
	if j.app.created {
		j.step("ISO-9: the Application "+j.app.name+" is deleted (DELETE /api/v1/sovereigns/{id}/applications/{name})", func(ev Evidence) error {
			path := j.appsPath() + "/" + url.PathEscape(j.app.name)
			if j.app.namespace != "" {
				path += "?namespace=" + url.QueryEscape(j.app.namespace)
			}
			res, err := j.cc.API.Do(cctx, "DELETE", path, nil)
			if err != nil {
				return err
			}
			ev["http"] = res.Status
			if res.Status < 200 || res.Status > 299 {
				return fmt.Errorf("HTTP %d: %s", res.Status, trunc(string(res.Body), 300))
			}
			return nil
		})
	}
	// What must be gone, per region: the host namespace of every Organization
	// deleted, in every region, and the namespace of the positive control in
	// the regions that created it.
	var gone []string
	want := map[string][]string{}
	for _, region := range j.cc.Regions {
		if j.controls[region.ID] != nil {
			want[region.ID] = append(want[region.ID], "iso-ctl-"+j.tag)
		}
	}
	for _, o := range j.orgs() {
		if !o.created {
			continue
		}
		if j.step("ISO: Organization "+o.label+" ("+o.slug+") is deleted (DELETE /api/v1/organizations/{id})", func(ev Evidence) error {
			if o.id == "" {
				return errors.New("the create answered no id, so the Organization cannot be deleted through the API")
			}
			res, err := j.cc.API.Do(cctx, "DELETE", "/api/v1/organizations/"+url.PathEscape(o.id), nil)
			if err != nil {
				return err
			}
			ev["id"], ev["http"] = o.id, res.Status
			if res.Status < 200 || res.Status > 299 {
				return fmt.Errorf("HTTP %d: %s", res.Status, trunc(string(res.Body), 300))
			}
			if o.namespace == "" {
				return errors.New("deleted, but the create answered no tenant_namespace, so no host namespace can be awaited")
			}
			return nil
		}) {
			gone = append(gone, o.namespace)
		}
	}
	controls := len(want) > 0
	if len(gone) == 0 && !controls {
		return
	}
	for _, region := range j.cc.Regions {
		want[region.ID] = append(want[region.ID], gone...)
	}
	name := "ISO: the host namespaces " + strings.Join(gone, " and ") + " are gone in every region"
	switch {
	case len(gone) == 0:
		name = "ISO: the namespace of the positive control is gone in the regions that created it"
	case controls:
		name += ", and the namespace of the positive control in the regions that created it"
	}
	j.step(name, func(ev Evidence) error {
		_, err := poll(cctx, j.c.waits.gone, j.c.waits.every, func() (bool, string, error) {
			left := []string{}
			for _, region := range j.cc.Regions {
				for _, ns := range want[region.ID] {
					out, err := j.c.kubectl(region, cctx, nil, "get", "namespace", ns, "--ignore-not-found", "-o", "name")
					switch {
					case err != nil:
						left = append(left, "Namespace "+ns+" in "+region.ID+" (not read: "+err.Error()+")")
					case strings.TrimSpace(string(out)) != "":
						left = append(left, "Namespace "+ns+" in "+region.ID)
					}
				}
			}
			ev["left"] = left
			return len(left) == 0, "left: " + strings.Join(left, ", "), nil
		})
		return err
	})
}

// portForward starts `kubectl port-forward` to port of the Service and
// returns the local address that kubectl prints. The forward ends when stop
// is called.
//
// kubectl port-forward ends for good on the first error of any one forwarded
// connection ("lost connection to pod"), and every later call through it is
// refused: on hw307 (23 November 2026) three forwards to vClusters ended
// within 20 seconds, each right after a dial had run through it, and 31 rows
// of NET-G07 failed on "connection to the server 127.0.0.1:<port> was
// refused". So when kubectl ends before stop is called, the forward starts
// again on the same local address and prints why the previous one ended;
// vcKubectl sends a call that the forward refused once more.
func portForward(ctx context.Context, region kube.Region, namespace, service string, port int) (string, func(), error) {
	fctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	where := fmt.Sprintf("[%s] kubectl port-forward %s/%s", region.ID, namespace, service)
	f, err := startForward(fctx, region, namespace, service, ":"+strconv.Itoa(port))
	if err != nil {
		cancel()
		return "", nil, fmt.Errorf("%s %v", where, err)
	}
	addr := f.addr
	_, local, _ := net.SplitHostPort(addr)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			why := f.wait()
			if fctx.Err() != nil {
				return
			}
			fmt.Fprintf(os.Stderr, "%s on %s ended (%s); it starts again on the same address\n", where, addr, why)
			for {
				next, err := startForward(fctx, region, namespace, service, local+":"+strconv.Itoa(port))
				if err == nil {
					f = next
					break
				}
				select {
				case <-fctx.Done():
					return
				case <-time.After(time.Second):
				}
			}
		}
	}()
	stop := func() {
		cancel()
		<-done
	}
	return addr, stop, nil
}

// forwardProc is one running `kubectl port-forward`.
type forwardProc struct {
	addr string
	done chan struct{} // closed when kubectl has ended
	why  string        // what kubectl wrote to stderr, or how it ended
}

// wait waits until kubectl ends and says why.
func (f *forwardProc) wait() string {
	<-f.done
	return f.why
}

// startForward starts `kubectl port-forward service/<service> <ports>` on
// 127.0.0.1 and waits until kubectl prints its local address.
func startForward(ctx context.Context, region kube.Region, namespace, service, ports string) (*forwardProc, error) {
	bin := region.Kubectl
	if bin == "" {
		bin = "kubectl"
	}
	args := []string{"-n", namespace, "port-forward", "--address", "127.0.0.1", "service/" + service, ports}
	if region.Kubeconfig != "" {
		args = append([]string{"--kubeconfig", region.Kubeconfig}, args...)
	}
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // G204: the kubectl of the region, with arguments of the check
	var stderr bytes.Buffer
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	f := &forwardProc{done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		_ = pw.Close()
		f.why = strings.TrimSpace(stderr.String())
		if f.why == "" && err != nil {
			f.why = err.Error()
		}
		close(f.done)
	}()
	found := make(chan string, 1)
	go func() {
		addr := ""
		// kubectl writes "Handling connection for <port>" to stdout for every
		// forwarded connection. The reader keeps reading to EOF after the
		// address line, and drains whatever the scanner stops on (a line over
		// its 64 KiB limit): a stdout nobody reads blocks exec's copy, so Wait
		// would never return after kubectl ended and the forward would never
		// start again.
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			if m := forwardRE.FindStringSubmatch(sc.Text()); m != nil && addr == "" {
				addr = m[1]
				found <- addr
			}
		}
		_, _ = io.Copy(io.Discard, pr)
		if addr == "" {
			found <- ""
		}
	}()
	select {
	case addr := <-found:
		if addr != "" {
			f.addr = addr
			return f, nil
		}
		return nil, fmt.Errorf("ended before it printed its local address: %s", f.wait())
	case <-time.After(isoForwardWait):
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("printed no local address in %s: %s", isoForwardWait, f.wait())
	}
}

// fetchHome reads https://<host>/ through the gateway, without checking the certificate.
func fetchHome(ctx context.Context, host string) (*wordpress.Page, error) {
	return wordpress.New(host, true).Get(ctx, "/")
}
