package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The e-invoice ARCHIVE (DESIGN.md §17, EPIC #6867).
//
// A tax authority that requires an electronic invoice requires the ISSUER to
// keep it: the document as it was signed, its hash, and the outcome of
// whatever submission the authority's model calls for. This table is that
// archive, one row per statement.
//
// What it deliberately does NOT hold is the signing KEY. The key comes from a
// mounted Secret, is used, and is never written here, never logged and never
// returned by any endpoint. What is archived is the document and the hash of
// the document — enough to prove the archive copy is the copy that was
// signed, and useless to anyone wanting to sign something else.

// einvoiceMigrationSQL is one transaction, idempotent against a database that
// already carries the shape. Appended at the very END of the migrations
// slice: migrations are positional. Located by content as MigrationEInvoice.
const einvoiceMigrationSQL = `
CREATE TABLE IF NOT EXISTS einvoice_documents (
	statement_id UUID PRIMARY KEY REFERENCES statements(id) ON DELETE CASCADE,
	profile TEXT NOT NULL,
	invoice_number TEXT NOT NULL DEFAULT '',
	-- The structured document (what GET /statements/{id}/einvoice returns)
	-- and the signed XML (what .../einvoice.xml returns). The XML is the
	-- ARCHIVAL COPY: it is what was signed, byte for byte.
	document JSONB NOT NULL,
	xml TEXT NOT NULL DEFAULT '',
	-- hash is the SHA-256 of the canonical document, hex, lower case. The
	-- archive holds the document and its hash, never the key.
	hash TEXT NOT NULL DEFAULT '',
	signature TEXT NOT NULL DEFAULT '',
	signature_algorithm TEXT NOT NULL DEFAULT '',
	key_id TEXT NOT NULL DEFAULT '',
	qr_payload TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL DEFAULT 'built' CHECK (state IN ('built','signed','archived','submitted','not_submitted')),
	submit_reason TEXT NOT NULL DEFAULT '',
	submit_reference TEXT NOT NULL DEFAULT '',
	built_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	submitted_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS einvoice_documents_state_idx ON einvoice_documents (state, built_at);
`

// MigrationEInvoice is the schema_migrations version of the e-invoice
// archive migration, located by content.
var MigrationEInvoice = func() int {
	for i, m := range migrations {
		if m == einvoiceMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

// The states an e-invoice passes through. They are cumulative: archived
// implies signed, which implies built.
const (
	EInvoiceBuilt = "built"
	// EInvoiceSigned — a signature over the canonical document exists.
	EInvoiceSigned = "signed"
	// EInvoiceArchived — the signed document and its hash are stored. This
	// is the terminal state wherever the authority has published no
	// clearance endpoint.
	EInvoiceArchived = "archived"
	// EInvoiceSubmitted — the authority (or the accredited service provider
	// standing in for it) accepted the document.
	EInvoiceSubmitted = "submitted"
	// EInvoiceNotSubmitted — submission was not attempted, or it failed,
	// and SubmitReason says exactly which. Never silently empty.
	EInvoiceNotSubmitted = "not_submitted"
)

// EInvoiceState is what a statement carries about its e-invoice. The XML
// itself is NOT on it: a statement document is read on every list screen and
// the archival copy is fetched deliberately, from .../einvoice.xml.
type EInvoiceState struct {
	Profile       string `json:"profile"`
	InvoiceNumber string `json:"invoice_number,omitempty"`
	State         string `json:"state"`
	// Hash is the SHA-256 of the canonical document, hex.
	Hash               string     `json:"hash,omitempty"`
	Signature          string     `json:"signature,omitempty"`
	SignatureAlgorithm string     `json:"signature_algorithm,omitempty"`
	KeyID              string     `json:"key_id,omitempty"`
	QRPayload          string     `json:"qr_payload,omitempty"`
	SubmitReason       string     `json:"submit_reason,omitempty"`
	SubmitReference    string     `json:"submit_reference,omitempty"`
	BuiltAt            time.Time  `json:"built_at"`
	SubmittedAt        *time.Time `json:"submitted_at,omitempty"`
}

// EInvoiceRecord is the full archive row, document and XML included.
type EInvoiceRecord struct {
	EInvoiceState
	StatementID string          `json:"statement_id"`
	Document    json.RawMessage `json:"document"`
	XML         string          `json:"xml,omitempty"`
}

const einvoiceColumns = `statement_id, profile, invoice_number, document, xml, hash, signature, signature_algorithm, key_id, qr_payload,
	state, submit_reason, submit_reference, built_at, submitted_at`

func scanEInvoice(row interface{ Scan(...any) error }) (EInvoiceRecord, error) {
	var r EInvoiceRecord
	var doc []byte
	var submitted sql.NullTime
	if err := row.Scan(&r.StatementID, &r.Profile, &r.InvoiceNumber, &doc, &r.XML, &r.Hash, &r.Signature, &r.SignatureAlgorithm, &r.KeyID, &r.QRPayload,
		&r.State, &r.SubmitReason, &r.SubmitReference, &r.BuiltAt, &submitted); err != nil {
		return r, mapErr(err)
	}
	r.Document = json.RawMessage(doc)
	r.BuiltAt = r.BuiltAt.UTC()
	r.SubmittedAt = timePtr(submitted)
	return r, nil
}

// PutEInvoice writes (or replaces) the archive row for a statement. Replacing
// is deliberate and bounded: a re-issue of the SAME statement rebuilds the
// same document from the same frozen figures, so the archive holds the
// current signed copy and not a pile of identical ones.
func (s *Store) PutEInvoice(ctx context.Context, in EInvoiceRecord) (EInvoiceRecord, error) {
	if strings.TrimSpace(in.StatementID) == "" {
		return EInvoiceRecord{}, fmt.Errorf("%w: statement_id is required", ErrInvalid)
	}
	if strings.TrimSpace(in.Profile) == "" {
		return EInvoiceRecord{}, fmt.Errorf("%w: profile is required", ErrInvalid)
	}
	if in.State == "" {
		in.State = EInvoiceBuilt
	}
	if len(in.Document) == 0 {
		in.Document = json.RawMessage("{}")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO einvoice_documents (statement_id, profile, invoice_number, document, xml, hash, signature, signature_algorithm, key_id, qr_payload, state, submit_reason, submit_reference, submitted_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (statement_id) DO UPDATE SET profile = EXCLUDED.profile, invoice_number = EXCLUDED.invoice_number, document = EXCLUDED.document,
			xml = EXCLUDED.xml, hash = EXCLUDED.hash, signature = EXCLUDED.signature, signature_algorithm = EXCLUDED.signature_algorithm,
			key_id = EXCLUDED.key_id, qr_payload = EXCLUDED.qr_payload, state = EXCLUDED.state,
			submit_reason = EXCLUDED.submit_reason, submit_reference = EXCLUDED.submit_reference, submitted_at = EXCLUDED.submitted_at`,
		in.StatementID, in.Profile, in.InvoiceNumber, string(in.Document), in.XML, in.Hash, in.Signature, in.SignatureAlgorithm, in.KeyID, in.QRPayload,
		in.State, in.SubmitReason, in.SubmitReference, in.SubmittedAt)
	if err != nil {
		return EInvoiceRecord{}, mapErr(err)
	}
	return s.GetEInvoice(ctx, in.StatementID)
}

// GetEInvoice reads the archive row. ErrNotFound when the statement has no
// e-invoice — which is the ordinary state of every statement on a Sovereign
// with no profile configured.
func (s *Store) GetEInvoice(ctx context.Context, statementID string) (EInvoiceRecord, error) {
	return scanEInvoice(s.db.QueryRowContext(ctx, `SELECT `+einvoiceColumns+` FROM einvoice_documents WHERE statement_id = $1`, statementID))
}

// eInvoiceState reads the state a statement document carries. A missing row
// is nil, never an error: no profile configured is not a failure.
func (s *Store) eInvoiceState(ctx context.Context, statementID string) (*EInvoiceState, error) {
	var st EInvoiceState
	var submitted sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT profile, invoice_number, hash, signature, signature_algorithm, key_id, qr_payload, state, submit_reason, submit_reference, built_at, submitted_at
		FROM einvoice_documents WHERE statement_id = $1`, statementID).
		Scan(&st.Profile, &st.InvoiceNumber, &st.Hash, &st.Signature, &st.SignatureAlgorithm, &st.KeyID, &st.QRPayload, &st.State, &st.SubmitReason, &st.SubmitReference, &st.BuiltAt, &submitted)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, mapErr(err)
	}
	st.BuiltAt = st.BuiltAt.UTC()
	st.SubmittedAt = timePtr(submitted)
	return &st, nil
}
