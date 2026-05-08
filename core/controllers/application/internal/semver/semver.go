// Package semver — minimal semver-range parser used by the
// application-controller to validate `Application.spec.blueprintRef.version`.
//
// The CRD's structural schema already pins this to `MAJOR.MINOR.PATCH`
// with an optional pre-release suffix (`^[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.-]+)?$`).
// We add the additional constraint that the controller resolves
// `MAJOR.MINOR.x` and `MAJOR.x` ranges as it does in slice C3
// (blueprint-controller) so the `upgrades.from[]` matrix shares a
// common parser.
//
// Per slice C3 brief and the CC1 consolidation roadmap, this file
// MIRRORS `core/controllers/blueprint/internal/semver/semver.go`. Any
// behavior change here must land in both copies until CC1 promotes
// the package to `core/controllers/internal/semver/`.
package semver

import (
	"fmt"
	"strconv"
	"strings"
)

// IsValidRange reports whether s is a syntactically-valid semver range
// per the limited grammar shipped by the platform.
func IsValidRange(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("empty range")
	}
	parts := strings.Fields(s)
	if len(parts) > 1 {
		for _, p := range parts {
			if err := validAtom(p); err != nil {
				return fmt.Errorf("compound range %q: atom %q: %w", s, p, err)
			}
		}
		return nil
	}
	return validAtom(s)
}

// IsExact reports whether s is an exact `MAJOR.MINOR.PATCH` version
// (with optional pre-release suffix). The CRD pattern enforces this
// shape on Application.spec.blueprintRef.version; the controller calls
// IsExact() during semantic validation to catch mismatches the schema
// wouldn't (e.g. range-form snuck in via a server-side patch).
func IsExact(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	core := s
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		core = s[:i]
	}
	segs := strings.Split(core, ".")
	if len(segs) != 3 {
		return false
	}
	for _, seg := range segs {
		if seg == "" {
			return false
		}
		if _, err := strconv.ParseUint(seg, 10, 32); err != nil {
			return false
		}
	}
	return true
}

func validAtom(s string) error {
	if s == "" {
		return fmt.Errorf("empty atom")
	}
	rest := s
	switch {
	case strings.HasPrefix(s, "^"):
		rest = s[1:]
	case strings.HasPrefix(s, "~"):
		rest = s[1:]
	case strings.HasPrefix(s, ">="):
		rest = s[2:]
	case strings.HasPrefix(s, "<="):
		rest = s[2:]
	case strings.HasPrefix(s, ">"):
		rest = s[1:]
	case strings.HasPrefix(s, "<"):
		rest = s[1:]
	case strings.HasPrefix(s, "="):
		rest = s[1:]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return fmt.Errorf("operator without version in %q", s)
	}
	core := rest
	if i := strings.IndexAny(rest, "-+"); i >= 0 {
		core = rest[:i]
		suffix := rest[i+1:]
		if suffix == "" {
			return fmt.Errorf("empty pre-release/build suffix in %q", s)
		}
		for _, seg := range strings.Split(suffix, ".") {
			if seg == "" {
				return fmt.Errorf("empty pre-release segment in %q", s)
			}
			for _, r := range seg {
				if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && r != '-' {
					return fmt.Errorf("invalid pre-release character %q in %q", r, s)
				}
			}
		}
	}
	segs := strings.Split(core, ".")
	if len(segs) < 1 || len(segs) > 3 {
		return fmt.Errorf("expected 1..3 dotted components in %q, got %d", s, len(segs))
	}
	for i, seg := range segs {
		if seg == "" {
			return fmt.Errorf("empty version segment in %q (index %d)", s, i)
		}
		if seg == "x" || seg == "X" || seg == "*" {
			continue
		}
		if _, err := strconv.ParseUint(seg, 10, 32); err != nil {
			return fmt.Errorf("non-numeric version segment %q in %q", seg, s)
		}
	}
	return nil
}
