package pgprobe

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// DefaultClusterDomain is the in-cluster DNS suffix the primary `-rw`
// Service is reached through. Overridable for clusters that customize
// the kubelet cluster domain.
const DefaultClusterDomain = "cluster.local"

// defaultDialTimeout bounds the connect + query so a wedged primary
// never stalls the per-CR reconcile loop; a slow probe is reported as
// an error (→ no determination) rather than blocking.
const defaultDialTimeout = 5 * time.Second

// statReplicationQuery reads the acting primary's connected standbys.
//
// Columns are coalesced so a NULL never aborts the row Scan; replay_lag
// (an interval) is converted to whole seconds via EXTRACT(EPOCH ...)
// and left nullable (NULL when the standby is caught up / idle). The
// mere EXISTENCE of a row is the present/absent signal (#5311); the
// detail columns enrich state/sync/lag when the probe role can read
// them.
const statReplicationQuery = `SELECT coalesce(application_name, ''),
       coalesce(state, ''),
       coalesce(sync_state, ''),
       EXTRACT(EPOCH FROM replay_lag)::bigint
FROM pg_stat_replication`

// PGXProber is the production Prober. It connects to a cnpg-pair
// primary's `-rw` Service as the `streaming_replica` role using the
// CNPG-provisioned client certificate — the exact credential + auth
// pattern the dr-promoter / dr-failback signals probes use
// (platform/cnpg-pair) — and reads pg_stat_replication.
//
// No cross-region Kubernetes client and no new secret scheme: the
// `<primary>-replication` Secret (tls.crt/tls.key) is created by CNPG
// for every Cluster and is trusted by that same primary, and the
// connection targets the LOCAL primary `-rw` Service (the region-b
// standby's stream reaches the primary over ClusterMesh on its own).
type PGXProber struct {
	// Secrets reads the streaming_replica client certificate.
	Secrets SecretReader
	// DialTimeout bounds connect+query. Zero → defaultDialTimeout.
	DialTimeout time.Duration
	// ClusterDomain is the `-rw` Service DNS suffix. Zero →
	// DefaultClusterDomain.
	ClusterDomain string
}

// Observe implements Prober.
func (p *PGXProber) Observe(ctx context.Context, ns, primaryName, expectedApp string) (Posture, error) {
	if p == nil || p.Secrets == nil {
		return Posture{}, errors.New("pgprobe: nil PGXProber or SecretReader")
	}
	if ns == "" || primaryName == "" {
		return Posture{}, fmt.Errorf("pgprobe: namespace/primaryName required (ns=%q primary=%q)", ns, primaryName)
	}

	// streaming_replica client certificate. CNPG provisions
	// `<cluster>-replication` (keys tls.crt/tls.key) for every Cluster;
	// the primary trusts its own. Reading it (Secret GET) is the ONLY
	// credential access — no password, no superuser, no new Secret.
	secretName := primaryName + "-replication"
	crt, err := p.Secrets.ReadSecret(ctx, ns, secretName, "tls.crt")
	if err != nil {
		return Posture{}, fmt.Errorf("pgprobe: read %s/%s tls.crt: %w", ns, secretName, err)
	}
	key, err := p.Secrets.ReadSecret(ctx, ns, secretName, "tls.key")
	if err != nil {
		return Posture{}, fmt.Errorf("pgprobe: read %s/%s tls.key: %w", ns, secretName, err)
	}
	clientCert, err := tls.X509KeyPair(crt, key)
	if err != nil {
		return Posture{}, fmt.Errorf("pgprobe: parse streaming_replica client cert: %w", err)
	}

	domain := p.ClusterDomain
	if domain == "" {
		domain = DefaultClusterDomain
	}
	host := fmt.Sprintf("%s-rw.%s.svc.%s", primaryName, ns, domain)

	timeout := p.DialTimeout
	if timeout <= 0 {
		timeout = defaultDialTimeout
	}

	// sslmode=require: encrypt the connection, present the client cert,
	// but do NOT verify the server certificate's CN — the CNPG server
	// cert is issued for the Cluster's internal name, not the `-rw`
	// Service FQDN, so CN verification would spuriously fail. This
	// matches the dr-promoter/dr-failback probes' `sslmode=require`.
	connStr := fmt.Sprintf(
		"host=%s port=5432 dbname=postgres user=streaming_replica sslmode=require connect_timeout=%d",
		host, int(timeout.Seconds()),
	)
	cfg, err := pgx.ParseConfig(connStr)
	if err != nil {
		return Posture{}, fmt.Errorf("pgprobe: parse conn config: %w", err)
	}
	injectClientCert(cfg, clientCert)

	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := pgx.ConnectConfig(dialCtx, cfg)
	if err != nil {
		return Posture{}, fmt.Errorf("pgprobe: connect %s: %w", host, err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	rows, err := conn.Query(dialCtx, statReplicationQuery)
	if err != nil {
		return Posture{}, fmt.Errorf("pgprobe: query pg_stat_replication on %s: %w", host, err)
	}
	defer rows.Close()

	var parsed []ReplicationRow
	for rows.Next() {
		var app, state, sync string
		var lag *int64
		if err := rows.Scan(&app, &state, &sync, &lag); err != nil {
			return Posture{}, fmt.Errorf("pgprobe: scan pg_stat_replication row: %w", err)
		}
		row := ReplicationRow{ApplicationName: app, State: state, SyncState: sync}
		if lag != nil {
			row.ReplayLagSeconds = int(*lag)
			row.HasReplayLag = true
		}
		parsed = append(parsed, row)
	}
	if err := rows.Err(); err != nil {
		return Posture{}, fmt.Errorf("pgprobe: iterate pg_stat_replication rows: %w", err)
	}

	return DerivePosture(parsed, expectedApp), nil
}

// injectClientCert stamps the streaming_replica client certificate onto
// the parsed config's TLS settings (and every fallback), and disables
// server-cert verification to match sslmode=require. pgx builds the
// TLSConfig from sslmode=require; we only add the client Certificates
// it cannot express through the connection string.
func injectClientCert(cfg *pgx.ConnConfig, cert tls.Certificate) {
	apply := func(t *tls.Config) *tls.Config {
		if t == nil {
			t = &tls.Config{}
		}
		t.Certificates = []tls.Certificate{cert}
		t.InsecureSkipVerify = true // sslmode=require: encrypt, do not verify server CN
		return t
	}
	cfg.TLSConfig = apply(cfg.TLSConfig)
	for i := range cfg.Fallbacks {
		cfg.Fallbacks[i].TLSConfig = apply(cfg.Fallbacks[i].TLSConfig)
	}
}
