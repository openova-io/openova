package suite

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/wordpress"
)

// The targets of the fake: per region the DNS Service and the DNS Pod of B,
// the Services of ISO-3 and the API server (the same in both regions), and
// two Ready nodes and one that is not Ready. gitea/gitea-http is headless, as
// the upstream gitea chart makes it: its two ready endpoints are its targets.
var isoFakeBDNS = map[string][2]string{"region-a": {"10.43.1.10", "10.42.1.5"}, "region-b": {"10.53.1.10", "10.52.1.5"}}

var isoFakeServices = map[string]string{"openbao/openbao": "10.43.0.20", "gitea/gitea-http": "None",
	"catalyst-system/catalyst-api": "10.43.0.22", "default/kubernetes": "10.43.0.1"}

// isoFakeMeshAPI is the ClusterIP of catalyst-system/catalyst-api in region-b
// when region-b is a secondary region (isoFake.meshB): the global stub of the
// edge, with no local endpoint, whose backends are the Pods of region-a.
const isoFakeMeshAPI = "10.53.0.22"

// isoFakeHostNamespaces are the namespaces of the host of every region, besides
// the host namespaces of the Organizations.
var isoFakeHostNamespaces = []string{"default", "kube-system", "catalyst-system", "openbao", "gitea", "keycloak"}

// isoFakeUID is the UID of the namespace name in the cluster: a region, or a vCluster <slug>@<region>.
func isoFakeUID(cluster, name string) string { return "uid-" + cluster + "-" + name }

const isoFakeNodes = `{"items": [
 {"metadata": {"name": "node-1"}, "status": {"conditions": [{"type": "Ready", "status": "True"}], "addresses": [{"type": "Hostname", "address": "node-1"}, {"type": "InternalIP", "address": "10.0.0.1"}]}},
 {"metadata": {"name": "node-2"}, "status": {"conditions": [{"type": "Ready", "status": "True"}], "addresses": [{"type": "InternalIP", "address": "10.0.0.2"}]}},
 {"metadata": {"name": "node-3"}, "status": {"conditions": [{"type": "Ready", "status": "False"}], "addresses": [{"type": "InternalIP", "address": "10.0.0.3"}]}}]}`

// isoAnswers are the outcomes of nc: the Pod of A before ISO-6 (A), after it
// (A6), the Pod of B (B), and the Pod of the positive control on the host
// network (C).
func isoAnswers() map[string]map[string]string {
	control := dialControlHost + ":53"
	a := map[string]string{control: DialConnected}
	for _, d := range []string{"10.43.1.10:53", "10.42.1.5:1053", "10.53.1.10:53", "10.52.1.5:1053", "10.43.0.20:8200", "10.42.0.31:3000",
		"10.42.0.32:3000", "10.43.0.22:8080", isoFakeMeshAPI + ":8080", "10.43.0.1:443", "10.0.0.1:10250", "10.0.0.2:10250", "169.254.169.254:80", "1.1.1.1:443"} {
		a[d] = DialTimedOut
	}
	a6 := map[string]string{}
	for k, v := range a {
		a6[k] = v
	}
	a6["1.1.1.1:443"] = DialConnected
	b := map[string]string{control: DialConnected, "10.43.1.10:53": DialConnected, "10.42.1.5:1053": DialConnected,
		"10.53.1.10:53": DialConnected, "10.52.1.5:1053": DialConnected}
	c := map[string]string{control: DialConnected, isoFakeMeshAPI + ":8080": DialConnected}
	return map[string]map[string]string{"A": a, "A6": a6, "B": b, "C": c}
}

func isoKubeconfig(slug string) string {
	return "apiVersion: v1\nclusters:\n- cluster:\n    certificate-authority-data: Zm9v\n    server: https://vcluster." + slug + ":443\n  name: vcluster\n" +
		"contexts:\n- context:\n    cluster: vcluster\n    user: vcluster\n  name: vcluster\ncurrent-context: vcluster\nkind: Config\n"
}

// isoFake is the cluster of check NET-G07: the kubectl of two regions and of
// the vClusters of A and B in each, and the port forwards to those vClusters.
// It hands the dial Pods to the fake of check NET-G06 (check_policy_dials_test.go).
type isoFake struct {
	t          *testing.T
	dial       *dialFake
	answers    map[string]map[string]string
	missing    string              // a Secret <namespace>/<name> prefix that region-b never holds
	listed     string              // a namespace of the host that every vCluster of A also lists, with the host's UID
	twin       string              // a namespace that every vCluster of A also lists with its own UID ("iso-b-": the host namespace of B)
	bare       string              // a namespace that every vCluster of A also lists without a UID
	hostOfA    bool                // the namespace of A's Applications in its vCluster carries the UID of A's host namespace
	hostLacks  string              // a namespace that the host does not list
	headless   map[string][]string // <namespace>/<service> of a headless Service -> its ready endpoint addresses
	listB      bool                // every vCluster of A also lists the dial Pod of B
	unsynced   bool                // the host namespaces hold no synced dial Pod
	stays      string              // a host namespace prefix that is never deleted
	forwardErr error
	page       *wordpress.Page
	forwards   int
	stops      int
	applied    map[string]bool     // vCluster -> the allow-all policy was created
	pods       map[string][]string // vCluster -> <namespace>/<name> of its Pods
	vcNS       map[string]string   // vCluster -> the namespace created in it
	kubeconfig []string            // the kubeconfig files the check wrote
	dialed     []string            // <org> <host>:<port> of every dial

	// meshB makes region-b a secondary region: catalyst-system/catalyst-api
	// there has the ClusterIP isoFakeMeshAPI and no local endpoint.
	// apiAnnotations are the annotations of catalyst-system/catalyst-api in
	// every region; newISOFake sets the pair that both charts render
	// (products/catalyst/chart/templates/api-service.yaml and the stub of
	// platform/catalyst-edge-routes/chart/templates/services.yaml).
	meshB          bool
	apiAnnotations map[string]string
	ctlCreated     []string // "<region> <namespace>" of every namespace of the positive control
	ctlDeleted     []string // "<region> <namespace>" of every one deleted
	ctlPods        []string // "<region> <namespace>/<name>" of every Pod on the host network
	ctlDialed      []string // "<region> <host>:<port>" of every dial of the positive control
}

