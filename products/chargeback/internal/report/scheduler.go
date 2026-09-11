package report

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/mail"
	"github.com/openova-io/openova/products/chargeback/internal/notify"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Store is what the scheduler needs from the database.
type Store interface {
	Reader
	DueReportSchedules(ctx context.Context, now time.Time) ([]store.ReportSchedule, error)
	ClaimReportRun(ctx context.Context, id string, expected, nextAt time.Time) (bool, error)
	MarkReportSent(ctx context.Context, id string, now, nextAt time.Time, d store.ReportDeliveryInput) (store.ReportDelivery, error)
	Audit(ctx context.Context, customerID *string, actor, action string, details any) error
}

// Scheduler mails every due schedule's report. It polls the store (default
// every 5 minutes, first run one minute after start) rather than keeping
// timers, so a restart or a second replica needs no state of its own.
//
// Once-only rests on the store: ClaimReportRun advances next_at with a
// compare-and-set on the due instant, and only the caller whose claim went
// through sends. A failed delivery is recorded (ok = false, error) and the
// schedule still moves to its next window — never a retry storm.
type Scheduler struct {
	Store Store
	Mail  mail.Sender
	// Notify routes the report through the notification catalogue as the
	// report.scheduled event (DESIGN.md §21); nil builds one over Mail.
	Notify *notify.Notifier
	// Now defaults to time.Now.
	Now func() time.Time
	// Interval between polls; default 5 minutes.
	Interval time.Duration
	// InitialDelay before the first poll; default one minute.
	InitialDelay time.Duration
	// PublicURL is the console base URL for the footer link.
	PublicURL string
}

// Report counts what one poll did.
type Report struct {
	Due     int `json:"due"`
	Sent    int `json:"sent"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"` // claimed by a concurrent poll
}

// Delivery is the outcome of one send.
type Delivery struct {
	Subject    string
	Body       string
	SentTo     []string
	WindowFrom time.Time
	WindowTo   time.Time
	Record     store.ReportDelivery
	// Err is the delivery failure (build or every recipient failed); the
	// record is still written.
	Err error
}

