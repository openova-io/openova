// Package types defines the FlowMessage wire contract — the JSON
// envelopes the openova-flow-server accepts on POST /v1/flows/{flowId}/events
// and replays on GET /v1/flows/{flowId}/stream (SSE).
//
// Schema version 1. Locked across all three OpenovaFlow agents:
//   - Agent #1 ships matching TypeScript types in @openova/flow-core.
//   - Agent #2 (this module) defines the Go-side shape.
//   - Agent #3 wires the emitters (catalyst-api proxy + flux adapter
//     sidecar) to POST these envelopes.
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//
//	#1 (waterfall) — every envelope variant defined in the locked
//	   contract is implemented at first cut; there is no "subset for
//	   v1" carve-out.
//	#4 (never hardcode) — message variants are open strings (Status,
//	   Family, Region) so per-deployment overlays can drive the
//	   palette without re-rolling this binary.
//
// FlowMessage is decoded in two phases: the envelope unmarshals to
// pick the Type discriminator, then the variant-specific payload is
// re-unmarshalled into the typed shape. See ingest.go.
package types

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Triggerer describes one "this flow caused that flow" edge across
// the runtime instance graph. Always a soft (no-FK) reference — the
// referenced flowId may have rolled out of the ring buffer or never
// been seen.
type Triggerer struct {
	FlowID string `json:"flowId"`
	// When ∈ {"success", "failure", "always"}.
	When string `json:"when"`
}

// FlowInstance — one runtime instance of a flow (e.g. one Temporal
// workflow execution, one bootstrap-kit reconcile, one CI job tree).
type FlowInstance struct {
	ID            string                 `json:"id"`
	DefinitionID  *string                `json:"definitionId,omitempty"`
	ParentFlowID  *string                `json:"parentFlowId,omitempty"`
	TriggeredBy   []Triggerer            `json:"triggeredBy,omitempty"`
	Status        string                 `json:"status"`
	StartedAt     int64                  `json:"startedAt"`
	EndedAt       *int64                 `json:"endedAt,omitempty"`
	Meta          map[string]interface{} `json:"meta,omitempty"`
}

// FlowNode — one node within a FlowInstance. The (flowId,id) pair is
// the natural key.
type FlowNode struct {
	ID        string                 `json:"id"`
	FlowID    string                 `json:"flowId"`
	Label     string                 `json:"label"`
	Status    string                 `json:"status"`
	Family    *string                `json:"family,omitempty"`
	Region    *string                `json:"region,omitempty"`
	StartedAt *int64                 `json:"startedAt,omitempty"`
	EndedAt   *int64                 `json:"endedAt,omitempty"`
	Meta      map[string]interface{} `json:"meta,omitempty"`
}

// Relationship — directed edge between two nodes. Cross-flow edges
// set ToFlowID; intra-flow edges leave both nullable flow-ids unset.
type Relationship struct {
	FromID     string  `json:"fromId"`
	ToID       string  `json:"toId"`
	FromFlowID *string `json:"fromFlowId,omitempty"`
	ToFlowID   *string `json:"toFlowId,omitempty"`
	// Type ∈ {"contains", "finish-to-start", "start-to-start",
	// "finish-to-finish", "start-to-finish", "triggers"}.
	Type string `json:"type"`
	// Condition ∈ {"on-success", "on-failure", "always"}.
	Condition string `json:"condition,omitempty"`
	// Lag in seconds (>= 0).
	Lag int64 `json:"lag,omitempty"`
}

// RelPair — minimal identity for a Relationship used by
// delete-rels envelopes.
type RelPair struct {
	FromID string `json:"fromId"`
	ToID   string `json:"toId"`
	Type   string `json:"type"`
}

// MessageType discriminator. New variants land here; envelopes whose
// Type is unknown fail validation at ingest.
type MessageType string

const (
	TypeSnapshot     MessageType = "snapshot"
	TypeUpsertFlow   MessageType = "upsert-flow"
	TypeUpsertNodes  MessageType = "upsert-nodes"
	TypeUpsertRels   MessageType = "upsert-rels"
	TypeDeleteNodes  MessageType = "delete-nodes"
	TypeDeleteRels   MessageType = "delete-rels"
)

// FlowMessage is the wire envelope. Fields beyond Type are optional;
// which subset is non-empty depends on Type (validated by Validate).
type FlowMessage struct {
	Type          MessageType    `json:"type"`
	Flow          *FlowInstance  `json:"flow,omitempty"`
	Nodes         []FlowNode     `json:"nodes,omitempty"`
	Relationships []Relationship `json:"relationships,omitempty"`
	IDs           []string       `json:"ids,omitempty"`
	Pairs         []RelPair      `json:"pairs,omitempty"`
}

// Validate enforces the per-variant required-fields contract. Returns
// nil on a well-formed envelope, an error suitable for HTTP 400 body
// otherwise.
func (m *FlowMessage) Validate() error {
	switch m.Type {
	case TypeSnapshot:
		if m.Flow == nil {
			return errors.New("snapshot: flow is required")
		}
	case TypeUpsertFlow:
		if m.Flow == nil {
			return errors.New("upsert-flow: flow is required")
		}
	case TypeUpsertNodes:
		if len(m.Nodes) == 0 {
			return errors.New("upsert-nodes: nodes must be non-empty")
		}
	case TypeUpsertRels:
		if len(m.Relationships) == 0 {
			return errors.New("upsert-rels: relationships must be non-empty")
		}
	case TypeDeleteNodes:
		if len(m.IDs) == 0 {
			return errors.New("delete-nodes: ids must be non-empty")
		}
	case TypeDeleteRels:
		if len(m.Pairs) == 0 {
			return errors.New("delete-rels: pairs must be non-empty")
		}
	default:
		return fmt.Errorf("unknown message type %q", m.Type)
	}
	return nil
}

// DecodeFlowMessage parses raw JSON bytes into a FlowMessage and
// validates the variant. Returns the typed envelope or a 400-eligible
// error.
func DecodeFlowMessage(raw []byte) (*FlowMessage, error) {
	var m FlowMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode FlowMessage: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}
