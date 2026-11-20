package main

// The source of a template's names (SEC-154).
//
// A template of references holds no value, only the name of one; Flux
// substitutes the value when it builds the folder. So a template keeps its
// value out of Git only when every NAME it references comes from a Secret of
// the cluster. Every NAME referenced under data or stringData of a template
// under clusters/ must therefore be supplied ONLY by a postBuild.substituteFrom
// entry of kind Secret of each Flux Kustomization that renders the template's
// folder, directly or through an overlay that builds it. Each of these is a
// finding, and a file with a finding is not a template (G1 counts it):
//
//	the NAME is also a key of the inline postBuild.substitute of that
//	Kustomization (its value is in Git);
//	the NAME is a key of a ConfigMap that a substituteFrom entry of that
//	Kustomization names, where that ConfigMap is a manifest tracked in the
//	repository (its value is in Git);
//	that Kustomization names no substituteFrom entry of kind Secret, or its
//	postBuild cannot be read;
//	no Flux Kustomization under clusters/ renders the folder;
//	a document under clusters/ with a line `kind: Kustomization` and a line
//	whose apiVersion starts with kustomize.toolkit.fluxcd.io/ cannot be read
//	(it does not parse, or it is not a mapping): which folder it renders is
//	not known, so every template is a finding that names its file, whichever
//	folder a readable Kustomization renders. The guard fails closed.
//
// Which folders a Flux Kustomization renders: its spec.path (a ${VAR} in it
// stands for any one folder name, since Flux substitutes the path before it
// builds), and every folder that a kustomization.yaml there lists under
// resources or components, again and again; a folder without a
// kustomization.yaml renders every folder below it, as Flux generates one.
// A path that only a kustomize patch sets is not read: a folder rendered only
// through such a patch has no Kustomization and is a finding.
//
// Which ConfigMap is tracked: a document of kind ConfigMap, named in its
// metadata as the substituteFrom entry names it, in a YAML or template file
// that Git lists outside docs/, whether it stands alone, in a chart template
// or inside the first-boot template. A key is any line `KEY:` of that
// document. A ConfigMap whose name is computed (a Helm expression) is not
// found; the reference in a label or an annotation is not judged, as
// metadata is not.

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// fluxKustomization is what the rule reads of a Flux Kustomization.
type fluxKustomization struct {
	file, name, path string
	substitute       map[string]bool // the keys of postBuild.substitute
	secrets          []string        // substituteFrom entries of kind Secret
	configMaps       []string        // substituteFrom entries of kind ConfigMap
	unreadable       string          // why postBuild cannot be read
}

func (k fluxKustomization) id() string { return fmt.Sprintf("%s (%s)", k.name, k.file) }

// unreadableDocument is why a Flux Kustomization document the reader cannot
// parse, or that is not a mapping, is a finding.
const unreadableDocument = "the document cannot be read"

var (
	fluxKind       = regexp.MustCompile(`^\s*kind:\s*["']?Kustomization["']?\s*(#.*)?$`)
	fluxAPIVersion = regexp.MustCompile(`^\s*apiVersion:\s*["']?kustomize\.toolkit\.fluxcd\.io/`)
	pathVariable   = regexp.MustCompile(`\$\{[^}]*\}`)
	configMapKind  = regexp.MustCompile(`^(\s*)kind:\s*["']?ConfigMap["']?\s*(#.*)?$`)
	keyLine        = regexp.MustCompile(`^\s*["']?([-._A-Za-z0-9]+)["']?\s*:(\s|$)`)
	trackedSuffix  = []string{".yaml", ".yml", ".tpl", ".tftpl"}
	kustomizations = []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}
)