// notifier is the notification path: the one wired in, or one built over
// Mail for a caller that wired only a sender.
func (s *Scheduler) notifier() *notify.Notifier {
	if s.Notify != nil {
		return s.Notify
	}
	return &notify.Notifier{Channels: notify.DefaultChannels(s.Mail)}
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Run blocks until ctx is done: one poll after InitialDelay, then one per
// Interval.
func (s *Scheduler) Run(ctx context.Context) {
	delay := s.InitialDelay
	if delay <= 0 {
		delay = time.Minute
	}
	interval := s.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}
	s.tick(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	rep, err := s.RunOnce(ctx)
	if err != nil {
		slog.Warn("report scheduler", "error", err)
		return
	}
	if rep.Due > 0 {
		slog.Info("report scheduler", "due", rep.Due, "sent", rep.Sent, "failed", rep.Failed, "skipped", rep.Skipped)
	}
}

// RunOnce sends every due schedule once. The returned error is only for the
// listing itself; per-schedule failures are counted and recorded.
func (s *Scheduler) RunOnce(ctx context.Context) (Report, error) {
	var rep Report
	now := s.now()
	due, err := s.Store.DueReportSchedules(ctx, now)
	if err != nil {
		return rep, err
	}
	for _, sched := range due {
		rep.Due++
		nextAt := NextRunFor(sched, now)
		claimed, err := s.Store.ClaimReportRun(ctx, sched.ID, sched.NextAt, nextAt)
		if err != nil {
			slog.Warn("claim report run", "schedule_id", sched.ID, "error", err)
			rep.Failed++
			continue
		}
		if !claimed {
			rep.Skipped++
			continue
		}
		d := s.Deliver(ctx, sched, now, nextAt, "system")
		if d.Err != nil {
			slog.Warn("report delivery failed", "schedule_id", sched.ID, "error", d.Err)
			rep.Failed++
			continue
		}
		rep.Sent++
	}
	return rep, nil
}

// NextRunFor is the schedule's next due instant after `after`.
func NextRunFor(s store.ReportSchedule, after time.Time) time.Time {
	dow, dom := DefaultDayOfWeek, DefaultDayOfMonth
	if s.DayOfWeek != nil {
		dow = *s.DayOfWeek
	}
	if s.DayOfMonth != nil {
		dom = *s.DayOfMonth
	}
	return NextRun(s.Cadence, dow, dom, s.HourUTC, after)
}

// Preview builds and renders the report a send at `now` would mail, without
// sending or recording anything.
func (s *Scheduler) Preview(ctx context.Context, sched store.ReportSchedule, now time.Time) (subject, body string, from, to time.Time, err error) {
	from, to = Window(sched.Cadence, now)
	in, err := Build(ctx, s.Store, sched, from, to, now, s.PublicURL)
	if err != nil {
		return "", "", from, to, err
	}
	subject, body = Render(in)
	return subject, body, from, to, nil
}

// Deliver builds the report for the window the cadence implies at `now`,
// mails every recipient, records the attempt (ok = every recipient
// accepted) with next_at set to nextAt, and audits it as report.sent or
// report.failed under actor. A manual "send now" passes the schedule's own
// NextAt so the timetable is untouched.
func (s *Scheduler) Deliver(ctx context.Context, sched store.ReportSchedule, now, nextAt time.Time, actor string) Delivery {
	now = now.UTC()
	from, to := Window(sched.Cadence, now)
	d := Delivery{WindowFrom: from, WindowTo: to, SentTo: []string{}}
	rec := store.ReportDeliveryInput{WindowFrom: from, WindowTo: to, Recipients: []string{}}

	in, err := Build(ctx, s.Store, sched, from, to, now, s.PublicURL)
	if err != nil {
		d.Err = fmt.Errorf("build report: %w", err)
		rec.Error = d.Err.Error()
	} else {
		d.Subject, d.Body = Render(in)
		rec.Subject = d.Subject
		// DESIGN.md §21 — the report goes out as the report.scheduled
		// event. The DOCUMENT is still rendered here (Render owns the
		// tables, the money and the column alignment); the template carries
		// it, which is what puts the send on the catalogue, the delivery log
		// and the preference rule without changing a byte of the report.
		payload := map[string]any{
			"subject": d.Subject, "document": d.Body,
			"schedule_id": sched.ID, "schedule_name": sched.Name, "cadence": sched.Cadence,
		}
		n := s.notifier()
		var failures []string
		for _, to := range sched.Recipients {
			res, err := n.Send(ctx, notify.Request{Event: notify.EventReportScheduled, To: to, CustomerID: sched.CustomerID, Payload: payload})
			if err != nil {
				failures = append(failures, to+": "+err.Error())
				continue
			}
			if !res.Sent {
				// Suppressed by a preference: not a failure, and not a
				// recipient this run reached either. Recorded in the
				// delivery log, with the preference that decided it.
				continue
			}
			d.SentTo = append(d.SentTo, to)
		}
		rec.Recipients = d.SentTo
		switch {
		case len(sched.Recipients) == 0:
			d.Err = errors.New("schedule has no recipients")
			rec.Error = d.Err.Error()
		case len(failures) > 0 && len(d.SentTo) == 0:
			d.Err = errors.New("send: " + strings.Join(failures, "; "))
			rec.Error = d.Err.Error()
		case len(failures) > 0:
			// Partial: some recipients got it. Recorded as ok with the
			// failures noted, so the run is not repeated for everyone.
			rec.Error = "partial: " + strings.Join(failures, "; ")
			rec.OK = true
		default:
			rec.OK = true
		}
	}

	record, err := s.Store.MarkReportSent(ctx, sched.ID, now, nextAt, rec)
	if err != nil {
		slog.Warn("record report delivery", "schedule_id", sched.ID, "error", err)
		if d.Err == nil {
			d.Err = fmt.Errorf("record delivery: %w", err)
		}
	}
	d.Record = record

	details := map[string]any{
		"schedule_id": sched.ID, "name": sched.Name, "cadence": sched.Cadence,
		"window_from": from.Format("2006-01-02"), "window_to": to.Format("2006-01-02"),
		"recipients": d.SentTo, "subject": d.Subject, "ok": rec.OK,
	}
	action := "report.sent"
	if !rec.OK {
		action = "report.failed"
	}
	if rec.Error != "" {
		details["error"] = rec.Error
	}
	if err := s.Store.Audit(ctx, sched.CustomerID, actor, action, details); err != nil {
		slog.Warn("audit report delivery", "schedule_id", sched.ID, "error", err)
	}
	return d
}
