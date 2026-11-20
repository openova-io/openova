package main

import (
	"fmt"
	"strconv"
	"strings"
)

// version is a provider version major.minor.patch. A listed version always has
// three parts; a constraint may give one, two or three.
type version [3]int

func (v version) String() string { return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]) }

func (v version) cmp(o version) int {
	for i := range v {
		if v[i] != o[i] {
			if v[i] < o[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// parseVersion reads 1, 1.2 or 1.2.3 (missing parts are 0) and returns how many
// parts were written. A pre-release or build suffix is not read: no listed
// version has one.
func parseVersion(s string) (version, int, error) {
	var v version
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) == 0 || len(parts) > 3 {
		return v, 0, fmt.Errorf("version %q is not major[.minor[.patch]]", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" || (len(p) > 1 && p[0] == '0') {
			return v, 0, fmt.Errorf("version %q is not major[.minor[.patch]]", s)
		}
		v[i] = n
	}
	return v, len(parts), nil
}

// term is one comparison of a constraint: op is one of = != > >= < <= ~>.
type term struct {
	op    string
	v     version
	parts int
}

// parseConstraint reads a version constraint as OpenTofu writes it: terms
// separated by commas, each an operator (= when absent) and a version.
func parseConstraint(s string) ([]term, error) {
	var out []term
	for _, raw := range strings.Split(s, ",") {
		t := strings.TrimSpace(raw)
		if t == "" {
			return nil, fmt.Errorf("constraint %q has an empty term", s)
		}
		op := "="
		for _, o := range []string{"~>", ">=", "<=", "!=", ">", "<", "="} {
			if strings.HasPrefix(t, o) {
				op, t = o, strings.TrimSpace(t[len(o):])
				break
			}
		}
		v, n, err := parseVersion(t)
		if err != nil {
			return nil, fmt.Errorf("constraint %q: %v", s, err)
		}
		out = append(out, term{op: op, v: v, parts: n})
	}
	return out, nil
}

// meets reports whether v meets every term. ~> lets only the right-most written
// part grow: ~> 1.2 is >= 1.2.0 and < 2.0.0, ~> 1.2.3 is >= 1.2.3 and < 1.3.0,
// ~> 1 is >= 1.0.0 and < 2.0.0.
func meets(v version, terms []term) bool {
	for _, t := range terms {
		c := v.cmp(t.v)
		ok := false
		switch t.op {
		case "=":
			ok = c == 0
		case "!=":
			ok = c != 0
		case ">":
			ok = c > 0
		case ">=":
			ok = c >= 0
		case "<":
			ok = c < 0
		case "<=":
			ok = c <= 0
		case "~>":
			upper := version{t.v[0] + 1, 0, 0}
			if t.parts == 3 {
				upper = version{t.v[0], t.v[1] + 1, 0}
			}
			ok = c >= 0 && v.cmp(upper) < 0
		}
		if !ok {
			return false
		}
	}
	return true
}
