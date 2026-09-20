package word

import "strconv"

// Count is "1 SKU" / "3 SKUs": a number with its noun, plural when it is not
// one. It covers the regular plural only — every noun it is called with here
// takes an s.
func Count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
