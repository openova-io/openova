// d31-acceptance — Pillar 3 zero-tx-loss acceptance harness.
//
// This is the OPERATOR-RUN binary that closes #2067 once the run on a
// fresh 2-region Sovereign produces a PASS and the operator attaches
// the result to the issue (CLAUDE.md §0 anti-theater rule: walk on
// fresh prov + screenshot or the issue stays open).
//
// Per CLAUDE.md §0 Pillar 3 deterministic step 10:
//
//  1. Bootstrap the schema (TRUNCATE-on-start so re-runs are clean).
//  2. Spawn 8 writer goroutines INSERTing rows into the primary CNPG
//     cluster at the configured cadence (1M-row default).
//  3. After --pre-kill-warmup (default 30s) of stable writes, kill
//     the primary by patching the Cluster CR's spec.instances to 0
//     (canonical region-kill proxy per platform/cnpg-pair/DESIGN.md
//     §"Test phases / Kill the primary region").
//  4. Promote the replica by flipping replica.enabled to false.
//  5. Poll the replica's status.currentPrimary until populated, or
//     fail with diagnostics after --rto-deadline (default 90s = 3x
//     the 30s RTO bar).
//  6. Reconnect to the new primary (now answering on the same
//     <cluster>-replica-rw Service) and SELECT every row id.
//  7. Assert zero-tx-loss: writer-ACK'd count floor + monotonic
//     contiguous IDs from 1..max.
//
// All ops are bounded by ctx deadlines — the harness MUST NEVER hang.
//
// Exit codes:
//
//	0 — PASS, zero-tx-loss verified.
//	1 — FAIL, gap detected (txs lost during failover).
//	2 — FAIL, RTO exceeded (replica did not promote in time).
//	3 — FAIL, harness error before failover (schema, writer, ...).
//
// The Containerfile builds this binary as the Pod entrypoint.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/openova-io/openova/platform/cnpg-pair/tests/acceptance/internal/harness"
)

const (
	exitPass        = 0
	exitFailGap     = 1 // also covers floor-failure (writer-ACK > visible) per zeroTxLoss assert.
	exitFailRTO     = 2
	exitFailHarness = 3
)

type opts struct {
	// Primary connection — points at <cluster>-primary-rw.<ns>.svc
	// when running in-cluster.
	primaryHost     string
	primaryPort     int
	primaryDB       string
	primaryUser     string
	primaryPassword string
	primarySSLMode  string

	// Replica connection — once promoted, the harness reconnects
	// here to SELECT the post-failover state.
	replicaHost     string
	replicaPort     int
	replicaDB       string
	replicaUser     string
	replicaPassword string
	replicaSSLMode  string

	// CR coordinates for the kill + promote ops.
	namespace      string
	primaryCluster string
	replicaCluster string

	// Knobs.
	targetRows    int64
	workers       int
	batchSize     int
	payloadSize   int
	preKillWarmup time.Duration
	rtoDeadline   time.Duration
	pollInterval  time.Duration
	postPromoteWait time.Duration
}

func parseFlags() opts {
	var o opts
	flag.StringVar(&o.primaryHost, "primary-host", "", "Primary CNPG -rw Service DNS (e.g. wp-db-primary-rw.tenant-1.svc)")
	flag.IntVar(&o.primaryPort, "primary-port", 5432, "Primary Postgres port")
	flag.StringVar(&o.primaryDB, "primary-db", "app", "Primary database name")
	flag.StringVar(&o.primaryUser, "primary-user", "app", "Primary DB user")
	flag.StringVar(&o.primarySSLMode, "primary-sslmode", "require", "Primary sslmode (require|disable)")

	flag.StringVar(&o.replicaHost, "replica-host", "", "Replica CNPG -rw Service DNS (e.g. wp-db-replica-rw.tenant-1.svc)")
	flag.IntVar(&o.replicaPort, "replica-port", 5432, "Replica Postgres port")
	flag.StringVar(&o.replicaDB, "replica-db", "app", "Replica DB name (typically same as primary)")
	flag.StringVar(&o.replicaUser, "replica-user", "app", "Replica DB user")
	flag.StringVar(&o.replicaSSLMode, "replica-sslmode", "require", "Replica sslmode")

	flag.StringVar(&o.namespace, "namespace", "default", "Kubernetes namespace holding the Cluster CRs")
	flag.StringVar(&o.primaryCluster, "primary-cluster", "", "Cluster CR name for the primary (the one we kill)")
	flag.StringVar(&o.replicaCluster, "replica-cluster", "", "Cluster CR name for the replica (the one we promote)")

	flag.Int64Var(&o.targetRows, "target-rows", 1_000_000, "Total rows the writer aims for")
	flag.IntVar(&o.workers, "workers", 8, "Writer goroutine count")
	flag.IntVar(&o.batchSize, "batch-size", 1000, "Rows per INSERT batch")
	flag.IntVar(&o.payloadSize, "payload-size", 1024, "Bytes per row payload (0 = NULL payload)")
	flag.DurationVar(&o.preKillWarmup, "pre-kill-warmup", 30*time.Second, "Stable-write duration before issuing region-kill")
	flag.DurationVar(&o.rtoDeadline, "rto-deadline", 90*time.Second, "Hard deadline for replica promotion (3x the 30s RTO bar)")
	flag.DurationVar(&o.pollInterval, "poll-interval", 1*time.Second, "Cluster-CR status poll interval during promotion wait")
	flag.DurationVar(&o.postPromoteWait, "post-promote-wait", 5*time.Second, "Grace period after currentPrimary populates before SELECTing the dataset")

	// Password sourced from env (Pod env-mounts a Secret) — NEVER
	// passed via flag so it doesn't leak into argv / process listings.
	o.primaryPassword = os.Getenv("D31_PRIMARY_PASSWORD")
	o.replicaPassword = os.Getenv("D31_REPLICA_PASSWORD")

	flag.Parse()
	return o
}

