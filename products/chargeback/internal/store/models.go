package store

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

// Decimal is an exact numeric value carried as its Postgres text form and
// emitted as a JSON number. Money and quantities never pass through float64
// between the database and the API.
type Decimal string

var decimalShape = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

// MarshalJSON emits the value as a bare JSON number ("0" when empty).
func (d Decimal) MarshalJSON() ([]byte, error) {
	s := strings.TrimSpace(string(d))
	if s == "" {
		return []byte("0"), nil
	}
	if !decimalShape.MatchString(s) {
		return nil, errors.New("store: decimal is not numeric: " + s)
	}
	return []byte(s), nil
}

// UnmarshalJSON accepts a JSON number or a numeric string.
func (d *Decimal) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*d = ""
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		s = strings.TrimSpace(str)
	}
	if s == "" {
		*d = ""
		return nil
	}
	if !decimalShape.MatchString(s) {
		return errors.New("store: decimal is not numeric: " + s)
	}
	*d = Decimal(s)
	return nil
}

// Customer is a buyer: an external account or a synced Organization.
type Customer struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	AdminEmail  string    `json:"admin_email"`
	Kind        string    `json:"kind"`
	OrgSlug     *string   `json:"org_slug,omitempty"`
	PriceBookID *string   `json:"price_book_id,omitempty"`
	BillingMode string    `json:"billing_mode"`
	Status      string    `json:"status"`
	StartDate   *string   `json:"start_date,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// List-view aggregates.
	SourceCount         int        `json:"source_count"`
	VerifiedSourceCount int        `json:"verified_source_count"`
	LastCollectedAt     *time.Time `json:"last_collected_at,omitempty"`
	LastStatementPeriod *string    `json:"last_statement_period,omitempty"`

	// Collecting reports whether the collector picks this customer up at
	// all: the customer is active AND at least one source is verified. It
	// exists so the UI can say why nothing flows for a pending customer.
	Collecting bool `json:"collecting"`
}

// CustomerUser grants an email a role on a customer.
type CustomerUser struct {
	CustomerID string `json:"customer_id"`
	Email      string `json:"email"`
	Role       string `json:"role"`
}

// CostSource is one metered origin of usage (today: a Huawei project).
type CostSource struct {
	ID              string     `json:"id"`
	CustomerID      string     `json:"customer_id"`
	Kind            string     `json:"kind"`
	Region          string     `json:"region"`
	ProjectID       string     `json:"project_id"`
	DomainID        *string    `json:"domain_id,omitempty"`
	CredentialID    *string    `json:"credential_id,omitempty"`
	Status          string     `json:"status"`
	VerifiedAt      *time.Time `json:"verified_at,omitempty"`
	LastCollectedAt *time.Time `json:"last_collected_at,omitempty"`
	LastError       *string    `json:"last_error,omitempty"`
	// AccessKey is the (non-secret) key id of the linked credential, for display.
	AccessKey string `json:"access_key,omitempty"`
}

// Credential is the API view of a stored AK/SK: the secret never leaves the
// store in this shape (it has no field for it).
type Credential struct {
	ID         string     `json:"id"`
	CustomerID string     `json:"customer_id"`
	Kind       string     `json:"kind"`
	AccessKey  string     `json:"access_key"`
	CreatedAt  time.Time  `json:"created_at"`
	RotatedAt  *time.Time `json:"rotated_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// InventoryItem is one observed cloud resource.
type InventoryItem struct {
	SourceID   string          `json:"source_id"`
	ResourceID string          `json:"resource_id"`
	Kind       string          `json:"kind"`
	Name       string          `json:"name"`
	Attrs      json.RawMessage `json:"attrs"`
	FirstSeen  time.Time       `json:"first_seen"`
	LastSeen   time.Time       `json:"last_seen"`
	DeletedAt  *time.Time      `json:"deleted_at,omitempty"`
}

// UsageRecord is one fact in the ledger.
type UsageRecord struct {
	ID           int64           `json:"id,omitempty"`
	CustomerID   string          `json:"customer_id"`
	SourceID     string          `json:"source_id"`
	ResourceID   string          `json:"resource_id"`
	ResourceKind string          `json:"resource_kind"`
	SKU          string          `json:"sku"`
	Quantity     Decimal         `json:"quantity"`
	Unit         string          `json:"unit"`
	WindowStart  time.Time       `json:"window_start"`
	WindowEnd    time.Time       `json:"window_end"`
	Region       string          `json:"region"`
	Labels       json.RawMessage `json:"labels,omitempty"`
	RawRef       string          `json:"raw_ref,omitempty"`
	CollectedAt  time.Time       `json:"collected_at,omitempty"`
}

