// Package transition is the ADR-0015 D8 topology-transition phase machine.
//
// A topology transition moves the Primary of a group of Applications (an
// Application plus the Applications it declares in spec.dependsOn) from one
// region to another. It is NOTHING but an ordered series of Git commits to the
// per-region files of ADR-0015 D1, each carried out by the native operator the
// file declares (Flux applies it, CNPG demotes / promotes, the chart moves its
// own traffic), each waited on through that operator's OWN status:
//
//	phase         commit (per member)                         wait (native status)
//	traffic-off   old primary's file: primary=<to>            CNPG status.demotionToken in the old region
//	              (dependents first — stop traffic top-down)  (stateless: HelmRelease applied + Ready)
//	data-primary  new primary's file: primary=<to>            CNPG promoted (status.lastPromotionToken ==
//	              + database.topology.promotionToken=<token>  token) + HelmRelease applied + Ready
//	              (backing services first — state bottom-up)
//	peers         every other region's file: primary=<to>     HelmRelease applied (when reachable)
//	traffic-on    — (traffic followed the chart's rendering)  new region serving: HelmRelease Ready, DB
//	              (restored last, bottom-up)                  primary Ready, Application Serving not False
//
// Two things outside a transition can break a phase, and both are handled
// here, level-triggered like the rest:
//
//   - a demotion that does not complete (the old primary's instance restarts
//     while CNPG applies it and comes back as a standby with no token) is
//     issued again: traffic-off is run a second time (Demotion);
//   - a move and the reboot agent (kured) take turns: a move does not start
//     until each region of the move holds its reboot agent, and no step runs
//     while that agent drains a node of those regions, each wait bounded by
//     Policy.RebootWait (gate.go). The hold itself is taken by each region's
//     controller (internal/reboot).
//
// The machine is PURE: given the group, the persisted transition record, the
// region files and the native observations it returns at most ONE commit and
// the updated record. The caller (internal/controller) owns I/O, the lease and
// the clock. Every evaluation is level-triggered: re-running it on the same
// inputs yields the same answer, so a restarted (or newly elected) actor in
// any region resumes exactly where the previous one stopped.
package transition

import (
	"time"

	"github.com/openova-io/openova/core/controllers/pkg/regionfile"
)

// Phases, in execution order.
const (
	PhaseTrafficOff  = "traffic-off"
	PhaseDataPrimary = "data-primary"
	PhasePeers       = "peers"
	PhaseTrafficOn   = "traffic-on"
)

// Phases is the execution order.
var Phases = []string{PhaseTrafficOff, PhaseDataPrimary, PhasePeers, PhaseTrafficOn}

// Activity states — the exact ADR-0015 D7 status.activity[].state vocabulary.
const (
	StatePending   = "pending"
	StateRunning   = "running"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
)

// Outcomes of one evaluation.
const (
	// OutcomeConverged — every member's files name the desired primary and the
	// new primary serves. Nothing to do.
	OutcomeConverged = "Converged"
	// OutcomeTransitioning — a phase is in progress (a commit was returned or
	// a native wait is not yet satisfied, within its deadline).
	OutcomeTransitioning = "Transitioning"
	// OutcomeFailed — a phase passed its deadline. The machine keeps
	// re-evaluating (it resumes by itself if the native condition later
	// holds), but the group reports failure: no partial claim of success.
	OutcomeFailed = "Failed"
	// OutcomeBlocked — the inputs do not describe a transition the machine
	// may perform (members disagree on the desired primary, a member has no
	// file in the target region, region files disagree with no record that
	// explains it). Nothing is committed.
	OutcomeBlocked = "Blocked"
	// OutcomePending — the group has no region files yet (first install has
	// not been committed); there is no primary to move.
	OutcomePending = "Pending"
)

// Tri is a three-valued native signal. Unknown is never rounded up.
type Tri int

const (
	Unknown Tri = iota
	True
	False
)

func (t Tri) String() string {
	switch t {
	case True:
		return "True"
	case False:
		return "False"
	}
	return "Unknown"
}

// MarshalJSON encodes a Tri as "True" / "False" / "Unknown".
func (t Tri) MarshalJSON() ([]byte, error) { return []byte(`"` + t.String() + `"`), nil }

// UnmarshalJSON decodes "True" / "False"; anything else is Unknown.
func (t *Tri) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case `"True"`:
		*t = True
	case `"False"`:
		*t = False
	default:
		*t = Unknown
	}
	return nil
}

