// Command pull-credentials holds that every Pod the kit runs from a
// first-party image carries the credential of the registry that serves it
// after the cutover.
//
// A first-party image is ghcr.io/openova-io/<path>; a chart pivoted by
// global.imageRegistry renders it as <registry>/openova-io/<path>. After the
// cutover every node pulls it from the Sovereign's own registry, project
// openova-io (step 03 copies it there, step 07 points the pod at
// registry.<fqdn>/openova-io/<path>), and that project is private: step 02
// creates it public:false (platform/self-sovereign-cutover/chart/templates/
// 02-harbor-projects-job.yaml). The node holds no login for that registry (the
// `configs` of the node registry file name only the origin hosts,
// infra/providers/huawei/main.tf registry_mirror_yaml_huawei), so the only
// credential is the pod's own: the Secret ghcr-pull, which step 06 extends
// with the login of registry.<fqdn> and Reflector mirrors into every
// namespace (clusters/_template/bootstrap-secrets/ghcr-pull.yaml). A pod that
// names no pull secret pulls anonymously, the registry refuses it, and the
// pod stays in ImagePullBackOff. Before the cutover the same pod runs, since
// the node pulls through the origin with its own login: the defect shows only
// after the cutover. On 27 Nov 2026, after the delivery of
// rc/20261127T093133Z-dbae275, guacamole/access-controller (ACC-103, pull
// request 8858) was in ImagePullBackOff on
// registry.<sovereign-fqdn>/openova-io/openova/access-controller@sha256:...
// while every other first-party controller of the kit names ghcr-pull.
//
// Input: a render of the kit, written by
//
//	KIT_RENDER_DIR=<dir> bash scripts/check-freshprov-image-registry.sh --list-images
//
// one file <n>.<chart>.<namespace>.jsonl a release, one JSON object a line.
//
// For every Pod, Deployment, StatefulSet, DaemonSet, ReplicaSet, Job and
// CronJob of the render the guard reads the Pod template, its images
// (containers, initContainers, ephemeralContainers) and its pull secrets. A
// template that runs a first-party image must name a pull secret itself, or
// run as a ServiceAccount that the render gives one (imagePullSecrets of the
// ServiceAccount of spec.serviceAccountName, default `default`, in the Pod's
// namespace). Otherwise it is a finding (exit 1) naming the chart, the object
// and the image. It stops (exit 2) when the render cannot be read or holds no
// Pod template with a first-party image: a render that judged nothing is not
// a pass.
//
// Not covered, by design: a Pod that a controller or a script creates at run
// time (no chart of the kit renders it); whether the named Secret holds the
// registry's login (the cutover's step 06 and its tests own that); a
// ServiceAccount that something outside the render patches.
//
// Usage, from tools/guards:
//
//	go run ./cmd/pull-credentials -render <dir>
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// The first path segment of a first-party image: the project openova-io of
// the Sovereign's registry after the cutover.
const firstPartyProject = "openova-io"

type ref struct {
	Name string `json:"name"`
}

type container struct {
	Image string `json:"image"`
}

type podSpec struct {
	ServiceAccountName  string      `json:"serviceAccountName"`
	ImagePullSecrets    []ref       `json:"imagePullSecrets"`
	Containers          []container `json:"containers"`
	InitContainers      []container `json:"initContainers"`
	EphemeralContainers []container `json:"ephemeralContainers"`
}

type template struct {
	Spec podSpec `json:"spec"`
}

type object struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	ImagePullSecrets []ref `json:"imagePullSecrets"`
	Spec             struct {
		podSpec
		Template    template `json:"template"`
		JobTemplate struct {
			Spec struct {
				Template template `json:"template"`
			} `json:"spec"`
		} `json:"jobTemplate"`
	} `json:"spec"`
}

// pod is one Pod template of the render.
type pod struct {
	chart, kind, namespace, name string
	spec                         podSpec
}

func (p pod) String() string { return fmt.Sprintf("%s %s %s/%s", p.chart, p.kind, p.namespace, p.name) }

type render struct {
	pods []pod
	// accounts: "namespace/name" of each ServiceAccount the render gives a pull secret.
	accounts map[string]bool
}

var templateKinds = map[string]bool{"Deployment": true, "StatefulSet": true, "DaemonSet": true, "ReplicaSet": true, "Job": true}

