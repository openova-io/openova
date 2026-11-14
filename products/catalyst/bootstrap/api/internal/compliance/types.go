// Package compliance holds the types of the compliance read model and its
// pure functions: the loader of the ComplianceModel object and the score
// rules. Nothing in this package reads a cluster, keeps state or registers a
// metric. The read model and the adapters that produce findings call it.
package compliance

import (
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// Result is the verdict of one check on one subject.
type Result string

const (
	Pass        Result = "pass"
	Fail        Result = "fail"
	Warn        Result = "warn"
	NA          Result = "na"           // the producer says the check does not apply
	Waived      Result = "waived"       // an active waiver covers the subject
	Exempt      Result = "exempt"       // a platform exemption of the model covers the subject
	NotMeasured Result = "not-measured" // no producer result, a producer error, or an incomplete scan
)

// Source is the enum of ComplianceModel.spec.checks[].source: the only
// producer whose findings count for a check. The loader accepts kyverno,
// evaluator, trivy-vulnerability, trivy-configaudit, trivy-exposedsecret and
// trivy-rbac.
type Source string

// The sources that have a producer today.
const (
	SourceKyverno   Source = "kyverno"
	SourceEvaluator Source = "evaluator"
)

// Scope names of a Posture.
const (
	ScopeSovereign    = "sovereign"
	ScopeOrganization = "organization"
	ScopeEnvironment  = "environment"
	ScopeApplication  = "application"
	ScopeNamespace    = "namespace"
	ScopeWorkload     = "workload"
)

// SubjectKey identifies what is judged: a top-level workload or a cluster object.
type SubjectKey struct {
	Cluster   string // informer cluster id
	Namespace string // "" for a cluster-scoped subject
	Kind      string // Deployment, StatefulSet, DaemonSet, CronJob, Job, Pod (bare only), Namespace, ClusterRole, Role, PersistentVolumeClaim, Service, HelmRelease, ...
	Name      string
}

// Subject is a SubjectKey with the facts the score rules read.
type Subject struct {
	Key             SubjectKey
	Organization    string // slug, "" for platform
	Environment     string // "" when none
	EnvironmentType string // the text after the last "-" of Environment; "" when none
	Application     string // "" when none
	Exposure        string // "internet" | "production" | "other"
}

// ObjectRef names a Kubernetes object.
type ObjectRef struct {
	APIVersion, Kind, Namespace, Name string
}

// Finding is one producer verdict of one check on one subject.
type Finding struct {
	Check      string // check id of the model
	Source     Source
	Subject    SubjectKey
	Result     Result            // as the producer reported; waivers and exemptions are applied by the read model, not by adapters
	Message    string            // what was found; at most 500 characters
	Evidence   ObjectRef         // the object that proves the verdict (the report, the backup, the certificate)
	Properties map[string]string // small, source-specific details; never a secret value
	ObservedAt time.Time
	NextChange time.Time // zero, or the instant at which the verdict changes with no new event
}

// Adapter turns one informer event into findings. One adapter per source kind.
type Adapter interface {
	// Kinds returns the informer kind names this adapter consumes.
	Kinds() []string
	// Apply returns the complete set of findings that the event's object now
	// supports. The read model replaces every finding previously attributed
	// to the same object. For a DELETED event it returns nil.
	Apply(ev k8scache.Event, r Resolver) []Finding
}

// Resolver answers questions from the informer cache. It never calls the API server.
type Resolver interface {
	// TopOwner follows ownerReferences (Pod to ReplicaSet to Deployment; Pod to Job to CronJob)
	// and returns the top controller, or the object itself when it has no controller owner.
	TopOwner(cluster, namespace, kind, name string) SubjectKey
	// Subject returns the subject with its Organization, Environment, Application and Exposure.
	Subject(k SubjectKey) Subject
	Now() time.Time
}

// Counts holds how many effective results of each kind a subject or a scope has.
type Counts struct {
	Pass, Fail, Warn, NA, Waived, Exempt, NotMeasured int
}

// AxisValue is the value of one axis of the model for one scope.
type AxisValue struct {
	ID          string
	Value       *int   // nil = not measured
	Level       string // "" when Value is nil
	Num, Den    int    // weighted; zero for a measure axis
	Subjects    int    // subjects with at least one result in the denominator
	NotMeasured int    // subjects in scope with none
	Gap         string // the check of the axis with the largest lost weight, "" when none
	GapLost     int    // that check's lost weight
	Reason      string // the axis naText when Value is nil
}

// Posture is the score of one scope.
type Posture struct {
	Scope, ID      string // sovereign | organization | environment | application | namespace | workload
	Cluster        string // "" = all regions
	Overall        *int
	Level          string
	Partial        bool // true when a group had no measured axis and was left out
	Capped         bool // workload scope only: the mandatory cap was applied
	Axes           []AxisValue
	Counts         Counts
	ModelVersion   string    // spec.version of the model that scored the scope; several versions joined by "," when clusters differ
	OldestInput    time.Time // oldest last-report time among the producers that feed the scope
	StaleProducers []string
}

// Covers reports a waiver. It returns Waived when an active waiver covers the
// subject and the check; any other value means that no waiver covers them.
// A nil Covers covers nothing.
type Covers func(k SubjectKey, check string) Result