// fluxKustomizations reads the Flux Kustomizations of one file. A document
// that names a Flux Kustomization and cannot be read is returned with
// unreadable set to unreadableDocument, never skipped.
func fluxKustomizations(file, text string) []fluxKustomization {
	var out []fluxKustomization
	for _, doc := range documents(text) {
		root, err := parseYAML(doc)
		if err != nil || !root.isMap {
			if namesFluxKustomization(doc) {
				out = append(out, fluxKustomization{file: file, substitute: map[string]bool{}, unreadable: unreadableDocument})
			}
			continue
		}
		if root.get("kind").scalar != "Kustomization" || !strings.HasPrefix(root.get("apiVersion").scalar, "kustomize.toolkit.fluxcd.io/") {
			continue
		}
		k := fluxKustomization{file: file, name: root.get("metadata").get("name").scalar, substitute: map[string]bool{}}
		spec := root.get("spec")
		k.path = spec.get("path").scalar
		post := spec.get("postBuild")
		switch {
		case post.isNull():
		case !post.isMap:
			k.unreadable = "postBuild is not a block mapping"
		default:
			sub, from := post.get("substitute"), post.get("substituteFrom")
			switch {
			case sub.isMap:
				for _, key := range sub.keys {
					k.substitute[key] = true
				}
			case !sub.isNull():
				k.unreadable = "postBuild.substitute is not a block mapping"
			}
			switch {
			case from.isSeq:
				for _, e := range from.seq {
					if !e.isMap {
						k.unreadable = "an entry of postBuild.substituteFrom is not a block mapping"
						continue
					}
					switch kind, name := e.get("kind").scalar, e.get("name").scalar; kind {
					case "Secret":
						k.secrets = append(k.secrets, name)
					case "ConfigMap":
						k.configMaps = append(k.configMaps, name)
					default:
						k.unreadable = fmt.Sprintf("an entry of postBuild.substituteFrom has the kind %q", kind)
					}
				}
			case !from.isNull():
				k.unreadable = "postBuild.substituteFrom is not a block sequence"
			}
		}
		out = append(out, k)
	}
	return out
}

// namesFluxKustomization reports whether a document holds a line
// `kind: Kustomization` and a line whose apiVersion is Flux's.
func namesFluxKustomization(doc []string) bool {
	kind, api := false, false
	for _, l := range doc {
		kind = kind || fluxKind.MatchString(l)
		api = api || fluxAPIVersion.MatchString(l)
	}
	return kind && api
}

// renderedFolders are the folders a Flux Kustomization renders.
func renderedFolders(tree fs.FS, k fluxKustomization) map[string]bool {
	out := map[string]bool{}
	p := strings.TrimPrefix(path.Clean(strings.TrimPrefix(k.path, "./")), "/")
	if k.path == "" || p == "." || strings.HasPrefix(p, "..") {
		return out
	}
	matches := []string{p}
	if pathVariable.MatchString(p) {
		var err error
		if matches, err = fs.Glob(tree, pathVariable.ReplaceAllString(p, "*")); err != nil {
			return out
		}
	}
	for _, m := range matches {
		if info, err := fs.Stat(tree, m); err == nil && info.IsDir() {
			build(tree, m, out)
		}
	}
	return out
}

// build adds dir and every folder it builds to out.
func build(tree fs.FS, dir string, out map[string]bool) {
	if out[dir] {
		return
	}
	out[dir] = true
	for _, name := range kustomizations {
		b, err := fs.ReadFile(tree, path.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return
		}
		root, err := parseYAML(strings.Split(string(b), "\n"))
		if err != nil {
			return
		}
		for _, field := range []string{"resources", "components"} {
			for _, e := range root.get(field).seq {
				target := path.Clean(path.Join(dir, e.scalar))
				if e.scalar == "" || strings.Contains(e.scalar, "://") || strings.HasPrefix(target, "..") {
					continue
				}
				if info, err := fs.Stat(tree, target); err == nil && info.IsDir() {
					build(tree, target, out)
				}
			}
		}
		return
	}
	// No kustomization.yaml: Flux generates one with every folder below.
	entries, err := fs.ReadDir(tree, dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			build(tree, path.Join(dir, e.Name()), out)
		}
	}
}

// configMapKeys returns, per ConfigMap name of names, the keys of its tracked
// documents and the files they are in.
func configMapKeys(tree fs.FS, files []string, names map[string]bool) map[string]map[string]string {
	out := map[string]map[string]string{}
	if len(names) == 0 {
		return out
	}
	for _, f := range files {
		if strings.HasPrefix(f, "docs/") || !hasSuffix(f, trackedSuffix) {
			continue
		}
		b, err := fs.ReadFile(tree, f)
		if err != nil || !strings.Contains(string(b), "ConfigMap") {
			continue
		}
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			m := configMapKind.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			base := len(m[1])
			start, end := documentBounds(lines, i, base)
			name := ""
			for _, d := range lines[start:end] {
				if v, ok := deeperField(d, base, "name"); ok && name == "" {
					name = v
				}
			}
			if !names[name] {
				continue
			}
			if out[name] == nil {
				out[name] = map[string]string{}
			}
			for _, d := range lines[start:end] {
				if km := keyLine.FindStringSubmatch(d); km != nil && indentOf(d) > base {
					if _, seen := out[name][km[1]]; !seen {
						out[name][km[1]] = f
					}
				}
			}
		}
	}
	return out
}