// readRender reads every <n>.<chart>.<namespace>.jsonl of dir.
func readRender(dir string) (*render, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s holds no release render (*.jsonl)", dir)
	}
	sort.Strings(files)
	r := &render{accounts: map[string]bool{}}
	for _, f := range files {
		parts := strings.Split(strings.TrimSuffix(filepath.Base(f), ".jsonl"), ".")
		if len(parts) < 3 {
			return nil, fmt.Errorf("%s: the name is not <n>.<chart>.<namespace>.jsonl", f)
		}
		if err := readFile(f, strings.Join(parts[1:len(parts)-1], "."), parts[len(parts)-1], r); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func readFile(f, chart, releaseNS string, r *render) error {
	fh, err := os.Open(f) // #nosec G304 -- the path is the guard's own input
	if err != nil {
		return err
	}
	defer func() { _ = fh.Close() }()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || line == "null" {
			continue
		}
		var o object
		if err := json.Unmarshal([]byte(line), &o); err != nil {
			return fmt.Errorf("%s:%d: not a JSON object: %v", f, n, err)
		}
		ns := o.Metadata.Namespace
		if ns == "" {
			ns = releaseNS
		}
		at := pod{chart: chart, kind: o.Kind, namespace: ns, name: o.Metadata.Name}
		switch {
		case o.Kind == "ServiceAccount":
			if named(o.ImagePullSecrets) {
				r.accounts[ns+"/"+o.Metadata.Name] = true
			}
		case o.Kind == "Pod":
			at.spec = o.Spec.podSpec
			r.pods = append(r.pods, at)
		case o.Kind == "CronJob":
			at.spec = o.Spec.JobTemplate.Spec.Template.Spec
			r.pods = append(r.pods, at)
		case templateKinds[o.Kind]:
			at.spec = o.Spec.Template.Spec
			r.pods = append(r.pods, at)
		}
	}
	return sc.Err()
}

// named reports whether a list of pull secrets names at least one Secret.
func named(list []ref) bool {
	for _, s := range list {
		if strings.TrimSpace(s.Name) != "" {
			return true
		}
	}
	return false
}

// The upstream registries whose images the cutover copies into a PUBLIC
// mirror project (offlineMirror.hostProjects of the cutover chart): an
// openova-io path on one of them is not the private project.
var publicMirrorHosts = map[string]bool{
	"docker.io": true, "registry-1.docker.io": true, "index.docker.io": true, "quay.io": true,
	"gcr.io": true, "k8s.gcr.io": true, "registry.k8s.io": true, "mirror.gcr.io": true,
	"public.ecr.aws": true, "cgr.dev": true,
}

// firstParty reports whether an image is pulled from the project openova-io:
// ghcr.io/openova-io/<path>, the origin's or the Sovereign's registry
// <registry>/openova-io/<path> (a pivot by global.imageRegistry renders that).
func firstParty(image string) bool {
	host, path, ok := strings.Cut(image, "/")
	isHost := strings.ContainsAny(host, ".:") || host == "localhost"
	if !ok || !isHost || publicMirrorHosts[host] {
		return false
	}
	return strings.HasPrefix(path, firstPartyProject+"/")
}

// images of a Pod template that are first-party, in order, once each.
func firstPartyImages(s podSpec) []string {
	var out []string
	seen := map[string]bool{}
	for _, list := range [][]container{s.InitContainers, s.Containers, s.EphemeralContainers} {
		for _, c := range list {
			if firstParty(c.Image) && !seen[c.Image] {
				seen[c.Image] = true
				out = append(out, c.Image)
			}
		}
	}
	return out
}

// judge returns a finding per Pod template that runs a first-party image
// without a pull secret (once, though each value set of the kit renders it),
// and how many such templates it judged.
func judge(r *render) (findings []string, judged int) {
	seen := map[string]bool{}
	for _, p := range r.pods {
		imgs := firstPartyImages(p.spec)
		if len(imgs) == 0 {
			continue
		}
		judged++
		if named(p.spec.ImagePullSecrets) {
			continue
		}
		sa := p.spec.ServiceAccountName
		if sa == "" {
			sa = "default"
		}
		if r.accounts[p.namespace+"/"+sa] {
			continue
		}
		f := fmt.Sprintf("%s runs %s and names no imagePullSecrets, nor does its ServiceAccount %s/%s: after the cutover the Sovereign's registry serves the private project %s only to a pull with a login (imagePullSecrets: [{name: ghcr-pull}])",
			p, strings.Join(imgs, ", "), p.namespace, sa, firstPartyProject)
		if !seen[f] {
			seen[f] = true
			findings = append(findings, f)
		}
	}
	sort.Strings(findings)
	return findings, judged
}

var errUsage = errors.New("usage: pull-credentials -render <dir>")

func run(args []string, out, errw io.Writer) int {
	fl := flag.NewFlagSet("pull-credentials", flag.ContinueOnError)
	fl.SetOutput(errw)
	dir := fl.String("render", "", "render of the kit (KIT_RENDER_DIR of scripts/check-freshprov-image-registry.sh --list-images)")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	if *dir == "" || fl.NArg() != 0 {
		say(errw, "%v\n", errUsage)
		return 2
	}
	r, err := readRender(*dir)
	if err != nil {
		say(errw, "pull-credentials: cannot read the render: %v\n", err)
		return 2
	}
	findings, judged := judge(r)
	if judged == 0 {
		say(errw, "pull-credentials: the render %s holds no Pod template with a first-party image: nothing to judge\n", *dir)
		return 2
	}
	for _, f := range findings {
		say(out, "FAIL %s\n", f)
	}
	if len(findings) > 0 {
		say(out, "pull-credentials: %d Pod template(s) with a first-party image pull it without a login (%d judged over every value set); after the cutover each stays in ImagePullBackOff\n", len(findings), judged)
		return 1
	}
	say(out, "pull-credentials: every one of the %d Pod templates with a first-party image names a pull secret, itself or through its ServiceAccount\n", judged)
	return 0
}

func say(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
