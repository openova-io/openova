package gitops

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The Organization's vCluster kubeconfig must be held in
// `<slug>/tenant-<slug>-kubeconfig` and `flux-system/tenant-<slug>-kubeconfig`
// as soon as the vCluster writes it, in whatever order Flux creates the
// boundary tree's Secrets and the vCluster writes its own.
//
// Measured on 2026-11-21 (release candidate rc/20261121T000707Z-9a080ac, check
// NET-G07 step 7): kustomize-controller created the two copies in the same
// second as the source request, the vCluster wrote its kubeconfig about 40 s
// later, and bp-reflector logged "Could not update ... Source .../vc-vcluster
// could not be found" and never retried. The copies were filled only when its
// Secret watch session ended and it listed again, 51 minutes later.
//
// No reflector runs in a unit test, so kcReflector below is a port of the
// decision logic of emberstack/kubernetes-reflector v7.1.288 (the image of
// platform/reflector, chart 7.1.288), file
// src/ES.Kubernetes.Reflector/Core/Mirroring/ResourceMirror.cs: Handle,
// HandleUpsert, AutoReflectionForSource, ResourceReflect, TryResourceGet, and
// the namespace matching of Extensions/ReflectionStatusExtensions.cs. Each
// branch names the upstream lines it ports. The vCluster's write is ported
// from loft-sh/vcluster v0.33.4 pkg/setup WriteKubeConfigToSecret and
// pkg/util/kubeconfig WriteKubeConfig (default `vc-<release>` first, then each
// exportKubeConfig.additionalSecrets entry, CreateOrPatch of the data with the
// annotations kept). TestKubeconfigReflectorModel_ReproducesTheMeasuredWait
// checks the port against the measurement before the other tests rely on it.

const (
	rfPrefix       = "reflector.v1.k8s.emberstack.com/"
	rfAllowed      = rfPrefix + "reflection-allowed"
	rfAllowedNS    = rfPrefix + "reflection-allowed-namespaces"
	rfAutoEnabled  = rfPrefix + "reflection-auto-enabled"
	rfAutoNS       = rfPrefix + "reflection-auto-namespaces"
	rfReflects     = rfPrefix + "reflects"
	rfAutoReflects = rfPrefix + "auto-reflects"
	rfReflectedVer = rfPrefix + "reflected-version"
)

// ── the rendered Organization ───────────────────────────────────────────────

type kcStub struct {
	Metadata struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Kind       string            `json:"kind"`
	Data       map[string]string `json:"data"`
	StringData map[string]string `json:"stringData"`
}

func (s kcStub) key() string { return s.Metadata.Namespace + "/" + s.Metadata.Name }

type kcHelmRelease struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Values struct {
			ExportKubeConfig struct {
				AdditionalSecrets []struct {
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
				} `json:"additionalSecrets"`
			} `json:"exportKubeConfig"`
		} `json:"values"`
	} `json:"spec"`
}

var kcDocSep = regexp.MustCompile(`(?m)^---\s*$`)

func kcRender(t *testing.T, slug string) map[string][]byte {
	t.Helper()
	out, err := Render(Inputs{Slug: slug, DisplayName: slug, Tier: "org", PlanSlug: "s",
		SovereignFQDN: "x.example", HostCluster: "hz", VClusterChartVersion: "0.33.4"})
	if err != nil {
		t.Fatalf("Render(%s): %v", slug, err)
	}
	return out
}

// kcStubs decodes the Secrets of a boundary-tree file, in file order.
func kcStubs(t *testing.T, raw string) []kcStub {
	t.Helper()
	var stubs []kcStub
	for _, doc := range kcDocSep.Split(raw, -1) {
		var s kcStub
		if err := yaml.Unmarshal([]byte(doc), &s); err != nil {
			t.Fatalf("decode %q: %v", doc, err)
		}
		if s.Metadata.Name == "" {
			continue
		}
		if s.Kind != "Secret" {
			t.Fatalf("%s is a %s, want Secret", s.key(), s.Kind)
		}
		stubs = append(stubs, s)
	}
	return stubs
}

