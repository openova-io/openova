// Package money formats amounts that arrive as DECIMAL STRINGS — never
// floats — at the minor unit of their currency. Arithmetic is exact
// (math/big); only the final rendering rounds, half away from zero, which is
// what an invoice total is expected to do.
package money

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// ErrNotDecimal reports a string that is not a plain decimal number.
var ErrNotDecimal = errors.New("not a decimal number")

// Style is the locale's digit grouping: the thousands separator and the
// decimal mark. English is "1,234.567".
type Style struct {
	Thousands string
	Decimal   string
}

// EnglishStyle is the `en` grouping.
var EnglishStyle = Style{Thousands: ",", Decimal: "."}

// threeDecimalCurrencies move at a thousandth: the dinars and the rial that
// subdivide into 1000 (baisa, fils, dirham). Everything else moves at a
// hundredth.
var threeDecimalCurrencies = map[string]bool{
	"OMR": true, "BHD": true, "KWD": true, "JOD": true, "IQD": true, "LYD": true, "TND": true,
}

// MinorUnits is the number of decimal places money in the currency moves at.
func MinorUnits(currency string) int {
	if threeDecimalCurrencies[strings.ToUpper(strings.TrimSpace(currency))] {
		return 3
	}
	return 2
}

// Parse accepts an optional sign, digits, and an optional fraction
// ("-12", "0.5", "1234.567890"). Anything else — exponents, thousands
// separators, currency symbols, blanks — is refused, because a document
// amount that needs cleaning up was produced wrong upstream.
func Parse(dec string) (*big.Rat, error) {
	s := strings.TrimSpace(dec)
	if s == "" {
		return nil, fmt.Errorf("%w: empty", ErrNotDecimal)
	}
	body := s
	if body[0] == '-' || body[0] == '+' {
		body = body[1:]
	}
	intPart, frac, hasDot := strings.Cut(body, ".")
	if intPart == "" || !digits(intPart) || (hasDot && (frac == "" || !digits(frac))) {
		return nil, fmt.Errorf("%w: %q", ErrNotDecimal, dec)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotDecimal, dec)
	}
	return r, nil
}

func digits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Format renders dec at the currency's minor unit with the style's grouping:
// Format("1234.5", "OMR", EnglishStyle) = "1,234.500". Rounding is half away
// from zero at the last kept digit.
func Format(dec, currency string, st Style) (string, error) {
	r, err := Parse(dec)
	if err != nil {
		return "", err
	}
	return FormatRat(r, MinorUnits(currency), st), nil
}

// FormatRat renders an exact value with `places` decimals.
func FormatRat(r *big.Rat, places int, st Style) string {
	neg := r.Sign() < 0
	abs := new(big.Rat).Abs(r)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(places)), nil)
	num := new(big.Int).Mul(abs.Num(), scale)
	q, rem := new(big.Int).QuoRem(num, abs.Denom(), new(big.Int))
	// half away from zero: 2*rem >= den rounds up
	if new(big.Int).Mul(rem, big.NewInt(2)).Cmp(abs.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	s := q.String()
	if places > 0 {
		for len(s) <= places {
			s = "0" + s
		}
	}
	intPart, frac := s, ""
	if places > 0 {
		intPart, frac = s[:len(s)-places], s[len(s)-places:]
	}
	out := group(intPart, st.Thousands)
	if places > 0 {
		out += st.Decimal + frac
	}
	if neg && q.Sign() != 0 {
		out = "-" + out
	}
	return out
}

func group(intPart, sep string) string {
	if sep == "" || len(intPart) <= 3 {
		return intPart
	}
	var b strings.Builder
	head := len(intPart) % 3
	if head > 0 {
		b.WriteString(intPart[:head])
	}
	for i := head; i < len(intPart); i += 3 {
		if b.Len() > 0 {
			b.WriteString(sep)
		}
		b.WriteString(intPart[i : i+3])
	}
	return b.String()
}

// FormatQuantity renders a quantity with up to six decimals and no trailing
// zeros ("720.000000" → "720", "0.500000" → "0.5"), grouped like money.
func FormatQuantity(dec string, st Style) (string, error) {
	r, err := Parse(dec)
	if err != nil {
		return "", err
	}
	s := FormatRat(r, 6, Style{Thousands: st.Thousands, Decimal: "\x00"})
	intPart, frac, _ := strings.Cut(s, "\x00")
	frac = strings.TrimRight(frac, "0")
	if frac == "" {
		return intPart, nil
	}
	return intPart + st.Decimal + frac, nil
}

// FormatUnitPrice renders a unit price with the precision it was priced at:
// at least the currency's minor unit, at most six decimals, trailing zeros
// beyond the minor unit trimmed. A vCPU-hour at 0.0015 OMR is shown as
// 0.0015, not rounded to 0.002; a flat 45 OMR plan is shown as 45.000.
func FormatUnitPrice(dec, currency string, st Style) (string, error) {
	r, err := Parse(dec)
	if err != nil {
		return "", err
	}
	minor := MinorUnits(currency)
	s := FormatRat(r, 6, Style{Thousands: st.Thousands, Decimal: "\x00"})
	intPart, frac, _ := strings.Cut(s, "\x00")
	for len(frac) > minor && strings.HasSuffix(frac, "0") {
		frac = frac[:len(frac)-1]
	}
	return intPart + st.Decimal + frac, nil
}

// FormatRatePercent renders a fractional rate as a percentage: "0.05" → "5%",
// "0.0525" → "5.25%", "0" → "0%". Up to four decimals, trailing zeros trimmed.
func FormatRatePercent(rate string, st Style) (string, error) {
	r, err := Parse(rate)
	if err != nil {
		return "", err
	}
	r.Mul(r, big.NewRat(100, 1))
	s := FormatRat(r, 4, Style{Thousands: "", Decimal: "\x00"})
	intPart, frac, _ := strings.Cut(s, "\x00")
	frac = strings.TrimRight(frac, "0")
	if frac == "" {
		return intPart + "%", nil
	}
	return intPart + st.Decimal + frac + "%", nil
}

// Add returns a+b as a decimal string with six places (the ledger's own
// precision), for the one derived figure a document needs: the list
// subtotal = net subtotal + discounts.
func Add(a, b string) (string, error) {
	ra, err := Parse(a)
	if err != nil {
		return "", err
	}
	rb, err := Parse(b)
	if err != nil {
		return "", err
	}
	return FormatRat(new(big.Rat).Add(ra, rb), 6, Style{Thousands: "", Decimal: "."}), nil
}

// Negate flips the sign of a decimal string, keeping its digits.
func Negate(dec string) (string, error) {
	r, err := Parse(dec)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(dec)
	if r.Sign() == 0 {
		return strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+"), nil
	}
	if strings.HasPrefix(s, "-") {
		return s[1:], nil
	}
	return "-" + strings.TrimPrefix(s, "+"), nil
}

// IsZero reports whether the decimal string is zero (or blank).
func IsZero(dec string) bool {
	if strings.TrimSpace(dec) == "" {
		return true
	}
	r, err := Parse(dec)
	return err == nil && r.Sign() == 0
}
