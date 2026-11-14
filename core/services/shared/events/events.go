package events

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Event is the standard envelope for all domain events.
type Event struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Source    string            `json:"source"`
	Timestamp time.Time         `json:"timestamp"`
	TenantID  string            `json:"tenant_id"`
	Data      json.RawMessage   `json:"data"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// NewEvent creates an Event with a unique ID and marshals data into the Data field.
func NewEvent(eventType, source, tenantID string, data any) (*Event, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return &Event{
		ID:        uuid.New().String(),
		Type:      eventType,
		Source:    source,
		Timestamp: time.Now().UTC(),
		TenantID:  tenantID,
		Data:      raw,
		Metadata:  make(map[string]string),
	}, nil
}
