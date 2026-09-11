// Package pdf lays a view.Model out as a print-ready A4 document and emits
// it as a PDF.
//
// There is NO browser here and no headless anything: the layout is drawn
// directly with github.com/go-pdf/fpdf, a pure-Go PDF library with real text
// metrics (GetStringWidth), so column widths, wrapped cells and
// page breaks are measured rather than guessed. The output is a genuine text
// layer — selectable, searchable, extractable — not a rasterised page.
package pdf

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"

	"github.com/openova-io/openova/products/chargeback/docrender/internal/view"
)

// Options tune the rendition. The zero value is the shipped default.
type Options struct {
	// Now fixes the document's creation timestamp. Leaving it zero uses the
	// wall clock; a test pins it so two renders of one document are
	// byte-identical.
	Now time.Time
	// FontFile is the path of a TrueType font to embed instead of the core
	// Helvetica. It is the seam a non-Latin locale needs (the core fonts are
	// cp1252-only); empty keeps the built-in font and adds nothing to the
	// image.
	FontFile string
	// Compress keeps the content streams Flate-compressed. Default true.
	Compress *bool
}

// A4 geometry, in millimetres.
const (
	pageW   = 210.0
	pageH   = 297.0
	marginL = 16.0
	marginR = 16.0
	marginT = 14.0
	marginB = 18.0

	contentW = pageW - marginL - marginR
	rightX   = pageW - marginR

	fontBody = 9.0
	fontSmal = 7.6
	fontLead = 19.0
)

type rgb struct{ r, g, b int }

var (
	ink     = rgb{17, 24, 39}
	muted   = rgb{107, 114, 128}
	ruleCol = rgb{209, 213, 219}
	band    = rgb{244, 245, 247}
	good    = rgb{4, 120, 87}
	bad     = rgb{185, 28, 28}
)

type doc struct {
	f     *fpdf.Fpdf
	m     *view.Model
	font  string
	title string
}

// Render lays the model out and returns the PDF bytes.
func Render(m *view.Model, opt Options) ([]byte, error) {
	f := fpdf.New("P", "mm", "A4", "")
	d := &doc{f: f, m: m, font: "Helvetica"}
	d.title = m.T(m.TitleKey) + " " + m.Number

	if opt.FontFile != "" {
		raw, err := os.ReadFile(opt.FontFile)
		if err != nil {
			return nil, fmt.Errorf("font file: %w", err)
		}
		f.AddUTF8FontFromBytes("doc", "", raw)
		d.font = "doc"
	}
	if opt.Compress != nil {
		f.SetCompression(*opt.Compress)
	}
	// REPRODUCIBILITY. fpdf emits its font, image and template objects by
	// ranging over Go maps, so without this the same document renders to
	// different bytes run to run — object 5 was Helvetica-Bold on one render
	// and Helvetica-Oblique on the next (measured: 3 failures in 8 runs of
	// the determinism test). SetCatalogSort replaces every one of those map
	// walks with a sorted key list. A document that will not render twice
	// the same cannot be cached, compared, hashed or signed.
	f.SetCatalogSort(true)
	stamp := opt.Now
	if stamp.IsZero() {
		stamp = time.Now()
	}
	stamp = stamp.UTC()
	f.SetCreationDate(stamp)
	f.SetModificationDate(stamp)
	f.SetTitle(d.enc(d.title), false)
	f.SetSubject(d.enc(m.T(m.TitleKey)), false)
	f.SetAuthor(d.enc(m.Seller.Name), false)
	f.SetCreator("OpenOva Catalyst docrender", false)
	f.SetLang(m.Locale)

	f.SetMargins(marginL, marginT, marginR)
	f.SetAutoPageBreak(true, marginB)
	f.AliasNbPages("")
	f.SetHeaderFunc(d.header)
	f.SetFooterFunc(d.footer)

	f.AddPage()
	d.parties()
	d.meta()
	d.lines()
	d.totals()
	d.taxSummary()
	d.taxQR()
	d.payments()
	d.terms()
	d.notes()

	if err := f.Error(); err != nil {
		return nil, fmt.Errorf("compose %s: %w", m.Template, err)
	}
	var buf bytes.Buffer
	if err := f.Output(&buf); err != nil {
		return nil, fmt.Errorf("emit %s: %w", m.Template, err)
	}
	return buf.Bytes(), nil
}

// ── primitives ─────────────────────────────────────────────────────────────

func (d *doc) enc(s string) string {
	if d.font != "Helvetica" {
		return s // an embedded UTF-8 font takes the string as it is
	}
	return encode(s)
}