func newISOFake(t *testing.T) *isoFake {
	return &isoFake{t: t, dial: &dialFake{t: t}, answers: isoAnswers(), applied: map[string]bool{}, pods: map[string][]string{}, vcNS: map[string]string{},
		headless:       map[string][]string{"gitea/gitea-http": {"10.42.0.31", "10.42.0.32"}},
		apiAnnotations: map[string]string{"service.cilium.io/global": "true", "service.cilium.io/shared": "true"},
		page:           &wordpress.Page{Status: 200, FinalURL: "https://wp.iso.test/wp-admin/install.php"}}
}

func (f *isoFake) kubectl(r kube.Region, ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	if len(args) > 4 && args[0] == "--server" {
		slug, _, _ := strings.Cut(r.ID, "@")
		if args[2] != "--tls-server-name" || args[3] != "vcluster."+slug || !strings.HasPrefix(args[1], "https://127.0.0.1:") {
			f.t.Errorf("a call of the vCluster %s has not the server of its forward and the host of its kubeconfig: %v", r.ID, args[:4])
		}
		if b, err := os.ReadFile(r.Kubeconfig); err != nil || string(b) != isoKubeconfig(slug) {
			f.t.Errorf("the vCluster %s is called with another kubeconfig than its Secret holds: %v", r.ID, err)
		} else if !contains(f.kubeconfig, r.Kubeconfig) {
			f.kubeconfig = append(f.kubeconfig, r.Kubeconfig)
		}
		return f.vcluster(r, ctx, stdin, args[4:])
	}
	return f.host(r, ctx, stdin, args)
}

func (f *isoFake) vcluster(r kube.Region, ctx context.Context, stdin io.Reader, args []string) ([]byte, error) {
	slug, region, _ := strings.Cut(r.ID, "@")
	org := "B"
	if strings.HasPrefix(slug, "iso-a-") {
		org = "A"
	}
	line := strings.Join(args, " ")
	switch {
	case line == "get namespace default -o name":
		return []byte("namespace/default\n"), nil
	case len(args) == 3 && args[0] == "create" && args[1] == "namespace":
		f.vcNS[r.ID] = args[2]
		return nil, nil
	case line == "get namespaces -o "+isoNamespaceUIDs:
		// As measured on a live install: the vCluster's own namespaces include
		// one named like the host namespace of its Organization (the target of
		// its Applications) and keycloak (the replicated host Service).
		out := ""
		for _, ns := range []string{"default", f.vcNS[r.ID], slug, "keycloak", "kube-node-lease", "kube-public", "kube-system"} {
			uid := isoFakeUID(r.ID, ns)
			if ns == slug && org == "A" && f.hostOfA {
				uid = isoFakeUID(region, ns)
			}
			out += ns + "=" + uid + " "
		}
		if twin := f.twin; org == "A" && twin != "" {
			if twin == "iso-b-" {
				twin += strings.TrimPrefix(slug, "iso-a-")
			}
			out += twin + "=" + isoFakeUID(r.ID, twin) + " "
		}
		if org == "A" && f.bare != "" {
			out += f.bare + " "
		}
		if org == "A" && f.listed != "" {
			out += f.listed + "=" + isoFakeUID(region, f.listed)
		}
		return []byte(out), nil
	case strings.HasPrefix(line, "get pods -A -o jsonpath="):
		pods := append([]string{"kube-system/coredns-6d4b75cb6d-x2x4q"}, f.pods[r.ID]...)
		if org == "A" && f.listB {
			pods = append(pods, f.pods["iso-b-"+strings.TrimPrefix(slug, "iso-a-")+"@"+region]...)
		}
		return []byte(strings.Join(pods, " ") + " "), nil
	case line == "-n kube-system get service kube-dns -o jsonpath={.spec.clusterIP}" && org == "B":
		return []byte(isoFakeBDNS[region][0]), nil
	case line == "-n kube-system get endpointslices -l kubernetes.io/service-name=kube-dns -o json" && org == "B":
		return []byte(`{"items": [{"endpoints": [{"addresses": ["10.9.9.9"], "conditions": {"ready": false}}, {"addresses": ["` + isoFakeBDNS[region][1] + `"], "conditions": {}}],
 "ports": [{"name": "dns", "port": 1053, "protocol": "UDP"}, {"name": "metrics", "port": 9153, "protocol": "TCP"}, {"name": "dns-tcp", "port": 1053, "protocol": "TCP"}]}]}`), nil
	case len(args) > 3 && args[0] == "-n" && args[2] == "create":
		b, _ := io.ReadAll(stdin)
		var o kube.Object
		if err := json.Unmarshal(b, &o); err == nil && o.Str("kind") == "NetworkPolicy" {
			if org != "A" || o.Str("metadata", "name") != isoAllowAll || o.Namespace() != args[1] || string(b) != mustJSON(f.t, isoAllowAllPolicy(args[1])) {
				f.t.Errorf("unexpected NetworkPolicy in %s: %s", r.ID, b)
			}
			f.applied[r.ID] = true
			return nil, nil
		}
		f.pods[r.ID] = append(f.pods[r.ID], args[1]+"/"+o.Str("metadata", "name"))
		return f.dial.kubectl(r, ctx, bytes.NewReader(b), args...)
	case len(args) > 3 && args[0] == "-n" && args[2] == "exec":
		which := org
		if org == "A" && f.applied[r.ID] {
			which = "A6"
		}
		f.dial.answers = f.answers[which]
		f.dialed = append(f.dialed, org+" "+args[len(args)-2]+":"+args[len(args)-1])
		return f.dial.kubectl(r, ctx, stdin, args...)
	case len(args) > 3 && args[0] == "-n" && args[2] == "wait":
		return f.dial.kubectl(r, ctx, stdin, args...)
	}
	f.t.Fatalf("unexpected kubectl call of the vCluster %s: %v", r.ID, args)
	return nil, nil
}

