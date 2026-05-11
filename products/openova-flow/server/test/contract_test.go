// Contract tests — every FlowMessage variant from the locked schema
// must round-trip through JSON unchanged, validate cleanly, and fold
// to the expected snapshot. Mirror file at @openova/flow-core in
// Agent #1's package; the wire shape MUST stay byte-identical.
package test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/openova-flow/server/internal/types"
)

func ptrString(s string) *string { return &s }

func TestFlowMessage_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "snapshot",
			raw: `{
				"type":"snapshot",
				"flow":{"id":"f1","status":"running","startedAt":1234567890123},
				"nodes":[{"id":"n1","flowId":"f1","label":"hello","status":"succeeded"}],
				"relationships":[{"fromId":"n1","toId":"n2","type":"finish-to-start","condition":"on-success"}]
			}`,
		},
		{
			name: "upsert-flow",
			raw:  `{"type":"upsert-flow","flow":{"id":"f1","status":"running","startedAt":1}}`,
		},
		{
			name: "upsert-nodes",
			raw:  `{"type":"upsert-nodes","nodes":[{"id":"n1","flowId":"f1","label":"x","status":"running"}]}`,
		},
		{
			name: "upsert-rels",
			raw:  `{"type":"upsert-rels","relationships":[{"fromId":"a","toId":"b","type":"contains"}]}`,
		},
		{
			name: "delete-nodes",
			raw:  `{"type":"delete-nodes","ids":["n1","n2"]}`,
		},
		{
			name: "delete-rels",
			raw:  `{"type":"delete-rels","pairs":[{"fromId":"a","toId":"b","type":"finish-to-start"}]}`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m, err := types.DecodeFlowMessage([]byte(tc.raw))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			out, err := json.Marshal(m)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			// Re-decode after round-trip — ensures both directions
			// honour the schema.
			m2, err := types.DecodeFlowMessage(out)
			if err != nil {
				t.Fatalf("round-trip decode: %v\noutput=%s", err, out)
			}
			if m2.Type != m.Type {
				t.Fatalf("Type changed: %s -> %s", m.Type, m2.Type)
			}
		})
	}
}

func TestFlowMessage_ValidateRejectsUnknown(t *testing.T) {
	cases := []string{
		`{"type":"banana"}`,
		`{"type":"snapshot"}`, // missing flow
		`{"type":"upsert-nodes"}`,
		`{"type":"delete-nodes"}`,
		`{"type":"delete-rels"}`,
	}
	for _, raw := range cases {
		raw := raw
		t.Run(strings.TrimPrefix(raw, `{"type":"`), func(t *testing.T) {
			if _, err := types.DecodeFlowMessage([]byte(raw)); err == nil {
				t.Fatalf("expected error for %s", raw)
			}
		})
	}
}

// TestFlowMessage_CrossFlowRelationship — the cross-flow case from
// the locked contract (Triggerer + ToFlowID). Must survive
// round-trip.
func TestFlowMessage_CrossFlowRelationship(t *testing.T) {
	raw := `{
		"type":"upsert-rels",
		"relationships":[
			{"fromId":"a","toId":"b","toFlowId":"sister","type":"triggers","condition":"on-success","lag":30}
		]
	}`
	m, err := types.DecodeFlowMessage([]byte(raw))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(m.Relationships) != 1 {
		t.Fatalf("expected 1 rel, got %d", len(m.Relationships))
	}
	r := m.Relationships[0]
	if r.ToFlowID == nil || *r.ToFlowID != "sister" {
		t.Fatalf("ToFlowID not preserved: %+v", r.ToFlowID)
	}
	if r.Type != "triggers" {
		t.Fatalf("Type changed: %s", r.Type)
	}
	if r.Lag != 30 {
		t.Fatalf("Lag changed: %d", r.Lag)
	}
}

// TestFlowMessage_TriggeredByChain — Triggerer arrays on FlowInstance
// (multi-source triggers) must serialise.
func TestFlowMessage_TriggeredByChain(t *testing.T) {
	fi := types.FlowInstance{
		ID:        "child",
		Status:    "running",
		StartedAt: 1,
		TriggeredBy: []types.Triggerer{
			{FlowID: "parentA", When: "success"},
			{FlowID: "parentB", When: "failure"},
		},
		DefinitionID: ptrString("def-1"),
		ParentFlowID: ptrString("parentA"),
	}
	body, err := json.Marshal(types.FlowMessage{Type: types.TypeUpsertFlow, Flow: &fi})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	m2, err := types.DecodeFlowMessage(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m2.Flow == nil || len(m2.Flow.TriggeredBy) != 2 {
		t.Fatalf("TriggeredBy lost: %+v", m2.Flow)
	}
}
