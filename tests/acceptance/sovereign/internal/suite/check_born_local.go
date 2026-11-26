package suite

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check SOV-G01, Born local: at its root a region reads nothing from outside
// the Sovereign. In every region (primary first) it asserts three clauses; the
// fourth of the design, the catalogue as a verified OCIRepository, is SOV-510's.
//
//  1. Every Node carries the annotation k3s.io/node-args, the arguments k3s
//     started with, and they hold --disable-default-registry-endpoint, not
//     followed by a false value. The registry file
//     /etc/rancher/k3s/registries.yaml of every node, read by the node probe
//     (sec_probe.go, namespace born-local-<run>, one Pod per node), lists
//     https://<LOCAL_REGISTRY_HOST> first for every name, and has the entry
//     "*", so that a name it does not list goes to that registry too.
//     LOCAL_REGISTRY_HOST is the region's, from the ConfigMap
//     flux-system/sovereign-variables. The file is read as YAML (bornLocalYAML),
//     of which the JSON the provider modules write is one form and the block
//     form of the local pack another; only its mirrors are recorded, never its
//     configs, which hold a login, and an error names a line, never its text.
//  2. Flux has a GitRepository, and none outside the Sovereign: the host of
//     every spec.url is a cluster Service (a name ending in .svc or
//     .svc.cluster.local), SOVEREIGN_FQDN of sovereign-variables or a name
//     under it.
//  3. The root Kustomizations (bornLocalRoots; infrastructure-providers is
//     asserted in the primary region only) are Ready and owned by Flux from
//     the Sovereign's own repository: the Flux labels of each (name and
//     namespace) name a Kustomization of flux-system whose source is a
//     GitRepository inside the Sovereign with the path
//     openova/sovereign-regions, and that Kustomization is Ready with a
//     lastAppliedRevision equal to the revision of its source's artifact.
//     Each pass reads the Kustomizations, then the GitRepositories, and
//     judges them together. While every fault of a pass is an owner that is
//     not Ready or has not applied the revision its source holds, as after a
//     commit to the Sovereign's repository, the pass is repeated every
//     bornLocalEvery for up to bornLocalSettle; any other fault fails at once.
//
// A ConfigMap, key, annotation, file or list the check cannot read fails the
// step that needs it; an absent value is never read as a pass.
const bornLocalID = "SOV-G01"

const (
	bornLocalFlag        = "--disable-default-registry-endpoint"
	bornLocalPrimaryOnly = "infrastructure-providers"
	bornLocalRepository  = "/openova/sovereign-regions"
	bornLocalGit         = "gitrepositories.source.toolkit.fluxcd.io"
	bornLocalKs          = "kustomizations.kustomize.toolkit.fluxcd.io"
)

// bornLocalRoots are RootKustomizations of core/cmd/sovereign-ops/internal/
// seedrepo/seedrepo.go, the Kustomizations that seed-repo commits; a test
// keeps the two lists equal.
var bornLocalRoots = []string{"bootstrap-kit", "bootstrap-kit-crs", "sovereign-tls", "infrastructure-providers"}

// bornLocalScript runs in a probe Pod as `sh -s -- /host born-local <registry
// host>`. It prints the host it was given and the registry file in base64, or
// registries=unreadable; it judges nothing and writes nothing.
const bornLocalScript = `f=$1/etc/rancher/k3s/registries.yaml
echo "own=$3"
if [ -r "$f" ]; then echo "registries=$(base64 < "$f" | tr -d '\n')"; else echo registries=unreadable; fi
`

// bornLocalSettle and bornLocalEvery bound the repeated pass of step 3: the
// owner sovereign-region reconciles within its spec.timeout (5m) once its
// source has fetched the commit, every spec.interval (1m), both in
// clusters/_template/sovereign-region/base.
const bornLocalSettle, bornLocalEvery = 6 * time.Minute, 15 * time.Second

// bornLocalCheck is check SOV-G01. get reads the Flux objects (kube.Region.GetList);
// settle and every bound the repeated pass of step 3, and a zero settle makes one pass.
type bornLocalCheck struct {
	kubectl       kubectlFunc
	get           func(kube.Region, context.Context, string, string, ...string) ([]kube.Object, error)
	settle, every time.Duration
}