// documentBounds is the document around line i whose fields stand at indent
// base: it ends at a separator or at a line indented less than base; a blank
// line, a comment and a template directive (%{ or {{ at its start) do not end it.
func documentBounds(lines []string, i, base int) (int, int) {
	inside := func(l string) bool {
		t := strings.TrimSpace(l)
		if t == "---" || strings.HasPrefix(t, "--- ") || t == "..." {
			return false
		}
		return t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "%{") || strings.HasPrefix(t, "{{") || indentOf(l) >= base
	}
	start, end := i, i+1
	for start > 0 && inside(lines[start-1]) {
		start--
	}
	for end < len(lines) && inside(lines[end]) {
		end++
	}
	return start, end
}

// deeperField returns the value of a line `field: value` indented more than base.
func deeperField(l string, base int, field string) (string, bool) {
	if indentOf(l) <= base {
		return "", false
	}
	t := strings.TrimSpace(l)
	if !strings.HasPrefix(t, field+":") {
		return "", false
	}
	return unquote(strings.TrimSpace(stripComment(strings.TrimPrefix(t, field+":")))), true
}

func indentOf(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }

func hasSuffix(s string, suffixes []string) bool {
	for _, x := range suffixes {
		if strings.HasSuffix(s, x) {
			return true
		}
	}
	return false
}

// sourceFindings judges the source of the names of every template: for each
// template file that has a finding, the first reason.
func sourceFindings(tree fs.FS, repoFiles []string, templates map[string][]string, ks []fluxKustomization) map[string]string {
	renders := map[string][]fluxKustomization{} // folder -> the Kustomizations that render it
	var blind []string                          // files of documents that cannot be read
	for _, k := range ks {
		if k.unreadable == unreadableDocument {
			blind = append(blind, k.file)
			continue
		}
		for dir := range renderedFolders(tree, k) {
			renders[dir] = append(renders[dir], k)
		}
	}
	wanted := map[string]bool{}
	for _, k := range ks {
		for _, c := range k.configMaps {
			wanted[c] = true
		}
	}
	keys := configMapKeys(tree, repoFiles, wanted)
	out := map[string]string{}
	paths := make([]string, 0, len(templates))
	for p := range templates {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if len(blind) > 0 {
			out[p] = fmt.Sprintf("a Flux Kustomization document in %s cannot be read, so the folder it renders and where its names come from are not known", blind[0])
			continue
		}
		dir := path.Dir(p)
		by := renders[dir]
		if len(by) == 0 {
			out[p] = fmt.Sprintf("no Flux Kustomization under clusters/ renders its folder %s, so nothing says where its names come from", dir)
			continue
		}
		why := ""
		for _, k := range by {
			switch {
			case k.unreadable != "":
				why = fmt.Sprintf("the Flux Kustomization %s renders its folder and its postBuild cannot be read: %s", k.id(), k.unreadable)
			case len(k.secrets) == 0:
				why = fmt.Sprintf("the Flux Kustomization %s renders its folder and names no postBuild.substituteFrom entry of kind Secret", k.id())
			}
			for _, name := range templates[p] {
				if why != "" {
					break
				}
				if k.substitute[name] {
					why = fmt.Sprintf("the name %s is also a key of the inline postBuild.substitute of the Flux Kustomization %s, so its value is in Git", name, k.id())
					continue
				}
				for _, c := range k.configMaps {
					if f, ok := keys[c][name]; ok {
						why = fmt.Sprintf("the name %s is a key of the ConfigMap %s (%s), which the Flux Kustomization %s names in postBuild.substituteFrom, so its value is in Git", name, c, f, k.id())
						break
					}
				}
			}
			if why != "" {
				break
			}
		}
		if why != "" {
			out[p] = why
		}
	}
	return out
}

// documents splits a file at its lines `---` or `...` at the left edge.
func documents(text string) [][]string {
	var out [][]string
	var cur []string
	for _, l := range strings.Split(text, "\n") {
		if separator.MatchString(l) {
			out = append(out, cur)
			cur = nil
			continue
		}
		cur = append(cur, l)
	}
	return append(out, cur)
}

// yNode is a node of the block YAML this rule reads: a mapping, a sequence or
// a scalar. A flow collection is a scalar holding its text.
type yNode struct {
	scalar       string
	isMap, isSeq bool
	keys         []string
	m            map[string]*yNode
	seq          []*yNode
}

var null = &yNode{}

func (n *yNode) get(key string) *yNode {
	if n == nil || !n.isMap || n.m[key] == nil {
		return null
	}
	return n.m[key]
}

func (n *yNode) isNull() bool { return !n.isMap && !n.isSeq && n.scalar == "" }

type yLine struct {
	indent int
	text   string
}

var blockScalar = regexp.MustCompile(`(?:^-|:)\s+[|>][-+0-9]*$`)

