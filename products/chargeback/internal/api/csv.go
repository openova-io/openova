package api

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// CustomerImportRow is one customer from a bulk import (CSV or JSON array).
type CustomerImportRow struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	AdminEmail  string   `json:"admin_email"`
	Region      string   `json:"region"`
	ProjectIDs  []string `json:"project_ids"`
	PriceBook   string   `json:"price_book"`
	BillingMode string   `json:"billing_mode"`
	StartDate   string   `json:"start_date"`
	Line        int      `json:"-"`
}

// ImportError is one rejected import row.
type ImportError struct {
	Line    int    `json:"line"`
	Slug    string `json:"slug,omitempty"`
	Message string `json:"message"`
}

// ParseCustomerImportCSV reads slug,name,admin_email,region,project_ids
// (;-separated),price_book,billing_mode,start_date with a header row in any
// column order.
func ParseCustomerImportCSV(r io.Reader) ([]CustomerImportRow, []ImportError, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("read header: %w", err)
	}
	idx := map[string]int{}
	for i, hd := range header {
		idx[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(hd, "\uFEFF")))] = i
	}
	for _, need := range []string{"slug", "name", "admin_email"} {
		if _, ok := idx[need]; !ok {
			return nil, nil, fmt.Errorf("missing column %q", need)
		}
	}
	get := func(rec []string, name string) string {
		i, ok := idx[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}
	var rows []CustomerImportRow
	var errs []ImportError
	line := 1
	for {
		rec, err := cr.Read()
		line++
		if err == io.EOF {
			break
		}
		if err != nil {
			errs = append(errs, ImportError{Line: line, Message: err.Error()})
			continue
		}
		if len(rec) == 0 || (len(rec) == 1 && strings.TrimSpace(rec[0]) == "") {
			continue
		}
		row := CustomerImportRow{
			Slug:        strings.ToLower(get(rec, "slug")),
			Name:        get(rec, "name"),
			AdminEmail:  normEmail(get(rec, "admin_email")),
			Region:      get(rec, "region"),
			ProjectIDs:  splitProjects(get(rec, "project_ids")),
			PriceBook:   get(rec, "price_book"),
			BillingMode: get(rec, "billing_mode"),
			StartDate:   get(rec, "start_date"),
			Line:        line,
		}
		if msg := validateImportRow(row); msg != "" {
			errs = append(errs, ImportError{Line: line, Slug: row.Slug, Message: msg})
			continue
		}
		rows = append(rows, row)
	}
	return rows, errs, nil
}

// ParseCustomerImportJSON reads a JSON array of rows.
func ParseCustomerImportJSON(r io.Reader) ([]CustomerImportRow, []ImportError, error) {
	var in []CustomerImportRow
	dec := json.NewDecoder(io.LimitReader(r, maxBodyBytes))
	if err := dec.Decode(&in); err != nil {
		return nil, nil, fmt.Errorf("decode JSON array: %w", err)
	}
	var rows []CustomerImportRow
	var errs []ImportError
	for i, row := range in {
		row.Line = i + 1
		row.Slug = strings.ToLower(strings.TrimSpace(row.Slug))
		row.AdminEmail = normEmail(row.AdminEmail)
		var pids []string
		for _, p := range row.ProjectIDs {
			pids = append(pids, splitProjects(p)...)
		}
		row.ProjectIDs = pids
		if msg := validateImportRow(row); msg != "" {
			errs = append(errs, ImportError{Line: row.Line, Slug: row.Slug, Message: msg})
			continue
		}
		rows = append(rows, row)
	}
	return rows, errs, nil
}

