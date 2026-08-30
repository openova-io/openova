// Package rating prices usage: the price book (annual list price divided by
// the book's annual divisor), the stopped-instance policy, and the statement
// run that turns a period's usage records into rated lines.
//
// All money math is exact rational arithmetic rounded once at the end.
package rating

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// parseRat parses a decimal string exactly.
func parseRat(s string) (*big.Rat, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return new(big.Rat), nil
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("not a number: %q", s)
	}
	return r, nil
}

// roundRat renders r rounded half-up to scale decimals.
func roundRat(r *big.Rat, scale int) string {
	neg := r.Sign() < 0
	abs := new(big.Rat).Abs(r)
	pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	scaled := new(big.Rat).Mul(abs, new(big.Rat).SetInt(pow))
	// half-up: floor(scaled + 1/2)
	half := big.NewRat(1, 2)
	scaled.Add(scaled, half)
	floor := new(big.Int).Quo(scaled.Num(), scaled.Denom())
	s := floor.String()
	if scale > 0 {
		if len(s) <= scale {
			s = strings.Repeat("0", scale-len(s)+1) + s
		}
		s = s[:len(s)-scale] + "." + s[len(s)-scale:]
	}
	if neg && strings.Trim(s, "0.") != "" {
		s = "-" + s
	}
	return s
}

// UnitPrice derives the hourly unit price from an annual list price:
// annual / divisor, rounded to 8 decimals.
func UnitPrice(annual string, divisor int) (store.Decimal, error) {
	if divisor <= 0 {
		return "", fmt.Errorf("annual_divisor must be positive")
	}
	a, err := parseRat(annual)
	if err != nil {
		return "", err
	}
	if a.Sign() < 0 {
		return "", fmt.Errorf("annual price must not be negative")
	}
	return store.Decimal(roundRat(new(big.Rat).Quo(a, big.NewRat(int64(divisor), 1)), 8)), nil
}

// Amount is quantity × unit price rounded to 6 decimals.
func Amount(quantity, unitPrice store.Decimal) (store.Decimal, error) {
	q, err := parseRat(string(quantity))
	if err != nil {
		return "", err
	}
	p, err := parseRat(string(unitPrice))
	if err != nil {
		return "", err
	}
	return store.Decimal(roundRat(new(big.Rat).Mul(q, p), 6)), nil
}

// Sub returns a − b at 6 decimals (never below zero).
func Sub(a, b store.Decimal) (store.Decimal, error) {
	x, err := parseRat(string(a))
	if err != nil {
		return "", err
	}
	y, err := parseRat(string(b))
	if err != nil {
		return "", err
	}
	d := new(big.Rat).Sub(x, y)
	if d.Sign() < 0 {
		d = new(big.Rat)
	}
	return store.Decimal(roundRat(d, 6)), nil
}

// Sum adds decimals at 6 decimals.
func Sum(values ...store.Decimal) (store.Decimal, error) {
	total := new(big.Rat)
	for _, v := range values {
		r, err := parseRat(string(v))
		if err != nil {
			return "", err
		}
		total.Add(total, r)
	}
	return store.Decimal(roundRat(total, 6)), nil
}

// Tax computes subtotal × rate at 6 decimals.
func Tax(subtotal, rate store.Decimal) (store.Decimal, error) {
	return Amount(subtotal, rate)
}