// kcExports returns the Secrets the rendered vcluster HelmRelease makes the
// vCluster write, in the order it writes them.
func kcExports(t *testing.T, out map[string][]byte, slug string) []string {
	t.Helper()
	exports := []string{slug + "/" + VClusterDefaultKubeconfigSecret}
	found := false
	for _, doc := range kcDocSep.Split(string(out["vcluster/vcluster.yaml"]), -1) {
		var hr kcHelmRelease
		if err := yaml.Unmarshal([]byte(doc), &hr); err != nil {
			t.Fatalf("decode vcluster.yaml: %v", err)
		}
		if hr.Kind != "HelmRelease" || hr.Metadata.Name != "vcluster" {
			continue
		}
		found = true
		for _, s := range hr.Spec.Values.ExportKubeConfig.AdditionalSecrets {
			ns, name := s.Namespace, s.Name
			if ns == "" {
				ns = slug
			}
			if name == "" {
				name = VClusterDefaultKubeconfigSecret
			}
			exports = append(exports, ns+"/"+name)
		}
	}
	if !found {
		t.Fatal("vcluster/vcluster.yaml renders no HelmRelease vcluster")
	}
	return exports
}

// ── the cluster: Secrets, versions and the watch stream ─────────────────────

type kcObj struct {
	ns, name    string
	annotations map[string]string
	data        map[string]string
	version     int
}

func (o *kcObj) key() string { return o.ns + "/" + o.name }

func (o *kcObj) clone() *kcObj {
	c := *o
	c.annotations = kcCopy(o.annotations)
	c.data = kcCopy(o.data)
	return &c
}

