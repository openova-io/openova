// Package types mirrors the locked FlowMessage wire contract, scoped
// to the subset the adapter EMITS (upsert-flow, upsert-nodes,
// upsert-rels, delete-nodes). The receiving end (openova-flow-server)
// owns the master copy in products/openova-flow/server/internal/types.
//
// We do NOT depend on the server module across the OCI image boundary
// because (a) two Go modules side-by-side keep the dep graphs decoupled,
// and (b) Agent #1 already locked the wire contract — re-declaring
// the types here keeps the producer side honest.
package types

// FlowInstance — runtime instance the adapter MAY emit (one per
// deployment in v1 — emitter overlay decides).
type FlowInstance struct {
	ID            string                 `json:"id"`
	DefinitionID  *string                `json:"definitionId,omitempty"`
	ParentFlowID  *string                `json:"parentFlowId,omitempty"`
	Status        string                 `json:"status"`
	StartedAt     int64                  `json:"startedAt"`
	EndedAt       *int64                 `json:"endedAt,omitempty"`
	Meta          map[string]interface{} `json:"meta,omitempty"`
}

// FlowNode — one node within the flow.
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

// Relationship — directed edge.
type Relationship struct {
	FromID     string  `json:"fromId"`
	ToID       string  `json:"toId"`
	FromFlowID *string `json:"fromFlowId,omitempty"`
	ToFlowID   *string `json:"toFlowId,omitempty"`
	Type       string  `json:"type"`
	Condition  string  `json:"condition,omitempty"`
	Lag        int64   `json:"lag,omitempty"`
}

// FlowMessage envelope variants the adapter emits.
type FlowMessage struct {
	Type          string         `json:"type"`
	Flow          *FlowInstance  `json:"flow,omitempty"`
	Nodes         []FlowNode     `json:"nodes,omitempty"`
	Relationships []Relationship `json:"relationships,omitempty"`
	IDs           []string       `json:"ids,omitempty"`
}

const (
	TypeUpsertFlow  = "upsert-flow"
	TypeUpsertNodes = "upsert-nodes"
	TypeUpsertRels  = "upsert-rels"
	TypeDeleteNodes = "delete-nodes"
)