func init() {
	RegisterCheck(bornLocalCheck{kubectl: kube.Region.Run, get: kube.Region.GetList, settle: bornLocalSettle, every: bornLocalEvery})
}

func (bornLocalCheck) ID() string { return bornLocalID }

func (c bornLocalCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	n := 0
	for i, region := range cc.Regions {
		vars, varsErr := bornLocalVariables(ctx, region)
		repos, reposErr := c.get(region, ctx, bornLocalGit, "")
		n++
		checkStep(rec, bornLocalID, n, "every node of region "+region.ID+" starts k3s with "+bornLocalFlag, func(ev Evidence) error {
			return bornLocalNodes(ctx, region, ev)
		})
		n++
		checkStep(rec, bornLocalID, n, "no Flux GitRepository of region "+region.ID+" reads from outside the Sovereign", func(ev Evidence) error {
			if err := errors.Join(varsErr, reposErr); err != nil {
				return err
			}
			return bornLocalSources(region, repos, vars["SOVEREIGN_FQDN"], ev)
		})
		n++
		checkStep(rec, bornLocalID, n, "the root Kustomizations of region "+region.ID+" are Ready and owned by Flux from the Sovereign's own repository "+
			"(an owner that settles is read again for up to "+c.settle.String()+")", func(ev Evidence) error {
			if varsErr != nil {
				return varsErr
			}
			passes := 0
			_, err := poll(ctx, c.settle, c.every, func() (bool, string, error) {
				passes++
				ks, err := c.get(region, ctx, bornLocalKs, "flux-system")
				repos, reposErr := c.get(region, ctx, bornLocalGit, "")
				if err = errors.Join(err, reposErr); err != nil {
					return false, "", err
				}
				settling, err := bornLocalOwned(region, i == 0, ks, repos, vars["SOVEREIGN_FQDN"], ev)
				if settling {
					return false, err.Error(), nil
				}
				return true, "", err
			})
			ev["passes"] = passes
			return err
		})
	}
	return secProbe{id: bornLocalID, prefix: "born-local", mode: "born-local", script: bornLocalScript, selector: "kubernetes.io/hostname",
		first: n, kubectl: c.kubectl, what: "the registry file lists the region's own registry first for every name",
		setup: func(ctx context.Context, region kube.Region, _ string) ([]string, error) {
			vars, err := bornLocalVariables(ctx, region)
			if err == nil && vars["LOCAL_REGISTRY_HOST"] == "" {
				err = fmt.Errorf("sovereign-variables of region %s has no LOCAL_REGISTRY_HOST", region.ID)
			}
			return []string{vars["LOCAL_REGISTRY_HOST"]}, err
		},
		judge: judgeBornLocalRegistry}.run(ctx, cc, rec)
}

// bornLocalVariables returns the data of the ConfigMap flux-system/sovereign-variables.
func bornLocalVariables(ctx context.Context, region kube.Region) (map[string]string, error) {
	list, err := region.GetList(ctx, "configmaps", "flux-system")
	if err != nil {
		return nil, err
	}
	for _, m := range list {
		if m.Name() == "sovereign-variables" {
			return sovereignObjectStrings(m.Get("data")), nil
		}
	}
	return nil, fmt.Errorf("region %s has no ConfigMap flux-system/sovereign-variables", region.ID)
}

func bornLocalNodes(ctx context.Context, region kube.Region, ev Evidence) error {
	nodes, err := region.GetList(ctx, "nodes", "")
	if err != nil {
		return err
	}
	names, lacking := []string{}, []string{}
	for _, node := range nodes {
		names = append(names, node.Name())
		var args []string
		if json.Unmarshal([]byte(node.Str("metadata", "annotations", "k3s.io/node-args")), &args) != nil || !bornLocalFlagSet(args) {
			lacking = append(lacking, node.Name())
		}
	}
	ev["nodes"], ev["without_flag"] = names, lacking
	switch {
	case len(nodes) == 0:
		return fmt.Errorf("region %s lists no node", region.ID)
	case len(lacking) > 0:
		return fmt.Errorf("region %s: k3s.io/node-args of the nodes [%s] does not hold %s", region.ID, strings.Join(lacking, " "), bornLocalFlag)
	}
	return nil
}