func kcCopy(m map[string]string) map[string]string {
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

type kcEvent struct {
	typ string // Added, Modified, Deleted
	obj *kcObj // the object as it was when the event was emitted
}

type kcCluster struct {
	namespaces []string
	objs       map[string]*kcObj
	rv         int
	queue      []kcEvent
}

func newKCCluster(namespaces ...string) *kcCluster {
	ns := append([]string{"flux-system"}, namespaces...)
	sort.Strings(ns)
	return &kcCluster{namespaces: ns, objs: map[string]*kcObj{}}
}

func (c *kcCluster) write(typ string, o *kcObj) {
	c.rv++
	o.version = c.rv
	if typ == "Added" {
		c.objs[o.key()] = o
	}
	c.queue = append(c.queue, kcEvent{typ, o.clone()})
}

func (c *kcCluster) delete(key string) {
	if o, ok := c.objs[key]; ok {
		delete(c.objs, key)
		c.queue = append(c.queue, kcEvent{"Deleted", o.clone()})
	}
}

func (c *kcCluster) config(key string) string {
	if o, ok := c.objs[key]; ok {
		return o.data["config"]
	}
	return ""
}

// fluxApply is kustomize-controller's server-side apply of a metadata-only
// stub: create it, or set its annotations; it never touches data. An apply
// that changes nothing emits no event.
func (c *kcCluster) fluxApply(s kcStub) {
	o, ok := c.objs[s.key()]
	if !ok {
		c.write("Added", &kcObj{ns: s.Metadata.Namespace, name: s.Metadata.Name,
			annotations: kcCopy(s.Metadata.Annotations), data: map[string]string{}})
		return
	}
	changed := false
	for k, v := range s.Metadata.Annotations {
		if o.annotations[k] != v {
			o.annotations[k] = v
			changed = true
		}
	}
	if changed {
		c.write("Modified", o)
	}
}

// vclusterWrite is the vCluster writing its kubeconfig into each export, in
// order: CreateOrPatch of the data, annotations kept.
func (c *kcCluster) vclusterWrite(exports []string, kubeconfig string) {
	for _, key := range exports {
		o, ok := c.objs[key]
		if !ok {
			ns, name, _ := strings.Cut(key, "/")
			c.write("Added", &kcObj{ns: ns, name: name, annotations: map[string]string{},
				data: map[string]string{"config": kubeconfig}})
			continue
		}
		if o.data["config"] == kubeconfig {
			continue
		}
		o.data = map[string]string{"config": kubeconfig}
		c.write("Modified", o)
	}
}

// ── the reflector (ResourceMirror.cs, v7.1.288) ─────────────────────────────

type kcProps struct {
	allowed, autoEnabled, isAuto bool
	allowedNS, autoNS            string
	reflects                     string // "" when not a reflection
	version, reflectedVersion    string
}

func kcBool(v string) bool { b, err := strconv.ParseBool(v); return err == nil && b }

// kcPropsOf ports ReflectorExtensions.GetReflectionProperties.
func kcPropsOf(o *kcObj) kcProps {
	a := o.annotations
	p := kcProps{
		allowed: kcBool(a[rfAllowed]), allowedNS: a[rfAllowedNS],
		autoEnabled: kcBool(a[rfAutoEnabled]), autoNS: a[rfAutoNS],
		isAuto: kcBool(a[rfAutoReflects]), reflectedVersion: strings.TrimSpace(a[rfReflectedVer]),
		version: strconv.Itoa(o.version),
	}
	if r := strings.TrimSpace(a[rfReflects]); r != "" {
		ns, name, ok := strings.Cut(r, "/")
		if !ok || ns == "" { // a bare name is in the reflection's own namespace
			ns, name = o.ns, strings.TrimPrefix(r, "/")
		}
		p.reflects = ns + "/" + name
	}
	return p
}

// kcPatternListMatch ports ReflectionStatusExtensions.PatternListMatch: an
// empty list matches every namespace; otherwise one comma-separated regular
// expression must match the whole name.
func kcPatternListMatch(list, value string) bool {
	if list == "" {
		return true
	}
	for _, p := range strings.Split(list, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if re, err := regexp.Compile("^(?:" + p + ")$"); err == nil && re.MatchString(value) {
			return true
		}
	}
	return false
}

func (p kcProps) canReflect(ns string) bool {
	return p.allowed && kcPatternListMatch(p.allowedNS, ns)
}

func (p kcProps) canAuto(ns string) bool {
	return p.canReflect(ns) && p.autoEnabled && kcPatternListMatch(p.autoNS, ns)
}

type kcReflector struct {
	c           *kcCluster
	props       map[string]kcProps  // _propertiesCache
	direct      map[string][]string // _directReflectionCache
	auto        map[string][]string // _autoReflectionCache
	autoSources map[string]bool     // _autoSources
	notFound    map[string]bool     // _notFoundCache
	log         []string
}

func newKCReflector(c *kcCluster) *kcReflector {
	r := &kcReflector{c: c, direct: map[string][]string{}}
	r.clearSessionCaches()
	return r
}

// clearSessionCaches ports Handle(WatcherClosed) (lines 44-60): the direct
// cache survives a session, the other four do not.
func (r *kcReflector) clearSessionCaches() {
	r.props = map[string]kcProps{}
	r.auto = map[string][]string{}
	r.autoSources = map[string]bool{}
	r.notFound = map[string]bool{}
}

func (r *kcReflector) logf(format string, a ...any) { r.log = append(r.log, fmt.Sprintf(format, a...)) }

// drain handles every queued watch event in order, including the events of
// the reflector's own writes: the watch session never ends here.
func (r *kcReflector) drain() {
	for len(r.c.queue) > 0 {
		ev := r.c.queue[0]
		r.c.queue = r.c.queue[1:]
		r.handle(ev)
	}
}

// relist is the end of a watch session: the caches are cleared and the new
// session's list delivers every Secret as Added, in key order.
func (r *kcReflector) relist() {
	r.clearSessionCaches()
	keys := make([]string, 0, len(r.c.objs))
	for k := range r.c.objs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r.c.queue = append(r.c.queue, kcEvent{"Added", r.c.objs[k].clone()})
	}
	r.drain()
}

func kcWithout(list []string, item string) []string {
	for i, v := range list {
		if v == item {
			return append(append([]string{}, list[:i]...), list[i+1:]...)
		}
	}
	return list
}

func kcNS(key string) string { ns, _, _ := strings.Cut(key, "/"); return ns }

// get ports TryResourceGet (lines 501-516).
func (r *kcReflector) get(key string) *kcObj {
	o, ok := r.c.objs[key]
	if !ok {
		r.notFound[key] = true
		return nil
	}
	delete(r.notFound, key)
	return o.clone()
}

