// Package signals turns alert events into signal records of the operations-records service.
//
// An alert event is a CloudEvents 1.0 message in structured JSON mode on the subject
// catalyst.alert.changed.v1 (contract C3; the data is described by
// docs/contracts/C3-alert-changed-v1.schema.json). Its attributes are specversion "1.0", id,
// type "io.openova.catalyst.alert.changed.v1", source, subject (the Organization slug; absent
// for a platform alert), time, datacontenttype "application/json" and data. The data holds the
// twelve fields alertId, organization, cluster, namespace, entityKind, entityName, name, state,
// severity, startsAt, summary and labels; a producer may add more.
//
// The rules of the intake:
//
//   - Organization. The signal belongs to the data's organization. A platform alert (empty
//     organization) belongs to the operations Organization of the Intake. With neither, the
//     event is refused.
//   - Fingerprint. "alert/<cluster>/<alertId>". The same alert reported again has the same
//     fingerprint, so at most one open signal exists per fingerprint and Organization.
//   - Entity. EntityKind is entityKind in lower case and EntityRef is "<namespace>/<entityName>",
//     or "<entityName>" when the namespace is empty. When entityKind or entityName is empty the
//     entity is the cluster itself: kind "cluster", ref the cluster name.
//   - Severity floor. From severity, ignoring letter case and spaces around it: "critical" 1;
//     "high" and "error" 2; "warning" and "medium" 3; anything else 4.
//   - Firing. An open signal with the fingerprint is updated: its last seen time rises to the
//     event time, its floor becomes the more urgent of the two, its title and body become the
//     new ones. Without one, a new open signal is inserted with first seen = startsAt and last
//     seen = the later of startsAt and the event time; it belongs to no incident.
//   - Late copy. A firing event without an open signal is a late copy, and is ignored, when its
//     time is not after the last seen time of a cleared signal with the fingerprint: the alert
//     was resolved at or after that time. Every cleared signal that is read counts, not only the
//     one first seen last. Intake reads at most store.MaxLimit cleared signals of the fingerprint
//     for this, the ones first seen earliest. An alert with more cleared signals than that is
//     judged against those only, so a late copy of it can be taken as a new firing. A firing
//     alert is never ignored because of the limit.
//   - Resolved. The open signal with the fingerprint is cleared: its last seen time rises to the
//     event time, and its title and body become those of the resolved event. Its floor stays.
//     Without one, nothing is written.
//   - Title. The summary, or the alert name when the summary is empty. Body: the data as received.
//
// Intake writes only signals. It never opens or changes an incident: package correlate does.
package signals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/openova-io/openova/products/ops-records/internal/ids"
	"github.com/openova-io/openova/products/ops-records/internal/store"
)

// Subject is the NATS subject of the alert events.
const Subject = "catalyst.alert.changed.v1"

// EventType is the CloudEvents type of an alert event: "io.openova." and the subject.
const EventType = "io.openova." + Subject

// Alert states.
const (
	StateFiring   = "firing"
	StateResolved = "resolved"
)

// What Intake did with an event.
const (
	Inserted = "inserted" // a new open signal
	Updated  = "updated"  // an open signal was reported again
	Cleared  = "cleared"  // an open signal was cleared
	Ignored  = "ignored"  // nothing was written
)

// ErrInvalidEvent is wrapped by every error about the event itself. Such an event can never be
// taken; the caller acknowledges it and does not retry it.
var ErrInvalidEvent = errors.New("signals: invalid alert event")