func (f *isoFake) host(r kube.Region, ctx context.Context, stdin io.Reader, args []string) ([]byte, error) {
	line := strings.Join(args, " ")
	switch {
	case len(args) == 8 && args[2] == "get" && args[3] == "secret" && strings.HasSuffix(line, " --ignore-not-found -o jsonpath={.data.config}"):
		key := args[1] + "/" + args[4]
		if f.missing != "" && r.ID == "region-b" && strings.HasPrefix(key, f.missing) {
			return nil, nil
		}
		slug := strings.TrimSuffix(strings.TrimPrefix(args[4], "tenant-"), "-kubeconfig")
		if args[4] == isoVClusterSecret {
			slug = args[1]
		}
		return []byte(base64.StdEncoding.EncodeToString([]byte(isoKubeconfig(slug))) + "\n"), nil
	case len(args) == 8 && args[2] == "get" && args[3] == "pods" && args[5] == isoManagedBy:
		names := []string{"coredns-6d4b75cb6d-x2x4q-x-kube-system-x-vcluster"}
		for vc, pods := range f.pods {
			if slug, region, _ := strings.Cut(vc, "@"); slug == args[1] && region == r.ID && !f.unsynced {
				for _, p := range pods {
					ns, name, _ := strings.Cut(p, "/")
					names = append(names, name+"-x-"+ns+"-x-vcluster")
				}
			}
		}
		return []byte(strings.Join(names, " ")), nil
	case len(args) == 7 && args[2] == "get" && args[3] == "service" && args[6] == "json":
		annotations := map[string]string{}
		if args[1]+"/"+args[4] == "catalyst-system/catalyst-api" {
			annotations = f.apiAnnotations
		}
		return []byte(mustJSON(f.t, map[string]any{"apiVersion": "v1", "kind": "Service",
			"metadata": map[string]any{"name": args[4], "namespace": args[1], "annotations": annotations}})), nil
	case len(args) == 7 && args[2] == "get" && args[3] == "service":
		ip, ok := isoFakeServices[args[1]+"/"+args[4]]
		if !ok {
			f.t.Fatalf("unexpected Service %s/%s", args[1], args[4])
		}
		if f.meshB && r.ID == "region-b" && args[1]+"/"+args[4] == "catalyst-system/catalyst-api" {
			ip = isoFakeMeshAPI
		}
		return []byte(ip), nil
	case len(args) > 5 && args[2] == "get" && args[3] == "endpointslices" && f.meshB && r.ID == "region-b" &&
		args[1]+"/"+args[5] == "catalyst-system/kubernetes.io/service-name=catalyst-api":
		// The stub selects no Pod of region-b: its EndpointSlice holds no endpoint.
		return []byte(`{"items": [{"endpoints": null, "ports": [{"name": "", "port": 8080, "protocol": "TCP"}]}]}`), nil
	case len(args) == 3 && args[0] == "create" && args[1] == "namespace" && strings.HasPrefix(args[2], "iso-ctl-"):
		f.ctlCreated = append(f.ctlCreated, r.ID+" "+args[2])
		return nil, nil
	case len(args) == 5 && line == "delete namespace "+args[2]+" --wait=false --ignore-not-found" && strings.HasPrefix(args[2], "iso-ctl-"):
		f.ctlDeleted = append(f.ctlDeleted, r.ID+" "+args[2])
		return nil, nil
	case len(args) > 3 && args[0] == "-n" && strings.HasPrefix(args[1], "iso-ctl-") && args[2] == "create":
		b, _ := io.ReadAll(stdin)
		var o kube.Object
		if err := json.Unmarshal(b, &o); err != nil || o.Str("kind") != "Pod" || o.Namespace() != args[1] || !contains(f.ctlCreated, r.ID+" "+args[1]) {
			f.t.Fatalf("the positive control creates another object than a Pod of its namespace in %s: %s", r.ID, b)
		}
		c := o.Objects("spec", "containers")[0]
		if c.Str("image") != secProbeImage || o.Get("spec", "hostNetwork") != true || o.Str("spec", "dnsPolicy") != "ClusterFirstWithHostNet" ||
			o.Get("spec", "automountServiceAccountToken") != false || o.Get("spec", "securityContext", "runAsNonRoot") != true || o.Str("metadata", "labels", DialLabel) == "" {
			f.t.Errorf("the Pod of the positive control runs another image, not on the host network with the cluster DNS, with a token, as root or without its label: %s", b)
		}
		f.ctlPods = append(f.ctlPods, r.ID+" "+args[1]+"/"+o.Name())
		return nil, nil
	case len(args) > 3 && args[0] == "-n" && strings.HasPrefix(args[1], "iso-ctl-") && args[2] == "wait":
		return nil, nil
	case len(args) > 3 && args[0] == "-n" && strings.HasPrefix(args[1], "iso-ctl-") && args[2] == "exec":
		f.dial.answers = f.answers["C"]
		f.ctlDialed = append(f.ctlDialed, r.ID+" "+args[len(args)-2]+":"+args[len(args)-1])
		return f.dial.kubectl(r, ctx, stdin, args...)
	case len(args) > 5 && args[2] == "get" && args[3] == "endpointslices":
		addrs, ok := f.headless[args[1]+"/"+strings.TrimPrefix(args[5], "kubernetes.io/service-name=")]
		if !ok {
			return f.dial.kubectl(r, ctx, stdin, args...)
		}
		// One endpoint that is not ready and one of another port, which are no target; then the ready ones.
		eps := []string{`{"addresses": ["10.42.0.99"], "conditions": {"ready": false}}`}
		for i, a := range addrs {
			cond := `"ready": true`
			if i == 0 {
				cond = "" // not set: it counts as ready
			}
			eps = append(eps, `{"addresses": ["`+a+`"], "conditions": {`+cond+`}}`)
		}
		return []byte(`{"items": [{"endpoints": [` + strings.Join(eps, ", ") + `], "ports": [{"name": "http", "port": 3000, "protocol": "TCP"}]},
 {"endpoints": [{"addresses": ["10.42.0.98"], "conditions": {"ready": true}}], "ports": [{"name": "ssh", "port": 2222, "protocol": "TCP"}]}]}`), nil
	case line == "get namespaces -o "+isoNamespaceUIDs:
		out := ""
		for vc := range f.vcNS {
			if slug, region, _ := strings.Cut(vc, "@"); region == r.ID {
				out += slug + "=" + isoFakeUID(r.ID, slug) + " "
			}
		}
		for _, ns := range isoFakeHostNamespaces {
			if ns != f.hostLacks {
				out += ns + "=" + isoFakeUID(r.ID, ns) + " "
			}
		}
		return []byte(out), nil
	case line == "get nodes -o json":
		return []byte(isoFakeNodes), nil
	case len(args) == 6 && args[0] == "get" && args[1] == "namespace" && line == "get namespace "+args[2]+" --ignore-not-found -o name":
		if f.stays != "" && strings.HasPrefix(args[2], f.stays) {
			return []byte("namespace/" + args[2] + "\n"), nil
		}
		return nil, nil
	}
	f.t.Fatalf("unexpected kubectl call of region %s: %v", r.ID, args)
	return nil, nil
}