// handle ports Handle(WatcherEvent) (lines 66-123).
func (r *kcReflector) handle(ev kcEvent) {
	key := ev.obj.key()
	delete(r.notFound, key)
	switch ev.typ {
	case "Added", "Modified":
		r.upsert(ev.obj)
	case "Deleted":
		delete(r.props, key)
		p := kcPropsOf(ev.obj)
		if p.reflects == "" {
			if p.allowed && p.autoEnabled {
				for _, t := range r.auto[key] {
					r.c.delete(t)
				}
			}
			delete(r.autoSources, key)
			delete(r.direct, key)
			delete(r.auto, key)
			return
		}
		for s := range r.direct {
			r.direct[s] = kcWithout(r.direct[s], key)
		}
		for s := range r.auto {
			r.auto[s] = kcWithout(r.auto[s], key)
		}
	}
}

// upsert ports HandleUpsert (lines 153-325).
func (r *kcReflector) upsert(o *kcObj) {
	key := o.key()
	p := kcPropsOf(o)
	r.props[key] = p

	if p.reflects == "" { // a source (lines 162-233)
		if list, ok := r.direct[key]; ok {
			for _, t := range list {
				if !p.canReflect(kcNS(t)) {
					r.direct[key] = kcWithout(r.direct[key], t)
				}
			}
		}
		if list, ok := r.auto[key]; ok {
			for _, t := range list {
				if p.canAuto(kcNS(t)) {
					continue
				}
				r.auto[key] = kcWithout(r.auto[key], t)
				r.c.delete(t)
			}
		}
		isAutoSource := p.allowed && p.autoEnabled
		r.autoSources[key] = isAutoSource
		if !isAutoSource {
			delete(r.auto, key)
		}
		if !p.allowed {
			delete(r.direct, key)
			return
		}
		if list, ok := r.direct[key]; ok {
			for _, t := range list {
				tp, ok := r.props[t]
				if !ok {
					r.direct[key] = kcWithout(r.direct[key], t)
					continue
				}
				if tp.reflectedVersion == p.version {
					continue
				}
				r.reflect(key, t, o, nil, false)
			}
		}
		if isAutoSource {
			r.autoForSource(key, o)
		}
		return
	}

	src := p.reflects
	if !p.isAuto { // a direct reflection (lines 236-279)
		sp, ok := r.props[src]
		if !ok {
			so := r.get(src)
			if so == nil {
				r.logf("Could not update %s - Source %s could not be found.", key, src)
				return // returns BEFORE the binding below is recorded
			}
			sp = kcPropsOf(so)
		}
		r.props[src] = sp
		r.direct[src] = append(r.direct[src], key)
		if !sp.canReflect(o.ns) {
			r.logf("Could not update %s - Source %s does not permit it.", key, src)
			r.direct[src] = kcWithout(r.direct[src], key)
			return
		}
		if sp.version == p.reflectedVersion {
			return
		}
		r.reflect(src, key, nil, o, false)
		return
	}

	// an auto-reflection (lines 282-324)
	if r.notFound[src] {
		r.c.delete(key)
		return
	}
	sp, ok := r.props[src]
	if !ok {
		so := r.get(src)
		if so == nil {
			r.c.delete(key)
			return
		}
		sp = kcPropsOf(so)
	}
	r.props[src] = sp
	if !sp.canAuto(o.ns) {
		r.c.delete(key)
	}
}

// autoForSource ports AutoReflectionForSource (lines 328-394).
func (r *kcReflector) autoForSource(key string, o *kcObj) {
	p := r.props[key]
	srcNS := kcNS(key)
	var matches []*kcObj // every Secret with the source's name, any namespace
	for _, m := range r.c.objs {
		if m.name == o.name {
			matches = append(matches, m.clone())
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].key() < matches[j].key() })
	byNS := map[string]*kcObj{}
	for _, m := range matches {
		r.props[m.key()] = kcPropsOf(m)
		byNS[m.ns] = m
	}
	toDelete := map[string]bool{}
	for _, m := range matches {
		if m.ns != srcNS && !p.canAuto(m.ns) && kcPropsOf(m).reflects == key {
			toDelete[m.key()] = true
			r.c.delete(m.key())
		}
	}
	var toCreate, toUpdate, toSkip []string
	for _, ns := range r.c.namespaces {
		if ns != srcNS && byNS[ns] == nil && p.canAuto(ns) {
			toCreate = append(toCreate, ns+"/"+o.name)
		}
	}
	for _, m := range matches {
		mp := kcPropsOf(m)
		if m.ns == srcNS || toDelete[m.key()] || mp.reflects != key {
			continue
		}
		if mp.reflectedVersion != p.version {
			toUpdate = append(toUpdate, m.key())
		} else {
			toSkip = append(toSkip, m.key())
		}
	}
	r.auto[key] = append(append(append([]string{}, toCreate...), toSkip...), toUpdate...)
	for _, t := range toCreate {
		r.reflect(key, t, o, nil, true)
	}
	for _, t := range toUpdate {
		r.reflect(key, t, o, byNS[kcNS(t)], true)
	}
}

