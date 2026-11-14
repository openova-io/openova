package store

// The PostgreSQL implementation of the port.
//
// OpenPostgres connects and applies the migrations of the folder migrations, in the order of
// their numbers, each at most once. The table schema_migrations records the migrations that were
// applied. An advisory lock held for the transaction makes two programs that start at the same
// time apply a migration once. Every change of a consent is one transaction that locks the
// consent's row, applies the event through the consent package's transition table, and writes the
// state with exactly one row of consent_history. The clock is the one given to OpenPostgres, never
// the database's, so the port tests can move time; times are stored to the microsecond.

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/openova-io/openova/products/fingate/internal/consent"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrationLock is the key of the advisory lock taken while the migrations are applied.
const migrationLock = 0x66696e67617465 // "fingate"

var migrationName = regexp.MustCompile(`^([0-9]{4})_[a-z0-9_]+\.sql$`)

// Postgres is a Store in a PostgreSQL database. It is safe for concurrent use.
type Postgres struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// OpenPostgres connects to the database at url (a PostgreSQL connection address), applies the
// migrations and returns the store. now may be nil: time.Now.
func OpenPostgres(ctx context.Context, url string, now func() time.Time) (*Postgres, error) {
	if now == nil {
		now = time.Now
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	p := &Postgres{pool: pool, now: now}
	if err := p.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return p, nil
}

// Close closes every connection of the store.
func (p *Postgres) Close() { p.pool.Close() }

// Ping reports whether the database answers.
func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

// Migrations returns the numbers of the migrations this package holds, in order.
func Migrations() ([]int, error) {
	files, err := migrationFiles()
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, len(files))
	for _, f := range files {
		out = append(out, f.version)
	}
	return out, nil
}

type migrationFile struct {
	version int
	name    string
}

func migrationFiles() ([]migrationFile, error) {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	var out []migrationFile
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("store: migration file %q is not named <four digits>_<name>.sql", e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		out = append(out, migrationFile{version: v, name: e.Name()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, f := range out {
		if f.version != i+1 {
			return nil, fmt.Errorf("store: migration %s is not number %04d: the numbers start at 0001 and have no gap", f.name, i+1)
		}
	}
	return out, nil
}

// migrate applies every migration that schema_migrations does not record, in one transaction.
func (p *Postgres) migrate(ctx context.Context) error {
	files, err := migrationFiles()
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(migrationLock)); err != nil {
			return fmt.Errorf("store: lock the migrations: %w", err)
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version integer PRIMARY KEY, name text NOT NULL, applied_at timestamptz NOT NULL)`); err != nil {
			return fmt.Errorf("store: create schema_migrations: %w", err)
		}
		rows, err := tx.Query(ctx, `SELECT version FROM schema_migrations`)
		if err != nil {
			return fmt.Errorf("store: read schema_migrations: %w", err)
		}
		done, err := pgx.CollectRows(rows, pgx.RowTo[int])
		if err != nil {
			return fmt.Errorf("store: read schema_migrations: %w", err)
		}
		for _, f := range files {
			if slices.Contains(done, f.version) {
				continue
			}
			sql, err := migrations.ReadFile("migrations/" + f.name)
			if err != nil {
				return fmt.Errorf("store: %w", err)
			}
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return fmt.Errorf("store: migration %s: %w", f.name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES ($1, $2, $3)`,
				f.version, f.name, p.stamp()); err != nil {
				return fmt.Errorf("store: record migration %s: %w", f.name, err)
			}
		}
		return nil
	})
}

// stamp returns the time now, in UTC, to the microsecond, as the database stores it.
func (p *Postgres) stamp() time.Time { return p.now().UTC().Truncate(time.Microsecond) }

func dbTime(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// Create implements Store.
func (p *Postgres) Create(ctx context.Context, c Consent) (Consent, error) {
	if c.ID == "" || c.ClientID == "" || !slices.Contains(consent.Families(), c.Family) {
		return Consent{}, fmt.Errorf("store: a consent needs an identifier, a client and a family, got %q, %q, %q", c.ID, c.ClientID, c.Family)
	}
	now := p.stamp()
	r := consent.Record{Family: c.Family}
	if err := r.Apply(consent.Create, consent.ThirdParty, "created", now); err != nil {
		return Consent{}, err
	}
	row := Consent{ID: c.ID, Family: c.Family, ClientID: c.ClientID, State: r.State,
		Request: slices.Clone(c.Request), CreatedAt: now, StateAt: now, History: r.History}
	if !c.ExpiresAt.IsZero() {
		row.ExpiresAt = dbTime(c.ExpiresAt)
	}
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO consent (id, family, client_id, state, request, expires_at, created_at, state_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			row.ID, string(row.Family), row.ClientID, string(row.State), jsonText(row.Request), nullTime(row.ExpiresAt), now, now)
		if err != nil {
			return err
		}
		return insertHistory(ctx, tx, row.ID, row.History[0])
	})
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return Consent{}, fmt.Errorf("%w: %s", ErrExists, c.ID)
	}
	if err != nil {
		return Consent{}, fmt.Errorf("store: create %s: %w", c.ID, err)
	}
	return row, nil
}

