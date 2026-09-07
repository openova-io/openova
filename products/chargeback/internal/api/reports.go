package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/report"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Scheduled cost reports (#6867 follow-up).
//
// Reads follow the session scope: the operator sees every schedule, a
// customer principal only the schedules naming its customer. The operator
// may write any schedule; a customer-admin may create, edit, delete, send
// and preview schedules for ITS OWN customer only — customer_id is forced to
// the session's customer on every customer write, whatever the body says. A
// customer-viewer reads and previews but never writes or sends.

const maxReportRecipients = 20

// reportBody is the request shape for POST and PUT. Every field is optional
// on the wire so PUT can be a partial replacement over the stored row.
// customer_id, day_of_week and day_of_month are raw so PUT can tell
// "absent" (keep) from null (clear).
type reportBody struct {
	Name       *string         `json:"name"`
	CustomerID json.RawMessage `json:"customer_id"`
	Cadence    *string         `json:"cadence"`
	DayOfWeek  json.RawMessage `json:"day_of_week"`
	DayOfMonth json.RawMessage `json:"day_of_month"`
	HourUTC    *int            `json:"hour_utc"`
	Recipients *[]string       `json:"recipients"`
	Sections   *[]string       `json:"sections"`
	Active     *bool           `json:"active"`
}

func rawInt(raw json.RawMessage, field string) (*int, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if string(raw) == "null" {
		return nil, true, nil
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, true, errors.New(field + " must be a whole number or null")
	}
	return &n, true, nil
}

// merge overlays the body on base (the stored row for PUT, the defaults for
// POST).
func (in reportBody) merge(base store.ReportScheduleInput) (store.ReportScheduleInput, error) {
	out := base
	if in.Name != nil {
		out.Name = *in.Name
	}
	if len(in.CustomerID) > 0 {
		if string(in.CustomerID) == "null" {
			out.CustomerID = nil
		} else {
			var s string
			if err := json.Unmarshal(in.CustomerID, &s); err != nil {
				return out, errors.New("customer_id must be a string or null")
			}
			s = strings.TrimSpace(s)
			if s == "" {
				out.CustomerID = nil
			} else {
				out.CustomerID = &s
			}
		}
	}
	if in.Cadence != nil {
		out.Cadence = *in.Cadence
	}
	if v, set, err := rawInt(in.DayOfWeek, "day_of_week"); err != nil {
		return out, err
	} else if set {
		out.DayOfWeek = v
	}
	if v, set, err := rawInt(in.DayOfMonth, "day_of_month"); err != nil {
		return out, err
	} else if set {
		out.DayOfMonth = v
	}
	if in.HourUTC != nil {
		out.HourUTC = *in.HourUTC
	}
	if in.Recipients != nil {
		out.Recipients = *in.Recipients
	}
	if in.Sections != nil {
		out.Sections = *in.Sections
	}
	if in.Active != nil {
		out.Active = *in.Active
	}
	return out, nil
}