func (f *isoFake) forward(_ context.Context, region kube.Region, ns, service string, port int) (string, func(), error) {
	if service != isoVClusterName || port != 443 || !strings.HasPrefix(ns, "iso-") || region.Kubeconfig != "" {
		f.t.Errorf("a forward to another Service than the vCluster API of an Organization, or not with the kubeconfig of the region: %s %s/%s:%d", region.ID, ns, service, port)
	}
	if f.forwardErr != nil {
		return "", nil, f.forwardErr
	}
	f.forwards++
	return "127.0.0.1:" + strconv.Itoa(40000+f.forwards), func() { f.stops++ }, nil
}

func (f *isoFake) fetch(_ context.Context, host string) (*wordpress.Page, error) {
	if host != "wp.iso.test" {
		f.t.Errorf("ISO-9 fetched %q, not the endpoint of the Application", host)
	}
	return f.page, nil
}

func mustJSON(t *testing.T, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// isoConsole is the console API of the fake: the Organizations, their
// Environments and the Application of ISO-9.
type isoConsole struct {
	t          *testing.T
	refuse     string // the slug prefix whose create is refused with HTTP 409
	noNS       string // the slug prefix whose create answers no tenant_namespace
	state      string // what GET of an Organization answers; "" is done
	requests   []map[string]any
	created    []string
	deleted    []string
	app        map[string]any
	appDeleted bool
}

func (c *isoConsole) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	app := ""
	if c.app != nil {
		app, _ = c.app["name"].(string)
	}
	reply := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case r.Method == "POST" && path == "/api/v1/organizations":
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		c.requests = append(c.requests, b)
		slug, _ := b["subdomain"].(string)
		if c.refuse != "" && strings.HasPrefix(slug, c.refuse) {
			reply(409, map[string]string{"error": "org-tenant-exists"})
			return
		}
		c.created = append(c.created, slug)
		ns := slug
		if c.noNS != "" && strings.HasPrefix(slug, c.noNS) {
			ns = ""
		}
		reply(202, map[string]string{"org_tenant_id": "id-" + slug, "subdomain": slug, "state": "pending", "tenant_namespace": ns})
	case r.Method == "GET" && strings.HasPrefix(path, "/api/v1/organizations/id-"):
		reply(200, map[string]string{"state": nonEmpty(c.state, "done"), "last_error": "vcluster: boom"})
	case r.Method == "DELETE" && strings.HasPrefix(path, "/api/v1/organizations/id-"):
		c.deleted = append(c.deleted, strings.TrimPrefix(path, "/api/v1/organizations/id-"))
		w.WriteHeader(204)
	case r.Method == "GET" && strings.HasPrefix(path, "/api/v1/refs/organizations/iso-a-") && strings.HasSuffix(path, "/environments"):
		org := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/refs/organizations/"), "/environments")
		reply(200, map[string]any{"environments": []map[string]string{{"name": org + "-prod", "envType": "prod", "organizationRef": org}}})
	case r.Method == "POST" && path == "/catalyst/v1/apps/instances":
		_ = json.NewDecoder(r.Body).Decode(&c.app)
		reply(201, map[string]string{"name": app})
	case r.Method == "GET" && path == "/api/v1/sovereigns/dep-1/applications":
		reply(200, map[string]any{"items": []map[string]string{{"name": app, "namespace": "apps"}}})
	case r.Method == "GET" && path == "/api/v1/sovereigns/dep-1/applications/"+app && r.URL.Query().Get("namespace") == "apps":
		reply(200, map[string]any{"uid": "uid-1", "conditions": []map[string]string{{"type": "Ready", "status": "True"}}})
	case r.Method == "GET" && path == "/catalyst/v1/apps/uid-1/endpoints":
		reply(200, map[string]any{"items": []map[string]any{{"hostname": "other.iso.test"}, {"hostname": "wp.iso.test", "launchDefault": true}}})
	case r.Method == "DELETE" && path == "/api/v1/sovereigns/dep-1/applications/"+app && r.URL.Query().Get("namespace") == "apps":
		c.appDeleted = true
		w.WriteHeader(200)
	default:
		c.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(500)
	}
}

func isoTestWaits() isoWaits {
	return isoWaits{org: 150 * time.Millisecond, app: 150 * time.Millisecond, gone: 150 * time.Millisecond, policy: 150 * time.Millisecond, every: time.Millisecond}
}

