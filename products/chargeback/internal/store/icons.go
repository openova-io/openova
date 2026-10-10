package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// ICONS AND BRANDING (DESIGN.md §22.10, founder direction 2026-10-10). The
// SME storefront's package table shows a logo or an icon beside every
// feature, floor item, group and package — and none of them is hardcoded in
// the storefront. BSS holds the bytes, content-addressed (the id is the
// lower-case hex SHA-256 of the bytes, so an upload is idempotent and a URL
// never changes meaning), and the published packages document names them by
// path: {"src": "/api/v1/public/icons/<sha256>", "alt": …, "bg": …}.
//
// An icon is an image a public page will render, so the bytes are checked
// before they are kept, not after: an SVG must parse as XML with an <svg>
// root and carries no script, no foreignObject, no on* handler, no link out
// of the document and no DTD; a PNG or WebP must start with its own magic
// bytes. The public GET adds a sandboxing Content-Security-Policy and
// nosniff on top, so a file that slipped past would still run nothing.

// iconsMigrationSQL is appended at the END of migrations and located by
// content as MigrationIcons. Every constraint is NAMED (dberr.go).
const iconsMigrationSQL = `
CREATE TABLE IF NOT EXISTS icons (
	id TEXT PRIMARY KEY,
	content_type TEXT NOT NULL,
	bytes BYTEA NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT icons_id_check CHECK (id ~ '^[0-9a-f]{64}$'),
	CONSTRAINT icons_content_type_check CHECK (content_type IN ('image/svg+xml','image/png','image/webp')),
	CONSTRAINT icons_size_check CHECK (octet_length(bytes) BETWEEN 1 AND 65536)
);

ALTER TABLE features ADD COLUMN IF NOT EXISTS icon_id TEXT;
ALTER TABLE features ADD COLUMN IF NOT EXISTS icon_bg TEXT;
ALTER TABLE features DROP CONSTRAINT IF EXISTS features_icon_id_fkey;
ALTER TABLE features ADD CONSTRAINT features_icon_id_fkey FOREIGN KEY (icon_id) REFERENCES icons(id) ON DELETE RESTRICT;
ALTER TABLE features DROP CONSTRAINT IF EXISTS features_icon_bg_check;
ALTER TABLE features ADD CONSTRAINT features_icon_bg_check CHECK (icon_bg IS NULL OR icon_bg ~ '^#[0-9A-F]{6}$');
CREATE INDEX IF NOT EXISTS features_icon_idx ON features (icon_id);

CREATE TABLE IF NOT EXISTS feature_group_settings (
	key TEXT PRIMARY KEY,
	icon_id TEXT,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT feature_group_settings_key_check CHECK (key IN ('capacity','features','access','ops','scope','resilience','service')),
	CONSTRAINT feature_group_settings_icon_id_fkey FOREIGN KEY (icon_id) REFERENCES icons(id) ON DELETE RESTRICT
);

ALTER TABLE package_settings ADD COLUMN IF NOT EXISTS icon_id TEXT;
ALTER TABLE package_settings ADD COLUMN IF NOT EXISTS accent TEXT;
ALTER TABLE package_settings ADD COLUMN IF NOT EXISTS badge TEXT NOT NULL DEFAULT '';
ALTER TABLE package_settings DROP CONSTRAINT IF EXISTS package_settings_icon_id_fkey;
ALTER TABLE package_settings ADD CONSTRAINT package_settings_icon_id_fkey FOREIGN KEY (icon_id) REFERENCES icons(id) ON DELETE RESTRICT;
ALTER TABLE package_settings DROP CONSTRAINT IF EXISTS package_settings_accent_check;
ALTER TABLE package_settings ADD CONSTRAINT package_settings_accent_check CHECK (accent IS NULL OR accent ~ '^#[0-9A-F]{6}$');
ALTER TABLE package_settings DROP CONSTRAINT IF EXISTS package_settings_badge_check;
ALTER TABLE package_settings ADD CONSTRAINT package_settings_badge_check CHECK (char_length(badge) <= 24);
`