// validateReport normalizes and checks a merged input and computes its
// next_at. The second return is the 400 message; the error is a store error
// (404 for an unknown customer). A customer session has its customer forced.
func (h *Handler) validateReport(ctx context.Context, s store.Session, in store.ReportScheduleInput) (store.ReportScheduleInput, string, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return in, "name is required", nil
	}
	if len(in.Name) > 120 {
		return in, "name must be 120 characters or fewer", nil
	}
	in.Cadence = strings.ToLower(strings.TrimSpace(in.Cadence))
	switch in.Cadence {
	case report.CadenceDaily:
		in.DayOfWeek, in.DayOfMonth = nil, nil
	case report.CadenceWeekly:
		in.DayOfMonth = nil
		if in.DayOfWeek == nil {
			d := report.DefaultDayOfWeek
			in.DayOfWeek = &d
		}
		if *in.DayOfWeek < 0 || *in.DayOfWeek > 6 {
			return in, "day_of_week must be 0 (Sunday) to 6 (Saturday)", nil
		}
	case report.CadenceMonthly:
		in.DayOfWeek = nil
		if in.DayOfMonth == nil {
			d := report.DefaultDayOfMonth
			in.DayOfMonth = &d
		}
		if *in.DayOfMonth < 1 || *in.DayOfMonth > 28 {
			return in, "day_of_month must be 1 to 28", nil
		}
	default:
		return in, "cadence must be daily, weekly or monthly", nil
	}
	if in.HourUTC < 0 || in.HourUTC > 23 {
		return in, "hour_utc must be 0 to 23", nil
	}

	emails := make([]string, 0, len(in.Recipients))
	seen := map[string]bool{}
	for _, e := range in.Recipients {
		e = normEmail(e)
		if e == "" {
			continue
		}
		if !validEmail(e) {
			return in, "recipients must be valid email addresses", nil
		}
		if seen[e] {
			continue
		}
		seen[e] = true
		emails = append(emails, e)
	}
	if len(emails) == 0 {
		return in, "at least one recipient is required", nil
	}
	if len(emails) > maxReportRecipients {
		return in, "at most " + strconv.Itoa(maxReportRecipients) + " recipients", nil
	}
	in.Recipients = emails

	if len(in.Sections) == 0 {
		in.Sections = report.Sections()
	}
	for _, sec := range in.Sections {
		if !report.ValidSection(strings.TrimSpace(sec)) {
			return in, "sections must be among " + strings.Join(report.Sections(), ", "), nil
		}
	}
	in.Sections = report.NormalizeSections(in.Sections)

	if s.Role != store.RoleOperator {
		// A customer writes only its own customer's schedules.
		if s.CustomerID == nil {
			return in, "", store.ErrNotFound
		}
		cid := *s.CustomerID
		in.CustomerID = &cid
	} else if in.CustomerID != nil {
		if _, err := h.Store.GetCustomer(ctx, store.OperatorScope, *in.CustomerID); err != nil {
			return in, "", err
		}
	}

	dow, dom := report.DefaultDayOfWeek, report.DefaultDayOfMonth
	if in.DayOfWeek != nil {
		dow = *in.DayOfWeek
	}
	if in.DayOfMonth != nil {
		dom = *in.DayOfMonth
	}
	in.NextAt = report.NextRun(in.Cadence, dow, dom, in.HourUTC, h.Now().UTC())
	return in, "", nil
}

func reportAudit(r store.ReportSchedule) map[string]any {
	return map[string]any{
		"schedule_id": r.ID, "name": r.Name, "customer_id": r.CustomerID, "cadence": r.Cadence,
		"day_of_week": r.DayOfWeek, "day_of_month": r.DayOfMonth, "hour_utc": r.HourUTC,
		"recipients": r.Recipients, "sections": r.Sections, "active": r.Active, "next_at": r.NextAt,
	}
}

// reports is the deliverer the handlers share with the background scheduler.
func (h *Handler) reports() *report.Scheduler {
	return &report.Scheduler{Store: h.Store, Mail: h.Mail, Now: h.Now, PublicURL: h.Config.PublicURL}
}

// requireReportWriter answers 401/403 unless the session may write
// schedules: operators always; customer-admins for their own customer.
func (h *Handler) requireReportWriter(w http.ResponseWriter, r *http.Request) (store.Session, bool) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return s, false
	}
	if s.Role == store.RoleOperator {
		return s, true
	}
	if s.Role != store.RoleCustomerAdmin || s.CustomerID == nil {
		writeErr(w, http.StatusForbidden, "customer admin role required")
		return s, false
	}
	return s, true
}

// listReportSchedules — GET /api/v1/reports/schedules.
func (h *Handler) listReportSchedules(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	rows, err := h.Store.ListReportSchedules(r.Context(), s.Scope())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": rows})
}

// customerReportSchedules — GET /api/v1/customers/{id}/reports/schedules.
func (h *Handler) customerReportSchedules(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireCustomer(w, r, id, false); !ok {
		return
	}
	rows, err := h.Store.ListReportSchedules(r.Context(), store.CustomerScope(id))
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": rows})
}