// bornLocalFlagSet reports whether the arguments set the flag. k3s records
// --name=value as the two words --name and value.
func bornLocalFlagSet(args []string) bool {
	for i, a := range args {
		if a == bornLocalFlag {
			if i+1 < len(args) {
				if b, err := strconv.ParseBool(args[i+1]); err == nil && !b {
					return false
				}
			}
			return true
		}
	}
	return false
}

func bornLocalSources(region kube.Region, repos []kube.Object, fqdn string, ev Evidence) error {
	urls, outside := map[string]string{}, []string{}
	for _, r := range repos {
		id := r.Namespace() + "/" + r.Name()
		urls[id] = r.Str("spec", "url")
		if !bornLocalInside(urls[id], fqdn) {
			outside = append(outside, id+" "+urls[id])
		}
	}
	sort.Strings(outside)
	ev["gitrepositories"] = urls
	switch {
	case fqdn == "":
		return fmt.Errorf("sovereign-variables of region %s has no SOVEREIGN_FQDN to judge the hosts by", region.ID)
	case len(repos) == 0:
		return fmt.Errorf("region %s: Flux has no GitRepository, not even the Sovereign's own repository", region.ID)
	case len(outside) > 0:
		return fmt.Errorf("region %s: GitRepositories read from outside the Sovereign: [%s]", region.ID, strings.Join(outside, ", "))
	}
	return nil
}

// bornLocalInside reports whether the host of a source URL is a cluster
// Service, the Sovereign's FQDN or a name under it.
func bornLocalInside(raw, fqdn string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h, fqdn := strings.ToLower(u.Hostname()), strings.ToLower(fqdn)
	return strings.HasSuffix(h, ".svc") || strings.HasSuffix(h, ".svc.cluster.local") || (fqdn != "" && (h == fqdn || strings.HasSuffix(h, "."+fqdn)))
}

// bornLocalOwned judges one pass of step 3. settling reports that it failed
// and that every fault is an owner that settles by itself (bornLocalOwner).
func bornLocalOwned(region kube.Region, primary bool, ks, repos []kube.Object, fqdn string, ev Evidence) (settling bool, err error) {
	byName, sources := map[string]kube.Object{}, map[string]kube.Object{}
	for _, k := range ks {
		byName[k.Name()] = k
	}
	for _, r := range repos {
		sources[r.Namespace()+"/"+r.Name()] = r
	}
	faults, owners, roots := []string{}, map[string][]string{}, Evidence{}
	for _, name := range bornLocalRoots {
		k, ok := byName[name]
		if !ok {
			if primary || name != bornLocalPrimaryOnly {
				faults = append(faults, "no Kustomization flux-system/"+name)
			}
			continue
		}
		if status, _, message, _ := k.Condition("Ready"); status != "True" {
			faults = append(faults, fmt.Sprintf("%s is not Ready (%s)", name, message))
		}
		owner := k.Str("metadata", "labels", "kustomize.toolkit.fluxcd.io/name")
		roots[name] = owner
		if owner == "" || k.Str("metadata", "labels", "kustomize.toolkit.fluxcd.io/namespace") != "flux-system" {
			faults = append(faults, name+" is not owned by a Kustomization of flux-system")
			continue
		}
		owners[owner] = append(owners[owner], name)
	}
	read, settles := Evidence{}, 0
	for _, o := range slices.Sorted(maps.Keys(owners)) {
		source, fault, settling := bornLocalOwner(byName, sources, o, fqdn)
		read[o] = source
		if fault != "" {
			faults = append(faults, fmt.Sprintf("the owner %s of [%s] %s", o, strings.Join(owners[o], " "), fault))
			if settling {
				settles++
			}
		}
	}
	ev["roots"], ev["owners"] = roots, read
	if len(faults) > 0 {
		return settles == len(faults), fmt.Errorf("region %s: %s", region.ID, strings.Join(faults, "; "))
	}
	return false, nil
}

