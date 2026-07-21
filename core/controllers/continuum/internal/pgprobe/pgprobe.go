// Package pgprobe observes a cnpg-pair's synchronous hot-standby from
// the acting PRIMARY's pg_stat_replication view (#5311).
//
// WHY this exists — the CR-based observation is structurally blind on a
// TRUE 2-region Sovereign. The continuum-controller runs on the
// primary-side cluster and can only list its LOCAL API server, so on a
// real cross-region pair it NEVER sees the region-b replica Cluster CR
// (that CR lives in region-b's SEPARATE control plane). `cnpg.FindPair`
// therefore returns "pair incomplete" every tick, the standby posture
// reads "no determination", and the Continuum CR stays false-green
// (phase=Healthy, standbyAvailable=UNSET, replicationLagSeconds=0) for
// the entire outage even when the standby is genuinely gone.
//
// The primary, by contrast, ALWAYS sees its connected streaming
// standbys — the standby streams TO the primary over the Cilium
// ClusterMesh regardless of the control-plane boundary, so
// pg_stat_replication on the primary lists it. "standby present +
// streaming + lag" is therefore readable from the primary alone, with
// no region-b Kubernetes client. A vanished standby drops out of
// pg_stat_replication (zero rows) — the working observation source for
// #4901's existing standby-absent/degrade branch on the topology that
// matters most for DR.
//
// The connection reuses the SAME credential + auth pattern the
// dr-promoter / dr-failback `signals` probes use (platform/cnpg-pair):
// a normal (non-replication) psql/pgx connection to the primary's `-rw`
// Service as the `streaming_replica` role, authenticated with the
// CNPG-provisioned client certificate (`<cluster>-replication` Secret,
// tls.crt/tls.key). No new secret scheme is invented.
package pgprobe

import "context"

// ReplicationRow is one row of pg_stat_replication read from the acting
// primary. Fields mirror the columns the probe SELECTs; string columns
// are coalesced to "" and the lag is EXTRACT(EPOCH FROM replay_lag) in
// whole seconds (absent when the standby is fully caught up / idle, in
// which case HasReplayLag is false).
type ReplicationRow struct {
	// ApplicationName is the standby's application_name — CNPG streams a
	// replica Cluster with application_name == the replica Cluster CR
	// name (`<pair-fullname>-replica`), matching the filter the
	// dr-failback signals probe uses.
	ApplicationName string
	// State is the walsender state: streaming | catchup | startup |
	// backup | stopping. "" when unknown.
	State string
	// SyncState is the replication sync mode: sync | async | quorum |
	// potential. "" when unknown.
	SyncState string
	// ReplayLagSeconds is the standby's replay lag in whole seconds.
	// Only meaningful when HasReplayLag is true.
	ReplayLagSeconds int
	HasReplayLag     bool
}

// Posture is the standby determination folded out of a
// pg_stat_replication snapshot for one cnpg-pair.
type Posture struct {
	// StandbyPresent — at least one connected standby (walsender row)
	// is present on the primary. This is the gone/not-gone signal that
	// drives #4901's standby-absent branch: zero rows == standby gone.
	// Deliberately robust to application_name column-masking (a
	// walsender row's mere existence is the signal), so a healthy pair
	// never false-degrades even if the privileged columns come back
	// NULL for the streaming_replica probe role.
	StandbyPresent bool
	// Streaming — a matched standby is in state='streaming' (fully
	// caught up and streaming, not still doing initial catchup).
	// Informational; not required for StandbyPresent.
	Streaming bool
	// SyncStandbyPresent — a matched standby is a synchronous / quorum
	// member (the RPO=0 durability path is intact). Informational.
	SyncStandbyPresent bool
	// ReplayLagSeconds — the max replay lag across connected standbys
	// in whole seconds (0 when unknown / caught up). Surfaced through
	// the Continuum CR's replicationLagSeconds field.
	ReplayLagSeconds int
	// AppName — the observed standby's application_name (the replica
	// Cluster CR name CNPG streams as); recorded on the CR as
	// status.standbyReplicaCluster. Prefers the row matching the
	// expected replica name.
	AppName string
}

// DerivePosture folds a pg_stat_replication snapshot into a Posture.
//
// present/absent is decided by whether ANY connected standby row exists
// — the region-kill signal is a standby dropping out of the view
// entirely (zero rows), and a bare walsender row is proof a standby is
// connected even if column-masking hides the detail columns from the
// streaming_replica probe role (#5239).
//
// expectedApp (the pair's replica Cluster name, may be "") only refines
// which row supplies AppName: an exact match wins, otherwise the first
// row's name is used. state/sync/lag are aggregated across all rows
// (any streaming → Streaming; any sync/quorum → SyncStandbyPresent; max
// lag → ReplayLagSeconds) so a single-standby pair reports that
// standby's posture verbatim.
func DerivePosture(rows []ReplicationRow, expectedApp string) Posture {
	p := Posture{}
	for _, row := range rows {
		p.StandbyPresent = true
		if row.State == "streaming" {
			p.Streaming = true
		}
		if row.SyncState == "sync" || row.SyncState == "quorum" {
			p.SyncStandbyPresent = true
		}
		if row.HasReplayLag && row.ReplayLagSeconds > p.ReplayLagSeconds {
			p.ReplayLagSeconds = row.ReplayLagSeconds
		}
		switch {
		case expectedApp != "" && row.ApplicationName == expectedApp:
			p.AppName = row.ApplicationName
		case p.AppName == "":
			p.AppName = row.ApplicationName
		}
	}
	return p
}

// Prober observes a cnpg-pair primary's connected standbys. The real
// implementation (PGXProber) connects to the primary's `-rw` Service
// and reads pg_stat_replication; tests inject a fake.
type Prober interface {
	// Observe connects to the acting primary <primaryName>-rw in
	// namespace ns and returns its standby Posture. expectedApp filters
	// pg_stat_replication to the pair's replica application_name (may be
	// "" = any connected standby).
	//
	// An error means the observation could NOT be made this tick
	// (credential missing, connection refused, query error). Callers
	// MUST treat an error as "no determination" — NEVER as
	// standby-absent — so a transient DB-connection blip does not
	// false-degrade a healthy pair.
	Observe(ctx context.Context, ns, primaryName, expectedApp string) (Posture, error)
}

// SecretReader fetches one key's bytes from a namespaced K8s Secret.
// Lets PGXProber read the streaming_replica client certificate without
// this package importing client-go (the controller supplies the
// implementation from its existing Kubernetes client).
type SecretReader interface {
	ReadSecret(ctx context.Context, namespace, name, key string) ([]byte, error)
}