// createReportSchedule — POST /api/v1/reports/schedules.
func (h *Handler) createReportSchedule(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireReportWriter(w, r)
	if !ok {
		return
	}
	var body reportBody
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	in, err := body.merge(store.ReportScheduleInput{Cadence: report.CadenceWeekly, HourUTC: report.DefaultHourUTC, Active: true})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	in, msg, err := h.validateReport(r.Context(), s, in)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err != nil {
		storeErr(w, err)
		return
	}
	row, err := h.Store.CreateReportSchedule(r.Context(), in)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, row.CustomerID, "report.schedule.create", reportAudit(row))
	writeJSON(w, http.StatusCreated, row)
}

// getReportSchedule — GET /api/v1/reports/schedules/{id}.
func (h *Handler) getReportSchedule(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	row, err := h.Store.GetReportSchedule(r.Context(), s.Scope(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

// updateReportSchedule — PUT /api/v1/reports/schedules/{id}. Fields absent
// from the body keep their stored value; next_at is recomputed.
func (h *Handler) updateReportSchedule(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireReportWriter(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	cur, err := h.Store.GetReportSchedule(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	var body reportBody
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	in, err := body.merge(store.ReportScheduleInput{
		Name: cur.Name, CustomerID: cur.CustomerID, Cadence: cur.Cadence, DayOfWeek: cur.DayOfWeek, DayOfMonth: cur.DayOfMonth,
		HourUTC: cur.HourUTC, Recipients: cur.Recipients, Sections: cur.Sections, Active: cur.Active,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	in, msg, err := h.validateReport(r.Context(), s, in)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err != nil {
		storeErr(w, err)
		return
	}
	row, err := h.Store.UpdateReportSchedule(r.Context(), id, in)
	if err != nil {
		storeErr(w, err)
		return
	}
	details := reportAudit(row)
	details["previous_customer_id"] = cur.CustomerID
	details["previous_cadence"] = cur.Cadence
	details["previous_recipients"] = cur.Recipients
	h.audit(r, row.CustomerID, "report.schedule.update", details)
	writeJSON(w, http.StatusOK, row)
}

// deleteReportSchedule — DELETE /api/v1/reports/schedules/{id}.
func (h *Handler) deleteReportSchedule(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireReportWriter(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	cur, err := h.Store.GetReportSchedule(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	if err := h.Store.DeleteReportSchedule(r.Context(), id); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, cur.CustomerID, "report.schedule.delete", reportAudit(cur))
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// sendReportNow — POST /api/v1/reports/schedules/{id}/send: build the
// report for the window the cadence implies today, mail it, record the
// delivery. The timetable (next_at) is untouched.
func (h *Handler) sendReportNow(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireReportWriter(w, r)
	if !ok {
		return
	}
	sched, err := h.Store.GetReportSchedule(r.Context(), s.Scope(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	d := h.reports().Deliver(r.Context(), sched, h.Now().UTC(), sched.NextAt, s.Email)
	out := map[string]any{
		"sent_to": d.SentTo, "subject": d.Subject,
		"window_from": d.WindowFrom.Format("2006-01-02"), "window_to": d.WindowTo.Format("2006-01-02"),
		"delivery": d.Record,
	}
	if d.Err != nil {
		out["error"] = d.Err.Error()
		writeJSON(w, http.StatusBadGateway, out)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// previewReport — GET /api/v1/reports/schedules/{id}/preview: the subject
// and body a send today would mail, without sending.
func (h *Handler) previewReport(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	sched, err := h.Store.GetReportSchedule(r.Context(), s.Scope(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	subject, body, from, to, err := h.reports().Preview(r.Context(), sched, h.Now().UTC())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subject": subject, "body": body,
		"window_from": from.Format("2006-01-02"), "window_to": to.Format("2006-01-02"),
		"recipients": sched.Recipients,
	})
}

// listReportDeliveries — GET /api/v1/reports/schedules/{id}/deliveries.
func (h *Handler) listReportDeliveries(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	sched, err := h.Store.GetReportSchedule(r.Context(), s.Scope(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeErr(w, http.StatusBadRequest, "limit must be 1..500")
			return
		}
		limit = n
	}
	rows, err := h.Store.ListReportDeliveries(r.Context(), sched.ID, limit)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": rows})
}
