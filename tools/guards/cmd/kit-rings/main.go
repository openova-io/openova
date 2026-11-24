// Command kit-rings writes the list of kit rings that the installation wizard
// shows: products/catalyst/bootstrap/ui/src/pages/wizard/steps/componentGroups.generated.ts.
//
// The kit (clusters/_template/bootstrap-kit) installs every release of every ring; a ring is switched off only
// after provisioning (docs/adr/0020, S12). The wizard therefore lists, read-only, the eight rings in their fixed
// order and under each ring the kit releases that carry its label catalyst.openova.io/ring, in install order.
// foundation and provider are required: they are never switched off.
//
// Input: the catalogue that python3 scripts/generate-catalog.py writes,
// products/catalyst/bootstrap/api/internal/catalog/blueprints.json: its bootstrapKit (id and ring of each kit
// slot, in install order) and its blueprints (title, class and lifecycle of each Blueprint). And the name of every
// chart: the top-level name: of each platform/*/chart/Chart.yaml and products/*/chart/Chart.yaml. A kit slot is
// listed only when its Blueprint is in the catalogue and a chart has its name; a Blueprint without a chart never
// appears in the list. The same file carries COMPONENT_LOGOS: the vendored logo file under
// products/catalyst/bootstrap/ui/public/component-logos of each catalogue Blueprint that has one, keyed by the
// Blueprint name without bp- (an SVG before a PNG of the same name). Run it after the catalogue generator:
//
//	sh tools/guards/cmd/kit-rings/write.sh           write the list
//	sh tools/guards/cmd/kit-rings/write.sh --check   write nothing; exit 1 when the committed list differs
//
// Exit codes: 0 written or in sync; 1 --check found the committed list stale; 2 an input is wrong: the catalogue
// or the logo folder cannot be read, or a kit slot has no ring or a ring that is not one of the eight.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	catalogPath = "products/catalyst/bootstrap/api/internal/catalog/blueprints.json"
	outPath     = "products/catalyst/bootstrap/ui/src/pages/wizard/steps/componentGroups.generated.ts"
	logoDir     = "products/catalyst/bootstrap/ui/public/component-logos"
	command     = "sh tools/guards/cmd/kit-rings/write.sh"
)

// logos returns one line per catalogue Blueprint with a vendored logo, sorted
// by name: the SVG when both an SVG and a PNG exist. The folder must exist: a
// missing one is an input error, never an empty map of letter marks.
func logos(root string, known map[string]int) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, logoDir))
	if err != nil {
		return nil, fmt.Errorf("%s: the vendored logo folder cannot be read: %w", logoDir, err)
	}
	pick := map[string]string{}
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		stem := strings.TrimSuffix(e.Name(), ext)
		if e.IsDir() || (ext != ".svg" && ext != ".png") {
			continue
		}
		if _, ok := known["bp-"+stem]; ok && (pick[stem] == "" || ext == ".svg") {
			pick[stem] = e.Name()
		}
	}
	var out []string
	for stem, file := range pick {
		out = append(out, fmt.Sprintf("  %s: %s,\n", strconv.Quote(stem), strconv.Quote(file)))
	}
	sort.Strings(out)
	return out, nil
}

// rings is the fixed table of docs/adr/0020 (S12): name, title, required.
var rings = []struct {
	name, title string
	required    bool
}{
	{"foundation", "Foundation", true},
	{"provider", "Provider", true},
	{"observability", "Observability", false},
	{"security-scanning", "Security scanning", false},
	{"access", "Access", false},
	{"ai", "AI", false},
	{"commerce", "Commerce", false},
	{"operations", "Operations", false},
}

type catalog struct {
	Blueprints []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Class     string `json:"class"`
		Lifecycle string `json:"lifecycle"`
	} `json:"blueprints"`
	BootstrapKit []struct {
		ID   string  `json:"id"`
		File string  `json:"file"`
		Ring *string `json:"ring"`
	} `json:"bootstrapKit"`
}

var chartName = regexp.MustCompile(`(?m)^name:[ \t]*["']?([^"'\s#]+)`)

// charts returns the names that the charts of platform/* and products/* declare.
func charts(root string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, top := range []string{"platform", "products"} {
		files, err := filepath.Glob(filepath.Join(root, top, "*", "chart", "Chart.yaml"))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			b, err := os.ReadFile(f) // #nosec G304 -- a Chart.yaml of the repository
			if err != nil {
				return nil, err
			}
			if m := chartName.FindSubmatch(b); m != nil {
				out[string(m[1])] = true
			}
		}
	}
	return out, nil
}