// reflect ports ResourceReflect (lines 397-489): create the copy, or patch
// its annotations and data unless it already holds this source version.
func (r *kcReflector) reflect(src, tgt string, so, to *kcObj, auto bool) {
	if src == tgt {
		return
	}
	if so == nil {
		if so = r.get(src); so == nil {
			r.logf("Could not update %s - Source %s could not be found.", tgt, src)
			return
		}
	}
	autoText := "False" // C# bool.ToString()
	if auto {
		autoText = "True"
	}
	ann := map[string]string{
		rfAutoReflects: autoText,
		rfReflects:     src,
		rfReflectedVer: strconv.Itoa(so.version),
	}
	if to == nil {
		live, ok := r.c.objs[tgt]
		if !ok {
			ns, name, _ := strings.Cut(tgt, "/")
			r.c.write("Added", &kcObj{ns: ns, name: name, annotations: ann, data: kcCopy(so.data)})
			return
		}
		to = live.clone() // 409 Conflict: fall back to a patch
	}
	if kcPropsOf(to).reflectedVersion == strconv.Itoa(so.version) {
		return
	}
	live, ok := r.c.objs[tgt]
	if !ok {
		r.logf("Could not reflect %s to %s due to exception.", src, tgt)
		return
	}
	merged := kcCopy(to.annotations)
	for k, v := range ann {
		merged[k] = v
	}
	live.annotations = merged
	live.data = kcCopy(so.data)
	r.c.write("Modified", live)
}

// ── scenarios ───────────────────────────────────────────────────────────────

// kcPermutations returns every order of 0..n-1.
func kcPermutations(n int) [][]int {
	if n == 0 {
		return [][]int{{}}
	}
	var out [][]int
	for _, p := range kcPermutations(n - 1) {
		for i := 0; i <= len(p); i++ {
			q := append(append(append([]int{}, p[:i]...), n-1), p[i:]...)
			out = append(out, q)
		}
	}
	return out
}

// kcOrg is one Organization's actions: Flux applies each stub, and the
// vCluster writes its exports (the last action index).
type kcOrg struct {
	slug, kubeconfig string
	stubs            []kcStub
	exports          []string
}

func (o kcOrg) apply(c *kcCluster, action int) {
	if action == len(o.stubs) {
		c.vclusterWrite(o.exports, o.kubeconfig)
		return
	}
	c.fluxApply(o.stubs[action])
}

func (o kcOrg) describe(order []int) string {
	parts := make([]string, len(order))
	for i, a := range order {
		if a == len(o.stubs) {
			parts[i] = "the vCluster writes " + strings.Join(o.exports, " and ")
		} else {
			parts[i] = "Flux creates " + o.stubs[a].key()
		}
	}
	return strings.Join(parts, ", then ")
}

func kcOrgFromRender(t *testing.T, slug string) kcOrg {
	t.Helper()
	out := kcRender(t, slug)
	return kcOrg{slug: slug, kubeconfig: "kubeconfig of the vCluster of " + slug,
		stubs: kcStubs(t, string(out["vcluster/"+kubeconfigMirrorDoc])), exports: kcExports(t, out, slug)}
}

func kcCopies(slug string) []string {
	return []string{slug + "/" + KubeconfigSecretName(slug), "flux-system/" + KubeconfigSecretName(slug)}
}