// MigrationIcons is the schema_migrations version of the icons migration,
// located by content like the others.
var MigrationIcons = func() int {
	for i, m := range migrations {
		if m == iconsMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

// IconMaxBytes is the largest icon kept: 64 KiB. A feature icon is a few
// hundred bytes of SVG; anything near the cap is a photograph, not an icon.
const IconMaxBytes = 64 << 10

// The three content types an icon may have.
const (
	IconSVG  = "image/svg+xml"
	IconPNG  = "image/png"
	IconWebP = "image/webp"
)

// IconPublicPrefix is the path every icon is published under; the document's
// `src` is this prefix and the id, relative to the origin that serves the
// document.
const IconPublicPrefix = "/api/v1/public/icons/"

// IconSrc is the published path of an icon.
func IconSrc(id string) string { return IconPublicPrefix + id }

// BadgeMaxRunes is the longest badge a package carries ("Most popular").
const BadgeMaxRunes = 24

var (
	iconIDShape = regexp.MustCompile(`^[0-9a-f]{64}$`)
	colourShape = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
)

// ValidIconID reports whether id is the lower-case hex SHA-256 an icon is
// stored under.
func ValidIconID(id string) bool { return iconIDShape.MatchString(id) }

// IconID is the content address of the bytes.
func IconID(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// NormalizeColour checks a "#RRGGBB" colour and returns it upper-case; ""
// stays "" (no colour). Anything else is ErrInvalid naming the field.
func NormalizeColour(field, c string) (string, error) {
	c = strings.TrimSpace(c)
	if c == "" {
		return "", nil
	}
	if !colourShape.MatchString(c) {
		return "", fmt.Errorf("%w: %s must be a colour written #RRGGBB (six hex digits), not %q", ErrInvalid, field, c)
	}
	return strings.ToUpper(c), nil
}

// ValidateIcon checks bytes against the content type they were declared
// with: an SVG is parsed and refused for anything that could run or reach
// out; a PNG or WebP must carry its own magic bytes. The error is ErrInvalid
// with a sentence an operator can act on.
func ValidateIcon(contentType string, b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("%w: the icon is empty", ErrInvalid)
	}
	if len(b) > IconMaxBytes {
		return fmt.Errorf("%w: an icon is at most 64 KiB; this one is %d bytes", ErrInvalid, len(b))
	}
	switch contentType {
	case IconSVG:
		return validateSVG(b)
	case IconPNG:
		if !bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) {
			return fmt.Errorf("%w: the file was sent as image/png but is not a PNG (its first bytes are not the PNG signature)", ErrInvalid)
		}
		return nil
	case IconWebP:
		if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
			return fmt.Errorf("%w: the file was sent as image/webp but is not a WebP image (no RIFF … WEBP header)", ErrInvalid)
		}
		return nil
	}
	return fmt.Errorf("%w: an icon is image/svg+xml, image/png or image/webp, not %q", ErrInvalid, contentType)
}

