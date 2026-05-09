// Package events is a placeholder for the K-Cont-2 NATS audit
// publisher.
//
// K-Cont-2 will land a publisher that emits the following audit
// event types onto the `catalyst.audit` JetStream subject (per
// docs/EPICS-1-6-unified-design.md §9 + master-brief F-1):
//
//	continuum-switchover-started
//	continuum-switchover-completed
//	continuum-switchover-failed
//	continuum-failback-started
//	continuum-failback-completed
//	continuum-lease-acquired
//	continuum-lease-lost
//	continuum-replication-degraded
//	continuum-replication-recovered
//
// Event payload shape (draft, finalized in K-Cont-2):
//
//	{
//	  "type":           "continuum-switchover-completed",
//	  "continuumRef":   "<namespace>/<name>",
//	  "applicationRef": "<namespace>/<name>",
//	  "from":           "<region>",
//	  "to":             "<region>",
//	  "reason":         "operator | auto-failover | lease-loss",
//	  "rtoObserved":    "23s",
//	  "rpoObserved":    "0s",
//	  "initiatedBy":    "<user-email> | auto-failover",
//	  "ts":             "<RFC3339>"
//	}
//
// The U-DR-1 UI subscribes to `catalyst.audit` filtered by
// `audit-type=continuum-*` to render the switchover history panel
// (master-brief U-DR-1).
package events