func (d *doc) set(style string, size float64, c rgb) {
	d.f.SetFont(d.font, style, size)
	d.f.SetTextColor(c.r, c.g, c.b)
}

// cell writes one measured cell. ln follows fpdf: 0 = stay on the line,
// 1 = move to the next line, 2 = move below.
func (d *doc) cell(w, h float64, txt, align string, ln int) {
	d.f.CellFormat(w, h, d.enc(txt), "", ln, align, false, 0, "")
}

func (d *doc) rule(y float64, c rgb) {
	d.f.SetDrawColor(c.r, c.g, c.b)
	d.f.SetLineWidth(0.2)
	d.f.Line(marginL, y, rightX, y)
}

// need starts a new page unless h millimetres are still free on this one.
func (d *doc) need(h float64) {
	if d.f.GetY()+h > pageH-marginB {
		d.f.AddPage()
	}
}

// width measures a string in the CURRENT font, in millimetres — the whole
// reason this library was chosen over hand-computed columns.
func (d *doc) width(s string) float64 { return d.f.GetStringWidth(d.enc(s)) }

// wrap breaks text into lines that each measure at most w millimetres in the
// current font, and returns them ENCODED and ready for CellFormat.
//
// fpdf's own SplitText cannot be used here: it does `[]rune(txt)` and indexes
// the font's 256-entry width table by code point, so a cp1252 byte ≥ 0x80 —
// which is exactly what a core font must be handed — decodes as U+FFFD and
// panics the process (measured: an em dash in a line description took the
// whole render down). The widths below come from the same font metrics via
// GetStringWidth, which IS byte-based for a core font, so this measures
// rather than estimates.
func (d *doc) wrap(s string, w float64) []string {
	var out []string
	for _, para := range strings.Split(d.enc(s), "\n") {
		out = append(out, d.wrapLine(para, w)...)
	}
	return out
}

// wrapLine wraps one already-encoded paragraph. Slicing by byte is safe: the
// encoding is single-byte.
func (d *doc) wrapLine(s string, w float64) []string {
	if s == "" {
		return []string{""}
	}
	if d.f.GetStringWidth(s) <= w {
		return []string{s}
	}
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		cand := word
		if cur != "" {
			cand = cur + " " + word
		}
		if d.f.GetStringWidth(cand) <= w {
			cur = cand
			continue
		}
		if cur != "" {
			lines = append(lines, cur)
			cur = ""
		}
		// A single word wider than the column is broken rather than left to
		// overflow the cell — a long resource id, typically.
		for d.f.GetStringWidth(word) > w && len(word) > 1 {
			n := len(word)
			for n > 1 && d.f.GetStringWidth(word[:n]) > w {
				n--
			}
			lines = append(lines, word[:n])
			word = word[n:]
		}
		cur = word
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}

// fit shortens a string until it measures at most w, appending an ellipsis.
// Used only where truncation is correct (a SKU in a fixed column); wrapping
// cells use wrap instead.
func (d *doc) fit(s string, w float64) string {
	if d.width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 1 {
		r = r[:len(r)-1]
		if d.width(string(r)+"…") <= w {
			return string(r) + "…"
		}
	}
	return string(r)
}

// ── header / footer ────────────────────────────────────────────────────────

func (d *doc) header() {
	f := d.f
	if f.PageNo() > 1 {
		f.SetY(marginT)
		d.set("", fontSmal, muted)
		d.cell(contentW/2, 5, d.title, "L", 0)
		d.cell(contentW/2, 5, d.m.Seller.Name, "R", 1)
		d.rule(f.GetY()+1, ruleCol)
		f.SetY(f.GetY() + 5)
		return
	}

	f.SetY(marginT)
	top := f.GetY()

	// Seller logo, top right, boxed into 38 x 18 mm without distortion.
	logoW := 0.0
	if l := d.m.Logo; l != nil {
		name := "seller-logo"
		info := f.RegisterImageOptionsReader(name, fpdf.ImageOptions{ImageType: l.Format}, bytes.NewReader(l.Bytes))
		if f.Error() == nil && info != nil {
			iw, ih := info.Extent()
			maxW, maxH := 38.0, 18.0
			scale := maxW / iw
			if s := maxH / ih; s < scale {
				scale = s
			}
			w, h := iw*scale, ih*scale
			f.ImageOptions(name, rightX-w, top, w, h, false, fpdf.ImageOptions{ImageType: l.Format}, 0, "")
			logoW = w + 6
		} else {
			// A logo that will not decode must not take the document down:
			// drop it and keep the invoice.
			f.ClearError()
		}
	}

	// Document title and number, top left.
	f.SetXY(marginL, top)
	d.set("B", fontLead, ink)
	d.cell(contentW-logoW, 9, d.m.T(d.m.TitleKey), "L", 1)
	f.SetX(marginL)
	d.set("B", 10.5, ink)
	d.cell(contentW-logoW, 5.5, d.m.Number, "L", 1)

	// Status stamp, when the status is one a reader must not miss.
	if d.m.StatusKey != "" {
		d.stamp(d.m.T(d.m.StatusKey))
	}

	y := f.GetY() + 2
	if d.m.Logo != nil && y < top+20 {
		y = top + 20
	}
	d.rule(y, ink)
	f.SetY(y + 5)
}