func main() {
	o := parseFlags()
	if err := validate(o); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: bad flags: %v\n", err)
		os.Exit(exitFailHarness)
	}

	log.SetFlags(log.LstdFlags | log.LUTC)
	log.Printf("d31-acceptance starting: ns=%s primary=%s replica=%s targetRows=%d",
		o.namespace, o.primaryCluster, o.replicaCluster, o.targetRows)

	primary := harness.ConnInfo{
		Host: o.primaryHost, Port: o.primaryPort, Database: o.primaryDB,
		User: o.primaryUser, Password: o.primaryPassword, SSLMode: o.primarySSLMode,
	}
	// Replica connection becomes the new-primary connection post-
	// promotion (CNPG re-points the -rw Service at the elected leader).
	replica := harness.ConnInfo{
		Host: o.replicaHost, Port: o.replicaPort, Database: o.replicaDB,
		User: o.replicaUser, Password: o.replicaPassword, SSLMode: o.replicaSSLMode,
	}
	d := harness.NewDriver()

	// Bound the entire harness run — even the most catastrophic stuck
	// state can't exceed this. preKillWarmup + rtoDeadline + a 10-min
	// floor for the writer + post-promotion SELECT.
	overall, cancelOverall := context.WithTimeout(context.Background(),
		o.preKillWarmup+o.rtoDeadline+15*time.Minute)
	defer cancelOverall()

	// Phase 0 — schema bootstrap.
	log.Printf("phase 0 — schema bootstrap on primary")
	bootstrapCtx, cancelBootstrap := context.WithTimeout(overall, 30*time.Second)
	if err := d.PsqlExec(bootstrapCtx, primary, harness.SchemaSQL); err != nil {
		cancelBootstrap()
		fmt.Fprintf(os.Stderr, "FAIL: schema bootstrap: %v\n", err)
		os.Exit(exitFailHarness)
	}
	cancelBootstrap()

	// Phase 1 — writer goroutine drives load on primary.
	log.Printf("phase 1 — writer running %d workers, batch %d, %d-byte payload",
		o.workers, o.batchSize, o.payloadSize)
	writerCtx, cancelWriter := context.WithCancel(overall)
	writerCfg := harness.WriterConfig{
		Table: "regression_d31_counter", TargetRows: o.targetRows,
		Workers: o.workers, BatchSize: o.batchSize, PayloadSize: o.payloadSize,
	}
	writerDone := make(chan harness.WriterResult, 1)
	go func() { writerDone <- harness.RunWriter(writerCtx, d, primary, writerCfg) }()

	// Phase 2 — let writers warm up before issuing the kill.
	log.Printf("phase 2 — warmup %s before region-kill", o.preKillWarmup)
	select {
	case <-time.After(o.preKillWarmup):
	case <-overall.Done():
		cancelWriter()
		fmt.Fprintf(os.Stderr, "FAIL: overall deadline expired during warmup\n")
		os.Exit(exitFailHarness)
	}

	// Phase 3 — REGION KILL. Scale primary Cluster CR to 0 instances.
	log.Printf("phase 3 — REGION KILL: scaling primary Cluster CR %s/%s to instances=0",
		o.namespace, o.primaryCluster)
	killCtx, cancelKill := context.WithTimeout(overall, 30*time.Second)
	killT, err := d.ScalePrimaryToZero(killCtx, o.namespace, o.primaryCluster)
	cancelKill()
	// Stop the writer immediately — anything it ACK'd before this
	// moment is the floor we must beat post-promotion.
	cancelWriter()
	wRes := <-writerDone
	log.Printf("writer stopped: acked=%d errors=%d", wRes.AckedRows, wRes.Errors)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: region-kill patch: %v\n", err)
		os.Exit(exitFailHarness)
	}

	// Phase 4 — Promote the replica (flip replica.enabled=false).
	log.Printf("phase 4 — promoting replica Cluster CR %s/%s", o.namespace, o.replicaCluster)
	promoteCtx, cancelPromote := context.WithTimeout(overall, 30*time.Second)
	if err := d.PromoteReplica(promoteCtx, o.namespace, o.replicaCluster); err != nil {
		cancelPromote()
		fmt.Fprintf(os.Stderr, "FAIL: promote patch: %v\n", err)
		os.Exit(exitFailHarness)
	}
	cancelPromote()

	// Phase 5 — Wait for status.currentPrimary on the replica CR.
	log.Printf("phase 5 — waiting up to %s for replica to elect a primary", o.rtoDeadline)
	waitCtx, cancelWait := context.WithTimeout(overall, o.rtoDeadline+5*time.Second)
	rto, err := d.WaitReplicaPrimary(waitCtx, o.namespace, o.replicaCluster,
		killT, o.rtoDeadline, o.pollInterval)
	cancelWait()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(exitFailRTO)
	}
	log.Printf("OK — replica promoted in %s (RTO bar = 30s)", rto)

	// Phase 6 — Settle period before reading; the just-elected primary
	// may still be finishing CNPG's promote-as-standalone work.
	log.Printf("phase 6 — settling %s before SELECT", o.postPromoteWait)
	select {
	case <-time.After(o.postPromoteWait):
	case <-overall.Done():
		fmt.Fprintf(os.Stderr, "FAIL: overall deadline expired during settle\n")
		os.Exit(exitFailHarness)
	}

	// Phase 7 — SELECT every row id from the new primary and assert.
	log.Printf("phase 7 — SELECT id FROM regression_d31_counter on new primary")
	readCtx, cancelRead := context.WithTimeout(overall, 5*time.Minute)
	ids, err := d.PsqlReadAllIDs(readCtx, replica, "regression_d31_counter")
	cancelRead()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: post-failover SELECT: %v\n", err)
		os.Exit(exitFailHarness)
	}

	log.Printf("post-failover state: rows=%d max_id=%d writer_acked=%d",
		len(ids), harness.MaxID(ids), wRes.AckedRows)

	if err := harness.AssertZeroTxLoss(ids, wRes.AckedRows); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		// Distinguish the two failure modes for the operator: a floor
		// failure (count<acked) vs a gap failure (count ok but holes
		// in the BIGSERIAL sequence). Both map to exit 1 per the
		// docs at top of file (the operator reads stderr to know
		// which).
		os.Exit(exitFailGap)
	}

	fmt.Printf("PASS — zero-tx-loss verified.\n")
	fmt.Printf("  RTO:            %s (bar: 30s)\n", rto)
	fmt.Printf("  rows_visible:   %d\n", len(ids))
	fmt.Printf("  writer_acked:   %d\n", wRes.AckedRows)
	fmt.Printf("  max_id:         %d\n", harness.MaxID(ids))
	fmt.Printf("  gaps_found:     0\n")
	os.Exit(exitPass)
}

func validate(o opts) error {
	if o.primaryHost == "" {
		return fmt.Errorf("--primary-host is required")
	}
	if o.replicaHost == "" {
		return fmt.Errorf("--replica-host is required")
	}
	if o.primaryCluster == "" {
		return fmt.Errorf("--primary-cluster is required")
	}
	if o.replicaCluster == "" {
		return fmt.Errorf("--replica-cluster is required")
	}
	if o.namespace == "" {
		return fmt.Errorf("--namespace is required")
	}
	if o.targetRows <= 0 {
		return fmt.Errorf("--target-rows must be positive, got %d", o.targetRows)
	}
	if o.workers <= 0 {
		return fmt.Errorf("--workers must be positive, got %d", o.workers)
	}
	if o.preKillWarmup < time.Second {
		return fmt.Errorf("--pre-kill-warmup too short, got %s (min 1s)", o.preKillWarmup)
	}
	if o.rtoDeadline < 30*time.Second {
		// CLAUDE.md §0 sets the RTO bar at 30s; deadline below that
		// would create false-FAIL noise. 30s is the absolute floor.
		return fmt.Errorf("--rto-deadline must be ≥30s (the Pillar 3 RTO bar), got %s", o.rtoDeadline)
	}
	return nil
}
