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

	Record     bool
	RecordDir  string
	KeepOnFail bool
	ReadOnly   bool
}
