// Command check-variables checks variables documents against contract C5.
// scripts/check-variables-documents.sh builds and runs it; it is guard G8.
//
//	check-variables [--root <dir>] [--own-fqdn <fqdn>] <file>...
//
// Each file is a path relative to --root (default: the current folder), or
// an absolute path. It
// must hold one YAML document: a v1 ConfigMap named sovereign-variables in
// the namespace flux-system whose data values are all strings. The data must
// pass upstream.FromMap (the schema, the kit keys, rule 5) and
// upstream.Validate (rules 1 to 4, 6 and 7).
//
// Rule 7 needs the Sovereign's own FQDN. It is --own-fqdn when given. Else it
// is the folder that follows the first path segment "clusters": the file
// clusters/<fqdn>/... names the Sovereign <fqdn>. A file with neither fails.
//
// Prints "ok <file>" or "FAIL <file>: <reason>" per file, one line per reason.
// Exit 0 when every file passes, 1 when one fails, 2 on a usage error.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openova-io/openova/core/controllers/pkg/upstream"
	"sigs.k8s.io/yaml"
)

type document struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Data map[string]interface{} `json:"data"`
}

func main() {
	root := flag.String("root", ".", "the folder the file paths are relative to")
	own := flag.String("own-fqdn", "", "the Sovereign's own FQDN for rule 7; default: taken from the path")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: check-variables [--root <dir>] [--own-fqdn <fqdn>] <file>...")
		os.Exit(2)
	}
	failed := false
	for _, file := range flag.Args() {
		reasons := check(*root, file, *own)
		if len(reasons) == 0 {
			fmt.Printf("ok %s\n", file)
			continue
		}
		failed = true
		for _, r := range reasons {
			fmt.Printf("FAIL %s: %s\n", file, r)
		}
	}
	if failed {
		os.Exit(1)
	}
}

// ownFromPath returns the folder after the first path segment "clusters", or "".
func ownFromPath(file string) string {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(file)), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "clusters" {
			return parts[i+1]
		}
	}
	return ""
}

func check(root, file, own string) []string {
	path := file
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, file)
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return []string{err.Error()}
	}
	var doc document
	if err := yaml.UnmarshalStrict(raw, &doc); err != nil {
		return []string{"not one ConfigMap document: " + err.Error()}
	}
	var reasons []string
	if doc.APIVersion != "v1" || doc.Kind != "ConfigMap" || doc.Metadata.Name != upstream.Name || doc.Metadata.Namespace != upstream.Namespace {
		reasons = append(reasons, fmt.Sprintf("must be the v1 ConfigMap %s/%s, is %s %s %s/%s", upstream.Namespace, upstream.Name, doc.APIVersion, doc.Kind, doc.Metadata.Namespace, doc.Metadata.Name))
	}
	data := map[string]string{}
	for k, v := range doc.Data {
		s, ok := v.(string)
		if !ok {
			reasons = append(reasons, fmt.Sprintf("the value of %s is not a string; quote it", k))
			continue
		}
		data[k] = s
	}
	if len(reasons) > 0 {
		return reasons
	}
	v, err := upstream.FromMap(data)
	if err != nil {
		return []string{err.Error()}
	}
	if own == "" {
		own = ownFromPath(file)
	}
	if own == "" {
		return []string{"no own FQDN for rule 7: the path has no clusters/<fqdn>/ part and --own-fqdn is not given"}
	}
	for _, e := range v.Validate(own) {
		reasons = append(reasons, e.Error())
	}
	return reasons
}
