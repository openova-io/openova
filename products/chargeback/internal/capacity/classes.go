package capacity

import (
	"fmt"
	"sort"
	"strings"
)

// WHAT A POOL CAN ENFORCE (founder direction 2026-09-20).
//
// The three classes need three different things from the substrate a pool
// runs on, and a class nothing can enforce is a label, not a product:
//
//	guaranteed  a fixed allocation. Every substrate has it.
//	spot        the right to DELETE the resource, with notice. The platform
//	            creates and deletes every resource it sells, so every
//	            substrate has this too — spot is a cheaper price plus a
//	            reclaim right, and neither asks the host for anything.
//	burstable   the HOST throttling a resource at runtime, so a burst can use
//	            idle cycles and is clawed back when a guarantee wants them.
//	            Kubernetes the platform operates does this (requests below
//	            limits, CPU shares). A resold cloud flavour with a fixed vCPU
//	            count does not: nothing there would ever pull a burst back.
//
// So the set of classes is a PROPERTY OF THE POOL, stated once, and a
// placement may only use a class its pool lists. The console offers only
// those, which is what stops an operator selling a class nobody can deliver.

// DefaultPoolClasses is what a new pool enforces until the operator says
// otherwise: the two classes every substrate can honour. Burstable is opted
// into, never assumed.
var DefaultPoolClasses = []string{ClassGuaranteed, ClassSpot}

// NormClasses validates a pool's class set and returns it in the canonical
// order (the order of Classes), lower-cased, without duplicates. An empty set
// is an error: a pool that enforces nothing can sell nothing.
func NormClasses(in []string) ([]string, error) {
	seen := map[string]bool{}
	for _, c := range in {
		k := strings.ToLower(strings.TrimSpace(c))
		if k == "" {
			continue
		}
		if !ValidClass(k) {
			return nil, fmt.Errorf("class %q is not one of %s", c, strings.Join(ClassKeys(), ", "))
		}
		seen[k] = true
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("a pool enforces at least one class: one of %s", strings.Join(ClassKeys(), ", "))
	}
	out := make([]string, 0, len(seen))
	for _, c := range ClassKeys() {
		if seen[c] {
			out = append(out, c)
		}
	}
	return out, nil
}

// HasClass reports whether a class set lists a class.
func HasClass(set []string, class string) bool {
	for _, c := range set {
		if c == class {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// which class a RUNNING resource is
// ---------------------------------------------------------------------------

// ClassTagKey is the tag a resource carries to say which class it was sold
// at: lifecycle=spot on the instance, set where it is created. The values are
// the class keys.
const ClassTagKey = "lifecycle"

// Where a running resource's class came from.
const (
	// ClassFromOverride: an operator said so for this one resource.
	ClassFromOverride = "override"
	// ClassFromTag: the resource's own lifecycle tag said so.
	ClassFromTag = "tag"
	// ClassFromDefault: nothing said, so it counts at the most conservative
	// class its SKU is placed at.
	ClassFromDefault = "default"
)

// ClassResolution is the class one running resource counts at, and why.
type ClassResolution struct {
	Class  string
	Source string
	// Asked is set when an override or a tag named a class the SKU is NOT
	// placed at here. The resource still counts — at Class — and the console
	// names the disagreement instead of absorbing it.
	Asked string
}

// ResolveClass decides which class a running resource counts at, given the
// classes its SKU is placed at where it runs.
//
//	override, when it names a placed class
//	else the lifecycle tag, when it names a placed class
//	else the MOST CONSERVATIVE placed class: guaranteed before burstable
//	before spot
//
// The default leans conservative on purpose. An untagged resource counted as
// spot would understate the hardware that is physically committed; counted
// as guaranteed it can only overstate it, and an overstated wall is an early
// order, never an oversold guarantee.
//
// ok is false when the SKU is placed at no class at all.
func ResolveClass(override, tag string, placed []string) (ClassResolution, bool) {
	var fallback string
	for _, c := range ClassKeys() {
		if HasClass(placed, c) {
			fallback = c
			break
		}
	}
	if fallback == "" {
		return ClassResolution{}, false
	}
	asked := ""
	for _, cand := range []struct{ value, source string }{{override, ClassFromOverride}, {tag, ClassFromTag}} {
		k := strings.ToLower(strings.TrimSpace(cand.value))
		if !ValidClass(k) {
			continue
		}
		if HasClass(placed, k) {
			return ClassResolution{Class: k, Source: cand.source}, true
		}
		if asked == "" {
			asked = k
		}
	}
	return ClassResolution{Class: fallback, Source: ClassFromDefault, Asked: asked}, true
}

// ---------------------------------------------------------------------------
// SKU families
// ---------------------------------------------------------------------------

// A placement names ONE SKU, or a FAMILY: a dotted prefix ending in ".*".
// "ecs.m7n.*" places every m7n flavour on a pool in one row, which matters
// because a Sovereign meters whatever flavours its customers run and nobody
// can type a row per flavour before the first of them is billed.
//
// An exact placement always wins over a family, and the longest family wins
// over a shorter one, so "ecs.*" on a general pool and "ecs.m7n.*" on the m7n
// pool compose the way they read.

// IsFamily reports whether a placement SKU is a family pattern.
func IsFamily(sku string) bool { return strings.HasSuffix(strings.TrimSpace(sku), ".*") }

// FamilyPrefix is the literal prefix a family matches on, including the dot:
// "ecs.m7n.*" → "ecs.m7n.". "" when sku is not a family.
func FamilyPrefix(sku string) string {
	s := strings.ToLower(strings.TrimSpace(sku))
	if !strings.HasSuffix(s, ".*") {
		return ""
	}
	return strings.TrimSuffix(s, "*")
}

// ValidPlacementSKU reports whether s is a SKU or a well-formed family. A
// bare "*" or ".*" is refused: a family that matches everything is a pool
// that consumes every resource of every product, which nobody means.
func ValidPlacementSKU(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\n") {
		return false
	}
	if !strings.Contains(s, "*") {
		return true
	}
	p := FamilyPrefix(s)
	return len(p) > 1 && !strings.Contains(p, "*")
}

// MatchFamily reports whether a metered SKU belongs to a family.
func MatchFamily(family, sku string) bool {
	p := FamilyPrefix(family)
	return p != "" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(sku)), p)
}