// validateSVG walks the document once. Refused: a DTD or entity
// declaration (<!DOCTYPE, <!ENTITY — the billion-laughs and external-entity
// doors), any root other than <svg>, a <script> or <foreignObject> anywhere,
// any on* event attribute, and any href / xlink:href that does not point
// inside the document (#id).
func validateSVG(b []byte) error {
	lower := bytes.ToLower(b)
	if bytes.Contains(lower, []byte("<!entity")) || bytes.Contains(lower, []byte("<!doctype")) {
		return fmt.Errorf("%w: the SVG carries a DOCTYPE or an ENTITY declaration; remove it", ErrInvalid)
	}
	if !utf8.Valid(b) {
		return fmt.Errorf("%w: the SVG is not UTF-8 text", ErrInvalid)
	}
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = true
	depth, roots := 0, 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: the SVG is not well-formed XML: %v", ErrInvalid, err)
		}
		switch t := tok.(type) {
		case xml.Directive:
			return fmt.Errorf("%w: the SVG carries a DOCTYPE or an ENTITY declaration; remove it", ErrInvalid)
		case xml.ProcInst:
			if t.Target != "xml" {
				return fmt.Errorf("%w: the SVG carries a processing instruction <?%s?>; remove it", ErrInvalid, t.Target)
			}
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			if depth == 0 {
				roots++
				if name != "svg" || roots > 1 {
					return fmt.Errorf("%w: an SVG icon has one <svg> root element; this one starts with <%s>", ErrInvalid, t.Name.Local)
				}
			}
			switch name {
			case "script":
				return fmt.Errorf("%w: the SVG carries a <script>; an icon runs nothing", ErrInvalid)
			case "foreignobject":
				return fmt.Errorf("%w: the SVG carries a <foreignObject>; an icon embeds no HTML", ErrInvalid)
			}
			for _, a := range t.Attr {
				an := strings.ToLower(a.Name.Local)
				if strings.HasPrefix(an, "on") {
					return fmt.Errorf("%w: the SVG carries an event handler %s=…; an icon runs nothing", ErrInvalid, a.Name.Local)
				}
				if an == "href" && !strings.HasPrefix(strings.TrimSpace(a.Value), "#") {
					return fmt.Errorf("%w: the SVG links out of itself (href=%q); an icon may only reference its own #ids", ErrInvalid, a.Value)
				}
				if strings.Contains(strings.ToLower(a.Value), "javascript:") {
					return fmt.Errorf("%w: the SVG carries a javascript: URL; an icon runs nothing", ErrInvalid)
				}
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && len(bytes.TrimSpace(t)) > 0 {
				return fmt.Errorf("%w: the SVG has text outside its <svg> element", ErrInvalid)
			}
		}
	}
	if roots != 1 {
		return fmt.Errorf("%w: an SVG icon needs an <svg> root element", ErrInvalid)
	}
	return nil
}

// Icon is one stored image, its bytes included only where asked for.
type Icon struct {
	ID          string    `json:"id"`
	Src         string    `json:"src"`
	ContentType string    `json:"content_type"`
	Size        int       `json:"size"`
	CreatedAt   time.Time `json:"created_at"`
	// References counts what names the icon: features, groups, packages.
	References int    `json:"references"`
	Bytes      []byte `json:"-"`
}

// IconDependants is what still names an icon.
type IconDependants struct {
	Features int `json:"features"`
	Groups   int `json:"groups"`
	Packages int `json:"packages"`
}

// Total is every reference.
func (d IconDependants) Total() int { return d.Features + d.Groups + d.Packages }

// PutIcon validates and keeps an icon. It is content-addressed: the same
// bytes always land on the same id, and created is false when they were
// already there.
func (s *Store) PutIcon(ctx context.Context, contentType string, b []byte) (Icon, bool, error) {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if err := ValidateIcon(contentType, b); err != nil {
		return Icon{}, false, err
	}
	id := IconID(b)
	res, err := s.db.ExecContext(ctx, `INSERT INTO icons (id, content_type, bytes) VALUES ($1, $2, $3) ON CONFLICT (id) DO NOTHING`, id, contentType, b)
	if err != nil {
		return Icon{}, false, mapErr(err)
	}
	n, _ := res.RowsAffected()
	ic, err := s.GetIcon(ctx, id)
	if err != nil {
		return Icon{}, false, err
	}
	return ic, n == 1, nil
}

// GetIcon returns one icon with its bytes; ErrNotFound for an unknown id.
func (s *Store) GetIcon(ctx context.Context, id string) (Icon, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if !ValidIconID(id) {
		return Icon{}, ErrNotFound
	}
	var ic Icon
	if err := s.db.QueryRowContext(ctx, `SELECT id, content_type, bytes, created_at FROM icons WHERE id = $1`, id).Scan(&ic.ID, &ic.ContentType, &ic.Bytes, &ic.CreatedAt); err != nil {
		return Icon{}, mapErr(err)
	}
	ic.Src, ic.Size, ic.CreatedAt = IconSrc(ic.ID), len(ic.Bytes), ic.CreatedAt.UTC()
	return ic, nil
}

const iconReferencesExpr = `(SELECT count(*) FROM features f WHERE f.icon_id = i.id) + (SELECT count(*) FROM feature_group_settings g WHERE g.icon_id = i.id) + (SELECT count(*) FROM package_settings p WHERE p.icon_id = i.id)`

