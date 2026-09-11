package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Scheduled cost reports (#6867 follow-up).
//
// A schedule names who receives a plain-text cost report, how often, and
// which sections it carries. customer_id NULL is the operator's
// Sovereign-wide report and, like a global budget, is never listed to a
// customer principal; a customer scope sees exactly the schedules naming its
// customer. The scheduler polls next_at; every attempt — sent or failed — is
// one report_deliveries row.

// ReportSchedule is one schedule. JSON tags are the wire contract with
// ui/src/api/types.ts (ReportSchedule).
type ReportSchedule struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	CustomerID   *string    `json:"customer_id"`
	CustomerName *string    `json:"customer_name,omitempty"`
	Cadence      string     `json:"cadence"`
	DayOfWeek    *int       `json:"day_of_week"`
	DayOfMonth   *int       `json:"day_of_month"`
	HourUTC      int        `json:"hour_utc"`
	Recipients   []string   `json:"recipients"`
	Sections     []string   `json:"sections"`
	Active       bool       `json:"active"`
	LastSentAt   *time.Time `json:"last_sent_at"`
	NextAt       time.Time  `json:"next_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	// Delivery aggregates for the list view: attempts in the last 30 days,
	// how many failed, and the newest failure's message.
	Sent30d   int     `json:"sent_30d"`
	Failed30d int     `json:"failed_30d"`
	LastError *string `json:"last_error"`
}

// ReportScheduleInput creates or replaces a schedule. Validation (cadence,
// day ranges, email shape, section names, customer existence) is the API's
// job; the CHECK constraints are the last line. NextAt is computed by the
// caller from the cadence (report.NextRun) so the store never owns calendar
// arithmetic.
type ReportScheduleInput struct {
	Name       string
	CustomerID *string
	Cadence    string
	DayOfWeek  *int
	DayOfMonth *int
	HourUTC    int
	Recipients []string
	Sections   []string
	Active     bool
	NextAt     time.Time
}

// ReportDelivery is one attempt to send a schedule's report.
type ReportDelivery struct {
	ID         int64     `json:"id"`
	ScheduleID string    `json:"schedule_id"`
	SentAt     time.Time `json:"sent_at"`
	WindowFrom string    `json:"window_from"`
	WindowTo   string    `json:"window_to"`
	Recipients []string  `json:"recipients"`
	Subject    string    `json:"subject"`
	OK         bool      `json:"ok"`
	Error      *string   `json:"error"`
}

// ReportDeliveryInput records one attempt. WindowTo is the half-open end
// (the day after the last reported day), stored as a DATE.
type ReportDeliveryInput struct {
	WindowFrom time.Time
	WindowTo   time.Time
	Recipients []string
	Subject    string
	OK         bool
	Error      string
}

const reportColumns = `r.id, r.name, r.customer_id, c.name, r.cadence, r.day_of_week, r.day_of_month, r.hour_utc,
	r.recipients, r.sections, r.active, r.last_sent_at, r.next_at, r.created_at, r.updated_at,
	(SELECT count(*) FROM report_deliveries d WHERE d.schedule_id = r.id AND d.sent_at >= now() - interval '30 days'),
	(SELECT count(*) FROM report_deliveries d WHERE d.schedule_id = r.id AND d.sent_at >= now() - interval '30 days' AND NOT d.ok),
	(SELECT d.error FROM report_deliveries d WHERE d.schedule_id = r.id AND NOT d.ok ORDER BY d.sent_at DESC, d.id DESC LIMIT 1)`

const reportFrom = ` FROM report_schedules r LEFT JOIN customers c ON c.id = r.customer_id`

func scanReportSchedule(row interface{ Scan(...any) error }) (ReportSchedule, error) {
	var r ReportSchedule
	var cust, custName, lastErr sql.NullString
	var dow, dom sql.NullInt64
	var lastSent sql.NullTime
	var recipients, sections pq.StringArray
	if err := row.Scan(&r.ID, &r.Name, &cust, &custName, &r.Cadence, &dow, &dom, &r.HourUTC,
		&recipients, &sections, &r.Active, &lastSent, &r.NextAt, &r.CreatedAt, &r.UpdatedAt,
		&r.Sent30d, &r.Failed30d, &lastErr); err != nil {
		return r, mapErr(err)
	}
	r.CustomerID = strPtr(cust)
	r.CustomerName = strPtr(custName)
	r.LastError = strPtr(lastErr)
	if dow.Valid {
		v := int(dow.Int64)
		r.DayOfWeek = &v
	}
	if dom.Valid {
		v := int(dom.Int64)
		r.DayOfMonth = &v
	}
	r.Recipients = []string(recipients)
	if r.Recipients == nil {
		r.Recipients = []string{}
	}
	r.Sections = []string(sections)
	if r.Sections == nil {
		r.Sections = []string{}
	}
	r.LastSentAt = timePtr(lastSent)
	r.NextAt = r.NextAt.UTC()
	r.CreatedAt = r.CreatedAt.UTC()
	r.UpdatedAt = r.UpdatedAt.UTC()
	return r, nil
}

func (s *Store) queryReportSchedules(ctx context.Context, where string, args ...any) ([]ReportSchedule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+reportColumns+reportFrom+where+` ORDER BY r.created_at DESC, r.id`, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []ReportSchedule{}
	for rows.Next() {
		r, err := scanReportSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListReportSchedules returns the schedules the scope may see: every
// schedule for the operator; for a customer scope only those naming that
// customer. A global schedule belongs to the operator and is never shown to
// a customer.
func (s *Store) ListReportSchedules(ctx context.Context, scope Scope) ([]ReportSchedule, error) {
	if scope.Operator {
		return s.queryReportSchedules(ctx, "")
	}
	if len(scope.Set()) == 0 {
		return nil, ErrNotFound
	}
	return s.queryReportSchedules(ctx, ` WHERE r.customer_id::text = ANY($1)`, pq.Array(scope.Set()))
}

// GetReportSchedule returns one schedule, or ErrNotFound when it does not
// exist or lies outside the scope.
func (s *Store) GetReportSchedule(ctx context.Context, scope Scope, id string) (ReportSchedule, error) {
	r, err := scanReportSchedule(s.db.QueryRowContext(ctx, `SELECT `+reportColumns+reportFrom+` WHERE r.id::text = $1`, id))
	if err != nil {
		return ReportSchedule{}, err
	}
	if !scope.Operator {
		if r.CustomerID == nil || !scope.Allows(*r.CustomerID) {
			return ReportSchedule{}, ErrNotFound
		}
	}
	return r, nil
}

func nullInt(p *int) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true}
}

func normReportInput(in ReportScheduleInput) ReportScheduleInput {
	in.Name = strings.TrimSpace(in.Name)
	in.Cadence = strings.ToLower(strings.TrimSpace(in.Cadence))
	if in.Recipients == nil {
		in.Recipients = []string{}
	}
	if in.Sections == nil {
		in.Sections = []string{}
	}
	in.NextAt = in.NextAt.UTC()
	return in
}

// CreateReportSchedule stores a schedule and returns it with the customer
// name joined.
func (s *Store) CreateReportSchedule(ctx context.Context, in ReportScheduleInput) (ReportSchedule, error) {
	in = normReportInput(in)
	var id string
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO report_schedules (name, customer_id, cadence, day_of_week, day_of_month, hour_utc, recipients, sections, active, next_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		in.Name, nullStr(in.CustomerID), in.Cadence, nullInt(in.DayOfWeek), nullInt(in.DayOfMonth), in.HourUTC,
		pq.StringArray(in.Recipients), pq.StringArray(in.Sections), in.Active, in.NextAt).Scan(&id)
	if err != nil {
		return ReportSchedule{}, mapErr(err)
	}
	return s.GetReportSchedule(ctx, OperatorScope, id)
}

// UpdateReportSchedule replaces every editable field of a schedule,
// including next_at (the caller recomputes it from the new cadence).
func (s *Store) UpdateReportSchedule(ctx context.Context, id string, in ReportScheduleInput) (ReportSchedule, error) {
	in = normReportInput(in)
	res, err := s.db.ExecContext(ctx,
		`UPDATE report_schedules SET name = $2, customer_id = $3, cadence = $4, day_of_week = $5, day_of_month = $6, hour_utc = $7,
		        recipients = $8, sections = $9, active = $10, next_at = $11, updated_at = now()
		  WHERE id::text = $1`,
		id, in.Name, nullStr(in.CustomerID), in.Cadence, nullInt(in.DayOfWeek), nullInt(in.DayOfMonth), in.HourUTC,
		pq.StringArray(in.Recipients), pq.StringArray(in.Sections), in.Active, in.NextAt)
	if err != nil {
		return ReportSchedule{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ReportSchedule{}, ErrNotFound
	}
	return s.GetReportSchedule(ctx, OperatorScope, id)
}

// DeleteReportSchedule removes a schedule and, through the FK cascade, its
// deliveries.
func (s *Store) DeleteReportSchedule(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM report_schedules WHERE id::text = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DueReportSchedules returns every active schedule whose next_at has passed
// at now, oldest due first — what the scheduler walks each tick.
func (s *Store) DueReportSchedules(ctx context.Context, now time.Time) ([]ReportSchedule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+reportColumns+reportFrom+` WHERE r.active AND r.next_at <= $1 ORDER BY r.next_at, r.id`, now.UTC())
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []ReportSchedule{}
	for rows.Next() {
		r, err := scanReportSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ClaimReportRun advances a schedule's next_at from expected to nextAt and
// reports whether THIS call did it. Two scheduler ticks (a second replica,
// an overlapping run) both see the same due row; only the one whose UPDATE
// matches the unchanged next_at sends, so a report is never mailed twice
// for one due instant. It is the analogue of RecordBudgetAlert.
func (s *Store) ClaimReportRun(ctx context.Context, id string, expected, nextAt time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE report_schedules SET next_at = $3, updated_at = now() WHERE id::text = $1 AND next_at = $2`,
		id, expected.UTC(), nextAt.UTC())
	if err != nil {
		return false, mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// MarkReportSent records one delivery attempt for a schedule, sets its
// next_at and — when the attempt succeeded — last_sent_at, in one
// transaction. A failed attempt still advances next_at: the next window is
// tried at its own time, never in a retry storm.
func (s *Store) MarkReportSent(ctx context.Context, id string, now, nextAt time.Time, d ReportDeliveryInput) (ReportDelivery, error) {
	if d.Recipients == nil {
		d.Recipients = []string{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReportDelivery{}, err
	}
	defer tx.Rollback()
	var errText sql.NullString
	if strings.TrimSpace(d.Error) != "" {
		errText = sql.NullString{String: d.Error, Valid: true}
	}
	var out ReportDelivery
	var recipients pq.StringArray
	var errOut sql.NullString
	err = tx.QueryRowContext(ctx,
		`INSERT INTO report_deliveries (schedule_id, sent_at, window_from, window_to, recipients, subject, ok, error)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id, schedule_id, sent_at, to_char(window_from, 'YYYY-MM-DD'), to_char(window_to, 'YYYY-MM-DD'), recipients, subject, ok, error`,
		id, now.UTC(), dateOnlyUTC(d.WindowFrom), dateOnlyUTC(d.WindowTo), pq.StringArray(d.Recipients), d.Subject, d.OK, errText,
	).Scan(&out.ID, &out.ScheduleID, &out.SentAt, &out.WindowFrom, &out.WindowTo, &recipients, &out.Subject, &out.OK, &errOut)
	if err != nil {
		return ReportDelivery{}, mapErr(err)
	}
	out.Recipients = []string(recipients)
	if out.Recipients == nil {
		out.Recipients = []string{}
	}
	out.Error = strPtr(errOut)
	out.SentAt = out.SentAt.UTC()
	if d.OK {
		_, err = tx.ExecContext(ctx, `UPDATE report_schedules SET last_sent_at = $2, next_at = $3, updated_at = now() WHERE id::text = $1`, id, now.UTC(), nextAt.UTC())
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE report_schedules SET next_at = $2, updated_at = now() WHERE id::text = $1`, id, nextAt.UTC())
	}
	if err != nil {
		return ReportDelivery{}, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return ReportDelivery{}, err
	}
	return out, nil
}

// ListReportDeliveries returns a schedule's attempts, newest first.
func (s *Store) ListReportDeliveries(ctx context.Context, scheduleID string, limit int) ([]ReportDelivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, schedule_id, sent_at, to_char(window_from, 'YYYY-MM-DD'), to_char(window_to, 'YYYY-MM-DD'), recipients, subject, ok, error
		   FROM report_deliveries WHERE schedule_id::text = $1 ORDER BY sent_at DESC, id DESC LIMIT $2`, scheduleID, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []ReportDelivery{}
	for rows.Next() {
		var d ReportDelivery
		var recipients pq.StringArray
		var errOut sql.NullString
		if err := rows.Scan(&d.ID, &d.ScheduleID, &d.SentAt, &d.WindowFrom, &d.WindowTo, &recipients, &d.Subject, &d.OK, &errOut); err != nil {
			return nil, err
		}
		d.Recipients = []string(recipients)
		if d.Recipients == nil {
			d.Recipients = []string{}
		}
		d.Error = strPtr(errOut)
		d.SentAt = d.SentAt.UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

// dateOnlyUTC truncates to the UTC calendar day for a DATE column.
func dateOnlyUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