// isoRun runs check NET-G07 the way the runner does. It fails the test unless
// the check passed (want is empty) or failed with a reason that holds want,
// and unless every Organization it created was deleted, every forward was
// ended and every kubeconfig file it wrote was removed.
func isoRun(t *testing.T, f *isoFake, console *isoConsole, regions []kube.Region, want string) *runner {
	t.Helper()
	srv := httptest.NewServer(console)
	defer srv.Close()
	r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
	c := orgIsolationCheck{kubectl: f.kubectl, forward: f.forward, fetch: f.fetch, waits: isoTestWaits()}
	cc := &CheckContext{Regions: regions, API: api.New(srv.URL, "session"), Deploy: &DeploymentInfo{ID: "dep-1"}}
	r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(c.ID(), StateRequired)}}, []Check{c}, cc, true)
	if res := r.run.Checks[0]; (want == "") != (res.Status == Pass) || !strings.Contains(res.Reason, want) {
		t.Errorf("status %s, reason %q; want a reason that holds %q (empty: PASS)", res.Status, res.Reason, want)
	}
	if strings.Join(console.created, " ") != strings.Join(console.deleted, " ") {
		t.Errorf("Organizations created %v, deleted %v", console.created, console.deleted)
	}
	if f.forwards != f.stops {
		t.Errorf("%d forwards started, %d ended", f.forwards, f.stops)
	}
	if strings.Join(f.ctlCreated, ",") != strings.Join(f.ctlDeleted, ",") {
		t.Errorf("namespaces of the positive control created %v, deleted %v", f.ctlCreated, f.ctlDeleted)
	}
	for _, p := range f.kubeconfig {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the kubeconfig file %s was not removed: %v", p, err)
		}
	}
	return r
}

// TestOrgIsolationCheck: the check passes only when every row of A times out,
// the targets of B answer B, the internet address answers A once the
// allow-all policy took effect, and the Application of A answers.
func TestOrgIsolationCheck(t *testing.T) {
	for name, c := range map[string]struct {
		which, target, outcome, want string
	}{
		"isolated":                                         {"", "", "", ""},
		"the DNS Service of B is reachable":                {"A", "10.43.1.10:53", DialConnected, "want deny; the dial of 10.43.1.10:53 connected"},
		"a Pod of B in the peer region":                    {"A", "10.52.1.5:1053", DialConnected, "want deny; the dial of 10.52.1.5:1053 connected"},
		"OpenBao refuses":                                  {"A", "10.43.0.20:8200", DialRefused, "want deny; the dial of 10.43.0.20:8200 was refused"},
		"one endpoint of the headless Git service answers": {"A", "10.42.0.32:3000", DialConnected, "want deny; the dial of 10.42.0.32:3000 connected"},
		"the host API server is reachable":                 {"A", "10.43.0.1:443", DialConnected, "want deny; the dial of 10.43.0.1:443 connected"},
		"a kubelet is reachable":                           {"A", "10.0.0.2:10250", DialConnected, "want deny; the dial of 10.0.0.2:10250 connected"},
		"the metadata address is reachable":                {"A", "169.254.169.254:80", DialConnected, "want deny; the dial of 169.254.169.254:80 connected"},
		"the internet before ISO-6":                        {"A", "1.1.1.1:443", DialConnected, "want deny; the dial of 1.1.1.1:443 connected"},
		"the allow-all policy widens":                      {"A6", "10.42.1.5:1053", DialConnected, "want deny; the dial of 10.42.1.5:1053 connected"},
		"the allow-all policy has no effect":               {"A6", "1.1.1.1:443", DialTimedOut, "the policy did not take effect, or the Sovereign has no internet egress"},
		"a target of B answers nobody":                     {"B", "10.42.1.5:1053", DialTimedOut, "want allow; the dial of 10.42.1.5:1053 timed-out"},
		"the Pod of A has no network":                      {"A", dialControlHost + ":53", DialTimedOut, "it has no working network"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newISOFake(t)
			if c.which != "" {
				f.answers[c.which][c.target] = c.outcome
			}
			console := &isoConsole{t: t}
			r := isoRun(t, f, console, twoRegions(), c.want)
			if c.want != "" {
				return
			}
			if len(r.run.Steps) != 74 {
				t.Fatalf("want 74 steps (3 create, ISO-9 install, 4 kubeconfigs, 2 starts, 2 lists, 2 targets, 4 rows of B, 24 rows of A, 26 of ISO-6, 2 of ISO-9, 4 to delete), got %d", len(r.run.Steps))
			}
			for i, req := range console.requests {
				slug, _ := req["subdomain"].(string)
				if req["kind"] != "internal" || req["plan_slug"] != []string{"m", "s"}[i] || req["admin_email"] != slug+"@acceptance.invalid" || !strings.HasPrefix(slug, []string{"iso-a-", "iso-b-"}[i]) {
					t.Errorf("create %d: %v", i, req)
				}
			}
			a, b := console.created[0], console.created[1]
			if strings.TrimPrefix(a, "iso-a-") != strings.TrimPrefix(b, "iso-b-") || len(a) != 16 {
				t.Errorf("want iso-a-<tag> and iso-b-<tag>, got %s and %s", a, b)
			}
			ns := "iso-" + strings.TrimPrefix(a, "iso-a-")
			for _, region := range []string{"region-a", "region-b"} {
				for _, slug := range []string{a, b} {
					if pods := f.pods[slug+"@"+region]; len(pods) != 1 || !strings.HasPrefix(pods[0], ns+"/pol-dial-") || f.vcNS[slug+"@"+region] != ns {
						t.Errorf("want one dial Pod in %s of the vCluster %s@%s, created through its kubeconfig: %v", ns, slug, region, pods)
					}
				}
				if !f.applied[a+"@"+region] || f.applied[b+"@"+region] {
					t.Errorf("want the allow-all policy in the vCluster of A only, in region %s: %v", region, f.applied)
				}
			}
			if len(f.kubeconfig) != 4 || f.forwards != 4 {
				t.Errorf("want four kubeconfigs and four forwards (A and B in two regions), got %v and %d", f.kubeconfig, f.forwards)
			}
			if n := strings.Count(strings.Join(f.dialed, ","), "B "); n != 4 || len(f.dialed) != 4+24+2+22 {
				t.Errorf("want 4 dials of B and 48 of A, got %d of %d: %v", n, len(f.dialed), f.dialed)
			}
			// catalyst-api is a global Service in both regions, as both charts render it, and has a
			// ready endpoint in each: it is judged by its endpoint, with no positive control.
			if len(f.ctlCreated) != 0 || len(f.ctlDialed) != 0 {
				t.Errorf("a positive control ran though every Service has a ready endpoint: %v %v", f.ctlCreated, f.ctlDialed)
			}
			// The headless Git service: each ready endpoint on its port, in ISO-3 and ISO-6 of both
			// regions; never the endpoint that is not ready nor the one of another port.
			dialed := strings.Join(f.dialed, ",") + ","
			if strings.Count(dialed, "A 10.42.0.31:3000,") != 4 || strings.Count(dialed, "A 10.42.0.32:3000,") != 4 || strings.Contains(dialed, "10.42.0.9") {
				t.Errorf("want each ready endpoint of gitea/gitea-http dialed four times and no other: %v", f.dialed)
			}
			// ISO-1: the namespaces of the vCluster named like a host namespace are its own (another UID): recorded, not failed.
			lists := 0
			for _, s := range r.run.Steps {
				if strings.HasPrefix(s.Name, "ISO-1: the kubeconfig of A") {
					lists++
					if got := mustJSON(t, s.Evidence["own_namespaces_named_like_host_namespaces"]); !strings.Contains(got, `"`+a+"=uid-"+a+"@") || !strings.Contains(got, `"keycloak=uid-`+a+"@") {
						t.Errorf("ISO-1 records %s, want the namespaces %s and keycloak of the vCluster", got, a)
					}
				}
			}
			if lists != 2 {
				t.Errorf("want two steps of ISO-1, got %d", lists)
			}
			if console.app["blueprint"] != isoBlueprint || console.app["org"] != a || console.app["environment"] != a+"-prod" || console.app["topology"] != "singleton" || !console.appDeleted {
				t.Errorf("ISO-9: want %s installed in the Environment of A, singleton, and deleted: %v, deleted %v", isoBlueprint, console.app, console.appDeleted)
			}
			for _, s := range r.run.Steps {
				if s.Evidence["expect"] == "deny" && s.Evidence["observed"] != DialTimedOut {
					t.Errorf("a deny row passed without a timeout: %s %v", s.Name, s.Evidence)
				}
			}
		})
	}
}