// ListIcons returns every icon without its bytes, oldest first, with how
// many things name it — the console's picker.
func (s *Store) ListIcons(ctx context.Context) ([]Icon, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT i.id, i.content_type, octet_length(i.bytes), i.created_at, `+iconReferencesExpr+` FROM icons i ORDER BY i.created_at, i.id`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []Icon{}
	for rows.Next() {
		var ic Icon
		if err := rows.Scan(&ic.ID, &ic.ContentType, &ic.Size, &ic.CreatedAt, &ic.References); err != nil {
			return nil, err
		}
		ic.Src, ic.CreatedAt = IconSrc(ic.ID), ic.CreatedAt.UTC()
		out = append(out, ic)
	}
	return out, rows.Err()
}

// DeleteIcon removes an icon nothing names. While a feature, a group or a
// package still shows it, the delete is ErrConflict with the counts.
func (s *Store) DeleteIcon(ctx context.Context, id string) (IconDependants, error) {
	var dep IconDependants
	id = strings.ToLower(strings.TrimSpace(id))
	if !ValidIconID(id) {
		return dep, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dep, err
	}
	defer tx.Rollback()
	var found string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM icons WHERE id = $1 FOR UPDATE`, id).Scan(&found); err != nil {
		return dep, mapErr(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM features WHERE icon_id = $1), (SELECT count(*) FROM feature_group_settings WHERE icon_id = $1), (SELECT count(*) FROM package_settings WHERE icon_id = $1)`, id).
		Scan(&dep.Features, &dep.Groups, &dep.Packages); err != nil {
		return dep, mapErr(err)
	}
	if dep.Total() > 0 {
		var parts []string
		if dep.Features > 0 {
			parts = append(parts, fmt.Sprintf("%d feature(s)", dep.Features))
		}
		if dep.Groups > 0 {
			parts = append(parts, fmt.Sprintf("%d group(s)", dep.Groups))
		}
		if dep.Packages > 0 {
			parts = append(parts, fmt.Sprintf("%d package(s)", dep.Packages))
		}
		return dep, fmt.Errorf("%w: the icon is still shown by %s; remove it there first", ErrConflict, strings.Join(parts, ", "))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM icons WHERE id = $1`, id); err != nil {
		return dep, mapDeleteErr(err)
	}
	return dep, tx.Commit()
}

// queryer is what iconRef needs: a *sql.DB or a *sql.Tx.
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// iconRef checks an icon id a feature, a group or a package is about to name:
// "" clears it (nil), a known id is kept, anything else is ErrInvalid — an
// unknown icon is a 400, never a broken image on the storefront.
func iconRef(ctx context.Context, q queryer, id string) (any, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return nil, nil
	}
	if !ValidIconID(id) {
		return nil, fmt.Errorf("%w: icon_id is the 64-character hex id POST /api/v1/icons returned, not %q", ErrInvalid, id)
	}
	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM icons WHERE id = $1)`, id).Scan(&exists); err != nil {
		return nil, mapErr(err)
	}
	if !exists {
		return nil, fmt.Errorf("%w: there is no icon %s; upload it first (POST /api/v1/icons)", ErrInvalid, id)
	}
	return id, nil
}

// FeatureGroupIcons returns the icon id of every group that has one.
func (s *Store) FeatureGroupIcons(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, icon_id FROM feature_group_settings WHERE icon_id IS NOT NULL ORDER BY key`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, id string
		if err := rows.Scan(&k, &id); err != nil {
			return nil, err
		}
		out[k] = id
	}
	return out, rows.Err()
}

// PutFeatureGroupIcon sets (or, with "", clears) the icon of one group. The
// group names stay the product's; only the icon is the operator's.
func (s *Store) PutFeatureGroupIcon(ctx context.Context, key, iconID string) (string, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	known := false
	for _, g := range FeatureGroups {
		if g.Key == key {
			known = true
		}
	}
	if !known {
		return "", fmt.Errorf("%w: no group %q; a group is capacity, features, access, ops, scope, resilience or service", ErrNotFound, key)
	}
	ref, err := iconRef(ctx, s.db, iconID)
	if err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO feature_group_settings (key, icon_id) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET icon_id = EXCLUDED.icon_id, updated_at = now()`, key, ref); err != nil {
		return "", mapErr(err)
	}
	if ref == nil {
		return "", nil
	}
	return ref.(string), nil
}