// UsageRow is one aggregated usage line.
type UsageRow struct {
	Key           string  `json:"key"`
	SKU           string  `json:"sku"`
	Unit          string  `json:"unit"`
	Quantity      Decimal `json:"quantity"`
	ResourceCount int     `json:"resource_count"`
	ResourceKind  string  `json:"resource_kind,omitempty"`
	ResourceName  string  `json:"resource_name,omitempty"`
}

// PriceBook is a rate card.
type PriceBook struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Currency      string      `json:"currency"`
	AnnualDivisor int         `json:"annual_divisor"`
	BillStopped   string      `json:"bill_stopped"`
	EffectiveFrom *string     `json:"effective_from,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	Items         []PriceItem `json:"items,omitempty"`
}

// PriceItem prices one SKU. UnitPrice is derived from AnnualPrice and the
// book's divisor when the item came from an annual list price.
type PriceItem struct {
	PriceBookID string   `json:"price_book_id,omitempty"`
	SKU         string   `json:"sku"`
	Unit        string   `json:"unit"`
	UnitPrice   Decimal  `json:"unit_price"`
	AnnualPrice *Decimal `json:"annual_price,omitempty"`
	Description string   `json:"description,omitempty"`
}

// Statement is one customer's rated period.
type Statement struct {
	ID           string      `json:"id"`
	CustomerID   string      `json:"customer_id"`
	PeriodStart  string      `json:"period_start"`
	PeriodEnd    string      `json:"period_end"`
	Currency     string      `json:"currency"`
	Subtotal     Decimal     `json:"subtotal"`
	TaxRate      Decimal     `json:"tax_rate"`
	Tax          Decimal     `json:"tax"`
	Total        Decimal     `json:"total"`
	Status       string      `json:"status"`
	IssuedAt     *time.Time  `json:"issued_at,omitempty"`
	CreatedAt    time.Time   `json:"created_at"`
	Lines        []RatedLine `json:"lines,omitempty"`
	CustomerName string      `json:"customer_name,omitempty"`
}

// RatedLine is one priced aggregate on a statement.
type RatedLine struct {
	ID            int64   `json:"id"`
	StatementID   string  `json:"statement_id"`
	CustomerID    string  `json:"customer_id"`
	SourceID      *string `json:"source_id,omitempty"`
	SKU           string  `json:"sku"`
	Quantity      Decimal `json:"quantity"`
	Unit          string  `json:"unit"`
	UnitPrice     Decimal `json:"unit_price"`
	Amount        Decimal `json:"amount"`
	ResourceCount int     `json:"resource_count"`
}

// Invite is a one-time activation link.
type Invite struct {
	Token      string     `json:"token"`
	CustomerID string     `json:"customer_id"`
	Email      string     `json:"email"`
	ExpiresAt  time.Time  `json:"expires_at"`
	UsedAt     *time.Time `json:"used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// AuditEntry records one mutation.
type AuditEntry struct {
	ID         int64           `json:"id"`
	CustomerID *string         `json:"customer_id,omitempty"`
	Actor      string          `json:"actor"`
	Action     string          `json:"action"`
	Details    json.RawMessage `json:"details,omitempty"`
	At         time.Time       `json:"at"`
}

// Session is a signed-in principal.
type Session struct {
	Token      string    `json:"-"`
	Email      string    `json:"email"`
	Role       string    `json:"role"`
	CustomerID *string   `json:"customer_id,omitempty"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// Roles.
const (
	RoleOperator       = "operator"
	RoleCustomerAdmin  = "customer-admin"
	RoleCustomerViewer = "customer-viewer"
)

// Scope is the authorization boundary every query is filtered by: the operator
// sees everything, a customer principal sees only its own customer.
type Scope struct {
	Operator   bool
	CustomerID string
}

// OperatorScope sees all rows.
var OperatorScope = Scope{Operator: true}

// CustomerScope sees one customer.
func CustomerScope(id string) Scope { return Scope{CustomerID: id} }

// Allows reports whether the scope may see a row of the given customer.
func (s Scope) Allows(customerID string) bool {
	return s.Operator || (s.CustomerID != "" && s.CustomerID == customerID)
}

// ErrNotFound is returned for absent rows and for rows outside the caller's scope.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned on unique-constraint or state conflicts.
var ErrConflict = errors.New("conflict")