// organizationPattern is the form of an Organization slug.
var organizationPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{2,31}$`)

// requiredFields are the twelve fields every alert data holds.
var requiredFields = []string{"alertId", "organization", "cluster", "namespace", "entityKind",
	"entityName", "name", "state", "severity", "startsAt", "summary", "labels"}

// Alert is the data of an alert event.
type Alert struct {
	AlertID      string            `json:"alertId"`
	Organization string            `json:"organization"`
	Cluster      string            `json:"cluster"`
	Namespace    string            `json:"namespace"`
	EntityKind   string            `json:"entityKind"`
	EntityName   string            `json:"entityName"`
	Name         string            `json:"name"`
	State        string            `json:"state"`
	Severity     string            `json:"severity"`
	StartsAt     time.Time         `json:"startsAt"`
	Summary      string            `json:"summary"`
	Labels       map[string]string `json:"labels"`
}

// Event is one decoded alert event.
type Event struct {
	ID      string          // the CloudEvents id
	Source  string          // who published it
	Subject string          // the Organization slug of the envelope; empty when absent
	Time    time.Time       // when it was published, in UTC
	Alert   Alert           // the data
	Data    json.RawMessage // the data as received
}

type envelope struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Type            string          `json:"type"`
	Source          string          `json:"source"`
	Subject         string          `json:"subject"`
	Time            string          `json:"time"`
	DataContentType string          `json:"datacontenttype"`
	Data            json.RawMessage `json:"data"`
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidEvent, fmt.Sprintf(format, args...))
}

// Decode reads one alert event. Every error wraps ErrInvalidEvent.
func Decode(msg []byte) (Event, error) {
	var env envelope
	if err := json.Unmarshal(msg, &env); err != nil {
		return Event{}, invalid("not a JSON object: %v", err)
	}
	switch {
	case env.SpecVersion != "1.0":
		return Event{}, invalid("specversion %q, want 1.0", env.SpecVersion)
	case env.Type != EventType:
		return Event{}, invalid("type %q, want %s", env.Type, EventType)
	case env.ID == "":
		return Event{}, invalid("id is empty")
	case env.Source == "":
		return Event{}, invalid("source is empty")
	case env.DataContentType != "application/json":
		return Event{}, invalid("datacontenttype %q, want application/json", env.DataContentType)
	}
	at, err := time.Parse(time.RFC3339Nano, env.Time)
	if err != nil {
		return Event{}, invalid("time %q is not RFC 3339", env.Time)
	}
	data := bytes.TrimSpace(env.Data)
	var fields map[string]json.RawMessage
	if len(data) == 0 || data[0] != '{' || json.Unmarshal(data, &fields) != nil {
		return Event{}, invalid("data is not a JSON object")
	}
	for _, k := range requiredFields {
		if _, ok := fields[k]; !ok {
			return Event{}, invalid("data has no field %s", k)
		}
	}
	var a Alert
	if err := json.Unmarshal(data, &a); err != nil {
		return Event{}, invalid("data: %v", err)
	}
	switch {
	case a.AlertID == "":
		return Event{}, invalid("alertId is empty")
	case a.Name == "":
		return Event{}, invalid("name is empty")
	case a.Cluster == "":
		return Event{}, invalid("cluster is empty")
	case a.State != StateFiring && a.State != StateResolved:
		return Event{}, invalid("state %q, want firing or resolved", a.State)
	case a.StartsAt.IsZero():
		return Event{}, invalid("startsAt is zero")
	case a.Organization != "" && !organizationPattern.MatchString(a.Organization):
		return Event{}, invalid("organization %q is not a slug", a.Organization)
	case env.Subject != "" && env.Subject != a.Organization:
		return Event{}, invalid("subject %q differs from organization %q", env.Subject, a.Organization)
	}
	a.StartsAt = a.StartsAt.UTC()
	return Event{ID: env.ID, Source: env.Source, Subject: env.Subject, Time: at.UTC(), Alert: a,
		Data: append(json.RawMessage(nil), data...)}, nil
}

// SeverityFloor maps an alert severity to a severity floor of 1 (most urgent) to 4.
func SeverityFloor(severity string) int {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical":
		return 1
	case "high", "error":
		return 2
	case "warning", "medium":
		return 3
	default:
		return 4
	}
}

// Fingerprint returns the fingerprint of an alert: "alert/<cluster>/<alertId>".
func Fingerprint(cluster, alertID string) string { return "alert/" + cluster + "/" + alertID }

// Entity returns the entity kind and reference of an alert.
func Entity(a Alert) (kind, ref string) {
	if a.EntityKind == "" || a.EntityName == "" {
		return "cluster", a.Cluster
	}
	ref = a.EntityName
	if a.Namespace != "" {
		ref = a.Namespace + "/" + a.EntityName
	}
	return strings.ToLower(a.EntityKind), ref
}

// Intake writes the signals of alert events.
type Intake struct {
	// OperationsOrganization receives the signals of platform alerts. Empty: a platform alert
	// is refused.
	OperationsOrganization string
	// NewID makes the identifier of a new signal. Nil means ids.New.
	NewID func() string
}

// Result says what Intake did with one event. Signal is the row as written, or the zero value
// when nothing was written.
type Result struct {
	Action string
	Signal store.Signal
}

// Take records one event in the transaction tx. It returns an error that wraps
// ErrInvalidEvent when the event cannot be taken, and a store error when the store fails.
func (in Intake) Take(ctx context.Context, tx store.Tx, ev Event) (Result, error) {
	a := ev.Alert
	org := a.Organization
	if org == "" {
		org = in.OperationsOrganization
	}
	if org == "" {
		return Result{}, invalid("platform alert %s and no operations Organization", a.AlertID)
	}
	fp := Fingerprint(a.Cluster, a.AlertID)
	open, err := tx.ListSignals(ctx, store.SignalFilter{Organization: org, Fingerprint: fp, State: store.SignalOpen})
	if err != nil {
		return Result{}, err
	}
	title := a.Summary
	if title == "" {
		title = a.Name
	}
	at := ev.Time
	if a.State == StateResolved {
		if len(open) == 0 {
			return Result{Action: Ignored}, nil
		}
		s := open[len(open)-1]
		s.State = store.SignalCleared
		s.LastSeen = later(s.LastSeen, at)
		s.Title, s.Body = title, ev.Data
		if err := tx.UpdateSignal(ctx, s); err != nil {
			return Result{}, err
		}
		return Result{Action: Cleared, Signal: s}, nil
	}
	floor := SeverityFloor(a.Severity)
	if len(open) > 0 {
		s := open[len(open)-1]
		s.LastSeen = later(s.LastSeen, at)
		s.SeverityFloor = min(s.SeverityFloor, floor)
		s.Title, s.Body = title, ev.Data
		if err := tx.UpdateSignal(ctx, s); err != nil {
			return Result{}, err
		}
		return Result{Action: Updated, Signal: s}, nil
	}
	cleared, err := tx.ListSignals(ctx, store.SignalFilter{Organization: org, Fingerprint: fp,
		State: store.SignalCleared, Limit: store.MaxLimit})
	if err != nil {
		return Result{}, err
	}
	for _, c := range cleared {
		if !at.After(c.LastSeen) {
			return Result{Action: Ignored}, nil
		}
	}
	newID := in.NewID
	if newID == nil {
		newID = ids.New
	}
	kind, ref := Entity(a)
	s := store.Signal{
		ID: newID(), Organization: org, Source: store.SourceAlert, Kind: a.Name,
		EntityKind: kind, EntityRef: ref, Cluster: a.Cluster, SeverityFloor: floor,
		Title: title, Body: ev.Data, Fingerprint: fp, State: store.SignalOpen,
		FirstSeen: a.StartsAt, LastSeen: later(a.StartsAt, at),
	}
	if err := tx.InsertSignal(ctx, s); err != nil {
		return Result{}, err
	}
	return Result{Action: Inserted, Signal: s}, nil
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