func (d *doc) stamp(label string) {
	f := d.f
	colour := muted
	switch strings.ToLower(label) {
	case "paid":
		colour = good
	case "overdue", "cancelled", "canceled":
		colour = bad
	}
	f.SetX(marginL)
	d.set("B", fontSmal, colour)
	w := d.width(label) + 6
	y := f.GetY() + 1
	f.SetDrawColor(colour.r, colour.g, colour.b)
	f.SetLineWidth(0.4)
	f.Rect(marginL, y, w, 6, "D")
	f.SetXY(marginL, y)
	d.cell(w, 6, label, "C", 1)
	f.SetY(y + 6)
}

func (d *doc) footer() {
	f := d.f
	f.SetY(-14)
	d.rule(f.GetY(), ruleCol)
	f.SetY(f.GetY() + 1.5)
	d.set("", fontSmal, muted)
	d.cell(contentW/2, 5, d.m.T("footer.generated"), "L", 0)
	d.cell(contentW/2, 5, d.m.Tf("footer.page", map[string]string{
		"page": fmt.Sprint(f.PageNo()), "pages": "{nb}",
	}), "R", 0)
}

// ── blocks ─────────────────────────────────────────────────────────────────

// parties draws the seller and the buyer side by side.
func (d *doc) parties() {
	f := d.f
	const gutter = 8.0
	colW := (contentW - gutter) / 2
	top := f.GetY()

	left := d.partyBlock(marginL, top, colW, d.m.SellerHeadingKey, d.m.Seller)
	right := d.partyBlock(marginL+colW+gutter, top, colW, d.m.BuyerHeadingKey, d.m.Buyer)
	if right > left {
		left = right
	}
	f.SetY(left + 5)
}

func (d *doc) partyBlock(x, y, w float64, headingKey string, p view.Party) float64 {
	f := d.f
	f.SetXY(x, y)
	d.set("B", fontSmal, muted)
	f.CellFormat(w, 4.5, d.enc(strings.ToUpper(d.m.T(headingKey))), "", 2, "L", false, 0, "")
	d.set("B", 10.5, ink)
	f.CellFormat(w, 5.5, d.enc(d.fit(p.Name, w)), "", 2, "L", false, 0, "")
	d.set("", fontBody, ink)
	for _, line := range p.AddressLines {
		for _, wrapped := range d.wrap(line, w) {
			f.CellFormat(w, 4.4, wrapped, "", 2, "L", false, 0, "")
		}
	}
	d.set("", fontSmal, muted)
	if p.TaxRegistration != "" {
		f.CellFormat(w, 4.2, d.enc(d.m.T("field.tax_registration")+": "+p.TaxRegistration), "", 2, "L", false, 0, "")
	}
	if p.Email != "" {
		f.CellFormat(w, 4.2, d.enc(d.fit(p.Email, w)), "", 2, "L", false, 0, "")
	}
	if p.Phone != "" {
		f.CellFormat(w, 4.2, d.enc(p.Phone), "", 2, "L", false, 0, "")
	}
	return f.GetY()
}