// TestOrgIsolationCheck_Faults: what fails before the dials, and that the
// check deletes what it created also then.
func TestOrgIsolationCheck_Faults(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *isoFake, c *isoConsole)
		want  string
		steps int // 0: not compared
	}{
		{"the create of A is refused", func(_ *isoFake, c *isoConsole) { c.refuse = "iso-a-" }, "HTTP 409 (want 202)", 4},
		{"the create of A names no namespace", func(_ *isoFake, c *isoConsole) { c.noNS = "iso-a-" }, "the answer names no tenant_namespace", 5},
		{"an Organization fails", func(_ *isoFake, c *isoConsole) { c.state = "failed" }, "is failed: vcluster: boom", 6},
		{"an Organization is never done", func(_ *isoFake, c *isoConsole) { c.state = "vcluster_created" }, "timed out after", 6},
		{"a mirror is missing in region b", func(f *isoFake, _ *isoConsole) { f.missing = "flux-system/tenant-iso-a-" }, "no kubeconfig in flux-system/tenant-iso-a-", 0},
		{"the forward fails", func(f *isoFake, _ *isoConsole) { f.forwardErr = errors.New("unable to forward") }, "unable to forward", 0},
		{"the dial Pods are not synced", func(f *isoFake, _ *isoConsole) { f.unsynced = true }, "holds no Pod pol-dial-", 0},
		{"A lists a host namespace", func(f *isoFake, _ *isoConsole) { f.listed = "catalyst-system" }, "lists the namespace catalyst-system of the host", 0},
		{"A lists its host namespace by the host's UID", func(f *isoFake, _ *isoConsole) { f.hostOfA = true }, "the kubeconfig of A lists the namespace iso-a-", 0},
		{"A lists the host namespace of B with its own UID", func(f *isoFake, _ *isoConsole) { f.twin = "iso-b-" }, "the kubeconfig of A lists the namespace iso-b-", 0},
		{"A lists openbao with its own UID", func(f *isoFake, _ *isoConsole) { f.twin = "openbao" }, "lists the namespace openbao of the host by name (with the UID uid-iso-a-", 0},
		{"A lists a namespace without a UID", func(f *isoFake, _ *isoConsole) { f.bare = "stray" },
			"the kubeconfig of A lists the namespace stray without a UID, so the identity proves nothing", 0},
		{"the host lists no namespace gitea", func(f *isoFake, _ *isoConsole) { f.hostLacks = "gitea" },
			"the kubeconfig of region region-a lists no namespace gitea with a UID, so the identity of the namespaces of A proves nothing", 0},
		{"the headless Git service has no ready endpoint", func(f *isoFake, _ *isoConsole) { f.headless["gitea/gitea-http"] = nil },
			"the headless Service gitea/gitea-http of region region-a has no ready endpoint on TCP port 3000: nothing listens, so a timeout would prove nothing", 0},
		{"A lists the Pod of B", func(f *isoFake, _ *isoConsole) { f.listB = true }, "the kubeconfig of A lists the Pod iso-", 0},
		{"OpenBao has no endpoint", func(f *isoFake, _ *isoConsole) { f.dial.ready = map[string]int{"openbao/openbao": 0} }, "openbao/openbao of region region-a has no ready endpoint", 0},
		{"the Application does not answer", func(f *isoFake, _ *isoConsole) {
			f.page = &wordpress.Page{Status: 503, FinalURL: "https://wp.iso.test/"}
		}, "last observed: HTTP 503", 74},
		{"a namespace stays", func(f *isoFake, _ *isoConsole) { f.stays = "iso-b-" }, "left: Namespace iso-b-", 74},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newISOFake(t)
			console := &isoConsole{t: t}
			c.setup(f, console)
			r := isoRun(t, f, console, twoRegions(), c.want)
			if c.steps > 0 && len(r.run.Steps) != c.steps {
				t.Errorf("want %d steps, got %d", c.steps, len(r.run.Steps))
			}
		})
	}
	f, console := newISOFake(t), &isoConsole{t: t}
	if r := isoRun(t, f, console, twoRegions()[:1], "journey ISO runs in two regions, and the run has 1"); len(console.requests) != 0 || len(r.run.Steps) != 1 {
		t.Errorf("one region: want no request and one failed step, got %v and %d steps", console.requests, len(r.run.Steps))
	}
	r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
	check := orgIsolationCheck{kubectl: f.kubectl, forward: f.forward, fetch: f.fetch, waits: isoTestWaits()}
	r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(check.ID(), StateRequired)}}, []Check{check}, &CheckContext{Regions: twoRegions()}, true)
	if res := r.run.Checks[0]; res.Status != Fail || !strings.Contains(res.Reason, "no console session") {
		t.Errorf("no console session: %s %q", res.Status, res.Reason)
	}
	if c, ok := registry[orgIsolationID].(orgIsolationCheck); !ok || c.kubectl == nil || c.forward == nil || c.fetch == nil || c.waits.org != 30*time.Minute || c.waits.policy != 3*time.Minute {
		t.Errorf("the registry must hold %s with kubectl, the port forward, the fetch and the waits of a cloud run", orgIsolationID)
	}
}