// render returns the text of the list, or the errors that stop it.
func render(root string) (string, []string) {
	raw, err := os.ReadFile(filepath.Join(root, catalogPath)) // #nosec G304 -- the catalogue of the repository
	if err != nil {
		return "", []string{fmt.Sprintf("%s: %v; run python3 scripts/generate-catalog.py first", catalogPath, err)}
	}
	var c catalog
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", []string{fmt.Sprintf("%s: not the catalogue: %v", catalogPath, err)}
	}
	if len(c.BootstrapKit) == 0 {
		return "", []string{catalogPath + ": bootstrapKit is empty; the list would be empty"}
	}
	have, err := charts(root)
	if err != nil {
		return "", []string{err.Error()}
	}
	known := map[string]bool{}
	for _, r := range rings {
		known[r.name] = true
	}
	byID := map[string]int{}
	for i, b := range c.Blueprints {
		byID[b.ID] = i
	}
	var errs []string
	per := map[string][]string{}
	for _, s := range c.BootstrapKit {
		if s.Ring == nil || !known[*s.Ring] {
			got := "none"
			if s.Ring != nil {
				got = strconv.Quote(*s.Ring)
			}
			errs = append(errs, fmt.Sprintf("%s: kit slot %s has ring %s; it must be one of the eight rings of docs/adr/0020", catalogPath, s.File, got))
			continue
		}
		i, ok := byID[s.ID]
		if !ok || !have[s.ID] {
			continue // no Blueprint in the catalogue, or no chart: never listed
		}
		b := c.Blueprints[i]
		per[*s.Ring] = append(per[*s.Ring], fmt.Sprintf("      { blueprint: %s, title: %s, class: %s, lifecycle: %s },\n",
			strconv.Quote(b.ID), strconv.Quote(b.Title), strconv.Quote(b.Class), strconv.Quote(b.Lifecycle)))
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return "", errs
	}
	logoLines, err := logos(root, byID)
	if err != nil {
		return "", []string{err.Error()}
	}
	var w strings.Builder
	w.WriteString(head)
	for _, r := range rings {
		fmt.Fprintf(&w, "  {\n    name: %s,\n    title: %s,\n    required: %t,\n    releases: [\n", strconv.Quote(r.name), strconv.Quote(r.title), r.required)
		for _, line := range per[r.name] {
			w.WriteString(line)
		}
		w.WriteString("    ],\n  },\n")
	}
	w.WriteString("]\n" + logoHead)
	for _, line := range logoLines {
		w.WriteString(line)
	}
	w.WriteString("}\n")
	return w.String(), nil
}

func run(args []string, root string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("kit-rings", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.Bool("check", false, "write nothing; exit 1 when the committed list differs")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	text, errs := render(root)
	if len(errs) > 0 {
		for _, e := range errs {
			_, _ = fmt.Fprintln(stderr, "kit-rings: "+e)
		}
		return 2
	}
	target := filepath.Join(root, outPath)
	if *check {
		old, err := os.ReadFile(target) // #nosec G304 -- the committed list
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			_, _ = fmt.Fprintln(stderr, "kit-rings: "+err.Error())
			return 2
		}
		if !bytes.Equal(old, []byte(text)) {
			_, _ = fmt.Fprintf(stderr, "kit-rings: %s is stale; run %s and commit the result\n", outPath, command)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "kit-rings: %s is in sync\n", outPath)
		return 0
	}
	err := os.WriteFile(target, []byte(text), 0o644) // #nosec G306 -- a committed file of the repository
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "kit-rings: "+err.Error())
		return 2
	}
	_, _ = fmt.Fprintf(stdout, "kit-rings: wrote %s\n", outPath)
	return 0
}

// repoRoot is the repository root: two folders above tools/guards, where go run starts.
func repoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func main() {
	os.Exit(run(os.Args[1:], repoRoot(), os.Stdout, os.Stderr))
}

const head = `// AUTO-GENERATED — do not edit by hand.
//
// Generated by ` + command + ` (tools/guards/cmd/kit-rings) from the bootstrap kit and
// the catalogue that python3 scripts/generate-catalog.py writes
// (products/catalyst/bootstrap/api/internal/catalog/blueprints.json) and the names of the charts under
// platform/*/chart and products/*/chart. Run it after the catalogue generator and commit the result.
//
// The eight rings of the kit (docs/adr/0020, S12) in their fixed order, each with the kit releases that
// carry its label catalyst.openova.io/ring, in install order. Every ring is installed; a ring can be
// switched off only after provisioning, and foundation and provider (required) never are. A kit slot
// whose Blueprint has no chart, or is not in the catalogue, is not listed. COMPONENT_LOGOS: the files of
// products/catalyst/bootstrap/ui/public/component-logos whose name is a catalogue Blueprint.

import type { BlueprintClass, BlueprintLifecycle } from '@/shared/constants/catalog.generated'

export interface KitRelease {
  /** Blueprint name, e.g. "bp-cilium". */
  blueprint: string
  /** Display title of the Blueprint (card.title). */
  title: string
  class: BlueprintClass
  lifecycle: BlueprintLifecycle
}

export interface KitRing {
  /** The value of the label catalyst.openova.io/ring. */
  name: string
  title: string
  /** true for foundation and provider, which are never switched off. */
  required: boolean
  /** The kit releases of the ring, in install order. */
  releases: readonly KitRelease[]
}

export const RINGS: readonly KitRing[] = [
`

const logoHead = `
/** The vendored logo file of each catalogue Blueprint that has one, under public/component-logos, keyed by
 *  the Blueprint name without bp- (an SVG before a PNG of the same name). */
export const COMPONENT_LOGOS: Readonly<Record<string, string>> = {
`
