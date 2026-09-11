package money

import "testing"

func TestMinorUnits(t *testing.T) {
	for cur, want := range map[string]int{"OMR": 3, "BHD": 3, "KWD": 3, "JOD": 3, "IQD": 3, "LYD": 3, "TND": 3, "omr": 3, "USD": 2, "EUR": 2, "SAR": 2, "AED": 2, "": 2} {
		if got := MinorUnits(cur); got != want {
			t.Errorf("MinorUnits(%q) = %d, want %d", cur, got, want)
		}
	}
}

// The minor-unit formatting table: exact decimal strings in, rounded and
// grouped strings out — half away from zero at the last kept digit.
func TestFormat(t *testing.T) {
	cases := []struct{ dec, cur, want string }{
		{"0", "OMR", "0.000"},
		{"0", "USD", "0.00"},
		{"14.856782", "OMR", "14.857"},
		{"14.856782", "USD", "14.86"},
		{"4.8565", "OMR", "4.857"}, // half rounds away from zero
		{"4.8564999", "OMR", "4.856"},
		{"-4.8565", "OMR", "-4.857"}, // ... on both sides of zero
		{"1234567.5", "OMR", "1,234,567.500"},
		{"1234.5", "EUR", "1,234.50"},
		{"999.999", "USD", "1,000.00"},
		{"-0.0001", "USD", "0.00"}, // rounds to zero: no negative zero
		{"12345678901234567890.123456", "KWD", "12,345,678,901,234,567,890.123"},
		{"  7.5  ", "USD", "7.50"},
	}
	for _, c := range cases {
		got, err := Format(c.dec, c.cur, EnglishStyle)
		if err != nil {
			t.Errorf("Format(%q,%q): %v", c.dec, c.cur, err)
			continue
		}
		if got != c.want {
			t.Errorf("Format(%q,%q) = %q, want %q", c.dec, c.cur, got, c.want)
		}
	}
}

func TestFormatRefusesNonDecimals(t *testing.T) {
	for _, bad := range []string{"", "abc", "1,000", "1e3", "$5", "5.", ".5", "--5", "5.5.5", "NaN", "0x10"} {
		if _, err := Format(bad, "USD", EnglishStyle); err == nil {
			t.Errorf("Format(%q) accepted", bad)
		}
	}
}

func TestFormatQuantityAndPercent(t *testing.T) {
	for dec, want := range map[string]string{"720.000000": "720", "0.500000": "0.5", "1234.5": "1,234.5", "3": "3", "0.000001": "0.000001", "0.0000004": "0"} {
		got, err := FormatQuantity(dec, EnglishStyle)
		if err != nil || got != want {
			t.Errorf("FormatQuantity(%q) = %q, %v; want %q", dec, got, err, want)
		}
	}
	for rate, want := range map[string]string{"0.05": "5%", "0.0525": "5.25%", "0": "0%", "0.15": "15%", "1": "100%", "0.000125": "0.0125%"} {
		got, err := FormatRatePercent(rate, EnglishStyle)
		if err != nil || got != want {
			t.Errorf("FormatRatePercent(%q) = %q, %v; want %q", rate, got, err, want)
		}
	}
}

func TestFormatUnitPrice(t *testing.T) {
	cases := []struct{ dec, cur, want string }{
		{"0.001500", "OMR", "0.0015"},
		{"0.012000", "OMR", "0.012"},
		{"45.000000", "OMR", "45.000"},
		{"45.000000", "USD", "45.00"},
		{"0.000100", "USD", "0.0001"},
		{"1234.5", "EUR", "1,234.50"},
	}
	for _, c := range cases {
		got, err := FormatUnitPrice(c.dec, c.cur, EnglishStyle)
		if err != nil || got != c.want {
			t.Errorf("FormatUnitPrice(%q,%q) = %q, %v; want %q", c.dec, c.cur, got, err, c.want)
		}
	}
}

func TestAddNegateIsZero(t *testing.T) {
	if got, _ := Add("100.000000", "25.500000"); got != "125.500000" {
		t.Errorf("Add = %q", got)
	}
	if got, _ := Add("0.1", "0.2"); got != "0.300000" {
		t.Errorf("Add(0.1,0.2) = %q (float arithmetic would say 0.30000000000000004)", got)
	}
	if got, _ := Negate("12.5"); got != "-12.5" {
		t.Errorf("Negate = %q", got)
	}
	if got, _ := Negate("-12.5"); got != "12.5" {
		t.Errorf("Negate(-) = %q", got)
	}
	if got, _ := Negate("0.000"); got != "0.000" {
		t.Errorf("Negate(0) = %q", got)
	}
	if !IsZero("0.000000") || !IsZero("") || IsZero("0.001") {
		t.Error("IsZero wrong")
	}
}