func splitProjects(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ' ' || r == ',' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func validateImportRow(row CustomerImportRow) string {
	switch {
	case !validSlug(row.Slug):
		return "slug must be 2-63 lowercase letters, digits or dashes"
	case strings.TrimSpace(row.Name) == "":
		return "name is required"
	case !validEmail(row.AdminEmail):
		return "admin_email is invalid"
	case row.BillingMode != "" && !validBillingMode(row.BillingMode):
		return "billing_mode must be real, chargeback or showback"
	case row.StartDate != "" && !store.ValidDate(row.StartDate):
		return "start_date must be YYYY-MM-DD"
	case len(row.ProjectIDs) > 0 && row.Region == "":
		return "region is required when project_ids are given"
	}
	return ""
}

// importCustomers accepts multipart CSV (field "file"), raw text/csv, or a
// JSON array; creates pending customers, updates existing slugs, and records
// pending huawei-project sources for the listed projects.
func (h *Handler) importCustomers(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	rows, errs, err := h.readImport(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	created, updated := 0, 0
	for _, row := range rows {
		ci := store.CustomerInput{Slug: row.Slug, Name: row.Name, AdminEmail: row.AdminEmail, BillingMode: row.BillingMode, StartDate: row.StartDate}
		if row.PriceBook != "" {
			pb, err := h.Store.GetPriceBookByName(r.Context(), row.PriceBook)
			if err != nil {
				if pb2, err2 := h.Store.GetPriceBook(r.Context(), row.PriceBook); err2 == nil {
					pb = pb2
				} else {
					errs = append(errs, ImportError{Line: row.Line, Slug: row.Slug, Message: "unknown price_book " + row.PriceBook})
					continue
				}
			}
			ci.PriceBookID = pb.ID
		}
		existing, err := h.Store.GetCustomerBySlug(r.Context(), row.Slug)
		var c store.Customer
		switch {
		case err == nil:
			p := store.CustomerPatch{Name: &ci.Name, AdminEmail: &ci.AdminEmail}
			if ci.PriceBookID != "" {
				p.PriceBookID = &ci.PriceBookID
			}
			if ci.BillingMode != "" {
				p.BillingMode = &ci.BillingMode
			}
			if ci.StartDate != "" {
				p.StartDate = &ci.StartDate
			}
			if c, err = h.Store.UpdateCustomer(r.Context(), existing.ID, p); err != nil {
				errs = append(errs, ImportError{Line: row.Line, Slug: row.Slug, Message: err.Error()})
				continue
			}
			updated++
		case errors.Is(err, store.ErrNotFound):
			if c, err = h.Store.CreateCustomer(r.Context(), ci); err != nil {
				errs = append(errs, ImportError{Line: row.Line, Slug: row.Slug, Message: err.Error()})
				continue
			}
			created++
		default:
			storeErr(w, err)
			return
		}
		for _, pid := range row.ProjectIDs {
			if _, _, err := h.Store.UpsertSource(r.Context(), c.ID, "huawei-project", row.Region, pid); err != nil {
				errs = append(errs, ImportError{Line: row.Line, Slug: row.Slug, Message: "project " + pid + ": " + err.Error()})
			}
		}
		h.audit(r, &c.ID, "customer.import", map[string]any{"slug": c.Slug, "projects": len(row.ProjectIDs)})
	}
	if errs == nil {
		errs = []ImportError{}
	}
	slog.Info("customer import", "created", created, "updated", updated, "errors", len(errs))
	writeJSON(w, http.StatusOK, map[string]any{"created": created, "updated": updated, "errors": errs})
}

func (h *Handler) readImport(r *http.Request) ([]CustomerImportRow, []ImportError, error) {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch {
	case ct == "multipart/form-data":
		if err := r.ParseMultipartForm(maxBodyBytes); err != nil {
			return nil, nil, fmt.Errorf("multipart: %w", err)
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			return nil, nil, errors.New("multipart field \"file\" is required")
		}
		defer f.Close()
		return ParseCustomerImportCSV(f)
	case ct == "application/json":
		return ParseCustomerImportJSON(r.Body)
	default:
		return ParseCustomerImportCSV(io.LimitReader(r.Body, maxBodyBytes))
	}
}