// meta draws the number / dates / references band: up to three label-value
// pairs per row on a light ground.
func (d *doc) meta() {
	if len(d.m.Meta) == 0 {
		return
	}
	f := d.f
	const perRow = 3
	rows := (len(d.m.Meta) + perRow - 1) / perRow
	h := float64(rows) * 11.0
	d.need(h + 4)

	y := f.GetY()
	f.SetFillColor(band.r, band.g, band.b)
	f.Rect(marginL, y, contentW, h, "F")

	colW := contentW / perRow
	for i, row := range d.m.Meta {
		cx := marginL + float64(i%perRow)*colW
		cy := y + float64(i/perRow)*11.0
		f.SetXY(cx+3, cy+1.6)
		d.set("", fontSmal, muted)
		f.CellFormat(colW-6, 4, d.enc(strings.ToUpper(d.m.Label(row))), "", 2, "L", false, 0, "")
		d.set("B", fontBody, ink)
		f.SetX(cx + 3)
		f.CellFormat(colW-6, 4.6, d.enc(d.fit(row.Value, colW-6)), "", 2, "L", false, 0, "")
	}
	f.SetY(y + h + 6)
}

// lineCols is the measured column grid of the charges table.
type lineCols struct {
	sku, desc, unit, qty, price, amount float64
}

func (d *doc) lineCols() lineCols {
	if d.m.HasDescription {
		return lineCols{sku: 28, desc: 56, unit: 18, qty: 24, price: 24, amount: 28}
	}
	return lineCols{sku: 66, unit: 22, qty: 28, price: 28, amount: 34}
}

func (d *doc) linesHeader(c lineCols) {
	f := d.f
	y := f.GetY()
	f.SetFillColor(band.r, band.g, band.b)
	f.Rect(marginL, y, contentW, 7, "F")
	f.SetXY(marginL+2, y)
	d.set("B", fontSmal, muted)
	up := func(k string) string { return strings.ToUpper(d.m.T(k)) }
	d.cell(c.sku-2, 7, up("table.sku"), "L", 0)
	if c.desc > 0 {
		d.cell(c.desc, 7, up("table.description"), "L", 0)
	}
	d.cell(c.unit, 7, up("table.unit"), "L", 0)
	d.cell(c.qty, 7, up("table.quantity"), "R", 0)
	d.cell(c.price, 7, up("table.unit_price"), "R", 0)
	d.cell(c.amount-2, 7, up("table.amount"), "R", 1)
	f.SetY(y + 7)
}

// lines draws the charges table, measuring each row and repeating the header
// after every page break.
func (d *doc) lines() {
	if len(d.m.Lines) == 0 {
		return
	}
	f := d.f
	c := d.lineCols()
	d.need(24)
	d.linesHeader(c)

	for _, l := range d.m.Lines {
		d.set("", fontBody, ink)
		// The description is the only wrapping cell; the row is as tall as it.
		var wrapped []string
		if c.desc > 0 && l.Description != "" {
			wrapped = d.wrap(l.Description, c.desc-2)
		}
		rowH := 6.0
		if n := float64(len(wrapped)); n > 1 {
			rowH = n * 4.6
			if rowH < 6 {
				rowH = 6
			}
		}
		if f.GetY()+rowH > pageH-marginB {
			f.AddPage()
			d.linesHeader(c)
		}

		y := f.GetY()
		f.SetXY(marginL+2, y)
		d.set("", fontBody, ink)
		d.cell(c.sku-2, rowH, d.fit(l.SKU, c.sku-4), "L", 0)
		if c.desc > 0 {
			x := f.GetX()
			if len(wrapped) > 1 {
				for i, w := range wrapped {
					f.SetXY(x, y+float64(i)*4.6+(rowH-float64(len(wrapped))*4.6)/2)
					f.CellFormat(c.desc, 4.6, w, "", 0, "L", false, 0, "")
				}
				f.SetXY(x+c.desc, y)
			} else {
				d.cell(c.desc, rowH, l.Description, "L", 0)
			}
		}
		d.set("", fontSmal, muted)
		d.cell(c.unit, rowH, d.fit(l.Unit, c.unit-2), "L", 0)
		d.set("", fontBody, ink)
		d.cell(c.qty, rowH, l.Quantity, "R", 0)
		d.cell(c.price, rowH, l.UnitPrice, "R", 0)
		d.cell(c.amount-2, rowH, l.Amount, "R", 1)

		f.SetY(y + rowH)
		d.rule(f.GetY(), ruleCol)
	}
	f.SetY(f.GetY() + 5)
}

