// INF-G01 — THE BSS DATABASE IS BACK WITHIN THE LIMIT AFTER A NODE DRAIN.
//
// Gate check INF-G01 drains a worker and gives every controller of a moved
// Pod 180 s to have its Ready Pods back. On hw307 (23 Nov 2026, record
// docs/ledger/acceptance/hw307-20261123T180938Z-b16e.json, step INF-G01.11)
// Cluster chargeback/chargeback-pg had 0 of 1 Ready Pods 191 s after its Pod
// left the worker. It was the only database of that worker with one
// instance: slot 13f never passed ${SOVEREIGN_CNPG_INSTANCES}, the count
// every other kit slot with a database of its own reads (2 on a two-region
// Sovereign). One instance makes a drain a full stop (smart shutdown, volume
// move, start); two make it a switchover.
//
// The test pins the slot on its values, then substitutes the slot file the
// way Flux does and renders the chart with the slot's cnpg values for both
// topologies: two regions give two instances spread by node with CNPG's
// budgets kept; one region gives one instance without a budget (#7922).
package bootstrapkit

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// cbdVariable is one Flux post-build variable: ${NAME}, ${NAME:=d},
// ${NAME:-d}, ${NAME=d} or ${NAME-d}.
var cbdVariable = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::?[=-]([^}]*))?\}`)

// cbdSubstitute gives each variable its value in vars, else its default,
// else the empty text.
func cbdSubstitute(s string, vars map[string]string) string {
	return cbdVariable.ReplaceAllStringFunc(s, func(m string) string {
		sub := cbdVariable.FindStringSubmatch(m)
		if v, ok := vars[sub[1]]; ok && v != "" {
			return v
		}
		return sub[2]
	})
}

// cbdSlotCnpg returns the cnpg values of HelmRelease bp-chargeback of slot
// 13f after Flux substitution with vars.
func cbdSlotCnpg(t *testing.T, root string, vars map[string]string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "clusters", "_template", "bootstrap-kit", "13f-bp-chargeback.yaml")) //nolint:gosec // a slot file of this repository, named by the test
	if err != nil {
		t.Fatalf("read slot 13f: %v", err)
	}
	dec := yaml.NewDecoder(strings.NewReader(cbdSubstitute(string(raw), vars)))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc == nil || doc["kind"] != "HelmRelease" || cbDig(doc, "metadata", "name") != "bp-chargeback" {
			continue
		}
		cnpg, _ := cbDig(doc, "spec", "values", "cnpg").(map[string]any)
		return cnpg
	}
	t.Fatal("slot 13f: HelmRelease bp-chargeback not found")
	return nil
}

// cbdRender is helm template that decodes the standard output only: a
// warning helm writes to its error output is not part of the render.
func cbdRender(t *testing.T, helmBin, dir string, args ...string) []map[string]any {
	t.Helper()
	cmd := exec.Command(helmBin, append([]string{"template"}, args...)...) //nolint:gosec // helm on PATH or HELM_BIN, arguments fixed by the test
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template %s %v: %v\n%s", dir, args, err, stderr.String())
	}
	var docs []map[string]any
	dec := yaml.NewDecoder(strings.NewReader(string(out)))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc != nil {
			docs = append(docs, doc)
		}
	}
	return docs
}

func TestBootstrapKit_ChargebackDatabaseSurvivesADrain(t *testing.T) {
	root := repoRoot(t)

	// ── The slot, on its values (pure YAML, always runs) ─────────────────
	cb := cbLoadHR(t, root, "13f-bp-chargeback.yaml", "bp-chargeback")
	if got, _ := cbDig(cb, "spec", "values", "cnpg", "cluster", "instances").(string); !strings.Contains(got, "${SOVEREIGN_CNPG_INSTANCES") {
		t.Errorf("slot 13f values.cnpg.cluster.instances = %q, want the ${SOVEREIGN_CNPG_INSTANCES} substitution: with one instance a drain of its node is a full stop, and INF-G01 found chargeback-pg 0 of 1 Ready 191 s after its Pod left the worker", got)
	}
	if got := cbDig(cb, "spec", "values", "cnpg", "cluster", "affinity", "topologyKey"); got != "kubernetes.io/hostname" {
		t.Errorf("slot 13f values.cnpg.cluster.affinity.topologyKey = %v, want kubernetes.io/hostname: the release lives in one regional cluster whose nodes share one region label, so the chart's region default lets both instances share the node a drain empties", got)
	}

	// ── The chart, with the substituted slot values, both topologies ─────
	helmBin := os.Getenv("HELM_BIN")
	if helmBin == "" {
		helmBin = "helm"
	}
	if _, err := exec.LookPath(helmBin); err != nil {
		t.Skipf("helm not on PATH (%v): the slot values above still ran", err)
	}
	cbDir := filepath.Join(root, "products", "chargeback", "chart")
	ensureChartDeps(t, helmBin, cbDir) // the docrender sub-chart, file://, charts/ is gitignored

	for _, tc := range []struct {
		topology, count string
		instances       int
	}{
		{"two regions", "2", 2},
		{"one region", "1", 1},
		{"variable unset", "", 1},
	} {
		t.Run(tc.topology, func(t *testing.T) {
			cnpg := cbdSlotCnpg(t, root, map[string]string{"SOVEREIGN_CNPG_INSTANCES": tc.count})
			if cnpg == nil {
				t.Fatal("slot 13f carries no cnpg values: the chart default of one instance holds")
			}
			b, err := yaml.Marshal(map[string]any{"cnpg": cnpg})
			if err != nil {
				t.Fatal(err)
			}
			values := filepath.Join(t.TempDir(), "values.yaml")
			if err := os.WriteFile(values, b, 0o600); err != nil {
				t.Fatal(err)
			}
			docs := cbdRender(t, helmBin, cbDir, "chargeback", ".", "-n", "chargeback",
				"--api-versions", "postgresql.cnpg.io/v1", "-f", values)
			cl := cbFind(docs, "Cluster", "", "chargeback-pg")
			if cl == nil {
				t.Fatal("no CNPG Cluster chargeback-pg in the render")
			}
			if got, _ := cbDig(cl, "spec", "instances").(int); got != tc.instances {
				t.Errorf("spec.instances = %v, want %d", cbDig(cl, "spec", "instances"), tc.instances)
			}
			spec, _ := cbDig(cl, "spec").(map[string]any)
			pdb, hasPDB := spec["enablePDB"]
			affinity := cbDig(cl, "spec", "affinity", "topologyKey")
			if tc.instances >= 2 {
				if hasPDB {
					t.Errorf("spec.enablePDB = %v with %d instances, want it absent: CNPG's budget on the primary is what makes it switch over before the drain evicts it", pdb, tc.instances)
				}
				if affinity != "kubernetes.io/hostname" {
					t.Errorf("spec.affinity.topologyKey = %v, want kubernetes.io/hostname: two instances on one node are both moved by one drain", affinity)
				}
				return
			}
			if pdb != false {
				t.Errorf("spec.enablePDB = %v with one instance, want false: a budget on the only Pod blocks every drain of its node (#7922)", pdb)
			}
			if affinity != nil {
				t.Errorf("spec.affinity.topologyKey = %v with one instance, want no affinity block (the single-region render stays as it was)", affinity)
			}
		})
	}
}
