package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Notification management — the preferences and the delivery log
// (DESIGN.md §21).
//
// The CATALOGUE of events, the TEMPLATES that render them and the CHANNELS
// that carry them live in internal/notify: they are code, versioned with the
// product, and a Sovereign cannot invent an event by writing a row. What
// lives HERE is the two things that are per-Sovereign state:
//
//   - notification_preferences — which events a recipient receives, on which
//     channel, in which locale. Four scopes, resolved most-specific-first.
//   - notification_deliveries — ONE ROW PER ATTEMPT, so "was the customer
//     told" is answerable months later, and a permanently failed delivery is
//     a row an operator can see rather than a line in a log nobody reads.
//
// A preference NEVER suppresses a MANDATORY event. The catalogue declares
// which events are mandatory (an invoice, a dunning notice); the resolver in
// internal/notify ignores a disabling row for one, and the API refuses to
// write it in the first place. Both, deliberately: the write path gives the
// operator a clear refusal, and the read path is what actually guarantees it.

// ---------------------------------------------------------------------------
// migration
// ---------------------------------------------------------------------------

// notifyMigrationSQL is one transaction, idempotent against a database that
// already carries the shape. Appended at the very END of the migrations
// slice: migrations are positional, so an entry inserted above a database's
// recorded version is silently skipped. Located by content as
// MigrationNotifications.
const notifyMigrationSQL = `
-- Which events a recipient receives, on which channel, in which locale.
--
-- FOUR SCOPES, in one table, told apart by which of the two identifying
-- columns are set:
--
--   customer_id  email   what it is
--   -----------  -----   ----------------------------------------------
--   set          set     one person, on one customer  (rank 3)
--   NULL         set     one person, Sovereign-wide   (rank 2)
--   set          ''      one customer's policy        (rank 1)
--   NULL         ''      the Sovereign default        (rank 0)
--
-- The highest-ranked row that exists for an event wins; with no row at all
-- the catalogue's own default applies. rank = 2*(email set) + (customer set):
-- a row naming the PERSON beats one naming only the organisation, and
-- between two rows naming the person the one that also names the customer
-- wins. That is arithmetic rather than a list of cases, which is what keeps
-- the resolver and this comment from drifting apart.
CREATE TABLE IF NOT EXISTS notification_preferences (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	event_key TEXT NOT NULL,
	customer_id UUID REFERENCES customers(id) ON DELETE CASCADE,
	email TEXT NOT NULL DEFAULT '',
	enabled BOOLEAN NOT NULL DEFAULT true,
	channels TEXT[] NOT NULL DEFAULT ARRAY['email']::TEXT[],
	locale TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT notification_preferences_event_check CHECK (event_key <> ''),
	CONSTRAINT notification_preferences_email_check CHECK (email = '' OR (email = lower(email) AND position('@' in email) > 1))
);
-- One row per (event, scope). COALESCE on the uuid cast rather than a
-- partial index, so the NULL-customer rows collide with each other too: two
-- Sovereign defaults for one event is a contradiction the schema refuses.
CREATE UNIQUE INDEX IF NOT EXISTS notification_preferences_key_idx
	ON notification_preferences (event_key, COALESCE(customer_id::TEXT, ''), email);
CREATE INDEX IF NOT EXISTS notification_preferences_customer_idx
	ON notification_preferences (customer_id);

-- One row per DELIVERY ATTEMPT. Not per notification: a transient SMTP
-- failure followed by a success is two rows, and reading only the last one
-- for a (event, recipient) pair is what tells an operator the mail took two
-- goes. status is the outcome of THIS attempt:
--
--   sent        the channel accepted it
--   retrying    this attempt failed and another follows
--   failed      this attempt failed and no more follow — the visible one
--   suppressed  a preference switched the event off for this recipient
--   unavailable the channel is declared but has no transport (see §21.5)
CREATE TABLE IF NOT EXISTS notification_deliveries (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	event_key TEXT NOT NULL,
	customer_id UUID REFERENCES customers(id) ON DELETE SET NULL,
	channel TEXT NOT NULL DEFAULT 'email',
	recipient TEXT NOT NULL,
	locale TEXT NOT NULL DEFAULT '',
	subject TEXT NOT NULL DEFAULT '',
	attempt INT NOT NULL DEFAULT 1 CHECK (attempt >= 1),
	status TEXT NOT NULL CHECK (status IN ('sent','retrying','failed','suppressed','unavailable')),
	reason TEXT NOT NULL DEFAULT '',
	at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS notification_deliveries_at_idx ON notification_deliveries (at DESC);
CREATE INDEX IF NOT EXISTS notification_deliveries_event_idx ON notification_deliveries (event_key, at DESC);
CREATE INDEX IF NOT EXISTS notification_deliveries_customer_idx ON notification_deliveries (customer_id, at DESC);
-- The partial index the console's default view rides on: what did NOT get
-- through. It is the question asked most often and the cheapest to index.
CREATE INDEX IF NOT EXISTS notification_deliveries_bad_idx
	ON notification_deliveries (at DESC) WHERE status IN ('failed','unavailable');
`

