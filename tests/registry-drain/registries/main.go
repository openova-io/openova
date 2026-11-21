// Command registries prints the node registry file /etc/rancher/k3s/registries.yaml
// of the registry-drain harness (SOV-139), rendered by the kit's own renderer,
// upstream.Variables.RegistriesYAML (core/controllers/pkg/upstream, the Go form
// of the provider modules' registry_mirror_yaml_<provider>, SOV-132), for a
// Sovereign after the cutover: SOVEREIGN_CONSUME local, no Dragonfly proxy and
// no hosted rewrites.
//
// It is not a module of its own: scripts/check-registry-drain.sh runs it with
// `go -C core/controllers run <this file>`, so it builds against that module.
//
// Usage: registries <LOCAL_REGISTRY_HOST> <proxy name>=<project path>...
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/openova-io/openova/core/controllers/pkg/upstream"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: registries <LOCAL_REGISTRY_HOST> <proxy name>=<project path>...")
		os.Exit(2)
	}
	projects := map[string]string{}
	for _, a := range os.Args[2:] {
		name, path, ok := strings.Cut(a, "=")
		if !ok || name == "" {
			fmt.Fprintf(os.Stderr, "registries: %q is not <proxy name>=<project path>\n", a)
			os.Exit(2)
		}
		projects[name] = path
	}
	v := upstream.Variables{LocalRegistryHost: os.Args[1], Consume: "local", ProxyProjects: projects}
	b, err := v.RegistriesYAML("", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(b); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
