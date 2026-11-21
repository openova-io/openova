package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tree of the repair: each pack states its node interface MTU once, the
// registry reads it, Hetzner writes CILIUM_MTU, Huawei and local take the
// default of the kit.
func goodTree() map[string]string {
	return map[string]string{
		"infra/providers/_shared/cloudinit-control-plane.tftpl": "node_interface_mtu = 1\n",
		"infra/providers/_contract/contract.json":               "{}\n",
		"infra/providers/huawei/main.tf": `locals {
  # node_interface_mtu = 1420 in a comment is not read
  node_interface_mtu = 1500

  network_registry = {
    podSupernet      = local.pod_supernet
    mtu              = local.node_interface_mtu # the registry
  }
}
`,
		"infra/providers/huawei/provider-substitutes.yaml.tftpl": "# CILIUM_MTU: \"1420\" in a comment is not read\nHUAWEI_HR_SUSPEND: \"false\"\n",
		"infra/providers/hetzner/main.tf": `locals {
  node_interface_mtu = 1450
  network_registry = {
    mtu = local.node_interface_mtu
  }
}
`,
		"infra/providers/hetzner/provider-substitutes.yaml.tftpl": "HCLOUD_HR_SUSPEND: \"false\"\nCILIUM_MTU: \"1450\"\n",
		"infra/providers/local/main.tf": `locals {
  pack_substitutes = {
    CILIUM_LB_IPAM_ENABLED = "true"
  }
}
locals {
  node_interface_mtu = 1500
  network_registry = {
    mtu              = local.node_interface_mtu
  }
}
`,
		kitPath:   "    cilium:\n      # MTU: ${CILIUM_MTU:=1420} in a comment is not read\n      MTU: ${CILIUM_MTU:=1500}\n",
		chartPath: "cilium:\n  # MTU: 1420\n  MTU: 1500\n",
	}
}

// The tree of main before the repair (21 Nov 2026).
func mainTree() map[string]string {
	t := goodTree()
	t["infra/providers/huawei/main.tf"] = "locals {\n  network_registry_mtu = 1420\n  network_registry = {\n    mtu              = local.network_registry_mtu\n  }\n}\n"
	t["infra/providers/hetzner/main.tf"] = "locals {\n  network_registry = {\n    mtu              = 1420\n  }\n}\n"
	t["infra/providers/hetzner/provider-substitutes.yaml.tftpl"] = "HCLOUD_HR_SUSPEND: \"false\"\n"
	t["infra/providers/local/main.tf"] = "locals {\n  network_registry = {\n    mtu              = 1420\n  }\n}\n"
	t[kitPath] = "      MTU: ${CILIUM_MTU:=1420}\n"
	t[chartPath] = "  MTU: 1420\n"
	return t
}

func write(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, text := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func runOn(t *testing.T, files map[string]string) (int, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := run([]string{"-root", write(t, files)}, &out, &errw)
	return code, out.String() + errw.String()
}

// edit replaces old by new in one file of the good tree; old must be there,
// so that no case passes because its edit did nothing.
func edit(t *testing.T, file, old, new string) map[string]string {
	t.Helper()
	tree := goodTree()
	text, ok := tree[file]
	if !ok {
		t.Fatalf("no file %s in the tree", file)
	}
	if old == "" {
		tree[file] = text + new
		return tree
	}
	if !strings.Contains(text, old) {
		t.Fatalf("%s does not hold %q", file, old)
	}
	tree[file] = strings.Replace(text, old, new, 1)
	return tree
}

func TestTheRepairPasses(t *testing.T) {
	code, out := runOn(t, goodTree())
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
	for _, want := range []string{
		"OK interface hetzner: node interface MTU 1450",
		"OK interface huawei: node interface MTU 1500",
		"OK interface local: node interface MTU 1500",
		"OK default: the slot default and the chart default are 1500, the node interface MTU of every pack that writes no CILIUM_MTU",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
}

func TestMainBeforeTheRepairFails(t *testing.T) {
	code, out := runOn(t, mainTree())
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out)
	}
	for _, want := range []string{
		"FAIL interface infra/providers/huawei/main.tf: states no local node_interface_mtu",
		"FAIL registry infra/providers/huawei/main.tf:4: mtu is local.network_registry_mtu, not local.node_interface_mtu",
		"FAIL registry infra/providers/hetzner/main.tf:3: mtu is 1420, not local.node_interface_mtu",
		"FAIL interface infra/providers/local/main.tf: states no local node_interface_mtu",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
}

// With the interface MTU stated, the old defaults of main fail by value.
func TestTheDefaultsOfMainFailByValue(t *testing.T) {
	tree := goodTree()
	tree[kitPath] = "      MTU: ${CILIUM_MTU:=1420}\n"
	tree[chartPath] = "  MTU: 1420\n"
	code, out := runOn(t, tree)
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out)
	}
	for _, want := range []string{
		"FAIL default " + kitPath + ":1: the default of CILIUM_MTU is 1420, but huawei writes no CILIUM_MTU and its node interface MTU is 1500",
		"FAIL default " + kitPath + ":1: the default of CILIUM_MTU is 1420, but local writes no CILIUM_MTU and its node interface MTU is 1500",
		"FAIL default " + chartPath + ":1: the chart default MTU is 1420, but huawei writes no CILIUM_MTU and its node interface MTU is 1500",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
}