// Member is one Application of a group.
type Member struct {
	// Name is the Continuum's name (dr-<app>); DependsOn references it.
	Name string `json:"name"`
	// Namespace of the Application and its Continuum.
	Namespace string `json:"namespace"`
	// App is the Application name — the region file key (regions/<r>/apps/<App>.yaml).
	App string `json:"app"`
	// Stateful members carry a CNPG distributed-topology member per region;
	// their traffic-off / data-primary waits are the CNPG tokens.
	Stateful bool `json:"stateful"`
	// DependsOn names the members this one consumes (its backing services).
	DependsOn []string `json:"dependsOn,omitempty"`
	// Regions are the regions of the member's placement, in placement order.
	Regions []string `json:"regions"`
}

// Key identifies a member inside observations and files: "<ns>/<app>".
func (m Member) Key() string { return m.Namespace + "/" + m.App }

// Policy is the per-group behaviour the Continuum spec declares.
type Policy struct {
	// AutoPromoteOnRegionLoss lets the machine commit the move WITHOUT a
	// demotion token when the old primary's region is unreachable beyond
	// RegionLoss. Default false: a lost region is reported, never acted on.
	AutoPromoteOnRegionLoss bool
	// RegionLoss is how long a region must stay unreachable to count as lost.
	RegionLoss time.Duration
	// PhaseTimeout is how long one wait may run before the phase reports
	// failed.
	PhaseTimeout time.Duration
	// RebootWait bounds how long a move waits for the reboot agent of a
	// region of the move (a node it drains, or the hold it does not yet
	// keep). Past it the move goes on and says so. 0 = DefaultRebootWait.
	RebootWait time.Duration
}

// DefaultRebootWait covers one drain of the reboot agent as bp-kured
// configures it: --drain-timeout 10m + --lock-release-delay 5m, plus 5m for
// the node's restart.
const DefaultRebootWait = 20 * time.Minute

// Re-issuing a demotion that did not complete (see Demotion).
const (
	// DemotionLostAfter is how long the old primary must be seen running no
	// primary, publishing no token and not switching before the demotion is
	// issued again: CNPG publishes the token within seconds of a demotion it
	// carries out, so a minute of that state means it will never come.
	DemotionLostAfter = time.Minute
	// ReissueAttempts bounds how often one member's demotion is issued again
	// within one transition.
	ReissueAttempts = 3
)

// Reasons of the Interrupted condition: what, outside the transition, holds
// it up, and what the machine does about it.
const (
	// ReasonNodeDraining — the reboot agent drains a node of a region of the
	// move; the next step waits (at most Policy.RebootWait).
	ReasonNodeDraining = "NodeDraining"
	// ReasonRebootHoldPending — a region of the move does not hold its reboot
	// agent yet; the move does not start (at most Policy.RebootWait).
	ReasonRebootHoldPending = "RebootHoldPending"
	// ReasonRebootAgentUnknown — the reboot agent of a region of the move
	// could not be read; the next step waits (at most Policy.RebootWait).
	ReasonRebootAgentUnknown = "RebootAgentUnknown"
	// ReasonRebootWaitExceeded — the wait for the reboot agent reached its
	// bound; the move goes on.
	ReasonRebootWaitExceeded = "RebootWaitExceeded"
	// ReasonDemotionInterrupted — the old primary came back as a standby
	// without a demotion token; the demotion is issued again.
	ReasonDemotionInterrupted = "DemotionInterrupted"
	// ReasonDemotionReissueExhausted — the demotion was issued again
	// ReissueAttempts times without a token; nothing more is re-issued.
	ReasonDemotionReissueExhausted = "DemotionReissueExhausted"
)

// Reasons of the RebootAgentHeld condition: whether a move holds the reboot
// agent of a region of it (HoldOf).
const (
	// ReasonHeldForMove — a move of the region is in progress and can still
	// act: the region's reboot agent is held, kured starts no drain there.
	ReasonHeldForMove = "HeldForMove"
	// ReasonReleasedFailedMove — the move has been Failed for longer than
	// Policy.RebootWait: the hold is released, and taken again only if the
	// move acts again (it is still evaluated).
	ReasonReleasedFailedMove = "ReleasedFailedMove"
	// ReasonReleasedReissueExhausted — the demotion was issued again
	// ReissueAttempts times without a token and nothing more is re-issued:
	// the hold is released.
	ReasonReleasedReissueExhausted = "ReleasedReissueExhausted"
)

