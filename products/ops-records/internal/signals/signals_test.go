package signals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/openova-io/openova/products/ops-records/internal/store"
)

var t0 = time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)

// alertData returns the data of an alert event with the twelve fields; change overrides fields
// and a value of nil removes one.
func alertData(change map[string]any) map[string]any {
	d := map[string]any{
		"alertId": "a1", "organization": "acme", "cluster": "region-a", "namespace": "acme",
		"entityKind": "Deployment", "entityName": "web", "name": "pod-crash-loop", "state": "firing",
		"severity": "warning", "startsAt": t0.Format(time.RFC3339), "summary": "web restarts",
		"labels": map[string]string{"team": "web"},
	}
	for k, v := range change {
		if v == nil {
			delete(d, k)
		} else {
			d[k] = v
		}
	}
	return d
}

// message returns one alert event; envChange overrides attributes of the envelope.
func message(data any, envChange map[string]any) []byte {
	env := map[string]any{
		"specversion": "1.0", "id": "01JCF0R1K2M3N4P5Q6R7S8T9V0", "type": EventType,
		"source": "alerting/sovereign.example.org/region-a", "subject": "acme",
		"time": t0.Add(time.Minute).Format(time.RFC3339), "datacontenttype": "application/json", "data": data,
	}
	if d, ok := data.(map[string]any); ok {
		if org, _ := d["organization"].(string); org != "" {
			env["subject"] = org
		} else {
			delete(env, "subject")
		}
	}
	for k, v := range envChange {
		if v == nil {
			delete(env, k)
		} else {
			env[k] = v
		}
	}
	b, err := json.Marshal(env)
	if err != nil {
		panic(err)
	}
	return b
}