func TestEachRuleCanFail(t *testing.T) {
	hz := "infra/providers/hetzner/provider-substitutes.yaml.tftpl"
	hw := "infra/providers/huawei/main.tf"
	lo := "infra/providers/local/main.tf"
	cases := []struct {
		name, file, old, new string
		want                 string
	}{
		{"the kit default is 1420", kitPath, "${CILIUM_MTU:=1500}", "${CILIUM_MTU:=1420}",
			"FAIL default " + kitPath + ":3: the default of CILIUM_MTU is 1420, the chart default MTU (" + chartPath + ":3) is 1500"},
		{"the chart default is 1420", chartPath, "  MTU: 1500", "  MTU: 1420",
			"FAIL default " + chartPath + ":3: the chart default MTU is 1420, but huawei writes no CILIUM_MTU"},
		{"the kit default is a subtraction", kitPath, "${CILIUM_MTU:=1500}", "${CILIUM_MTU:=1500-80}",
			"FAIL subtraction " + kitPath + ":3: the default of CILIUM_MTU is written as a subtraction (1500-80)"},
		{"the chart default is a subtraction", chartPath, "  MTU: 1500", "  MTU: 1500 - 80",
			"FAIL subtraction " + chartPath + ":3: the chart default MTU is written as a subtraction"},
		{"the interface MTU is a subtraction", hw, "node_interface_mtu = 1500", "node_interface_mtu = 1500 - 80",
			"FAIL subtraction " + hw + ":3: node_interface_mtu is written as a subtraction (1500 - 80)"},
		{"the registry subtracts", hw, "mtu              = local.node_interface_mtu", "mtu              = local.node_interface_mtu - 80",
			"FAIL subtraction " + hw + ":7: mtu is written as a subtraction"},
		{"the registry is a literal", lo, "mtu              = local.node_interface_mtu", "mtu              = 1500",
			"FAIL registry " + lo + ":9: mtu is 1500, not local.node_interface_mtu"},
		{"the registry is absent", lo, "    mtu              = local.node_interface_mtu\n", "",
			"FAIL registry " + lo + ": the network registry states no mtu"},
		{"Hetzner writes no CILIUM_MTU", hz, "CILIUM_MTU: \"1450\"\n", "",
			"FAIL default " + kitPath + ":3: the default of CILIUM_MTU is 1500, but hetzner writes no CILIUM_MTU and its node interface MTU is 1450"},
		{"Hetzner writes the overhead off", hz, "CILIUM_MTU: \"1450\"", "CILIUM_MTU: \"1370\"",
			"FAIL pack " + hz + ":2: CILIUM_MTU is 1370, the node interface MTU of hetzner is 1450"},
		{"Hetzner writes a subtraction", hz, "CILIUM_MTU: \"1450\"", "CILIUM_MTU: \"${node_interface_mtu - 80}\"",
			"FAIL subtraction " + hz + ":2: CILIUM_MTU is written as a subtraction"},
		{"Hetzner writes a template variable", hz, "CILIUM_MTU: \"1450\"", "CILIUM_MTU: \"${node_interface_mtu}\"",
			"FAIL pack " + hz + ":2: CILIUM_MTU is \"\\\"${node_interface_mtu}\\\"\", not a plain integer"},
		{"Hetzner writes CILIUM_MTU twice", hz, "", "CILIUM_MTU: \"1450\"\n",
			"FAIL pack " + hz + ":3: the pack writes CILIUM_MTU 2 times"},
		{"the interface MTU is stated twice", hw, "  node_interface_mtu = 1500\n", "  node_interface_mtu = 1500\n  node_interface_mtu = 1500\n",
			"FAIL interface " + hw + ":4: node_interface_mtu is stated 2 times"},
		{"the interface MTU is out of range", hw, "node_interface_mtu = 1500", "node_interface_mtu = 900",
			"FAIL interface " + hw + ":3: node_interface_mtu 900 is not from 1280 to 9000"},
		{"the interface MTU is a reference", hw, "node_interface_mtu = 1500", "node_interface_mtu = var.mtu",
			"FAIL interface " + hw + ":3: node_interface_mtu is \"var.mtu\", not a plain integer"},
		{"the kit pins a literal", kitPath, "MTU: ${CILIUM_MTU:=1500}", "MTU: 1500",
			"FAIL default " + kitPath + ":3: MTU is 1500, not ${CILIUM_MTU:=<n>}"},
		{"the chart sets no MTU", chartPath, "  MTU: 1500\n", "",
			"FAIL default " + chartPath + ": the chart sets no default MTU"},
		{"the kit sets MTU twice", kitPath, "", "      MTU: ${CILIUM_MTU:=1500}\n",
			"FAIL default " + kitPath + ":4: the slot sets MTU 2 times"},
		{"local writes another CILIUM_MTU", lo, "CILIUM_LB_IPAM_ENABLED = \"true\"", "CILIUM_LB_IPAM_ENABLED = \"true\"\n    CILIUM_MTU = \"1400\"",
			"FAIL pack " + lo + ":4: CILIUM_MTU is 1400, the node interface MTU of local is 1500"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out := runOn(t, edit(t, c.file, c.old, c.new))
			if code != 1 {
				t.Fatalf("exit %d, want 1\n%s", code, out)
			}
			if !strings.Contains(out, c.want) {
				t.Fatalf("output lacks %q\n%s", c.want, out)
			}
		})
	}
}

