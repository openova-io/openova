package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// sharedKit is the kit every pack applies unless it has its own.
	sharedKit = "clusters/_template/bootstrap-kit/kustomization.yaml"
	// packKit is the kit of one pack, the path its bootstrap-kit Kustomization
	// is patched to; %s is the pack's folder name.
	packKit = "clusters/_template/providers/%s/kit/kustomization.yaml"
	// baseVars is the value set every kit check renders with; secondaryVars is
	// laid over it for a region of a two-region install.
	baseVars      = "tests/fixtures/variables/today.env"
	secondaryVars = "tests/fixtures/variables/two-region-secondary.env"
)

// unit is one kit judged at one pack's values in one region role.
type unit struct {
	kit, pack, role string
	vars            []string // value files, later files win
}

func (u unit) String() string {
	pack := "pack " + u.pack
	if u.pack == "" {
		pack = "no pack, the base values"
	}
	return fmt.Sprintf("%s (%s, %s)", u.kit, pack, u.role)
}

var (
	// KEY: "value" of a provider-substitutes.yaml.tftpl.
	reYAMLEntry = regexp.MustCompile(`^([A-Z][A-Z0-9_]*):\s*"([^"]*)"\s*$`)
	// KEY = "value" inside the block pack_substitutes of a main.tf.
	reHCLEntry = regexp.MustCompile(`^\s*"?([A-Z][A-Z0-9_]*)"?\s*=\s*"([^"]*)"\s*$`)
	reHCLBlock = regexp.MustCompile(`^\s*pack_substitutes\s*=`)
)

// plan lists the units of a tree: for each pack (a folder infra/providers/<p>
// with a pack.yaml) the kit it applies at its values, and each kit that no
// pack applies at the base values alone; each in the roles primary and
// secondary. The packs' value files are written into work.
func plan(root, work string) ([]unit, error) {
	for _, v := range []string{baseVars, secondaryVars} {
		if st, err := os.Stat(filepath.Join(root, v)); err != nil || !st.Mode().IsRegular() {
			return nil, fmt.Errorf("the value set %s is not a file of the tree", v)
		}
	}
	if st, err := os.Stat(filepath.Join(root, sharedKit)); err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("the shared kit %s is not a file of the tree", sharedKit)
	}
	kits := []string{sharedKit}
	own, err := filepath.Glob(filepath.Join(root, fmt.Sprintf(packKit, "*")))
	if err != nil {
		return nil, err
	}
	for _, k := range own {
		rel, err := filepath.Rel(root, k)
		if err != nil {
			return nil, err
		}
		kits = append(kits, filepath.ToSlash(rel))
	}
	sort.Strings(kits[1:])
	packs, err := filepath.Glob(filepath.Join(root, "infra", "providers", "*", "pack.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(packs)
	applied := map[string]bool{}
	var out []unit
	add := func(kit, pack, file string) {
		applied[kit] = true
		for _, role := range []string{"primary", "secondary"} {
			vars := []string{filepath.Join(root, baseVars)}
			if role == "secondary" {
				vars = append(vars, filepath.Join(root, secondaryVars))
			}
			if file != "" {
				vars = append(vars, file)
			}
			out = append(out, unit{kit: kit, pack: pack, role: role, vars: vars})
		}
	}
	for _, p := range packs {
		name := filepath.Base(filepath.Dir(p))
		if strings.HasPrefix(name, "_") {
			continue
		}
		kit := sharedKit
		if st, err := os.Stat(filepath.Join(root, fmt.Sprintf(packKit, name))); err == nil && st.Mode().IsRegular() {
			kit = fmt.Sprintf(packKit, name)
		}
		entries, err := packSubstitutes(filepath.Join(root, "infra", "providers", name))
		if err != nil {
			return nil, err
		}
		file := ""
		if len(entries) > 0 {
			file = filepath.Join(work, "pack-"+name+".env")
			if err := os.WriteFile(file, []byte(strings.Join(entries, "\n")+"\n"), 0o600); err != nil {
				return nil, err
			}
		}
		add(kit, name, file)
	}
	for _, k := range kits {
		if !applied[k] {
			add(k, "", "")
		}
	}
	return out, nil
}

// packSubstitutes reads the literal substitutes of one pack, KEY=value each:
// the entries KEY: "value" of its provider-substitutes.yaml.tftpl, then the
// entries KEY = "value" of the block pack_substitutes of its main.tf (a later
// entry of a key wins). A value the pack computes (one that holds ${ or %{, or
// is not a quoted string) is left out, so the base value stands for it.
func packSubstitutes(dir string) ([]string, error) {
	var out []string
	b, err := os.ReadFile(filepath.Join(dir, "provider-substitutes.yaml.tftpl")) // #nosec G304 -- a file of the tree
	switch {
	case err == nil:
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			if m := reYAMLEntry.FindStringSubmatch(sc.Text()); m != nil && literal(m[2]) {
				out = append(out, m[1]+"="+m[2])
			}
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	b, err = os.ReadFile(filepath.Join(dir, "main.tf")) // #nosec G304 -- a file of the tree
	switch {
	case err == nil:
		in, opened, depth := false, false, 0
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			line := sc.Text()
			if !in {
				if !reHCLBlock.MatchString(line) {
					continue
				}
				in, opened, depth = true, false, 0
			}
			if m := reHCLEntry.FindStringSubmatch(line); m != nil && literal(m[2]) {
				out = append(out, m[1]+"="+m[2])
			}
			opened = opened || strings.Contains(line, "{")
			depth += strings.Count(line, "{") - strings.Count(line, "}")
			if opened && depth <= 0 {
				in = false
			}
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	return out, nil
}

func literal(v string) bool { return !strings.Contains(v, "${") && !strings.Contains(v, "%{") }