func TestDecode(t *testing.T) {
	cases := []struct {
		name    string
		msg     []byte
		wantErr bool
		check   func(t *testing.T, ev Event)
	}{
		{"firing event", message(alertData(nil), nil), false, func(t *testing.T, ev Event) {
			if ev.Alert.AlertID != "a1" || ev.Alert.State != StateFiring || !ev.Alert.StartsAt.Equal(t0) ||
				!ev.Time.Equal(t0.Add(time.Minute)) || ev.Alert.Labels["team"] != "web" || ev.Subject != "acme" {
				t.Fatalf("decoded %+v", ev)
			}
		}},
		{"resolved event", message(alertData(map[string]any{"state": "resolved"}), nil), false, func(t *testing.T, ev Event) {
			if ev.Alert.State != StateResolved {
				t.Fatalf("state %q", ev.Alert.State)
			}
		}},
		{"platform alert has no subject", message(alertData(map[string]any{"organization": ""}), nil), false, func(t *testing.T, ev Event) {
			if ev.Alert.Organization != "" || ev.Subject != "" {
				t.Fatalf("organization %q subject %q", ev.Alert.Organization, ev.Subject)
			}
		}},
		{"a field the producer added is kept in the body", message(alertData(map[string]any{"runbook": "r1"}), nil), false, func(t *testing.T, ev Event) {
			var m map[string]any
			if err := json.Unmarshal(ev.Data, &m); err != nil || m["runbook"] != "r1" {
				t.Fatalf("data %s", ev.Data)
			}
		}},
		{"not JSON", []byte("not json"), true, nil},
		{"specversion 0.3", message(alertData(nil), map[string]any{"specversion": "0.3"}), true, nil},
		{"another type", message(alertData(nil), map[string]any{"type": "io.openova.catalyst.audit.recorded.v1"}), true, nil},
		{"empty id", message(alertData(nil), map[string]any{"id": ""}), true, nil},
		{"no source", message(alertData(nil), map[string]any{"source": nil}), true, nil},
		{"no datacontenttype", message(alertData(nil), map[string]any{"datacontenttype": nil}), true, nil},
		{"time not RFC 3339", message(alertData(nil), map[string]any{"time": "yesterday"}), true, nil},
		{"data is a list", message([]string{"a"}, nil), true, nil},
		{"data lacks labels", message(alertData(map[string]any{"labels": nil}), nil), true, nil},
		{"data lacks summary", message(alertData(map[string]any{"summary": nil}), nil), true, nil},
		{"empty alertId", message(alertData(map[string]any{"alertId": ""}), nil), true, nil},
		{"empty name", message(alertData(map[string]any{"name": ""}), nil), true, nil},
		{"empty cluster", message(alertData(map[string]any{"cluster": ""}), nil), true, nil},
		{"state pending", message(alertData(map[string]any{"state": "pending"}), nil), true, nil},
		{"startsAt not a time", message(alertData(map[string]any{"startsAt": "soon"}), nil), true, nil},
		{"startsAt zero", message(alertData(map[string]any{"startsAt": "0001-01-01T00:00:00Z"}), nil), true, nil},
		{"organization not a slug", message(alertData(map[string]any{"organization": "Acme"}), map[string]any{"subject": "Acme"}), true, nil},
		{"subject differs from organization", message(alertData(nil), map[string]any{"subject": "other"}), true, nil},
		{"label value not a string", message(alertData(map[string]any{"labels": map[string]int{"n": 1}}), nil), true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, err := Decode(c.msg)
			if c.wantErr {
				if !errors.Is(err, ErrInvalidEvent) {
					t.Fatalf("error %v, want ErrInvalidEvent", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, ev)
		})
	}
}

func TestSeverityFloor(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"critical", 1}, {" CRITICAL ", 1}, {"high", 2}, {"error", 2}, {"warning", 3}, {"Medium", 3},
		{"info", 4}, {"low", 4}, {"", 4}, {"page", 4},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%q", c.in), func(t *testing.T) {
			if got := SeverityFloor(c.in); got != c.want {
				t.Fatalf("SeverityFloor(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestEntity(t *testing.T) {
	cases := []struct {
		name              string
		a                 Alert
		wantKind, wantRef string
	}{
		{"namespaced entity", Alert{Cluster: "c", Namespace: "acme", EntityKind: "Deployment", EntityName: "web"}, "deployment", "acme/web"},
		{"cluster-scoped entity", Alert{Cluster: "c", EntityKind: "Node", EntityName: "worker-1"}, "node", "worker-1"},
		{"no entity kind means the cluster", Alert{Cluster: "c", Namespace: "acme", EntityName: "web"}, "cluster", "c"},
		{"no entity name means the cluster", Alert{Cluster: "c", EntityKind: "Pod"}, "cluster", "c"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k, r := Entity(c.a)
			if k != c.wantKind || r != c.wantRef {
				t.Fatalf("Entity = %q %q, want %q %q", k, r, c.wantKind, c.wantRef)
			}
		})
	}
}

// step is one event given to Take and what it must do.
type step struct {
	data       map[string]any // changes to alertData
	at         time.Duration  // event time after t0
	wantAction string
	wantErr    bool
}

func TestTake(t *testing.T) {
	cases := []struct {
		name   string
		opsOrg string
		steps  []step
		check  func(t *testing.T, all []store.Signal)
	}{
		{"a firing alert inserts an open signal", "", []step{{nil, time.Minute, Inserted, false}}, func(t *testing.T, all []store.Signal) {
			s := one(t, all)
			if s.Organization != "acme" || s.Source != store.SourceAlert || s.Kind != "pod-crash-loop" ||
				s.EntityKind != "deployment" || s.EntityRef != "acme/web" || s.Cluster != "region-a" ||
				s.SeverityFloor != 3 || s.Title != "web restarts" || s.Fingerprint != "alert/region-a/a1" ||
				s.State != store.SignalOpen || !s.FirstSeen.Equal(t0) || !s.LastSeen.Equal(t0.Add(time.Minute)) ||
				s.IncidentID != "" {
				t.Fatalf("signal %+v", s)
			}
		}},
		{"the same alert again updates the open signal", "", []step{{nil, time.Minute, Inserted, false},
			{nil, 5 * time.Minute, Updated, false}}, func(t *testing.T, all []store.Signal) {
			if s := one(t, all); !s.LastSeen.Equal(t0.Add(5 * time.Minute)) {
				t.Fatalf("last seen %s", s.LastSeen)
			}
		}},
		{"an older copy does not lower last seen", "", []step{{nil, 5 * time.Minute, Inserted, false},
			{nil, time.Minute, Updated, false}}, func(t *testing.T, all []store.Signal) {
			if s := one(t, all); !s.LastSeen.Equal(t0.Add(5 * time.Minute)) {
				t.Fatalf("last seen %s", s.LastSeen)
			}
		}},
		{"a more urgent severity raises the floor", "", []step{{nil, time.Minute, Inserted, false},
			{map[string]any{"severity": "critical"}, 2 * time.Minute, Updated, false}}, func(t *testing.T, all []store.Signal) {
			if s := one(t, all); s.SeverityFloor != 1 {
				t.Fatalf("floor %d", s.SeverityFloor)
			}
		}},
		{"a less urgent severity keeps the floor", "", []step{{map[string]any{"severity": "high"}, time.Minute, Inserted, false},
			{map[string]any{"severity": "info", "summary": "web restarts less"}, 2 * time.Minute, Updated, false}}, func(t *testing.T, all []store.Signal) {
			if s := one(t, all); s.SeverityFloor != 2 || s.Title != "web restarts less" {
				t.Fatalf("floor %d title %q", s.SeverityFloor, s.Title)
			}
		}},
		{"resolved clears the open signal", "", []step{{nil, time.Minute, Inserted, false},
			{map[string]any{"state": "resolved"}, 9 * time.Minute, Cleared, false}}, func(t *testing.T, all []store.Signal) {
			if s := one(t, all); s.State != store.SignalCleared || !s.LastSeen.Equal(t0.Add(9*time.Minute)) {
				t.Fatalf("signal %+v", s)
			}
		}},
		{"resolved replaces the title and the body and keeps the floor", "", []step{{nil, time.Minute, Inserted, false},
			{map[string]any{"state": "resolved", "summary": "web recovered", "severity": "critical"}, 9 * time.Minute, Cleared, false}},
			func(t *testing.T, all []store.Signal) {
				s := one(t, all)
				var body map[string]any
				if err := json.Unmarshal(s.Body, &body); err != nil {
					t.Fatal(err)
				}
				if s.Title != "web recovered" || body["state"] != "resolved" || s.SeverityFloor != 3 {
					t.Fatalf("title %q body %s floor %d", s.Title, s.Body, s.SeverityFloor)
				}
			}},
		{"resolved without an open signal writes nothing", "", []step{{map[string]any{"state": "resolved"}, time.Minute, Ignored, false}},
			func(t *testing.T, all []store.Signal) {
				if len(all) != 0 {
					t.Fatalf("%d signals", len(all))
				}
			}},
		{"a late copy of the firing event after the clear is ignored", "", []step{{nil, time.Minute, Inserted, false},
			{map[string]any{"state": "resolved"}, 9 * time.Minute, Cleared, false}, {nil, time.Minute, Ignored, false}},
			func(t *testing.T, all []store.Signal) {
				if s := one(t, all); s.State != store.SignalCleared {
					t.Fatalf("state %q", s.State)
				}
			}},
		{"a firing event at the very time of the clear is a late copy", "", []step{{nil, time.Minute, Inserted, false},
			{map[string]any{"state": "resolved"}, 9 * time.Minute, Cleared, false}, {nil, 9 * time.Minute, Ignored, false}},
			func(t *testing.T, all []store.Signal) {
				if s := one(t, all); s.State != store.SignalCleared {
					t.Fatalf("state %q", s.State)
				}
			}},
		{"a late copy is judged against every cleared signal, not only the one first seen last", "", []step{
			{map[string]any{"startsAt": t0.Add(10 * time.Minute).Format(time.RFC3339)}, 11 * time.Minute, Inserted, false},
			{map[string]any{"state": "resolved"}, 12 * time.Minute, Cleared, false},
			{nil, 13 * time.Minute, Inserted, false},
			{map[string]any{"state": "resolved"}, 30 * time.Minute, Cleared, false},
			{nil, 20 * time.Minute, Ignored, false}},
			func(t *testing.T, all []store.Signal) {
				if len(all) != 2 || all[0].State != store.SignalCleared || all[1].State != store.SignalCleared ||
					!all[0].LastSeen.Equal(t0.Add(30*time.Minute)) {
					t.Fatalf("signals %+v", all)
				}
			}},
		{"firing again after the clear inserts a new signal", "", []step{{nil, time.Minute, Inserted, false},
			{map[string]any{"state": "resolved"}, 9 * time.Minute, Cleared, false},
			{map[string]any{"startsAt": t0.Add(20 * time.Minute).Format(time.RFC3339)}, 21 * time.Minute, Inserted, false}},
			func(t *testing.T, all []store.Signal) {
				if len(all) != 2 || all[0].State != store.SignalCleared || all[1].State != store.SignalOpen ||
					all[0].Fingerprint != all[1].Fingerprint {
					t.Fatalf("signals %+v", all)
				}
			}},
		{"a platform alert goes to the operations Organization", "ops", []step{{map[string]any{"organization": ""}, time.Minute, Inserted, false}},
			func(t *testing.T, all []store.Signal) {
				if s := one(t, all); s.Organization != "ops" {
					t.Fatalf("organization %q", s.Organization)
				}
			}},
		{"a platform alert without an operations Organization is refused", "", []step{{map[string]any{"organization": ""}, time.Minute, "", true}},
			func(t *testing.T, all []store.Signal) {
				if len(all) != 0 {
					t.Fatalf("%d signals", len(all))
				}
			}},
		{"the same alert id in two clusters makes two signals", "", []step{{nil, time.Minute, Inserted, false},
			{map[string]any{"cluster": "region-b"}, time.Minute, Inserted, false}}, func(t *testing.T, all []store.Signal) {
			if len(all) != 2 || all[0].Fingerprint == all[1].Fingerprint {
				t.Fatalf("signals %+v", all)
			}
		}},
		{"the same alert id in two Organizations makes two signals", "", []step{{nil, time.Minute, Inserted, false},
			{map[string]any{"organization": "beta"}, time.Minute, Inserted, false}}, func(t *testing.T, all []store.Signal) {
			if len(all) != 2 || all[0].Organization == all[1].Organization {
				t.Fatalf("signals %+v", all)
			}
		}},
		{"an empty summary makes the alert name the title", "", []step{{map[string]any{"summary": ""}, time.Minute, Inserted, false}},
			func(t *testing.T, all []store.Signal) {
				if s := one(t, all); s.Title != "pod-crash-loop" {
					t.Fatalf("title %q", s.Title)
				}
			}},
		{"a start after the event time makes last seen the start", "", []step{{map[string]any{"startsAt": t0.Add(10 * time.Minute).Format(time.RFC3339)}, time.Minute, Inserted, false}},
			func(t *testing.T, all []store.Signal) {
				if s := one(t, all); !s.LastSeen.Equal(s.FirstSeen) {
					t.Fatalf("first %s last %s", s.FirstSeen, s.LastSeen)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			st := store.NewMemory()
			defer func() { _ = st.Close() }()
			in := Intake{OperationsOrganization: c.opsOrg}
			for i, s := range c.steps {
				data := alertData(s.data)
				ev, err := Decode(message(data, map[string]any{"time": t0.Add(s.at).Format(time.RFC3339)}))
				if err != nil {
					t.Fatalf("step %d: %v", i, err)
				}
				var res Result
				err = st.InTx(ctx, func(ctx context.Context, tx store.Tx) error {
					var err error
					res, err = in.Take(ctx, tx, ev)
					return err
				})
				if s.wantErr {
					if !errors.Is(err, ErrInvalidEvent) {
						t.Fatalf("step %d: error %v, want ErrInvalidEvent", i, err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("step %d: %v", i, err)
				}
				if res.Action != s.wantAction {
					t.Fatalf("step %d: action %q, want %q", i, res.Action, s.wantAction)
				}
			}
			all, err := st.ListSignals(ctx, store.SignalFilter{})
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, all)
		})
	}
}

// TestTakeKeepsTheIncident: a signal that belongs to an incident keeps it when it is cleared.
func TestTakeKeepsTheIncident(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	defer func() { _ = st.Close() }()
	take := func(data map[string]any, at time.Duration) Result {
		ev, err := Decode(message(alertData(data), map[string]any{"time": t0.Add(at).Format(time.RFC3339)}))
		if err != nil {
			t.Fatal(err)
		}
		var res Result
		if err := st.InTx(ctx, func(ctx context.Context, tx store.Tx) error {
			res, err = Intake{}.Take(ctx, tx, ev)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return res
	}
	s := take(nil, time.Minute).Signal
	in := store.Incident{ID: "INC-01JCF0R1K2M3N4P5Q6R7S8T9V0", Organization: "acme", Title: "web", Severity: 3,
		SeveritySetBy: store.ByRules, State: store.IncidentOpen, OpenedBy: store.ByRules, OpenedAt: t0}
	if err := st.InTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.InsertIncident(ctx, in); err != nil {
			return err
		}
		s.IncidentID = in.ID
		return tx.UpdateSignal(ctx, s)
	}); err != nil {
		t.Fatal(err)
	}
	if r := take(map[string]any{"state": "resolved"}, 3*time.Minute); r.Action != Cleared || r.Signal.IncidentID != in.ID {
		t.Fatalf("result %+v", r)
	}
}

func one(t *testing.T, all []store.Signal) store.Signal {
	t.Helper()
	if len(all) != 1 {
		t.Fatalf("%d signals, want 1: %+v", len(all), all)
	}
	return all[0]
}
