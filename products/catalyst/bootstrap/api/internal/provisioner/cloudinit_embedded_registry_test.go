// cloudinit_embedded_registry_test.go - SOV-136: the nodes of a region share
// the images they hold.
//
// When one worker is drained, its Pods are created again on other nodes, and
// every image those nodes do not hold is pulled. After the cutover a node may
// pull only from the Sovereign's own registry, so a Pod of the registry's own
// pull path (its front, say) that lands on a node without its image waits
// forever, and every Pod behind it waits too, while the drained node, back
// within minutes, still holds every image they need.
//
// k3s carries a distributed registry mirror (Spegel): with --embedded-registry
// on the server, every node of the cluster serves the images of its containerd
// store to the other nodes (a client certificate of the cluster CA, a
// preshared key of the server), and containerd asks those peers first for
// every name that has an entry in registries.yaml, and the configured
// endpoints after them. An image that no node holds still comes from the
// region's registry.
//
// These tests hold the four facts that make it work:
//  1. the one k3s server line of first boot passes --embedded-registry, once,
//     and keeps --disable-default-registry-endpoint (the mirror adds a source,
//     it never brings the public fallback back);
//  2. no worker template passes it (an agent refuses the flag; agents take the
//     setting from the server);
//  3. the k3s release every provider pins has the flag (it arrived in
//     v1.29.1);
//  4. the registries.yaml of every cloud provider has the wildcard entry "*",
//     so every name is shared, not only the listed ones.
package provisioner

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const embeddedRegistryFlag = "--embedded-registry"

// readProviderFile reads one file of infra/providers.
func readProviderFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(filepath.Dir(modulePath(t)), rel)))
	if err != nil {
		t.Fatalf("read infra/providers/%s: %v", rel, err)
	}
	return string(raw)
}

// providerGlob lists the files of infra/providers that match pattern,
// relative to infra/providers.
func providerGlob(t *testing.T, pattern string) []string {
	t.Helper()
	dir := filepath.Dir(modulePath(t))
	found, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	var rel []string
	for _, f := range found {
		r, err := filepath.Rel(dir, f)
		if err != nil {
			t.Fatalf("rel %s: %v", f, err)
		}
		rel = append(rel, r)
	}
	return rel
}

// codeLines drops the comment lines, as the provider modules do before the
// render goes to the cloud (the pattern ^[ ]*#( |$)).
func codeLines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		s := strings.TrimLeft(l, " ")
		if s == "#" || strings.HasPrefix(s, "# ") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// serverFlags returns the words of INSTALL_K3S_EXEC="server ..." of every
// code line of text that starts k3s as a server.
func serverFlags(text string) [][]string {
	var lines [][]string
	for _, l := range codeLines(text) {
		_, rest, ok := strings.Cut(l, `INSTALL_K3S_EXEC="server `)
		if !ok {
			continue
		}
		value, _, _ := strings.Cut(rest, `"`)
		lines = append(lines, strings.Fields(value))
	}
	return lines
}

func count(words []string, w string) int {
	n := 0
	for _, x := range words {
		if x == w {
			n++
		}
	}
	return n
}

// TestEmbeddedRegistry_ServerLineEnablesIt: fact 1.
func TestEmbeddedRegistry_ServerLineEnablesIt(t *testing.T) {
	tpl := readProviderFile(t, filepath.Join("_shared", "cloudinit-control-plane.tftpl"))
	lines := serverFlags(tpl)
	if len(lines) != 1 {
		t.Fatalf("SOV-136: the shared control-plane template must start k3s as a server on exactly one line, found %d", len(lines))
	}
	words := lines[0]
	if n := count(words, embeddedRegistryFlag); n != 1 {
		t.Errorf("SOV-136: the k3s server line must pass %s exactly once (found %d): without it no node serves its images to the others, and a Pod a drain moves waits on the registry", embeddedRegistryFlag, n)
	}
	if n := count(words, "--disable-default-registry-endpoint"); n != 1 {
		t.Errorf("SOV-136: the k3s server line must keep --disable-default-registry-endpoint exactly once (found %d): the shared mirror adds the peers, it never brings back the public fallback (SOV-132)", n)
	}
	n := 0
	for _, l := range codeLines(tpl) {
		n += strings.Count(l, "embedded-registry")
	}
	if n != 1 {
		t.Errorf("SOV-136: embedded-registry must occur on the server line only, once, in the code of the template (found %d): no k3s config file may set it a second time", n)
	}
}

// TestEmbeddedRegistry_NoAgentLineCarriesIt: fact 2.
func TestEmbeddedRegistry_NoAgentLineCarriesIt(t *testing.T) {
	workers := providerGlob(t, filepath.Join("*", "cloudinit-worker.tftpl"))
	if len(workers) < 2 {
		t.Fatalf("SOV-136: expected a worker template for every provider under infra/providers, found %v", workers)
	}
	for _, w := range workers {
		text := readProviderFile(t, w)
		if !regexp.MustCompile(`(?m)\bagent\b`).MatchString(strings.Join(codeLines(text), "\n")) {
			t.Errorf("SOV-136: %s does not start a k3s agent", w)
		}
		for _, l := range codeLines(text) {
			if strings.Contains(l, "embedded-registry") {
				t.Errorf("SOV-136: %s passes embedded-registry to a k3s agent, which refuses it; agents take the setting from the server", w)
			}
		}
	}
}

// TestEmbeddedRegistry_PinnedK3sHasTheFlag: fact 3.
func TestEmbeddedRegistry_PinnedK3sHasTheFlag(t *testing.T) {
	block := regexp.MustCompile(`(?s)variable "k3s_version" \{.*?\n  default *= *"v(\d+)\.(\d+)\.(\d+)\+k3s\d+"`)
	files := providerGlob(t, filepath.Join("*", "variables.tf"))
	pinned := 0
	for _, f := range files {
		text := readProviderFile(t, f)
		if !strings.Contains(text, `variable "k3s_version"`) {
			continue
		}
		m := block.FindStringSubmatch(text)
		if m == nil {
			t.Errorf("SOV-136: %s declares k3s_version without a default of the form vX.Y.Z+k3sN", f)
			continue
		}
		pinned++
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		patch, _ := strconv.Atoi(m[3])
		if major*1000000+minor*1000+patch < 1029001 {
			t.Errorf("SOV-136: %s pins k3s v%s.%s.%s, older than v1.29.1, the first release with %s", f, m[1], m[2], m[3], embeddedRegistryFlag)
		}
	}
	if pinned < 2 {
		t.Errorf("SOV-136: expected the k3s pin of every provider under infra/providers, found %d", pinned)
	}
}

// TestEmbeddedRegistry_EveryNameIsMirrored: fact 4.
func TestEmbeddedRegistry_EveryNameIsMirrored(t *testing.T) {
	local := regexp.MustCompile(`(?m)^  registry_mirror_yaml_[a-z0-9]+ = jsonencode\(\{`)
	wildcard := `{ "*" = { endpoint = [`
	clouds := 0
	for _, f := range providerGlob(t, filepath.Join("*", "main.tf")) {
		text := readProviderFile(t, f)
		loc := local.FindStringIndex(text)
		if loc == nil {
			continue
		}
		clouds++
		body, _, _ := strings.Cut(text[loc[1]:], "\n  })\n")
		if !strings.Contains(body, wildcard) {
			t.Errorf("SOV-136: the registries.yaml of %s has no wildcard entry %q: the peers share only the names that have an entry", f, wildcard)
		}
	}
	if clouds < 2 {
		t.Errorf("SOV-136: expected the registries.yaml of every cloud provider under infra/providers, found %d", clouds)
	}
}