// parseYAML reads one document of block YAML: mappings, sequences, plain and
// quoted scalars, comments and block scalars (skipped). A tab in the
// indentation or a line that is neither an entry nor an item is an error.
func parseYAML(doc []string) (*yNode, error) {
	var ls []yLine
	for i := 0; i < len(doc); i++ {
		raw := strings.TrimRight(doc[i], " \r")
		t := strings.TrimLeft(raw, " ")
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "\t") {
			return nil, fmt.Errorf("line %d: a tab in the indentation", i+1)
		}
		ind := len(raw) - len(t)
		t = stripComment(t)
		ls = append(ls, yLine{ind, t})
		if blockScalar.MatchString(t) {
			for i+1 < len(doc) && (strings.TrimSpace(doc[i+1]) == "" || indentOf(doc[i+1]) > ind) {
				i++
			}
		}
	}
	if len(ls) == 0 {
		return null, nil
	}
	i := 0
	n, err := parseBlock(ls, &i, ls[0].indent)
	if err == nil && i < len(ls) {
		err = fmt.Errorf("an entry at indent %d after the document's block", ls[i].indent)
	}
	return n, err
}

func isItem(t string) bool { return t == "-" || strings.HasPrefix(t, "- ") }

func parseBlock(ls []yLine, i *int, ind int) (*yNode, error) {
	if isItem(ls[*i].text) {
		return parseSeq(ls, i, ind)
	}
	return parseMap(ls, i, ind)
}

func parseSeq(ls []yLine, i *int, ind int) (*yNode, error) {
	n := &yNode{isSeq: true}
	for *i < len(ls) && ls[*i].indent == ind && isItem(ls[*i].text) {
		rest := strings.TrimLeft(strings.TrimPrefix(ls[*i].text, "-"), " ")
		var item *yNode
		switch _, _, isEntry := splitEntry(rest); {
		case rest == "":
			*i++
			item = null
			if *i < len(ls) && ls[*i].indent > ind {
				var err error
				if item, err = parseBlock(ls, i, ls[*i].indent); err != nil {
					return nil, err
				}
			}
		case isEntry:
			at := ind + len(ls[*i].text) - len(rest)
			ls[*i] = yLine{at, rest}
			var err error
			if item, err = parseMap(ls, i, at); err != nil {
				return nil, err
			}
		default:
			item = &yNode{scalar: unquote(rest)}
			*i++
		}
		n.seq = append(n.seq, item)
	}
	return n, nil
}

func parseMap(ls []yLine, i *int, ind int) (*yNode, error) {
	n := &yNode{isMap: true, m: map[string]*yNode{}}
	for *i < len(ls) && ls[*i].indent == ind && !isItem(ls[*i].text) {
		key, value, ok := splitEntry(ls[*i].text)
		if !ok {
			return nil, fmt.Errorf("%q is not an entry key: value", ls[*i].text)
		}
		*i++
		child := null
		switch {
		case value != "":
			child = &yNode{scalar: unquote(value)}
		case *i < len(ls) && (ls[*i].indent > ind || ls[*i].indent == ind && isItem(ls[*i].text)):
			var err error
			if child, err = parseBlock(ls, i, ls[*i].indent); err != nil {
				return nil, err
			}
		}
		if _, dup := n.m[key]; !dup {
			n.keys = append(n.keys, key)
		}
		n.m[key] = child
	}
	if *i < len(ls) && ls[*i].indent > ind {
		return nil, fmt.Errorf("%q is indented more than its mapping", ls[*i].text)
	}
	return n, nil
}

// splitEntry splits `key: value` (a plain or quoted key); ok is false when the
// text is not an entry.
func splitEntry(t string) (string, string, bool) {
	if t == "" || strings.ContainsAny(t[:1], "[{|>*&!%@`") {
		return "", "", false
	}
	if t[0] == '"' || t[0] == '\'' {
		end := strings.IndexByte(t[1:], t[0])
		if end < 0 || !strings.HasPrefix(t[end+2:], ":") {
			return "", "", false
		}
		return t[1 : end+1], strings.TrimSpace(t[end+3:]), true
	}
	for j := 0; j < len(t); j++ {
		if t[j] == ':' && (j == len(t)-1 || t[j+1] == ' ') {
			return t[:j], strings.TrimSpace(t[j+1:]), true
		}
	}
	return "", "", false
}

// stripComment removes a comment ` #...` that follows a value outside quotes.
func stripComment(t string) string {
	quote := byte(0)
	for j := 0; j < len(t); j++ {
		switch c := t[j]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			if j == 0 || t[j-1] == ' ' || t[j-1] == ':' || t[j-1] == '-' {
				quote = c
			}
		case c == '#' && (j == 0 || t[j-1] == ' '):
			return strings.TrimRight(t[:j], " ")
		}
	}
	return t
}