func TestAPackMayWriteTheLocalOrItsValue(t *testing.T) {
	lo := "infra/providers/local/main.tf"
	for _, v := range []string{`tostring(local.node_interface_mtu)`, `"${local.node_interface_mtu}"`, `"1500"`, `1500`} {
		code, out := runOn(t, edit(t, lo, `CILIUM_LB_IPAM_ENABLED = "true"`, "CILIUM_LB_IPAM_ENABLED = \"true\"\n    CILIUM_MTU = "+v))
		if code != 0 {
			t.Errorf("CILIUM_MTU = %s: exit %d, want 0\n%s", v, code, out)
		}
	}
	// The :- form of the substitution is a default too.
	if code, out := runOn(t, edit(t, kitPath, "${CILIUM_MTU:=1500}", "${CILIUM_MTU:-1500}")); code != 0 {
		t.Errorf("${CILIUM_MTU:-1500}: exit %d, want 0\n%s", code, out)
	}
}

func TestNothingToJudgeIsNotAPass(t *testing.T) {
	tree := goodTree()
	for p := range tree {
		if strings.HasPrefix(p, "infra/providers/") && !strings.HasPrefix(p, "infra/providers/_") {
			delete(tree, p)
		}
	}
	if code, out := runOn(t, tree); code != 2 || !strings.Contains(out, "no provider module under infra/providers") {
		t.Fatalf("exit %d, want 2 naming the missing modules\n%s", code, out)
	}
	tree = goodTree()
	delete(tree, kitPath)
	if code, out := runOn(t, tree); code != 2 || !strings.Contains(out, "cannot read "+kitPath) {
		t.Fatalf("exit %d, want 2 naming the slot\n%s", code, out)
	}
}

func TestInteger(t *testing.T) {
	for in, want := range map[string]string{
		`1500`: "", `"1500"`: "", `'1450'`: "", `1500-80`: "subtraction", `"1500 - 80"`: "subtraction",
		`local.x - 80`: "subtraction", `-80`: "not a plain integer", `local.x`: "not a plain integer", `""`: "not a plain integer",
	} {
		if _, why := integer(in); why != want {
			t.Errorf("integer(%s) = %q, want %q", in, why, want)
		}
	}
}
