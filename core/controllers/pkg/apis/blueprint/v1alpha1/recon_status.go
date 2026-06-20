package v1alpha1

// recon_status.go — #3969 §7.4: the ONE placement status, derived from the
// per-target HR readback rollup using the recon vocabulary already used
// across the console (Reconciled / Reconciling / Degraded + reason). A
// mismatch is NEVER rendered as a second contradictory value ("effective
// singleton" / "mandate unbuilt") — it is just "not Reconciled yet,
// reason: …". This file deliberately carries NO effectiveClass, observed,
// or mandate concept — those are deleted by this ticket.

// ReconStatus is the single placement status value (#3969 §7.4).
type ReconStatus string

const (
	// ReconStatusReconciled — every target has converged.
	ReconStatusReconciled ReconStatus = "Reconciled"
	// ReconStatusReconciling — at least one target has not converged yet
	// (still within its budget).
	ReconStatusReconciling ReconStatus = "Reconciling"
	// ReconStatusDegraded — at least one target failed / passed its budget.
	ReconStatusDegraded ReconStatus = "Degraded"
)

// ObservedTarget is the per-target readback rollup (#3969 §7.4): the
// desired target identity plus the observed readiness/degraded signal the
// controller derived from that target's HR `status.conditions[Ready]`.
type ObservedTarget struct {
	Region      string      `json:"region"`
	Cluster     string      `json:"cluster,omitempty"`
	Role        DataRole    `json:"role"`
	StandbyType StandbyType `json:"standbyType,omitempty"`
	// Ready — the target's HR reported Ready=True.
	Ready bool `json:"ready"`
	// Degraded — the target's HR reported a hard failure (Ready=False with
	// a terminal reason, or past its reconcile budget).
	Degraded bool `json:"degraded,omitempty"`
}

// PlacementStatus is the §7.4 status block: ONE status value, an optional
// plain reason, and the per-target rollup. There is no second class.
type PlacementStatus struct {
	Placement ReconStatus      `json:"placement"`
	Reason    string           `json:"reason,omitempty"`
	Targets   []ObservedTarget `json:"targets,omitempty"`
}

// DeriveReconStatus rolls the per-target readback up into the ONE recon
// status (#3969 §7.4). The rule:
//   - any target Degraded                 -> Degraded (reason names it);
//   - all targets Ready (and ≥1 observed) -> Reconciled;
//   - otherwise                           -> Reconciling (reason names the
//     first not-yet-ready target).
//
// The reason is a PLAIN, operator-readable string — never a second class.
// It NEVER fabricates an "effective" or "observed" class.
func DeriveReconStatus(observed []ObservedTarget) PlacementStatus {
	st := PlacementStatus{Targets: observed}
	if len(observed) == 0 {
		// No targets observed yet — the controller has not reported a
		// rollout state. Honest "Reconciling" with a plain reason.
		st.Placement = ReconStatusReconciling
		st.Reason = "no target status reported yet"
		return st
	}

	var allReady = true
	var firstNotReady string
	for _, t := range observed {
		if t.Degraded {
			st.Placement = ReconStatusDegraded
			st.Reason = describeTarget(t) + " is degraded"
			return st
		}
		if !t.Ready {
			allReady = false
			if firstNotReady == "" {
				firstNotReady = describeTarget(t)
			}
		}
	}
	if allReady {
		st.Placement = ReconStatusReconciled
		return st
	}
	st.Placement = ReconStatusReconciling
	st.Reason = firstNotReady + " is reconciling"
	return st
}

// describeTarget renders a plain target label for a status reason, e.g.
// "region-b Standby·Hot" — never a class.
func describeTarget(t ObservedTarget) string {
	s := t.Region
	switch t.Role {
	case DataRolePrimary:
		s += " Primary"
	case DataRoleStandby:
		s += " Standby"
		if t.StandbyType != "" {
			s += "·" + string(t.StandbyType)
		}
	}
	return s
}
