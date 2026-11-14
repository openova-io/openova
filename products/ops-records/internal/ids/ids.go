// Package ids makes the identifiers of the operations records.
//
// Every identifier is a ULID: 26 characters of Crockford base 32. The first 10 characters hold
// the time in milliseconds since 1970 (48 bits), the last 16 hold 80 random bits. Identifiers
// made later sort later as plain strings. Within one millisecond a Generator adds one to the
// random part of the identifier before, so identifiers made by one Generator always rise.
//
// Incidents carry the prefix "INC-" and problems the prefix "PRB-". Signals, judgements and
// outbox rows carry the ULID alone.
package ids

import (
	"crypto/rand"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

// Length is the number of characters of a ULID.
const Length = 26

// Prefixes of the identifiers that carry one.
const (
	IncidentPrefix = "INC-"
	ProblemPrefix  = "PRB-"
)

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// maxMillis is the largest time a ULID can hold: 2^48 - 1 milliseconds.
const maxMillis = 1<<48 - 1

// ErrInvalid is returned for a string that is not a ULID.
var ErrInvalid = errors.New("ids: not a ULID")

// ErrOverflow is returned when the random part cannot rise within one millisecond.
var ErrOverflow = errors.New("ids: random part overflows within one millisecond")

// Generator makes rising ULIDs. The zero value is not usable; call NewGenerator.
type Generator struct {
	mu      sync.Mutex
	now     func() time.Time
	entropy io.Reader
	last    uint64   // milliseconds of the last identifier
	random  [10]byte // random part of the last identifier
	started bool
}

// NewGenerator returns a Generator that reads the time from now and random bits from entropy.
// A nil now means time.Now; a nil entropy means crypto/rand.
func NewGenerator(now func() time.Time, entropy io.Reader) *Generator {
	if now == nil {
		now = time.Now
	}
	if entropy == nil {
		entropy = rand.Reader
	}
	return &Generator{now: now, entropy: entropy}
}

// Next returns a new ULID. It is greater than every ULID this Generator returned before,
// also when the clock stands still or steps back.
func (g *Generator) Next() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ms := g.now().UnixMilli()
	if ms < 0 || ms > maxMillis {
		return "", errors.New("ids: time out of range")
	}
	t := uint64(ms) // #nosec G115 -- checked to lie in 0..2^48-1 above
	if g.started && t <= g.last {
		// Same millisecond, or the clock stepped back: keep the last time, add one.
		for i := len(g.random) - 1; ; i-- {
			if i < 0 {
				return "", ErrOverflow
			}
			g.random[i]++
			if g.random[i] != 0 {
				break
			}
		}
		return encode(g.last, g.random), nil
	}
	var r [10]byte
	if _, err := io.ReadFull(g.entropy, r[:]); err != nil {
		return "", err
	}
	g.last, g.random, g.started = t, r, true
	return encode(t, r), nil
}

var std = NewGenerator(nil, nil)

// New returns a new ULID from the package's own Generator. It panics only when the system's
// random source fails or the clock is outside the years 1970 to 10889.
func New() string {
	id, err := std.Next()
	if err != nil {
		panic(err)
	}
	return id
}

// NewIncident returns "INC-" followed by a new ULID.
func NewIncident() string { return IncidentPrefix + New() }

// NewProblem returns "PRB-" followed by a new ULID.
func NewProblem() string { return ProblemPrefix + New() }

// Valid reports whether s is a ULID in the canonical form: 26 characters of the alphabet,
// upper case, and a first character of at most 7.
func Valid(s string) bool {
	if len(s) != Length || s[0] > '7' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(alphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

// ValidIncident reports whether s is "INC-" followed by a ULID.
func ValidIncident(s string) bool {
	return strings.HasPrefix(s, IncidentPrefix) && Valid(strings.TrimPrefix(s, IncidentPrefix))
}

// ValidProblem reports whether s is "PRB-" followed by a ULID.
func ValidProblem(s string) bool {
	return strings.HasPrefix(s, ProblemPrefix) && Valid(strings.TrimPrefix(s, ProblemPrefix))
}

// Time returns the time a ULID holds, in UTC with millisecond precision.
func Time(s string) (time.Time, error) {
	if !Valid(s) {
		return time.Time{}, ErrInvalid
	}
	var ms int64
	for i := 0; i < 10; i++ {
		ms = ms<<5 | int64(strings.IndexByte(alphabet, s[i]))
	}
	return time.UnixMilli(ms).UTC(), nil
}

// encode writes 48 bits of time and 80 bits of randomness as 26 characters, most significant
// bits first. The 128 bits are read in groups of five from the left, after two zero bits.
func encode(ms uint64, r [10]byte) string {
	var b [16]byte
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (8 * (5 - i))) // #nosec G115 -- one byte of the 48-bit time
	}
	copy(b[6:], r[:])
	out := make([]byte, Length)
	// Bit position p counts from the left of the 130-bit string (two leading zero bits).
	for c := 0; c < Length; c++ {
		v := 0
		for k := 0; k < 5; k++ {
			p := c*5 + k - 2
			bit := 0
			if p >= 0 {
				bit = int(b[p/8]>>(7-p%8)) & 1
			}
			v = v<<1 | bit
		}
		out[c] = alphabet[v]
	}
	return string(out)
}