// RebootHold says whether a move holds the reboot agent of one region of it,
// and why.
type RebootHold struct {
	Held    bool
	Reason  string
	Message string
}

// Interruption is a named reason that holds a transition up from outside it.
type Interruption struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// Demotion is the state of one member's demotion that did not complete and is
// issued again (the traffic-off phase run a second time).
//
// CNPG demotes a running primary and publishes status.demotionToken. An old
// primary whose instance STARTS inside a replica cluster (its Pod re-created
// while the demotion was being applied — a node drain) is demoted at once by
// the instance manager and publishes no token (cloudnative-pg 1.29
// internal/management/controller/instance_startup.go); the operator then sees
// no primary instance and starts no transition (pkg/reconciler/
// replicaclusterswitch, containsPrimaryInstance). The token only a running
// primary's demotion yields is then never published. The machine restores the
// old primary in the old primary's file, waits until its Cluster runs a
// primary again, and commits the demotion once more.
type Demotion struct {
	// LostSince — when the old primary was first seen running no primary
	// with no token and no switch in progress.
	LostSince string `json:"lostSince,omitempty"`
	// Attempts — how often the demotion was issued again.
	Attempts int `json:"attempts,omitempty"`
	// Restoring — the old primary's file names the old primary again; the
	// demotion is committed once more when its Cluster runs a primary.
	Restoring bool `json:"restoring,omitempty"`
	// Exhausted — the demotion was issued again ReissueAttempts times and the
	// old primary is still seen without a primary and without a token:
	// nothing more is re-issued (cleared when that state ends).
	Exhausted bool `json:"exhausted,omitempty"`
}

// DBStatus is a member's CNPG Cluster in one region, read from the Cluster's
// own status by that region's Continuum.
type DBStatus struct {
	Found              bool   `json:"found"`
	Cluster            string `json:"cluster,omitempty"`
	Primary            bool   `json:"primary"`
	Ready              bool   `json:"ready"`
	DemotionToken      string `json:"demotionToken,omitempty"`
	LastPromotionToken string `json:"lastPromotionToken,omitempty"`
	Phase              string `json:"phase,omitempty"`
	// TopologyPrimary is the `openova.topology.primary` the chart rendered
	// onto the Cluster (annotation openova.io/topology-primary) — set by
	// contract charts installed by a bootstrap-kit slot, whose HelmRelease
	// receives the contract through valuesFrom.
	TopologyPrimary string `json:"topologyPrimary,omitempty"`
	// RunningPrimary — whether an instance of the Cluster runs PostgreSQL as
	// a primary, from status.instancesReportedState[].isPrimary (the
	// operator's own reading of each instance; standby.signal absent). True
	// when one does, False when the instances are reported and none does,
	// Unknown when none is reported.
	RunningPrimary Tri `json:"runningPrimary"`
	// Switching — status.switchReplicaClusterStatus.inProgress: CNPG is
	// demoting the Cluster (fenced, waiting for the shutdown to read the
	// token).
	Switching bool `json:"switching,omitempty"`
}

// RebootStatus is a region's reboot agent (kured), read by that region from
// kured's own objects: the lock annotation of its DaemonSet and its nodes.
type RebootStatus struct {
	// Agent — the reboot agent's DaemonSet exists in the region.
	Agent bool `json:"agent"`
	// Holdable — kured takes one lock (--concurrency=1), so one live lock
	// that names no node stops every drain.
	Holdable bool `json:"holdable,omitempty"`
	// Holder is the nodeID of kured's live lock ("" = free).
	Holder string `json:"holder,omitempty"`
	// Held — no drain can start: the live lock names no node of the region
	// (this region's Continuum, the install hold, a person's manual hold).
	Held bool `json:"held,omitempty"`
	// Draining are the nodes kured holds its lock for and has cordoned.
	Draining []string `json:"draining,omitempty"`
	// Unknown — the agent could not be read (its DaemonSet or the nodes: a
	// refusal, a timeout, a 5xx), so whether it exists, holds or drains is
	// not known. It is not "no agent": a move waits on it (Message says why).
	Unknown bool `json:"unknown,omitempty"`
	// Message says why the agent could not be read or held.
	Message string `json:"message,omitempty"`
}