// bornLocalOwner judges the Kustomization that applied a root Kustomization:
// the URL of its source, what is wrong with it ("" when nothing is), and
// whether that settles by itself: the owner is not Ready, or its source holds
// a revision the owner has not applied.
func bornLocalOwner(byName, sources map[string]kube.Object, name, fqdn string) (string, string, bool) {
	o, ok := byName[name]
	if !ok {
		return "", "is not a Kustomization of flux-system", false
	}
	kind, ns := o.Str("spec", "sourceRef", "kind"), o.Str("spec", "sourceRef", "namespace")
	if ns == "" {
		ns = o.Namespace()
	}
	src, ok := sources[ns+"/"+o.Str("spec", "sourceRef", "name")]
	if kind != "GitRepository" || !ok {
		return "", fmt.Sprintf("reads the %s %s/%s, which is no GitRepository of the region", kind, ns, o.Str("spec", "sourceRef", "name")), false
	}
	raw := src.Str("spec", "url")
	u, err := url.Parse(raw)
	if err != nil || !bornLocalInside(raw, fqdn) || strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git") != bornLocalRepository {
		return raw, "reads " + raw + ", not the Sovereign's own repository", false
	}
	if status, _, message, _ := o.Condition("Ready"); status != "True" {
		return raw, "is not Ready (" + message + ")", true
	}
	applied, held := o.Str("status", "lastAppliedRevision"), src.Str("status", "artifact", "revision")
	if applied == "" || applied != held {
		return raw, fmt.Sprintf("applied the revision %q, its source holds %q", applied, held), held != ""
	}
	return raw, "", false
}

// judgeBornLocalRegistry judges what the probe printed in the mode born-local.
func judgeBornLocalRegistry(v probeValues, ev Evidence) error {
	own := "https://" + v.one("own")
	ev["own_registry"] = own
	switch raw := v["registries"]; {
	case len(raw) == 0:
		return errors.New("the probe did not finish")
	case raw[len(raw)-1] == "unreadable":
		return errors.New("the node has no readable /etc/rancher/k3s/registries.yaml")
	}
	var file struct {
		Mirrors map[string]struct {
			Endpoint []string `json:"endpoint"`
		} `json:"mirrors"`
	}
	b, err := base64.StdEncoding.DecodeString(v.one("registries"))
	if err != nil {
		return errors.New("the probe printed the registry file in no base64")
	}
	tree, err := bornLocalYAML(b)
	if err == nil {
		j, _ := json.Marshal(tree)
		if json.Unmarshal(j, &file) != nil {
			err = errors.New("its mirrors are not names that each hold a list of endpoints")
		}
	}
	if err != nil {
		return fmt.Errorf("/etc/rancher/k3s/registries.yaml is not YAML this check reads: %w", err)
	}
	mirrors, other := map[string][]string{}, []string{}
	for name, m := range file.Mirrors {
		mirrors[name] = m.Endpoint
		if len(m.Endpoint) == 0 || m.Endpoint[0] != own {
			other = append(other, name)
		}
	}
	sort.Strings(other)
	ev["mirrors"] = mirrors
	faults := []string{}
	if len(mirrors) == 0 {
		faults = append(faults, "the registry file lists no name")
	} else if _, ok := file.Mirrors["*"]; !ok {
		faults = append(faults, `the registry file has no entry "*", so a name it does not list does not go to the region's own registry`)
	}
	if len(other) > 0 {
		faults = append(faults, fmt.Sprintf("the first endpoint is not %s for [%s]", own, strings.Join(other, " ")))
	}
	if len(faults) > 0 {
		return errors.New(strings.Join(faults, "; "))
	}
	return nil
}

// yamlLine is one line of a YAML document that holds content: its number,
// its indent in spaces and its text after the indent.
type yamlLine struct {
	n, indent int
	text      string
}

