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
	// Outcome / Message of the last evaluation.
	Outcome  string     `json:"outcome,omitempty"`
	Message  string     `json:"message,omitempty"`
	Activity []Activity `json:"activity"`
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
}