// The defect, in the order measured live and in every other order: after the
// vCluster has written its kubeconfig, both copies hold it, with no end of the
// reflector's watch session. Bound: the copy in flux-system is written while
// the reflector handles the watch event of the source's own write (one event,
// one LIST of that name and one PATCH; the live reflector does that within
// the second, e.g. "Auto-reflected flux-system/openova-org-tenants-git-auth"
// at 04:08:48 on the gate run); the copy in `<slug>` is the vCluster's own
// write. Fails on main: the order of the gate run leaves both empty.
func TestKubeconfigCopies_AreFilledWhenTheSourceAppearsAfterThem(t *testing.T) {
	org := kcOrgFromRender(t, "iso-a-040755fec9")
	if len(org.stubs) == 0 {
		t.Fatal("the boundary tree renders no kubeconfig Secret")
	}
	failed := 0
	for _, order := range kcPermutations(len(org.stubs) + 1) {
		c := newKCCluster(org.slug, "iso-b-040755fec9")
		r := newKCReflector(c)
		for _, a := range order {
			org.apply(c, a)
			r.drain()
		}
		for _, k := range kcCopies(org.slug) {
			if got := c.config(k); got != org.kubeconfig {
				failed++
				t.Errorf("%s holds %q after: %s.\nreflector log: %s", k, got, org.describe(order),
					strings.Join(r.log, "; "))
			}
		}
	}
	if failed > 0 {
		t.Logf("%d copies empty without a re-list: the reflector would fill them only at its next watch session", failed)
	}
}

// Isolation (check NET-G07 also proves it live): with two Organizations and a
// Secret in B's namespace that asks for A's kubeconfig, A's kubeconfig is held
// only in A's namespace and in A's copy in flux-system, and B's likewise.
func TestKubeconfigCopies_NeverReachAnotherOrganization(t *testing.T) {
	a := kcOrgFromRender(t, "iso-a-040755fec9")
	b := kcOrgFromRender(t, "iso-b-040755fec9")
	c := newKCCluster(a.slug, b.slug)
	r := newKCReflector(c)
	aSource := a.slug + "/" + KubeconfigSecretName(a.slug)
	aCopy := "flux-system/" + KubeconfigSecretName(a.slug)
	intruders := []kcStub{}
	for _, want := range []struct{ name, reflects string }{
		{"steal-direct", aSource},
		{"steal-copy", aCopy},
		{KubeconfigSecretName(a.slug), aSource},
	} {
		var s kcStub
		s.Metadata.Name, s.Metadata.Namespace = want.name, b.slug
		s.Metadata.Annotations = map[string]string{rfReflects: want.reflects}
		intruders = append(intruders, s)
	}
	// The intruders first, then both Organizations in the measured order
	// (stubs, then the vCluster's write), then a re-list: none of it may move
	// a kubeconfig across.
	for _, s := range intruders {
		c.fluxApply(s)
		r.drain()
	}
	for _, org := range []kcOrg{a, b} {
		for i := range org.stubs {
			org.apply(c, i)
			r.drain()
		}
		org.apply(c, len(org.stubs))
		r.drain()
	}
	r.relist()
	for _, org := range []kcOrg{a, b} {
		for _, k := range kcCopies(org.slug) {
			if got := c.config(k); got != org.kubeconfig {
				t.Errorf("%s holds %q, want the kubeconfig of %s", k, got, org.slug)
			}
		}
		for key, o := range c.objs {
			if o.data["config"] != org.kubeconfig {
				continue
			}
			ownCopy := o.ns == "flux-system" && o.name == KubeconfigSecretName(org.slug)
			if o.ns != org.slug && !ownCopy {
				t.Errorf("%s holds the kubeconfig of %s", key, org.slug)
			}
		}
	}
}