// totals draws the waterfall on the right and the discount detail on the left
// of the same band, so the two are read together.
func (d *doc) totals() {
	f := d.f
	const totalsW = 92.0
	totalsX := rightX - totalsW
	const labelW = 54.0

	h := float64(len(d.m.Totals))*5.6 + 4
	if n := len(d.m.Discounts); n > 0 {
		if dh := float64(n)*4.6 + 10; dh > h {
			h = dh
		}
	}
	d.need(h + 6)
	top := f.GetY()

	// Discount detail, left.
	if len(d.m.Discounts) > 0 {
		f.SetXY(marginL, top)
		d.set("B", fontSmal, muted)
		f.CellFormat(totalsX-marginL-8, 5, d.enc(strings.ToUpper(d.m.T("discounts.title"))), "", 2, "L", false, 0, "")
		d.set("", fontSmal, ink)
		for _, row := range d.m.Discounts {
			w := totalsX - marginL - 8
			f.CellFormat(w*0.62, 4.6, d.enc(d.fit(d.m.Label(row), w*0.62)), "", 0, "L", false, 0, "")
			f.CellFormat(w*0.38, 4.6, d.enc(row.Value), "", 2, "R", false, 0, "")
			f.SetX(marginL)
		}
	}

	// Waterfall, right.
	f.SetY(top)
	for _, row := range d.m.Totals {
		f.SetX(totalsX)
		style, size, colour := "", fontBody, ink
		if row.Emphasis {
			style, size = "B", 10.0
			d.rule2(totalsX, f.GetY()+0.4, rightX, ink)
			f.SetY(f.GetY() + 1.2)
		} else if row.Negative {
			colour = muted
		}
		d.set(style, size, colour)
		rowH := 5.6
		if row.Emphasis {
			rowH = 6.4
		}
		f.SetX(totalsX)
		d.cell(labelW, rowH, d.fit(d.m.Label(row), labelW), "L", 0)
		d.cell(totalsW-labelW, rowH, row.Value, "R", 1)
	}

	y := f.GetY()
	if len(d.m.Discounts) > 0 && top+h > y {
		y = top + h
	}
	f.SetY(y + 3)

	// Informational lines under the totals (tax exemption, discount rule).
	if len(d.m.Notes) > 0 {
		d.set("I", fontSmal, muted)
		for _, row := range d.m.Notes {
			d.need(5)
			f.SetX(marginL)
			for _, w := range d.wrap(d.m.Label(row), contentW) {
				f.CellFormat(contentW, 4.4, w, "", 2, "L", false, 0, "")
			}
		}
		f.SetY(f.GetY() + 2)
	}
}

// taxSummary draws the per-rate block. An invoice carrying several rates has
// to state what each rate was charged on: the waterfall's single "Tax (5 %)"
// line cannot, and an invoice whose tax the customer cannot reconcile is one
// the customer's own accountant will reject.
func (d *doc) taxSummary() {
	if len(d.m.TaxSummary) == 0 {
		return
	}
	f := d.f
	d.need(14 + float64(len(d.m.TaxSummary))*5)
	f.SetY(f.GetY() + 2)
	f.SetX(marginL)
	d.set("B", fontSmal, muted)
	d.cell(contentW, 5, strings.ToUpper(d.m.T("tax.summary.title")), "L", 1)

	cols := [3]float64{90, 44, 44}
	f.SetX(marginL)
	d.set("B", fontSmal, muted)
	d.cell(cols[0], 4.6, d.m.T("tax.summary.rate"), "L", 0)
	d.cell(cols[1], 4.6, d.m.T("tax.summary.base"), "R", 0)
	d.cell(cols[2], 4.6, d.m.T("tax.summary.amount"), "R", 1)
	d.rule(f.GetY(), ruleCol)

	d.set("", fontBody, ink)
	for _, row := range d.m.TaxSummary {
		d.need(6)
		label := row.Rate
		if row.KindKey != "" {
			label += " (" + d.m.T(row.KindKey) + ")"
		}
		if row.Label != "" {
			label += " — " + row.Label
		}
		f.SetX(marginL)
		d.cell(cols[0], 5, d.fit(label, cols[0]), "L", 0)
		d.cell(cols[1], 5, row.Base, "R", 0)
		d.cell(cols[2], 5, row.Amount, "R", 1)
	}
	f.SetY(f.GetY() + 2)
}