// TestOrgIsolationCheck_SecondaryRegion: region-b is a secondary region, as on
// every install with two regions. It runs no catalyst-api: its
// catalyst-system/catalyst-api is the global and shared stub of the edge,
// with no local endpoint, whose backends answer from region-a over the mesh.
// The check proves that it listens from region-b with a positive control on
// the host network, then requires the Pod of A to time out on it, before and
// after ISO-6. Region-a, whose catalyst-api has a ready endpoint, is judged
// by that endpoint, as before.
func TestOrgIsolationCheck_SecondaryRegion(t *testing.T) {
	mesh := isoFakeMeshAPI + ":8080"
	for name, c := range map[string]struct {
		setup func(f *isoFake)
		want  string
	}{
		"the control reaches it and the Pod of A times out": {func(*isoFake) {}, ""},
		"the namespace of the positive control stays":       {func(f *isoFake) { f.stays = "iso-ctl-" }, "left: Namespace iso-ctl-"},
		"the control does not reach it": {func(f *isoFake) { f.answers["C"][mesh] = DialTimedOut },
			"the Service catalyst-system/catalyst-api of region region-b has no ready endpoint, and the dial of its ClusterIP " + mesh +
				" from a Pod on the host network of region region-b timed-out"},
		"the control has no network": {func(f *isoFake) { f.answers["C"][dialControlHost+":53"] = DialTimedOut },
			"the Service catalyst-system/catalyst-api of region region-b has no ready endpoint, and the positive control from a Pod on the host network of the region has no working network"},
		"the Pod of A reaches it through the mesh": {func(f *isoFake) { f.answers["A"][mesh] = DialConnected },
			"want deny; the dial of " + mesh + " connected"},
		"the Pod of A reaches it through the mesh after ISO-6": {func(f *isoFake) { f.answers["A6"][mesh] = DialConnected },
			"want deny; the dial of " + mesh + " connected"},
		"the Pod of A is refused by it": {func(f *isoFake) { f.answers["A"][mesh] = DialRefused },
			"want deny; the dial of " + mesh + " was refused"},
		"it is no global Service": {func(f *isoFake) { f.apiAnnotations = nil },
			`the Service catalyst-system/catalyst-api of region region-b has no ready endpoint and is no Cilium global and shared Service (service.cilium.io/global "", service.cilium.io/shared "")`},
		"it is global and not shared": {func(f *isoFake) { f.apiAnnotations["service.cilium.io/shared"] = "false" },
			`has no ready endpoint and is no Cilium global and shared Service (service.cilium.io/global "true", service.cilium.io/shared "false")`},
	} {
		t.Run(name, func(t *testing.T) {
			f := newISOFake(t)
			f.meshB = true
			c.setup(f)
			r := isoRun(t, f, &isoConsole{t: t}, twoRegions(), c.want)
			if c.want != "" {
				return
			}
			if len(r.run.Steps) != 75 {
				t.Fatalf("want 75 steps (the 74 of a primary region and the deletion of the namespace of the positive control), got %d", len(r.run.Steps))
			}
			if len(f.ctlCreated) != 1 || !strings.HasPrefix(f.ctlCreated[0], "region-b iso-ctl-") || len(f.ctlPods) != 1 ||
				strings.Join(f.ctlDialed, ",") != "region-b "+mesh {
				t.Errorf("want one positive control, in region-b only, on the host network, dialing %s once: %v %v %v", mesh, f.ctlCreated, f.ctlPods, f.ctlDialed)
			}
			dialed := strings.Join(f.dialed, ",") + ","
			if strings.Count(dialed, "A "+mesh+",") != 2 || strings.Count(dialed, "A 10.43.0.22:8080,") != 2 || len(f.dialed) != 4+24+2+22 {
				t.Errorf("want the Pod of A to dial catalyst-api in each region in ISO-3 and ISO-6, and 52 dials: %v", f.dialed)
			}
			listeners := map[string]string{}
			for _, s := range r.run.Steps {
				if s.Evidence["expect"] == "deny" && s.Evidence["observed"] != DialTimedOut {
					t.Errorf("a deny row passed without a timeout: %s %v", s.Name, s.Evidence)
				}
				if l, ok := s.Evidence["listeners"].(map[string]string); ok {
					listeners[s.Evidence["region"].(string)] = l["catalyst-system/catalyst-api"]
				}
			}
			if listeners["region-a"] != "a ready endpoint in the region" || !strings.Contains(listeners["region-b"], "positive control") ||
				!strings.Contains(listeners["region-b"], "dialed "+mesh+": control connected, target connected") {
				t.Errorf("the targets record another listener of catalyst-api: %v", listeners)
			}
		})
	}
}