// Get implements Store.
func (p *Postgres) Get(ctx context.Context, id string) (Consent, bool, error) {
	c, ok, err := read(ctx, p.pool, id, false)
	if err != nil || !ok {
		return Consent{}, false, err
	}
	if c.State != consent.Authorised || c.ExpiresAt.IsZero() || p.now().Before(c.ExpiresAt) {
		return c, true, nil
	}
	// Expired on read: the event is recorded once, under the row lock, by whichever read comes first.
	err = pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		cur, _, err := read(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if cur.State == consent.Authorised {
			cur, err = applyEvent(ctx, tx, cur, consent.Expire, consent.System, "expires_at passed", p.stamp(), nil)
			if err != nil {
				return err
			}
		}
		c = cur
		return nil
	})
	if err != nil {
		return Consent{}, false, err
	}
	return c, true, nil
}

// Authorise implements Store.
func (p *Postgres) Authorise(ctx context.Context, id, customerID string, accounts []string) (Consent, error) {
	if customerID == "" {
		return Consent{}, errors.New("store: authorise needs a customer")
	}
	return p.change(ctx, id, consent.Authorise, consent.Customer, "authorised", func(c *Consent) {
		c.CustomerID, c.Accounts = customerID, slices.Clone(accounts)
	})
}

// Apply implements Store.
func (p *Postgres) Apply(ctx context.Context, id string, event consent.Event, actor consent.Actor, reason string) (Consent, error) {
	if event == consent.Authorise || event == consent.Create {
		return Consent{}, fmt.Errorf("store: use Authorise or Create for the event %s", event)
	}
	return p.change(ctx, id, event, actor, reason, nil)
}

// change applies one event to the consent id in one transaction.
func (p *Postgres) change(ctx context.Context, id string, event consent.Event, actor consent.Actor, reason string, set func(*Consent)) (Consent, error) {
	var out Consent
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		c, ok, err := read(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		out, err = applyEvent(ctx, tx, c, event, actor, reason, p.stamp(), set)
		return err
	})
	if err != nil {
		return Consent{}, err
	}
	return out, nil
}

// querier is a pool or a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// read returns the consent id with its history; lock takes the row lock of the transaction q.
func read(ctx context.Context, q querier, id string, lock bool) (Consent, bool, error) {
	sql := `SELECT id, family, client_id, state, request::text, customer_id, accounts, expires_at, created_at, state_at
		FROM consent WHERE id = $1`
	if lock {
		sql += ` FOR UPDATE`
	}
	var (
		c                   Consent
		family, state       string
		request, customerID *string
		expires             *time.Time
	)
	err := q.QueryRow(ctx, sql, id).Scan(&c.ID, &family, &c.ClientID, &state, &request, &customerID, &c.Accounts, &expires, &c.CreatedAt, &c.StateAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Consent{}, false, nil
	}
	if err != nil {
		return Consent{}, false, fmt.Errorf("store: read %s: %w", id, err)
	}
	c.Family, c.State = consent.Family(family), consent.State(state)
	if request != nil {
		c.Request = []byte(*request)
	}
	if customerID != nil {
		c.CustomerID = *customerID
	}
	if expires != nil {
		c.ExpiresAt = *expires
	}
	rows, err := q.Query(ctx, `SELECT seq, from_state, to_state, actor, reason, at FROM consent_history
		WHERE consent_id = $1 ORDER BY seq`, id)
	if err != nil {
		return Consent{}, false, fmt.Errorf("store: read the history of %s: %w", id, err)
	}
	c.History, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (consent.HistoryEntry, error) {
		var h consent.HistoryEntry
		var from, to, actor string
		err := r.Scan(&h.Seq, &from, &to, &actor, &h.Reason, &h.At)
		h.From, h.To, h.Actor = consent.State(from), consent.State(to), consent.Actor(actor)
		return h, err
	})
	if err != nil {
		return Consent{}, false, fmt.Errorf("store: read the history of %s: %w", id, err)
	}
	return c, true, nil
}

// applyEvent applies one event to c through the transition table and writes the state, the
// fields set changes, and the one new history row. A refused transition writes nothing.
func applyEvent(ctx context.Context, tx pgx.Tx, c Consent, event consent.Event, actor consent.Actor, reason string, at time.Time, set func(*Consent)) (Consent, error) {
	r := consent.Record{Family: c.Family, State: c.State, History: slices.Clone(c.History)}
	if err := r.Apply(event, actor, reason, at); err != nil {
		return Consent{}, err
	}
	if set != nil {
		set(&c)
	}
	c.State, c.StateAt, c.History = r.State, at, r.History
	if _, err := tx.Exec(ctx, `UPDATE consent SET state = $2, state_at = $3, customer_id = $4, accounts = $5 WHERE id = $1`,
		c.ID, string(c.State), at, nullText(c.CustomerID), c.Accounts); err != nil {
		return Consent{}, fmt.Errorf("store: write %s: %w", c.ID, err)
	}
	if err := insertHistory(ctx, tx, c.ID, c.History[len(c.History)-1]); err != nil {
		return Consent{}, err
	}
	return c, nil
}

func insertHistory(ctx context.Context, tx pgx.Tx, id string, h consent.HistoryEntry) error {
	_, err := tx.Exec(ctx, `INSERT INTO consent_history (consent_id, seq, from_state, to_state, actor, reason, at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, h.Seq, string(h.From), string(h.To), string(h.Actor), h.Reason, h.At)
	if err != nil {
		return fmt.Errorf("store: write the history of %s: %w", id, err)
	}
	return nil
}

func jsonText(b []byte) *string {
	if b == nil {
		return nil
	}
	s := string(b)
	return &s
}

func nullText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