// MigrationNotifications is the schema_migrations version of the
// notification migration, located by content so a migration appended after
// it cannot move it.
var MigrationNotifications = func() int {
	for i, m := range migrations {
		if m == notifyMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

// ---------------------------------------------------------------------------
// model
// ---------------------------------------------------------------------------

// Delivery statuses. One attempt, one outcome.
const (
	// NotifyStatusSent — the channel accepted the message.
	NotifyStatusSent = "sent"
	// NotifyStatusRetrying — this attempt failed and another follows.
	NotifyStatusRetrying = "retrying"
	// NotifyStatusFailed — this attempt failed and no more follow. This is
	// the status the console surfaces and the metric counts.
	NotifyStatusFailed = "failed"
	// NotifyStatusSuppressed — a preference switched the event off for this
	// recipient. Recorded, never silent: "we chose not to tell them" is an
	// answer, and an unrecorded non-send is not.
	NotifyStatusSuppressed = "suppressed"
	// NotifyStatusUnavailable — the channel is declared but carries no
	// transport (DESIGN.md §21.5). The reason column says why.
	NotifyStatusUnavailable = "unavailable"
)

// NotifyStatuses lists every status, in display order.
var NotifyStatuses = []string{NotifyStatusSent, NotifyStatusRetrying, NotifyStatusFailed, NotifyStatusSuppressed, NotifyStatusUnavailable}

// NotificationPreference is one row: which events reach which recipient, on
// which channel, in which locale.
type NotificationPreference struct {
	ID      string `json:"id,omitempty"`
	Event   string `json:"event"`
	Enabled bool   `json:"enabled"`
	// Channels are the channel names this recipient receives the event on.
	// Empty means "the event's default channels".
	Channels []string `json:"channels"`
	// Locale is the template locale; empty means the default locale.
	Locale string `json:"locale,omitempty"`
	// CustomerID and Email identify the SCOPE (see the migration comment).
	CustomerID *string   `json:"customer_id,omitempty"`
	Email      string    `json:"email,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

// Rank is the specificity of the row's scope: 2 for naming a person, 1 more
// for naming a customer. The highest rank present wins.
func (p NotificationPreference) Rank() int {
	rank := 0
	if strings.TrimSpace(p.Email) != "" {
		rank += 2
	}
	if p.CustomerID != nil && *p.CustomerID != "" {
		rank++
	}
	return rank
}

// ScopeLabel names the scope the way the console shows it.
func (p NotificationPreference) ScopeLabel() string {
	switch {
	case p.Email != "" && p.CustomerID != nil:
		return "customer:" + *p.CustomerID + " · " + p.Email
	case p.Email != "":
		return "sovereign · " + p.Email
	case p.CustomerID != nil:
		return "customer:" + *p.CustomerID
	}
	return "sovereign"
}

// NotificationDelivery is one recorded attempt.
type NotificationDelivery struct {
	ID         string    `json:"id"`
	Event      string    `json:"event"`
	CustomerID *string   `json:"customer_id,omitempty"`
	Channel    string    `json:"channel"`
	Recipient  string    `json:"recipient"`
	Locale     string    `json:"locale,omitempty"`
	Subject    string    `json:"subject,omitempty"`
	Attempt    int       `json:"attempt"`
	Status     string    `json:"status"`
	Reason     string    `json:"reason,omitempty"`
	At         time.Time `json:"at"`
}

// NotificationDeliveryInput is what the notifier records per attempt.
type NotificationDeliveryInput struct {
	Event      string
	CustomerID *string
	Channel    string
	Recipient  string
	Locale     string
	Subject    string
	Attempt    int
	Status     string
	Reason     string
	At         time.Time
}

// NotificationDeliveryFilter narrows the delivery log.
type NotificationDeliveryFilter struct {
	Event string
	// Status filters to one status; "problems" is the shorthand the console
	// opens with — failed and unavailable together.
	Status string
	// CustomerID filters to one customer's deliveries.
	CustomerID string
	// Recipient filters to one address.
	Recipient string
	// Limit caps the rows; 0 means DefaultNotificationDeliveryLimit.
	Limit int
}

// StatusProblems is the Status shorthand for "everything that did not get
// through": failed and unavailable.
const StatusProblems = "problems"

// DefaultNotificationDeliveryLimit is how many attempts a listing returns
// when the caller asks for no limit.
const DefaultNotificationDeliveryLimit = 200

// MaxNotificationDeliveryLimit caps what a caller may ask for.
const MaxNotificationDeliveryLimit = 1000

// ---------------------------------------------------------------------------
// preferences
// ---------------------------------------------------------------------------

// ListNotificationPreferences returns every preference row the scope may
// read, most specific first, then by event. An operator scope reads all of
// them; a customer scope reads only rows on its own customers — never the
// Sovereign defaults, which are the operator's policy and not the
// customer's to read as its own.
func (s *Store) ListNotificationPreferences(ctx context.Context, sc Scope) ([]NotificationPreference, error) {
	q := `SELECT id, event_key, customer_id, email, enabled, channels, locale, updated_at FROM notification_preferences`
	var args []any
	if !sc.Operator {
		set := sc.Set()
		if len(set) == 0 {
			return []NotificationPreference{}, nil
		}
		q += ` WHERE customer_id = ANY($1)`
		args = append(args, pq.Array(set))
	}
	q += ` ORDER BY event_key, COALESCE(customer_id::TEXT, ''), email`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	return scanNotificationPreferences(rows)
}

// NotificationPreferencesFor returns every row that could apply to one
// (customer, email) pair: the Sovereign default, the Sovereign-wide personal
// row, the customer row and the personal row on that customer. customerID ""
// and email "" each simply drop the rows that would name them.
//
// This is the read the resolver makes, and it is ONE query: a notifier that
// asked four times per recipient would make the mail path four round trips
// deep.
func (s *Store) NotificationPreferencesFor(ctx context.Context, customerID, email string) ([]NotificationPreference, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	rows, err := s.db.QueryContext(ctx, `
SELECT id, event_key, customer_id, email, enabled, channels, locale, updated_at
  FROM notification_preferences
 WHERE (customer_id IS NULL OR customer_id::TEXT = $1)
   AND (email = '' OR email = $2)
 ORDER BY event_key, COALESCE(customer_id::TEXT, ''), email`, customerID, email)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	return scanNotificationPreferences(rows)
}

func scanNotificationPreferences(rows *sql.Rows) ([]NotificationPreference, error) {
	out := []NotificationPreference{}
	for rows.Next() {
		var p NotificationPreference
		var cid sql.NullString
		var channels pq.StringArray
		if err := rows.Scan(&p.ID, &p.Event, &cid, &p.Email, &p.Enabled, &channels, &p.Locale, &p.UpdatedAt); err != nil {
			return nil, mapErr(err)
		}
		p.CustomerID = strPtr(cid)
		p.Channels = []string(channels)
		if p.Channels == nil {
			p.Channels = []string{}
		}
		p.UpdatedAt = p.UpdatedAt.UTC()
		out = append(out, p)
	}
	return out, mapErr(rows.Err())
}

// PutNotificationPreference writes one row, replacing whatever the scope
// held for that event. The scope is (CustomerID, Email) on the input.
func (s *Store) PutNotificationPreference(ctx context.Context, p NotificationPreference) (NotificationPreference, error) {
	p.Event = strings.TrimSpace(p.Event)
	if p.Event == "" {
		return NotificationPreference{}, fmt.Errorf("%w: event is required", ErrInvalid)
	}
	p.Email = strings.ToLower(strings.TrimSpace(p.Email))
	if p.Email != "" && !strings.Contains(p.Email, "@") {
		return NotificationPreference{}, fmt.Errorf("%w: %q is not an email address", ErrInvalid, p.Email)
	}
	channels := cleanChannelList(p.Channels)
	var out NotificationPreference
	var cid sql.NullString
	var got pq.StringArray
	// COALESCE in the conflict target is not allowed, so the upsert is done
	// against the unique INDEX by naming its expressions — which is exactly
	// what notification_preferences_key_idx indexes.
	err := s.db.QueryRowContext(ctx, `
INSERT INTO notification_preferences (event_key, customer_id, email, enabled, channels, locale)
VALUES ($1, NULLIF($2,'')::UUID, $3, $4, $5, $6)
ON CONFLICT (event_key, COALESCE(customer_id::TEXT, ''), email)
DO UPDATE SET enabled = EXCLUDED.enabled, channels = EXCLUDED.channels, locale = EXCLUDED.locale, updated_at = now()
RETURNING id, event_key, customer_id, email, enabled, channels, locale, updated_at`,
		p.Event, derefStr(p.CustomerID), p.Email, p.Enabled, pq.Array(channels), strings.TrimSpace(p.Locale)).
		Scan(&out.ID, &out.Event, &cid, &out.Email, &out.Enabled, &got, &out.Locale, &out.UpdatedAt)
	if err != nil {
		return NotificationPreference{}, mapErr(err)
	}
	out.CustomerID = strPtr(cid)
	out.Channels = []string(got)
	if out.Channels == nil {
		out.Channels = []string{}
	}
	out.UpdatedAt = out.UpdatedAt.UTC()
	return out, nil
}

// DeleteNotificationPreference removes one scope's row for an event, which
// returns that scope to whatever the next-less-specific row says, and
// ultimately to the catalogue default. Deleting a row that is not there is
// not an error: the caller asked for "no row here", and there is none.
func (s *Store) DeleteNotificationPreference(ctx context.Context, event string, customerID *string, email string) error {
	_, err := s.db.ExecContext(ctx, `
DELETE FROM notification_preferences
 WHERE event_key = $1
   AND COALESCE(customer_id::TEXT, '') = $2
   AND email = $3`, strings.TrimSpace(event), derefStr(customerID), strings.ToLower(strings.TrimSpace(email)))
	return mapErr(err)
}

// cleanChannelList trims, lowercases and de-duplicates a channel list,
// preserving order. It does NOT validate the names: internal/notify owns
// which channels exist, and a store that also knew would be a second
// catalogue to keep in step.
func cleanChannelList(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, c := range in {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// ---------------------------------------------------------------------------
// the delivery log
// ---------------------------------------------------------------------------

// RecordNotificationDelivery appends one attempt.
func (s *Store) RecordNotificationDelivery(ctx context.Context, in NotificationDeliveryInput) (NotificationDelivery, error) {
	if in.Attempt < 1 {
		in.Attempt = 1
	}
	if strings.TrimSpace(in.Channel) == "" {
		in.Channel = "email"
	}
	at := in.At
	if at.IsZero() {
		at = time.Now()
	}
	var out NotificationDelivery
	var cid sql.NullString
	err := s.db.QueryRowContext(ctx, `
INSERT INTO notification_deliveries (event_key, customer_id, channel, recipient, locale, subject, attempt, status, reason, at)
VALUES ($1, NULLIF($2,'')::UUID, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, event_key, customer_id, channel, recipient, locale, subject, attempt, status, reason, at`,
		strings.TrimSpace(in.Event), derefStr(in.CustomerID), strings.TrimSpace(in.Channel), strings.TrimSpace(in.Recipient),
		strings.TrimSpace(in.Locale), in.Subject, in.Attempt, in.Status, in.Reason, at.UTC()).
		Scan(&out.ID, &out.Event, &cid, &out.Channel, &out.Recipient, &out.Locale, &out.Subject, &out.Attempt, &out.Status, &out.Reason, &out.At)
	if err != nil {
		return NotificationDelivery{}, mapErr(err)
	}
	out.CustomerID = strPtr(cid)
	out.At = out.At.UTC()
	return out, nil
}

// ListNotificationDeliveries returns recorded attempts, newest first, inside
// the scope. A customer scope sees only its own customers' deliveries — a
// row with no customer (a sign-in code for an operator address, say) is
// never visible to a customer principal.
func (s *Store) ListNotificationDeliveries(ctx context.Context, sc Scope, f NotificationDeliveryFilter) ([]NotificationDelivery, error) {
	var where []string
	var args []any
	add := func(clause string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if !sc.Operator {
		set := sc.Set()
		if len(set) == 0 {
			return []NotificationDelivery{}, nil
		}
		add("customer_id = ANY($%d)", pq.Array(set))
	}
	if e := strings.TrimSpace(f.Event); e != "" {
		add("event_key = $%d", e)
	}
	if c := strings.TrimSpace(f.CustomerID); c != "" {
		add("customer_id::TEXT = $%d", c)
	}
	if r := strings.ToLower(strings.TrimSpace(f.Recipient)); r != "" {
		add("lower(recipient) = $%d", r)
	}
	switch st := strings.TrimSpace(f.Status); st {
	case "":
	case StatusProblems:
		where = append(where, "status IN ('failed','unavailable')")
	default:
		add("status = $%d", st)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultNotificationDeliveryLimit
	}
	if limit > MaxNotificationDeliveryLimit {
		limit = MaxNotificationDeliveryLimit
	}
	q := `SELECT id, event_key, customer_id, channel, recipient, locale, subject, attempt, status, reason, at FROM notification_deliveries`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY at DESC, id DESC LIMIT $%d", len(args))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []NotificationDelivery{}
	for rows.Next() {
		var d NotificationDelivery
		var cid sql.NullString
		if err := rows.Scan(&d.ID, &d.Event, &cid, &d.Channel, &d.Recipient, &d.Locale, &d.Subject, &d.Attempt, &d.Status, &d.Reason, &d.At); err != nil {
			return nil, mapErr(err)
		}
		d.CustomerID = strPtr(cid)
		d.At = d.At.UTC()
		out = append(out, d)
	}
	return out, mapErr(rows.Err())
}

// NotificationDeliveryCount is one status's tally over a window.
type NotificationDeliveryCount struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

// NotificationDeliveryStats counts attempts by status since `since`, inside
// the scope — what the console's tiles read.
func (s *Store) NotificationDeliveryStats(ctx context.Context, sc Scope, since time.Time) ([]NotificationDeliveryCount, error) {
	q := `SELECT status, count(*) FROM notification_deliveries WHERE at >= $1`
	args := []any{since.UTC()}
	if !sc.Operator {
		set := sc.Set()
		if len(set) == 0 {
			return []NotificationDeliveryCount{}, nil
		}
		args = append(args, pq.Array(set))
		q += fmt.Sprintf(" AND customer_id = ANY($%d)", len(args))
	}
	q += ` GROUP BY status`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	byStatus := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, mapErr(err)
		}
		byStatus[st] = n
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	out := []NotificationDeliveryCount{}
	for _, st := range NotifyStatuses {
		if n, ok := byStatus[st]; ok {
			out = append(out, NotificationDeliveryCount{Status: st, Count: n})
			delete(byStatus, st)
		}
	}
	// A status the code no longer knows about is still reported rather than
	// dropped: a tile that silently omits rows is how a count stops adding up.
	rest := make([]string, 0, len(byStatus))
	for st := range byStatus {
		rest = append(rest, st)
	}
	sort.Strings(rest)
	for _, st := range rest {
		out = append(out, NotificationDeliveryCount{Status: st, Count: byStatus[st]})
	}
	return out, nil
}

// PurgeNotificationDeliveries drops attempts older than age; age <= 0 keeps
// the log for ever. Called from the hourly housekeeping pass with
// config.NotificationRetention (NOTIFICATION_RETENTION_DAYS) — how long a
// record of what was sent to whom may be held is the operator's own
// data-retention decision, not this product's.
func (s *Store) PurgeNotificationDeliveries(ctx context.Context, age time.Duration) error {
	if age <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM notification_deliveries WHERE at < now() - $1::interval`, fmt.Sprintf("%d seconds", int64(age.Seconds())))
	return mapErr(err)
}
