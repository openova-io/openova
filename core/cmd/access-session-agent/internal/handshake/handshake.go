// Package handshake speaks the Guacamole protocol to guacd on the loopback
// address: the instruction format and the handshake of ADR-0033's tunnel, hop
// 3, as spike ACC-020 recorded them against guacd 1.6.0.
package handshake

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxElement     = 1 << 20 // code points of one element
	maxInstruction = 4 << 20 // bytes of one instruction
	// Timeout bounds the whole handshake.
	Timeout = 30 * time.Second
)

// Encode writes one instruction: each element is <length>.<value>, the length
// counted in Unicode code points; a comma separates elements, a semicolon ends
// the instruction.
func Encode(elems ...string) string {
	var b strings.Builder
	for i, e := range elems {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(utf8.RuneCountInString(e)))
		b.WriteByte('.')
		b.WriteString(e)
	}
	b.WriteByte(';')
	return b.String()
}

// Reader reads instructions from guacd.
type Reader struct{ r *bufio.Reader }

// NewReader reads instructions from r.
func NewReader(r io.Reader) *Reader { return &Reader{bufio.NewReaderSize(r, 64<<10)} }

// Buffered is the number of bytes read from the connection and not returned.
func (r *Reader) Buffered() int { return r.r.Buffered() }

// Next returns the next whole instruction as it was sent, and its elements
// when elems is true. A connection that ends between two instructions is
// io.EOF; anything else that is not one valid instruction is an error.
func (r *Reader) Next(elems bool) ([]byte, []string, error) {
	var raw bytes.Buffer
	var out []string
	for {
		head, err := r.r.ReadSlice('.')
		if err != nil || len(head) > 11 {
			if errors.Is(err, io.EOF) && len(head) == 0 && raw.Len() == 0 {
				return nil, nil, io.EOF
			}
			return nil, nil, fmt.Errorf("no element length: %v", err)
		}
		n, err := strconv.Atoi(string(head[:len(head)-1]))
		if err != nil || n < 0 || n > maxElement {
			return nil, nil, fmt.Errorf("bad element length %q", head)
		}
		raw.Write(head)
		start := raw.Len()
		for i := 0; i < n; i++ {
			b, err := r.r.ReadByte()
			if err != nil {
				return nil, nil, io.ErrUnexpectedEOF
			}
			raw.WriteByte(b)
			for k := width(b); k > 1; k-- {
				c, err := r.r.ReadByte()
				if err != nil {
					return nil, nil, io.ErrUnexpectedEOF
				}
				raw.WriteByte(c)
			}
		}
		if elems {
			out = append(out, string(raw.Bytes()[start:]))
		}
		t, err := r.r.ReadByte()
		if err != nil {
			return nil, nil, io.ErrUnexpectedEOF
		}
		raw.WriteByte(t)
		if raw.Len() > maxInstruction {
			return nil, nil, errors.New("an instruction is longer than 4 MiB")
		}
		switch t {
		case ';':
			if !utf8.Valid(raw.Bytes()) {
				return nil, nil, errors.New("an instruction is not valid UTF-8")
			}
			return raw.Bytes(), out, nil
		case ',':
		default:
			return nil, nil, fmt.Errorf("bad terminator %q", t)
		}
	}
}

// width is the length in bytes of the UTF-8 sequence that b starts.
func width(b byte) int {
	switch {
	case b >= 0xF0:
		return 4
	case b >= 0xE0:
		return 3
	case b >= 0xC0:
		return 2
	}
	return 1
}

// Size is what the browser reported of its display.
type Size struct {
	Width, Height, DPI int
	Timezone           string
}

// GuacdError is an error instruction guacd sent instead of args or ready.
type GuacdError struct{ Message, Status string }

func (e *GuacdError) Error() string { return "guacd: " + e.Message + " (status " + e.Status + ")" }

// Result is what a handshake gave: the connection identifier of ready and the
// parameter names that guacd asked for.
type Result struct {
	ID   string
	Args []string
}

// Run performs the handshake on conn: select with protocol; read args; send
// size, audio and video (both empty), image, timezone and name; send connect
// with, when args starts with a protocol version, that version first, then one
// value per name in args, empty for a name params lacks; read ready. Every
// byte guacd sends after ready stays in r.
func Run(conn net.Conn, r *Reader, protocol, name string, s Size, params map[string]string) (Result, error) {
	if err := conn.SetDeadline(time.Now().Add(Timeout)); err != nil {
		return Result{}, err
	}
	send := func(elems ...string) error {
		_, err := io.WriteString(conn, Encode(elems...))
		return err
	}
	if err := send("select", protocol); err != nil {
		return Result{}, err
	}
	args, err := expect(r, "args")
	if err != nil {
		return Result{}, err
	}
	for _, in := range [][]string{
		{"size", strconv.Itoa(s.Width), strconv.Itoa(s.Height), strconv.Itoa(s.DPI)},
		{"audio"}, {"video"}, {"image", "image/png", "image/jpeg", "image/webp"},
		{"timezone", s.Timezone}, {"name", name},
	} {
		if err := send(in...); err != nil {
			return Result{}, err
		}
	}
	values := []string{"connect"}
	if len(args) > 0 && strings.HasPrefix(args[0], "VERSION_") {
		values, args = append(values, args[0]), args[1:]
	}
	for _, a := range args {
		values = append(values, params[a])
	}
	if err := send(values...); err != nil {
		return Result{}, err
	}
	ready, err := expect(r, "ready")
	if err != nil {
		return Result{}, err
	}
	if len(ready) != 1 {
		return Result{}, fmt.Errorf("ready has %d values, want 1", len(ready))
	}
	return Result{ID: ready[0], Args: args}, conn.SetDeadline(time.Time{})
}

// expect reads one instruction and wants opcode op; it returns the values.
func expect(r *Reader, op string) ([]string, error) {
	_, el, err := r.Next(true)
	switch {
	case err != nil:
		return nil, fmt.Errorf("waiting for %s: %w", op, err)
	case el[0] == "error" && len(el) == 3:
		return nil, &GuacdError{Message: el[1], Status: el[2]}
	case el[0] != op:
		return nil, fmt.Errorf("guacd sent %q, want %s", el[0], op)
	}
	return el[1:], nil
}