// MemberStatus is one member in one region, as that region observes it.
type MemberStatus struct {
	// Observed — the region's Continuum found the member's HelmRelease.
	Observed bool `json:"observed"`
	// AppliedPrimary is `openova.topology.primary` of the LIVE HelmRelease —
	// i.e. what Flux has actually applied from Git (not what Git says).
	AppliedPrimary string `json:"appliedPrimary,omitempty"`
	// ReleaseReady — the HelmRelease's own Ready=True for its current
	// generation.
	ReleaseReady bool `json:"releaseReady"`
	// ReleaseMessage is the HelmRelease Ready message, verbatim.
	ReleaseMessage string `json:"releaseMessage,omitempty"`
	// DB is the member's CNPG Cluster in this region (stateful members).
	DB DBStatus `json:"db"`
	// Serving is the Application's per-region Serving condition (ADR-0015
	// D7) when the Application is visible in this region; Unknown otherwise.
	Serving Tri `json:"serving"`
}

// RegionView is everything known about one region in this evaluation.
type RegionView struct {
	// Reachable — this region's observations were read in this evaluation.
	Reachable bool
	// LastSeen is when its observations were last read successfully.
	LastSeen time.Time
	// Members by Member.Key().
	Members map[string]MemberStatus
	// Reboot is the region's reboot agent (nil = not reported: a region
	// whose Continuum predates the report, or a reader without the agent).
	Reboot *RebootStatus
}

// FileView is one member's region file as it stands in Git.
type FileView struct {
	Exists bool
	Fields regionfile.TransitionFields
	// Desired is the desiredPrimaryRegion of the Continuum document in the
	// file (written by the application-controller from Application placement).
	Desired string
}

// View is the input of one evaluation.
type View struct {
	Now time.Time
	// Files: member key → region → file.
	Files map[string]map[string]FileView
	// Regions: region id → view.
	Regions map[string]RegionView
}

// Activity is one status.activity[] entry — the exact ADR-0015 D7 shape.
type Activity struct {
	ID         string `json:"id"`
	Phase      string `json:"phase"`
	Member     string `json:"member"`
	State      string `json:"state"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Message    string `json:"message,omitempty"`
}

// Record is the persisted state of one transition (kept in the group's lease
// file in Git, so any region's actor can resume it).
type Record struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	StartedAt string `json:"startedAt"`
	// LossMode — the old primary's region was lost and the policy allowed
	// promotion without a demotion token.
	LossMode bool `json:"lossMode,omitempty"`
	// Tokens: member key → the CNPG demotion token read in traffic-off. Kept
	// so data-primary can commit it even if the old region vanishes after
	// publishing it.
	Tokens map[string]string `json:"tokens,omitempty"`
	// Demotions: member key → its demotion issued again (see Demotion).
	Demotions map[string]Demotion `json:"demotions,omitempty"`
	// RebootWaitSince — when the move began waiting for a region's reboot
	// agent (cleared when the wait ends).
	RebootWaitSince string `json:"rebootWaitSince,omitempty"`
	// GateHeld: step id → seconds the reboot gate held the step after it had
	// begun, for waits that ended. The step's phase deadline moves by that
	// much: its clock does not run while it only waits for the reboot agent.
	GateHeld map[string]int64 `json:"gateHeld,omitempty"`
	// FailedSince — when the move was first reported Failed (cleared when it
	// is not). A Failed move holds the reboot agent at most Policy.RebootWait
	// past it (HoldOf).
	FailedSince string `json:"failedSince,omitempty"`
	// Outcome / Message of the last evaluation.
	Outcome string `json:"outcome,omitempty"`
	Message string `json:"message,omitempty"`
	// Interruption of the last evaluation (nil = none).
	Interruption *Interruption `json:"interruption,omitempty"`
	Activity     []Activity    `json:"activity"`
}

// Commit is one region-file write the caller performs.
type Commit struct {
	Member string // Member.Key()
	App    string
	Region string
	Fields regionfile.TransitionFields
	Reason string
}

// Result of one evaluation.
type Result struct {
	// Commit is the single write to perform now (nil = wait or done).
	Commit *Commit
	// Record is the updated record (nil when there is no transition at all).
	Record *Record
	// Outcome / Message describe the group.
	Outcome string
	Message string
	// Committed is the primary every member's files currently converge on
	// ("" when they disagree).
	Committed string
	// Desired is the primary the group's members declare.
	Desired string
	// Interruption names what outside the transition holds it up (nil =
	// nothing).
	Interruption *Interruption
}
