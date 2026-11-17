package agentguard

import (
	"strconv"
	"unicode/utf8"
)

// MaxAnswer is the largest answer, in characters, that a tool gives an agent.
const MaxAnswer = 24000

// Fit returns text when it has at most max characters. A longer text keeps its start and its
// end, which holds the newest lines of a log, with the line "[N characters dropped]" between
// them, and has at most max characters in all. A max of zero or less returns "".
func Fit(text string, max int) string {
	n := utf8.RuneCountInString(text)
	if n <= max {
		return text
	}
	if max <= 0 {
		return ""
	}
	note := func(k int) string { return "\n[" + strconv.Itoa(k) + " characters dropped]\n" }
	r := []rune(text)
	room := max - utf8.RuneCountInString(note(n)) // the note for n is at least as long as any shorter one
	if room < 2 {
		return string(r[:max])
	}
	head := (room + 1) / 2
	tail := room - head
	return string(r[:head]) + note(n-room) + string(r[n-tail:])
}

// FitList chooses the entries of a list, in the list's order with the newest last, whose sizes
// add up to at most max. It takes the first entry, then entries alternately from the end and
// from the start, and stops at the first entry that does not fit. It returns the indexes it
// keeps, in rising order, and the number of entries it drops. The caller keeps room in max for
// the line that says how many were dropped.
func FitList(sizes []int, max int) (keep []int, dropped int) {
	lo, hi := 0, len(sizes)-1
	used, fromEnd := 0, false
	var front, back []int
	for lo <= hi {
		i := lo
		if fromEnd {
			i = hi
		}
		if sizes[i] < 0 || used+sizes[i] > max {
			break
		}
		used += sizes[i]
		if fromEnd {
			back = append(back, i)
			hi--
		} else {
			front = append(front, i)
			lo++
		}
		fromEnd = !fromEnd
	}
	keep = front
	for j := len(back) - 1; j >= 0; j-- {
		keep = append(keep, back[j])
	}
	return keep, len(sizes) - len(keep)
}