// TestOrgIsolationCheck_Sources: the organization controller writes what the
// check reads: the server of the kubeconfig, the additional Secret
// tenant-<slug>-kubeconfig that the vCluster writes beside its default
// vc-vcluster (the source of the flux-system mirror), the two mirrors, and the
// label of synced Pods that both objects of the fence select.
func TestOrgIsolationCheck_Sources(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Clean(filepath.Join("..", "..", "..", "..", "..", "core", "controllers", "organization", "internal", "gitops", name)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	manifests, mirrors := read("manifests.go"), read("region_local.go")
	for _, want := range []struct {
		text, line string
		n          int
	}{
		{manifests, "      server: https://" + isoVClusterName + ".{{ .Slug }}:443", 1},
		{manifests, "        - name: tenant-{{ .Slug }}-kubeconfig", 1},
		{manifests, "      - key: " + isoManagedBy, 2},
		{mirrors, "  name: tenant-{{ .Slug }}-kubeconfig", 2},
		{mirrors, "  namespace: flux-system", 1},
	} {
		if got := strings.Count(want.text, "\n"+want.line+"\n"); got != want.n {
			t.Errorf("the controller holds the line %q %d times, want %d", want.line, got, want.n)
		}
	}
}

// TestPortForward: the forward returns the local address that kubectl prints
// and ends when stopped; a kubectl that ends first is an error with its output.
func TestPortForward(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	hostFile(t, dir, "forwards", 0o700, []byte("#!/bin/sh\necho \"$@\" > "+argsFile+"\necho 'Forwarding from 127.0.0.1:43123 -> 8443'\necho 'Forwarding from [::1]:43123 -> 8443'\nexec sleep 60\n"))
	hostFile(t, dir, "fails", 0o700, []byte("#!/bin/sh\necho 'error: services \"vcluster\" not found' >&2\nexit 1\n"))
	region := kube.Region{ID: "region-a", Kubeconfig: "k.yaml", Kubectl: filepath.Join(dir, "forwards")}
	addr, stop, err := portForward(context.Background(), region, "iso-a-x", isoVClusterName, 443)
	if err != nil || addr != "127.0.0.1:43123" {
		t.Fatalf("want 127.0.0.1:43123, got %q, %v", addr, err)
	}
	start := time.Now()
	stop()
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("stop took %s", d)
	}
	if b, _ := os.ReadFile(filepath.Clean(argsFile)); strings.TrimSpace(string(b)) != "--kubeconfig k.yaml -n iso-a-x port-forward --address 127.0.0.1 service/vcluster :443" {
		t.Errorf("kubectl got %q", b)
	}
	region.Kubectl = filepath.Join(dir, "fails")
	if _, _, err := portForward(context.Background(), region, "iso-a-x", isoVClusterName, 443); err == nil ||
		!strings.Contains(err.Error(), `ended before it printed its local address: error: services "vcluster" not found`) {
		t.Errorf("a kubectl that ends first: %v", err)
	}
}

// TestPortForwardStartsAgain: a kubectl port-forward that ends before stop
// (on hw307 each forward ended after a dial ran through it) starts again on
// the same local address, until stop.
func TestPortForwardStartsAgain(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	// The first kubectl prints its address and ends as kubectl does after an
	// error of one forwarded connection; every later one keeps running.
	hostFile(t, dir, "forwards", 0o700, []byte("#!/bin/sh\necho \"$@\" >> "+calls+"\n"+
		"echo 'Forwarding from 127.0.0.1:43123 -> 8443'\n"+
		"if [ \"$(wc -l < "+calls+")\" -eq 1 ]; then echo 'error: lost connection to pod' >&2; exit 1; fi\n"+
		"exec sleep 60\n"))
	region := kube.Region{ID: "region-a", Kubeconfig: "k.yaml", Kubectl: filepath.Join(dir, "forwards")}
	addr, stop, err := portForward(context.Background(), region, "iso-a-x", isoVClusterName, 443)
	if err != nil || addr != "127.0.0.1:43123" {
		t.Fatalf("want 127.0.0.1:43123, got %q, %v", addr, err)
	}
	var lines []string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		b, _ := os.ReadFile(filepath.Clean(calls))
		if lines = strings.Split(strings.TrimSpace(string(b)), "\n"); len(lines) >= 2 {
			break
		}
	}
	start := time.Now()
	stop()
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("stop took %s", d)
	}
	if len(lines) != 2 {
		t.Fatalf("want the forward started twice, got %d: %q", len(lines), lines)
	}
	if want := "--kubeconfig k.yaml -n iso-a-x port-forward --address 127.0.0.1 service/vcluster 43123:443"; lines[1] != want {
		t.Errorf("the second kubectl got %q, want %q (the same local address)", lines[1], want)
	}
	time.Sleep(300 * time.Millisecond)
	if b, _ := os.ReadFile(filepath.Clean(calls)); strings.Count(string(b), "\n") != 2 {
		t.Errorf("the forward started again after stop: %q", b)
	}
}

// TestVCKubectlSendsARefusedCallAgain: a call that the port forward refused
// is sent again with the same input once the forward listens; any other
// error is returned at once, and a forward that keeps refusing ends the
// retries.
func TestVCKubectlSendsARefusedCallAgain(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	local := ln.Addr().String()
	refused := fmt.Errorf("exit status 1: The connection to the server %s was refused - did you specify the right host or port?", local)
	for name, tc := range map[string]struct {
		answers []error
		calls   int
		err     string
	}{
		"refused once, then served": {[]error{refused, nil}, 2, ""},
		"another error":             {[]error{errors.New("exit status 1: pods \"x\" not found")}, 1, "not found"},
		"refused every time":        {[]error{refused, refused, refused, refused, refused}, isoForwardRetries + 1, "was refused"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdins []string
			n := 0
			kubectl := func(_ kube.Region, _ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
				b, _ := io.ReadAll(stdin)
				stdins = append(stdins, string(b))
				if args[1] != "https://"+local || args[3] != "vcluster.iso-a-x" {
					t.Errorf("not the server of the forward: %v", args[:4])
				}
				err := tc.answers[n]
				n++
				return []byte("out"), err
			}
			j := &isoJourney{c: orgIsolationCheck{kubectl: kubectl}}
			v := &isoVC{server: "https://" + local, sni: "vcluster.iso-a-x"}
			_, err := j.vcKubectl(v)(kube.Region{}, context.Background(), strings.NewReader("the dial script"), "exec", "-i", "p")
			if n != tc.calls {
				t.Errorf("want %d calls, got %d", tc.calls, n)
			}
			for _, s := range stdins {
				if s != "the dial script" {
					t.Errorf("a call got the input %q", s)
				}
			}
			if (tc.err == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.err)) {
				t.Errorf("want error %q, got %v", tc.err, err)
			}
		})
	}
}
