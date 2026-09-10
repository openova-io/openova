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

// SKUEIPBandwidth bills the RESERVED size of a pipe, per Mbps per hour. It
// is what dominates the bill on a Sovereign whose reserved bandwidth is
// about forty times its compute.
const (
	SKUEIPBandwidth  = "eip.bandwidth_mbps"
	UnitEIPBandwidth = "mbps-hour"
)

// BillsTraffic reports whether the cloud bills this address (or pipe) by the
// traffic that crossed it rather than by the size it reserved. Unknown —
// which is what an older gateway that does not report a charge mode gives —
// is deliberately NOT traffic: the reservation keeps billing, because
// guessing "traffic" on an address that really reserved a pipe would drop a
// real charge off the bill and nothing would look wrong.
func BillsTraffic(attrs map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(str(attrs[attrChargeMode])), ChargeModeTraffic)
}

// SharesBandwidth reports whether the address hangs off a pipe shared with
// others, in which case the reservation is billed against the pipe.
func SharesBandwidth(attrs map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(str(attrs[attrShareType])), ShareTypeWhole)
}

// BandwidthIDOf is the pipe an address (or a shared-pipe resource) belongs
// to; empty when the gateway reported none, in which case its traffic
// cannot be sampled and its reservation keeps billing.
func BandwidthIDOf(attrs map[string]any) string {
	return strings.TrimSpace(str(attrs[attrBandwidthID]))
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
		// An address always costs its hourly address fee. What it costs on
		// top of that is EITHER the pipe it reserves OR the traffic that
		// crossed it — never both (#6867):
		//
		//   traffic-billed  → no reservation line at all; the hourly
		//                     SKUEIPTrafficGB records the CES sampler writes
		//                     are the meter.
		//   shared pipe     → the reservation belongs to the pipe's own
		//                     KindBandwidth resource, which bills it ONCE
		//                     however many addresses hang off it. Billing it
		//                     here too would charge the same pipe once per
		//                     address.
		//   otherwise       → the reserved size, exactly as before. That is
		//                     also the case when the gateway reported no
		//                     charge mode, so an address whose shape is
		//                     unknown is never silently un-billed.
		out := []SKU{{Name: "eip", Unit: "hour", Multiplier: 1}}
		if BillsTraffic(attrs) || SharesBandwidth(attrs) {
			return out
		}
		if bw := num(attrs["bandwidth_mbps"]); bw > 0 {
			out = append(out, SKU{Name: SKUEIPBandwidth, Unit: UnitEIPBandwidth, Multiplier: bw})
		}
		return out
	case KindBandwidth:
		// The shared pipe itself. It carries no address fee — each attached
		// address pays its own — and it is traffic-billed or size-billed on
		// the same either/or rule.
		if BillsTraffic(attrs) {
			return nil
		}
		if bw := num(attrs["bandwidth_mbps"]); bw > 0 {
			return []SKU{{Name: SKUEIPBandwidth, Unit: UnitEIPBandwidth, Multiplier: bw}}
		}
		return nil
	case KindELB:
		return []SKU{{Name: "elb", Unit: "hour", Multiplier: 1}}
	case KindNAT:
		spec := str(attrs["spec"])
		if spec == "" {
			spec = "unknown"
		}
		return []SKU{{Name: "nat." + spec, Unit: "hour", Multiplier: 1}}

	// #6853 — the remaining provisionable kinds. Each derives a SKU that is
	// stable for the resource's billable shape, so the price book can carry a
	// rate for it before the first one is ever created.
	case KindRDS, KindDDS, KindGaussDB:
		// The catalog prices a managed database by engine, flavor and whether
		// it is a single instance or a primary-standby pair, and prices its
		// storage separately per GB. Both lines are emitted so a pair is not
		// billed as a single, and storage is not billed as compute.
		engine := strings.ToLower(str(attrs["engine"]))
		if engine == "" {
			engine = "unknown"
		}
		flavor := str(attrs["flavor"])
		if flavor == "" {
			flavor = "unknown"
		}
		// The catalog prices a single instance and a primary-standby pair
		// differently, so the mode is part of the SKU. Huawei reports "Ha"
		// for a pair; normalise both spellings to one token or the same
		// deployment bills under two different SKUs.
		mode := strings.ToLower(str(attrs["mode"]))
		switch mode {
		case "ha", "primary-standby", "replicaset", "replica":
			mode = "ha"
		default:
			mode = "single"
		}
		// A Huawei DB flavor_ref already carries kind+engine
		// ("rds.mysql.c7.large.2"), so prepending them again would yield
		// "rds.mysql.rds.mysql.c7.large.2" — a SKU no price list can match.
		base := flavor
		if !strings.HasPrefix(base, kind+".") {
			base = kind + "." + engine + "." + flavor
		}
		out := []SKU{{Name: base + "." + mode, Unit: "instance-hour", Multiplier: 1}}
		if gb := num(attrs["size_gb"]); gb > 0 {
			out = append(out, SKU{Name: kind + ".storage." + mode + ".gb", Unit: "gb-hour", Multiplier: gb})
		}
		return out
	case KindCBR:
		return []SKU{{Name: "cbr.gb", Unit: "gb-hour", Multiplier: num(attrs["size_gb"])}}
	case KindCCE:
		flavor := str(attrs["flavor"])
		if flavor == "" {
			flavor = "unknown"
		}
		return []SKU{{Name: "cce." + flavor, Unit: "cluster-hour", Multiplier: 1}}
	case KindIMS:
		return []SKU{{Name: "ims.gb", Unit: "gb-hour", Multiplier: num(attrs["size_gb"])}}
	case KindVPC:
		return []SKU{{Name: "vpc", Unit: "hour", Multiplier: 1}}
	case KindDNS:
		return []SKU{{Name: "dns", Unit: "hour", Multiplier: 1}}
	case KindWAF:
		return []SKU{{Name: "waf", Unit: "hour", Multiplier: 1}}
	case KindAS:
		return []SKU{{Name: "as", Unit: "hour", Multiplier: 1}}
	case KindVPCEP:
		return []SKU{{Name: "vpcep", Unit: "hour", Multiplier: 1}}
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
