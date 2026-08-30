package huawei

import (
	"fmt"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/window"
)

// The hour-slice window math moved to internal/window (#6723 lane D) so the
// OpenOva platform collector slices usage windows EXACTLY like this cloud
// collector (ADR-0014 D3a) — one implementation, re-exported here so every
// existing caller and test keeps its name. What stays in this file is the
// Huawei-specific part: the resource-kind → SKU mapping.

// Transition is a point in a resource's life (see window.Transition).
type Transition = window.Transition

// Lifecycle is the window-math input for one resource (see window.Lifecycle).
type Lifecycle = window.Lifecycle

// Slice is one hour-bounded billing interval (see window.Slice).
type Slice = window.Slice

// HourSlices splits the resource's life inside [from, to) into hour-bounded,
// single-status slices. See window.HourSlices.
func HourSlices(from, to time.Time, lc Lifecycle) []Slice {
	return window.HourSlices(from, to, lc)
}

// stateAt returns the status/flavor in force at t. See window.StateAt.
func stateAt(t time.Time, lc Lifecycle, trs []Transition) (string, string) {
	return window.StateAt(t, lc, trs)
}

// IsStopped reports whether an ECS status means the instance is powered off.
func IsStopped(status string) bool {
	return window.IsStopped(status)
}

// Quantity rounds hours × multiplier to the 6 decimals usage_records store.
func Quantity(hours, multiplier float64) float64 {
	return window.Quantity(hours, multiplier)
}

// MergeTransition folds an exact-time CTS transition into the observed list.
// See window.MergeTransition.
func MergeTransition(existing []Transition, ev Transition, tolerance time.Duration) []Transition {
	return window.MergeTransition(existing, ev, tolerance)
}

// AdoptObserved attributes an observed change to a preceding CTS transition
// within tolerance. See window.AdoptObserved.
func AdoptObserved(existing []Transition, obs Transition, tolerance time.Duration) []Transition {
	return window.AdoptObserved(existing, obs, tolerance)
}

// SKU is one billable line a resource produces per slice.
type SKU struct {
	Name       string
	Unit       string
	Multiplier float64 // quantity = hours × Multiplier
}

// SKUsFor maps a resource kind + attributes (+ the flavor in force for ECS)
// to the SKUs of the price book.
func SKUsFor(kind string, attrs map[string]any, flavor string) []SKU {
	switch kind {
	case KindECS:
		if flavor == "" {
			flavor = str(attrs["flavor"])
		}
		if flavor == "" {
			flavor = "unknown"
		}
		return []SKU{{Name: "ecs." + flavor, Unit: "instance-hour", Multiplier: 1}}
	case KindEVS:
		size := num(attrs["size_gb"])
		class := "hdd"
		vt := strings.ToUpper(str(attrs["volume_type"]))
		if strings.Contains(vt, "SSD") || strings.HasPrefix(vt, "GP") || strings.HasPrefix(vt, "ESSD") {
			class = "ssd"
		}
		return []SKU{{Name: "evs." + class + ".gb", Unit: "gb-hour", Multiplier: size}}
	case KindEIP:
		out := []SKU{{Name: "eip", Unit: "hour", Multiplier: 1}}
		if bw := num(attrs["bandwidth_mbps"]); bw > 0 {
			out = append(out, SKU{Name: "eip.bandwidth_mbps", Unit: "mbps-hour", Multiplier: bw})
		}
		return out
	case KindELB:
		return []SKU{{Name: "elb", Unit: "hour", Multiplier: 1}}
	case KindNAT:
		spec := str(attrs["spec"])
		if spec == "" {
			spec = "unknown"
		}
		return []SKU{{Name: "nat." + spec, Unit: "hour", Multiplier: 1}}
	}
	return nil
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		var f float64
		if _, err := fmt.Sscanf(x, "%g", &f); err == nil {
			return f
		}
	}
	return 0
}
