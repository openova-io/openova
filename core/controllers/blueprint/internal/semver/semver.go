// Package semver — minimal in-tree semver-range parser.
//
// Per slice C3 brief: "Use a small in-tree validator — do NOT add a new
// go.mod dep just for this." The blueprint-controller validates
// `Blueprint.spec.upgrades.from[]` entries which are documented in
// docs/BLUEPRINT-AUTHORING.md §3 to take forms like:
//
//   1.2.x        — wildcard at the patch level
//   1.x          — wildcard at the minor level
//   ^1.4         — caret range (compatible with 1.4.0, < 2.0.0)
//   ~1.4         — tilde range (compatible with 1.4.0, < 1.5.0)
//   >=1.0.0 <2   — bounded compound range
//   1.0.0        — exact version
//
// Existing 61 blueprint.yaml files in the monorepo use only:
//
//   - "0.x" (most common — appearing in cilium, cnpg, keycloak, ...)
//   - "1.x", "1.0.x", "1.1.x"
//   - "^1.0", "^1.4"
//   - exact "1.0.0"
//
// We support the union of those plus `~MAJOR.MINOR` and bare
// `MAJOR.MINOR.PATCH` for completeness. Anything else returns a
// validation error rather than silently accepting it — the controller
// surfaces the error as a Pending condition with reason
// "InvalidUpgradeRange".
package semver

import (
	"fmt"
	"strconv"
	"strings"
)

// IsValidRange reports whether s is a syntactically-valid semver range
// string per the limited grammar documented in the package doc.
// Whitespace around s is trimmed; an empty string returns
// (false, error).
//
// We deliberately do NOT validate that the constraints are
// internally-consistent (e.g. ">=2 <1" is unsatisfiable but parses).
// The controller's job is to reject syntactic garbage; semantic
// reachability of an upgrade path is enforced at install time by
// `application-controller` (slice C4).
func IsValidRange(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("empty range")
	}

	// Compound range — every space-separated atom must parse.
	// Per BLUEPRINT-AUTHORING.md §10 examples + node-semver convention.
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

// validAtom validates a single range atom. See package doc for the
// grammar. Returns nil on success, error with the offending input
// otherwise.
func validAtom(s string) error {
	if s == "" {
		return fmt.Errorf("empty atom")
	}

	// Strip operator prefix.
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

	// Strip pre-release / build suffix per semver §10/11. We only
	// validate that what remains before any '-' or '+' is dotted
	// digits-or-x; the suffix itself is permissive (alnum + dot + dash).
	core := rest
	if i := strings.IndexAny(rest, "-+"); i >= 0 {
		core = rest[:i]
		// Validate suffix loosely: each component must be alnum or
		// hyphen, separated by dots. Reject empty suffix or stray
		// dots. (Accepts forms like "1.0.0-rc.1", "1.0.0-beta-2",
		// "1.0.0+build.5".)
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
		// "x" or "X" wildcard allowed at any position. Strict semver
		// requires the wildcard to be at the trailing position only,
		// but the existing blueprint corpus has no leading-wildcard
		// usage so we err loose-side and accept "x.x.x" / "1.x.0".
		if seg == "x" || seg == "X" || seg == "*" {
			continue
		}
		if _, err := strconv.ParseUint(seg, 10, 32); err != nil {
			return fmt.Errorf("non-numeric version segment %q in %q", seg, s)
		}
	}
	return nil
}