// bornLocalYAML reads a registry file as YAML into maps, lists and strings.
// It reads a document that is one JSON value (the provider modules write
// one), and block mappings and lists (the local pack, scripts/local-pack.sh)
// whose scalars are plain, single- or double-quoted, or JSON. It refuses
// every other form (a tab in an indent, an anchor, an alias, a tag, a block
// scalar, a second document, a line under no key, a repeated key), so that
// what it cannot read is never judged as absent. An error names a line,
// never its text.
func bornLocalYAML(b []byte) (any, error) {
	lines := []yamlLine{}
	for i, raw := range strings.Split(string(b), "\n") {
		raw = strings.TrimRight(raw, " \r")
		if text := strings.TrimLeft(raw, " "); text != "" && text[0] != '#' && (text != "---" || len(lines) > 0) {
			lines = append(lines, yamlLine{i + 1, len(raw) - len(text), text})
		}
	}
	if len(lines) == 0 {
		return nil, errors.New("it holds no document")
	}
	if c := lines[0].text[0]; c == '{' || c == '[' {
		texts := []string{}
		for _, l := range lines {
			texts = append(texts, l.text)
		}
		return yamlJSON(strings.Join(texts, "\n"), lines[0].n)
	}
	for _, l := range lines {
		if l.text[0] == '\t' {
			return nil, fmt.Errorf("line %d is indented with a tab", l.n)
		}
	}
	v, err := yamlWhole(lines, lines[0].indent)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func yamlItem(text string) bool { return text == "-" || strings.HasPrefix(text, "- ") }

// yamlWhole reads the block node of lines, which must hold nothing else.
func yamlWhole(lines []yamlLine, indent int) (any, error) {
	v, rest, err := yamlNode(lines, indent)
	if err == nil && len(rest) > 0 {
		err = fmt.Errorf("line %d is outside the structure above it", rest[0].n)
	}
	return v, err
}

// yamlNode reads the block mapping or list whose lines start at indent and
// returns the lines after it.
func yamlNode(lines []yamlLine, indent int) (node any, rest []yamlLine, err error) {
	if yamlItem(lines[0].text) {
		node, rest, err = yamlList(lines, indent)
	} else {
		node, rest, err = yamlMap(lines, indent)
	}
	if err == nil && len(rest) > 0 && rest[0].indent > indent {
		err = fmt.Errorf("line %d is indented under no key", rest[0].n)
	}
	return node, rest, err
}

func yamlList(lines []yamlLine, indent int) ([]any, []yamlLine, error) {
	list := []any{}
	for len(lines) > 0 && lines[0].indent == indent && yamlItem(lines[0].text) {
		l, end := lines[0], 1
		for end < len(lines) && lines[end].indent > indent {
			end++
		}
		item := strings.TrimLeft(l.text[1:], " ")
		col := l.indent + len(l.text) - len(item)
		var v any
		var err error
		keyed := false
		if item != "" {
			_, _, keyed, err = yamlKey(item, l.n)
		}
		switch {
		case err != nil:
		case item == "" && end == 1:
		case item == "":
			v, err = yamlWhole(lines[1:end], lines[1].indent)
		case !keyed && !yamlItem(item) && end == 1:
			v, err = yamlScalar(item, l.n)
		default: // "- key: value" or "- - item" opens a node at the column of item
			v, err = yamlWhole(append([]yamlLine{{l.n, col, item}}, lines[1:end]...), col)
		}
		if err != nil {
			return nil, nil, err
		}
		list, lines = append(list, v), lines[end:]
	}
	return list, lines, nil
}

func yamlMap(lines []yamlLine, indent int) (map[string]any, []yamlLine, error) {
	m := map[string]any{}
	for len(lines) > 0 && lines[0].indent == indent && !yamlItem(lines[0].text) {
		l := lines[0]
		key, value, keyed, err := yamlKey(l.text, l.n)
		if _, seen := m[key]; err == nil && (!keyed || seen) {
			err = fmt.Errorf("line %d is no new key of the mapping it is in", l.n)
		}
		lines = lines[1:]
		switch {
		case err != nil:
		case value != "":
			m[key], err = yamlScalar(value, l.n)
		case len(lines) > 0 && (lines[0].indent > indent || (lines[0].indent == indent && yamlItem(lines[0].text))):
			m[key], lines, err = yamlNode(lines, lines[0].indent)
		default:
			m[key] = nil
		}
		if err != nil {
			return nil, nil, err
		}
	}
	return m, lines, nil
}

// yamlKey splits "key: value"; keyed is false when text is no mapping entry.
// text is not empty.
func yamlKey(text string, n int) (key, value string, keyed bool, err error) {
	rest := ""
	if text[0] == '"' || text[0] == '\'' {
		end, qerr := yamlQuoteEnd(text, n)
		if qerr != nil {
			return "", "", false, qerr
		}
		if rest = text[end:]; rest != ":" && !strings.HasPrefix(rest, ": ") {
			return "", "", false, nil
		}
		k, qerr := yamlScalar(text[:end], n)
		if qerr != nil {
			return "", "", false, qerr
		}
		key = k.(string)
	} else {
		i := strings.Index(text, ": ")
		if i < 0 && strings.HasSuffix(text, ":") {
			i = len(text) - 1
		}
		if i < 1 {
			return "", "", false, nil
		}
		if key, rest = text[:i], text[i:]; strings.ContainsAny(key[:1], yamlIndicators+"{[]},") {
			return "", "", false, fmt.Errorf("line %d has a key in a YAML form this check does not read", n)
		}
	}
	if value = strings.TrimLeft(rest[1:], " "); strings.HasPrefix(value, "#") {
		value = ""
	}
	return key, value, true, nil
}

// yamlIndicators start an anchor, an alias, a tag, a block scalar, a
// directive, a reserved character or a complex key.
const yamlIndicators = "&*!|>%@`?"

// yamlScalar reads one scalar or JSON flow collection of line n.
func yamlScalar(s string, n int) (any, error) {
	switch {
	case s[0] == '"' || s[0] == '\'':
		end, err := yamlQuoteEnd(s, n)
		if t := strings.TrimLeft(s[end:], " "); err == nil && t != "" && (t[0] != '#' || len(t) == len(s)-end) {
			err = fmt.Errorf("line %d holds text after a quoted scalar", n)
		}
		if err != nil {
			return nil, err
		}
		if s[0] == '\'' {
			return strings.ReplaceAll(s[1:end-1], "''", "'"), nil
		}
		var v string
		if json.Unmarshal([]byte(s[:end]), &v) != nil {
			return nil, fmt.Errorf("line %d holds an escape in a double-quoted scalar that this check does not read", n)
		}
		return v, nil
	case s[0] == '{' || s[0] == '[':
		return yamlJSON(s, n)
	case strings.ContainsAny(s[:1], yamlIndicators):
		return nil, fmt.Errorf("line %d holds a value in a YAML form this check does not read", n)
	}
	if i := strings.Index(s, " #"); i >= 0 {
		s = strings.TrimRight(s[:i], " ")
	}
	switch {
	case strings.Contains(s, ": ") || strings.HasSuffix(s, ":"):
		return nil, fmt.Errorf("line %d holds a mapping where a scalar belongs", n)
	case s == "~" || s == "null":
		return nil, nil
	}
	return s, nil
}

// yamlQuoteEnd returns the index after the quote that ends the quoted scalar s.
func yamlQuoteEnd(s string, n int) (int, error) {
	for i := 1; i < len(s); i++ {
		switch {
		case s[0] == '"' && s[i] == '\\':
			i++
		case s[0] == '\'' && s[i] == '\'' && i+1 < len(s) && s[i+1] == '\'':
			i++
		case s[i] == s[0]:
			return i + 1, nil
		}
	}
	return 0, fmt.Errorf("line %d holds a quoted scalar that does not end on it", n)
}

// yamlJSON reads a flow collection that starts on line n as JSON.
func yamlJSON(s string, n int) (any, error) {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return nil, fmt.Errorf("line %d starts a flow collection that is not JSON", n)
	}
	return v, nil
}