// BestMatches picks, from the placement SKUs present somewhere, the ones
// that apply to a metered SKU: the exact one when there is one, otherwise the
// longest matching family. It returns the placement SKU that won, or "".
func BestMatches(placementSKUs []string, sku string) string {
	want := strings.ToLower(strings.TrimSpace(sku))
	best := ""
	for _, p := range placementSKUs {
		k := strings.ToLower(strings.TrimSpace(p))
		if k == want {
			return p
		}
		if MatchFamily(k, want) && len(FamilyPrefix(k)) > len(FamilyPrefix(best)) {
			best = p
		}
	}
	return best
}

// Family is one family the console offers, with how many known SKUs it takes.
type Family struct {
	Pattern string `json:"pattern"`
	SKUs    int    `json:"skus"`
}

// familyDepth is how many dotted segments a family offered by the console may
// have: "ecs.*" and "ecs.m7n.*", never "ecs.m7n.2xlarge.*". A real price book
// carries well over a hundred SKUs, and every deeper prefix of every one of
// them is a family in the strict sense — seventy-five of them on the National
// Cloud list — which is a list nobody can choose from. A service and a series
// are the two levels hardware is actually bought and pooled at. (A deeper
// family still WORKS as a placement; it is only not offered.)
const familyDepth = 2

// Families lists the families worth offering for a set of known SKUs: every
// FIRST-level prefix whatever it takes (evs.* is a family of one today and the
// obvious thing to place), and every second-level prefix that takes at least
// two SKUs and fewer than its parent — a series that IS its whole service
// says nothing the service does not. Sorted by pattern.
func Families(skus []string) []Family {
	count := map[string]int{}
	parent := map[string]string{}
	for _, sku := range skus {
		parts := strings.Split(strings.ToLower(strings.TrimSpace(sku)), ".")
		if len(parts) < 2 {
			continue
		}
		for i := 1; i < len(parts) && i <= familyDepth; i++ {
			pattern := strings.Join(parts[:i], ".") + ".*"
			count[pattern]++
			if i > 1 {
				parent[pattern] = strings.Join(parts[:i-1], ".") + ".*"
			}
		}
	}
	out := []Family{}
	for pattern, n := range count {
		up, nested := parent[pattern]
		if nested && (n < 2 || n == count[up]) {
			continue
		}
		out = append(out, Family{Pattern: pattern, SKUs: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pattern < out[j].Pattern })
	return out
}
