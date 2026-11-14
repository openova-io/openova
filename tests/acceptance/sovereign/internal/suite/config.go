package suite

import "time"

// Config is everything a run needs. Secrets (Session) come from the
// environment only and are never written to output or the JSON record.
type Config struct {
	Env          string
	Console      string
	DeploymentID string
	Session      string `json:"-"`

	KubeconfigA, KubeconfigB string
	RegionA, RegionB         string

	Org, Environment, Blueprint, AppName string
	RegionIPs                            string
	Journeys                             string

	InstallTimeout, MoveTimeout, DeleteTimeout, CommitBudget time.Duration

	InsecureAppTLS bool   // skip TLS verification of the APP FQDN only (staging LE certs)
	WPTablePrefix  string // WordPress table prefix for the replica SELECTs (default wp_)
	DNSServer      string // resolver for the DNS assertion (default: system resolver)

	// The gate checks. Manifest is the JSON export of docs/ledger/gates.yaml;
	// when it is empty no check is selected. Capabilities is a comma list.
	RunIn, Manifest, CandidateID, RepoRoot, Capabilities string
	// NodesRequested is what the deployment request asked for:
	// "<region id>=<nodes>,<region id>=<nodes>". Check FND-G03 reads it.
	NodesRequested string
	// The wipe that this fire follows: the deployment that was wiped, and the
	// number of its resources that a listing of the cloud account still found
	// after the wipe. Both are given, or neither. Check FND-G13 reads them.
	PreviousDeploymentID, PreviousWipeOrphans string
	// Preflight is the numbers of the flight checklist, read before the fire:
	// "<UTC time>;<kind>=<used>/<allowed>/<left>,..." (see parsePreflight).
	// The record carries them as provision.preflight; check FND-G13 reads them.
	Preflight string

	Record     bool
	RecordDir  string
	KeepOnFail bool
	ReadOnly   bool
}