// taxQR draws the tax authority's QR. The modules are filled rectangles
// rather than an embedded image: fpdf would have to be handed a PNG, and
// encoding one would add a dependency to draw squares this already draws
// exactly. A quiet zone of four modules is part of the symbol — a QR printed
// flush against text does not scan.
func (d *doc) taxQR() {
	q := d.m.QR
	if q == nil || q.Size == 0 {
		return
	}
	const sideMM = 28.0
	const quiet = 4 // modules, per the specification
	f := d.f
	d.need(sideMM + 8)
	f.SetY(f.GetY() + 2)
	top := f.GetY()

	module := sideMM / float64(q.Size+2*quiet)
	originX, originY := marginL, top
	// The quiet zone is white paper, which is what the page already is; only
	// the dark modules are drawn.
	f.SetFillColor(ink.r, ink.g, ink.b)
	for y := 0; y < q.Size; y++ {
		for x := 0; x < q.Size; x++ {
			if !q.Dark(x, y) {
				continue
			}
			f.Rect(originX+float64(x+quiet)*module, originY+float64(y+quiet)*module, module, module, "F")
		}
	}
	// The caption, to the right of the symbol.
	capX := originX + sideMM + 6
	f.SetXY(capX, top+4)
	d.set("B", fontSmal, muted)
	f.CellFormat(contentW-(capX-marginL), 4.6, d.enc(strings.ToUpper(d.m.T("tax.qr.title"))), "", 2, "L", false, 0, "")
	f.SetX(capX)
	d.set("", fontSmal, muted)
	f.CellFormat(contentW-(capX-marginL), 4.6, d.enc(d.m.T("tax.qr.caption")), "", 2, "L", false, 0, "")

	f.SetY(top + sideMM + 3)
}

func (d *doc) rule2(x1, y, x2 float64, c rgb) {
	d.f.SetDrawColor(c.r, c.g, c.b)
	d.f.SetLineWidth(0.3)
	d.f.Line(x1, y, x2, y)
}

// payments lists the received payments behind the paid figure.
func (d *doc) payments() {
	if len(d.m.Payments) == 0 {
		return
	}
	f := d.f
	d.need(22)
	f.SetY(f.GetY() + 3)
	f.SetX(marginL)
	d.set("B", fontSmal, muted)
	d.cell(contentW, 5, strings.ToUpper(d.m.T("payments.title")), "L", 1)

	cols := [4]float64{32, 34, 76, 36}
	f.SetX(marginL + 2)
	d.set("B", fontSmal, muted)
	d.cell(cols[0], 5.5, strings.ToUpper(d.m.T("payments.date")), "L", 0)
	d.cell(cols[1], 5.5, strings.ToUpper(d.m.T("payments.method")), "L", 0)
	d.cell(cols[2], 5.5, strings.ToUpper(d.m.T("payments.reference")), "L", 0)
	d.cell(cols[3]-2, 5.5, strings.ToUpper(d.m.T("payments.amount")), "R", 1)
	d.rule(f.GetY(), ruleCol)

	for _, p := range d.m.Payments {
		d.need(6)
		f.SetX(marginL + 2)
		d.set("", fontBody, ink)
		d.cell(cols[0], 5.4, p.Date, "L", 0)
		d.cell(cols[1], 5.4, d.fit(p.Method, cols[1]-2), "L", 0)
		d.cell(cols[2], 5.4, d.fit(p.Reference, cols[2]-2), "L", 0)
		d.cell(cols[3]-2, 5.4, p.Amount, "R", 1)
	}
	f.SetY(f.GetY() + 4)
}

// terms writes the payment terms sentences.
func (d *doc) terms() {
	if len(d.m.Terms) == 0 && d.m.TermsText == "" {
		return
	}
	f := d.f
	d.need(16)
	f.SetX(marginL)
	d.set("B", fontSmal, muted)
	d.cell(contentW, 5, strings.ToUpper(d.m.T("terms.title")), "L", 1)
	d.set("", fontBody, ink)
	for _, row := range d.m.Terms {
		d.need(5)
		f.SetX(marginL)
		for _, w := range d.wrap(d.m.Label(row), contentW) {
			f.CellFormat(contentW, 4.6, w, "", 2, "L", false, 0, "")
		}
	}
	if d.m.TermsText != "" {
		d.paragraph(d.m.TermsText)
	}
	f.SetY(f.GetY() + 3)
}

// notes writes the free-text note block.
func (d *doc) notes() {
	if d.m.NotesText == "" {
		return
	}
	f := d.f
	d.need(14)
	f.SetX(marginL)
	d.set("B", fontSmal, muted)
	d.cell(contentW, 5, strings.ToUpper(d.m.T("notes.title")), "L", 1)
	d.set("", fontBody, ink)
	d.paragraph(d.m.NotesText)
}

func (d *doc) paragraph(text string) {
	f := d.f
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			f.SetY(f.GetY() + 2)
			continue
		}
		for _, w := range d.wrap(line, contentW) {
			d.need(5)
			f.SetX(marginL)
			f.CellFormat(contentW, 4.6, w, "", 2, "L", false, 0, "")
		}
	}
}