// The declaration itself: the source is the vCluster's own export in the
// Organization's namespace, and it may be reflected, and is auto-reflected,
// into flux-system and nowhere else. An empty list would mean every namespace.
func TestKubeconfigSource_IsTheVClusterExportReflectedOnlyIntoFluxSystem(t *testing.T) {
	const slug = "acme"
	out := kcRender(t, slug)
	name := KubeconfigSecretName(slug)
	exports := kcExports(t, out, slug)
	if len(exports) != 2 || exports[1] != slug+"/"+name {
		t.Fatalf("the vCluster writes %v, want its default and %s/%s", exports, slug, name)
	}
	if !strings.Contains(string(out["vcluster/kustomization.yaml"]), kubeconfigMirrorDoc) {
		t.Fatalf("vcluster/kustomization.yaml does not list %s", kubeconfigMirrorDoc)
	}
	stubs := map[string]kcStub{}
	for _, s := range kcStubs(t, string(out["vcluster/"+kubeconfigMirrorDoc])) {
		if len(s.Data) > 0 || len(s.StringData) > 0 {
			t.Errorf("%s carries data: Flux must own metadata only", s.key())
		}
		stubs[s.key()] = s
	}
	want := map[string]map[string]string{
		slug + "/" + name: {
			rfAllowed: "true", rfAllowedNS: "flux-system",
			rfAutoEnabled: "true", rfAutoNS: "flux-system",
		},
		"flux-system/" + name:                        {rfReflects: slug + "/" + name},
		slug + "/" + VClusterDefaultKubeconfigSecret: {},
	}
	if len(stubs) != len(want) {
		t.Fatalf("stubs %v, want %d", stubs, len(want))
	}
	for key, ann := range want {
		s, ok := stubs[key]
		if !ok {
			t.Fatalf("no stub %s", key)
		}
		got := map[string]string{}
		for k, v := range s.Metadata.Annotations {
			if strings.HasPrefix(k, rfPrefix) {
				got[k] = v
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(ann) {
			t.Errorf("%s reflector annotations = %v, want %v", key, got, ann)
		}
	}
}

// The measured tree of rc/20261121T000707Z-9a080ac (region_local.go before
// this repair), for the model check below.
const kcMeasuredTree = `apiVersion: v1
kind: Secret
metadata:
  name: vc-vcluster
  namespace: iso-a-040755fec9
  annotations:
    reflector.v1.k8s.emberstack.com/reflection-allowed: "true"
    reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: "iso-a-040755fec9,flux-system"
type: Opaque
---
apiVersion: v1
kind: Secret
metadata:
  name: tenant-iso-a-040755fec9-kubeconfig
  namespace: iso-a-040755fec9
  annotations:
    reflector.v1.k8s.emberstack.com/reflects: "iso-a-040755fec9/vc-vcluster"
type: Opaque
---
apiVersion: v1
kind: Secret
metadata:
  name: tenant-iso-a-040755fec9-kubeconfig
  namespace: flux-system
  annotations:
    reflector.v1.k8s.emberstack.com/reflects: "iso-a-040755fec9/vc-vcluster"
type: Opaque
`

// The port must reproduce the gate run before the tests above can rely on it:
// on the measured tree, in the measured order (the copy in flux-system, then
// the one in the Organization's namespace, then the source; the vCluster's
// write 40 s later), the reflector logs "could not be found" for both copies,
// leaves them empty after the source's write, and fills them at the next
// re-list.
func TestKubeconfigReflectorModel_ReproducesTheMeasuredWait(t *testing.T) {
	const slug = "iso-a-040755fec9"
	stubs := kcStubs(t, kcMeasuredTree)
	org := kcOrg{slug: slug, kubeconfig: "kubeconfig", stubs: stubs,
		exports: []string{slug + "/vc-vcluster", slug + "/vc-vcluster"}}
	c := newKCCluster(slug)
	r := newKCReflector(c)
	for _, a := range []int{2, 1, 0, 3} {
		org.apply(c, a)
		r.drain()
	}
	wantLog := []string{
		"Could not update flux-system/tenant-iso-a-040755fec9-kubeconfig - Source iso-a-040755fec9/vc-vcluster could not be found.",
		"Could not update iso-a-040755fec9/tenant-iso-a-040755fec9-kubeconfig - Source iso-a-040755fec9/vc-vcluster could not be found.",
	}
	if strings.Join(r.log, "\n") != strings.Join(wantLog, "\n") {
		t.Fatalf("reflector log = %q, want %q", r.log, wantLog)
	}
	for _, k := range []string{"flux-system/tenant-" + slug + "-kubeconfig", slug + "/tenant-" + slug + "-kubeconfig"} {
		if got := c.config(k); got != "" {
			t.Fatalf("%s holds %q before the re-list; the gate run saw it empty", k, got)
		}
	}
	r.relist()
	for _, k := range []string{"flux-system/tenant-" + slug + "-kubeconfig", slug + "/tenant-" + slug + "-kubeconfig"} {
		if got := c.config(k); got != "kubeconfig" {
			t.Fatalf("%s holds %q after the re-list; the gate run saw it filled", k, got)
		}
	}
}
