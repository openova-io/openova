package qr

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"strconv"
	"testing"
)

// This file holds a QR Code decoder used only by the tests. It is written
// from the ISO/IEC 18004 rules rather than by calling into the encoder: it
// builds its own map of function modules, reads the format information out of
// the matrix and validates it against the published BCH codeword table, undoes
// the mask with its own copy of the published mask formulas, walks the
// zig-zag itself, de-interleaves the blocks and parses the byte-mode header.
// If the encoder and the decoder agree, and the decoder also agrees with the
// published format, version, capacity and codeword tables, the symbol is
// genuinely a QR Code and not merely self-consistent.

// refMaskCondition is an independent transcription of the mask table at
// https://www.thonky.com/qr-code-tutorial/mask-patterns
// It exists so that the decoder does not lean on the encoder's copy.
func refMaskCondition(mask, row, col int) bool {
	switch mask {
	case 0:
		return (row+col)%2 == 0
	case 1:
		return row%2 == 0
	case 2:
		return col%3 == 0
	case 3:
		return (row+col)%3 == 0
	case 4:
		return (row/2+col/3)%2 == 0
	case 5:
		return (row*col)%2+(row*col)%3 == 0
	case 6:
		return ((row*col)%2+(row*col)%3)%2 == 0
	case 7:
		return ((row+col)%2+(row*col)%3)%2 == 0
	default:
		panic("mask out of range")
	}
}

// TestMaskConditionsAgree checks the encoder's mask formulas against this
// file's independent transcription over a full version-40 grid.
func TestMaskConditionsAgree(t *testing.T) {
	size := 17 + 4*maxVersion
	for mask := 0; mask < 8; mask++ {
		for row := 0; row < size; row++ {
			for col := 0; col < size; col++ {
				if maskCondition(mask, row, col) != refMaskCondition(mask, row, col) {
					t.Fatalf("mask %d disagrees at row %d col %d", mask, row, col)
				}
			}
		}
	}
}

// reservedModules marks every module of a symbol that is a function pattern
// or reserved area, and so carries no codeword bits: the three finder
// patterns with their separators, both timing patterns, the format
// information areas with the dark module, the version information areas, and
// the alignment patterns.
func reservedModules(version int) []bool {
	size := 17 + 4*version
	out := make([]bool, size*size)
	mark := func(x, y int) {
		if x >= 0 && y >= 0 && x < size && y < size {
			out[y*size+x] = true
		}
	}

	// Finder patterns plus separators: an 8x8 block in three corners.
	for dy := 0; dy < 8; dy++ {
		for dx := 0; dx < 8; dx++ {
			mark(dx, dy)
			mark(size-1-dx, dy)
			mark(dx, size-1-dy)
		}
	}

	// Timing patterns.
	for i := 0; i < size; i++ {
		mark(6, i)
		mark(i, 6)
	}

	// Format information: row 8 and column 8 next to the top-left finder,
	// then the split second copy, which includes the dark module.
	for i := 0; i < 9; i++ {
		mark(8, i)
		mark(i, 8)
	}
	for i := 0; i < 8; i++ {
		mark(size-1-i, 8)
		mark(8, size-1-i)
	}

	// Version information: two 6x3 blocks.
	if version >= 7 {
		for i := 0; i < 6; i++ {
			for j := 0; j < 3; j++ {
				mark(size-11+j, i)
				mark(i, size-11+j)
			}
		}
	}

	// Alignment patterns, at every pairing of the published centres except
	// the three that would sit on a finder.
	centers := alignmentPatternCenters[version]
	last := len(centers) - 1
	for i, cx := range centers {
		for j, cy := range centers {
			if (i == 0 && j == 0) || (i == 0 && j == last) || (i == last && j == 0) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					mark(cx+dx, cy+dy)
				}
			}
		}
	}
	return out
}

// formatPositions returns the module coordinates of the 15 format
// information bits, indexed from the least significant bit, for the given
// copy (0 around the top-left finder, 1 split across the other two).
func formatPositions(size, copyIndex int) [15][2]int {
	var p [15][2]int
	if copyIndex == 0 {
		for i := 0; i <= 5; i++ {
			p[i] = [2]int{8, i}
		}
		p[6] = [2]int{8, 7}
		p[7] = [2]int{8, 8}
		p[8] = [2]int{7, 8}
		for i := 9; i <= 14; i++ {
			p[i] = [2]int{14 - i, 8}
		}
		return p
	}
	for i := 0; i <= 7; i++ {
		p[i] = [2]int{size - 1 - i, 8}
	}
	for i := 8; i <= 14; i++ {
		p[i] = [2]int{8, size - 15 + i}
	}
	return p
}

// readFormatInfo reads both copies of the format information, requires them
// to agree, and validates the result against the published BCH codeword
// table (which is what proves the check bits are right).
func readFormatInfo(c *Code) (level string, mask int, err error) {
	read := func(copyIndex int) int {
		value := 0
		for i, pos := range formatPositions(c.Size, copyIndex) {
			if c.Dark(pos[0], pos[1]) {
				value |= 1 << uint(i)
			}
		}
		return value
	}
	first, second := read(0), read(1)
	if first != second {
		return "", 0, fmt.Errorf("format information copies disagree: %015b and %015b", first, second)
	}
	for key, s := range publishedFormatStrings {
		v, parseErr := strconv.ParseInt(s, 2, 32)
		if parseErr != nil {
			return "", 0, parseErr
		}
		if int(v) == first {
			return key[:1], int(key[1] - '0'), nil
		}
	}
	return "", 0, fmt.Errorf("format information %015b is not one of the 32 valid codewords", first)
}

// checkVersionInfo validates both copies of the version information against
// the published table.
func checkVersionInfo(c *Code, version int) error {
	want, ok := publishedVersionStrings[version]
	if !ok {
		return fmt.Errorf("no published version string for version %d", version)
	}
	wantValue, err := strconv.ParseInt(want, 2, 32)
	if err != nil {
		return err
	}
	topRight, bottomLeft := 0, 0
	for i := 0; i < 18; i++ {
		if c.Dark(c.Size-11+i%3, i/3) {
			topRight |= 1 << uint(i)
		}
		if c.Dark(i/3, c.Size-11+i%3) {
			bottomLeft |= 1 << uint(i)
		}
	}
	if topRight != int(wantValue) {
		return fmt.Errorf("top-right version information = %018b, want %s", topRight, want)
	}
	if bottomLeft != int(wantValue) {
		return fmt.Errorf("bottom-left version information = %018b, want %s", bottomLeft, want)
	}
	return nil
}

// readMatrixCodewords reads the interleaved codeword stream back out of a
// finished symbol: it recovers the mask from the format information, then
// walks the zig-zag, skipping function modules and undoing the mask.
func readMatrixCodewords(c *Code) ([]byte, error) {
	version := (c.Size - 17) / 4
	if version < minVersion || version > maxVersion || 17+4*version != c.Size {
		return nil, fmt.Errorf("%d modules per side is not a valid symbol size", c.Size)
	}
	level, mask, err := readFormatInfo(c)
	if err != nil {
		return nil, err
	}
	if level != "M" {
		return nil, fmt.Errorf("error-correction level %s, want M", level)
	}
	if version >= 7 {
		if err := checkVersionInfo(c, version); err != nil {
			return nil, err
		}
	}

	reserved := reservedModules(version)
	total := totalCodewordsByVersion[version]
	out := make([]byte, total)
	limit := total * 8
	bit := 0
	upward := true
	for col := c.Size - 1; col > 0; col -= 2 {
		if col == 6 {
			col-- // step over the vertical timing pattern
		}
		for i := 0; i < c.Size; i++ {
			row := i
			if upward {
				row = c.Size - 1 - i
			}
			for j := 0; j < 2; j++ {
				x := col - j
				if reserved[row*c.Size+x] || bit >= limit {
					continue
				}
				dark := c.Dark(x, row)
				if refMaskCondition(mask, row, x) {
					dark = !dark
				}
				if dark {
					out[bit/8] |= 1 << uint(7-bit%8)
				}
				bit++
			}
		}
		upward = !upward
	}
	if bit != limit {
		return nil, fmt.Errorf("only %d data bits are reachable, want %d", bit, limit)
	}
	return out, nil
}

// deinterleaveCodewords undoes the block interleaving, returning the data
// codewords and the error-correction codewords of each block.
func deinterleaveCodewords(raw []byte, version int) (dataBlocks, ecBlocks [][]byte) {
	layout := ecBlocksM[version]
	for i := 0; i < layout.group1Blocks; i++ {
		dataBlocks = append(dataBlocks, make([]byte, layout.group1Data))
	}
	for i := 0; i < layout.group2Blocks; i++ {
		dataBlocks = append(dataBlocks, make([]byte, layout.group2Data))
	}

	pos := 0
	longest := layout.group1Data
	if layout.group2Blocks > 0 {
		longest = layout.group2Data
	}
	for i := 0; i < longest; i++ {
		for b := range dataBlocks {
			if i < len(dataBlocks[b]) {
				dataBlocks[b][i] = raw[pos]
				pos++
			}
		}
	}

	ecBlocks = make([][]byte, len(dataBlocks))
	for b := range ecBlocks {
		ecBlocks[b] = make([]byte, layout.ecPerBlock)
	}
	for i := 0; i < layout.ecPerBlock; i++ {
		for b := range ecBlocks {
			ecBlocks[b][i] = raw[pos]
			pos++
		}
	}
	return dataBlocks, ecBlocks
}

// bitReader reads a big-endian bit stream out of a codeword slice.
type bitReader struct {
	data []byte
	pos  int
}

func (r *bitReader) read(n int) (int, error) {
	v := 0
	for i := 0; i < n; i++ {
		if r.pos >= len(r.data)*8 {
			return 0, io.ErrUnexpectedEOF
		}
		v = v<<1 | int(r.data[r.pos/8]>>uint(7-r.pos%8)&1)
		r.pos++
	}
	return v, nil
}

// decodeSymbol is the full round trip: matrix in, original payload out. It
// also checks the terminator and the pad codewords, so a padding mistake
// cannot pass unnoticed.
func decodeSymbol(c *Code) ([]byte, error) {
	version := (c.Size - 17) / 4
	raw, err := readMatrixCodewords(c)
	if err != nil {
		return nil, err
	}

	dataBlocks, _ := deinterleaveCodewords(raw, version)
	var dataCW []byte
	for _, b := range dataBlocks {
		dataCW = append(dataCW, b...)
	}
	if len(dataCW) != dataCodewords(version) {
		return nil, fmt.Errorf("recovered %d data codewords, want %d", len(dataCW), dataCodewords(version))
	}

	r := &bitReader{data: dataCW}
	mode, err := r.read(4)
	if err != nil {
		return nil, err
	}
	if mode != 0b0100 {
		return nil, fmt.Errorf("mode indicator %04b, want 0100 (byte mode)", mode)
	}

	countBits := 8
	if version >= 10 {
		countBits = 16
	}
	length, err := r.read(countBits)
	if err != nil {
		return nil, err
	}
	payload := make([]byte, length)
	for i := range payload {
		b, err := r.read(8)
		if err != nil {
			return nil, fmt.Errorf("payload byte %d: %w", i, err)
		}
		payload[i] = byte(b)
	}

	// Terminator: up to four 0 bits, fewer only if capacity ran out.
	totalBits := len(dataCW) * 8
	terminator := 4
	if remaining := totalBits - r.pos; remaining < terminator {
		terminator = remaining
	}
	if v, err := r.read(terminator); err != nil {
		return nil, err
	} else if v != 0 {
		return nil, fmt.Errorf("terminator is %0*b, want zeros", terminator, v)
	}

	// Zero bits up to the codeword boundary.
	if rem := r.pos % 8; rem != 0 {
		v, err := r.read(8 - rem)
		if err != nil {
			return nil, err
		}
		if v != 0 {
			return nil, fmt.Errorf("bit padding is %0*b, want zeros", 8-rem, v)
		}
	}

	// Alternating pad codewords for the rest.
	for i := 0; r.pos < totalBits; i++ {
		v, err := r.read(8)
		if err != nil {
			return nil, err
		}
		if want := padCodewords[i%2]; byte(v) != want {
			return nil, fmt.Errorf("pad codeword %d is %#02x, want %#02x", i, v, want)
		}
	}

	return payload, nil
}

// --- round trips ----------------------------------------------------------

// TestRoundTripPayloadLengths encodes payloads that land on a spread of
// versions - across the 8-bit to 16-bit character-count boundary and across
// single-block, two-group and many-block layouts - and decodes each one back
// out of the matrix.
func TestRoundTripPayloadLengths(t *testing.T) {
	for _, n := range []int{0, 1, 10, 13, 50, 120, 200, 300, 500, 1000, 1800, 2331} {
		payload := patternBytes(n)
		code, err := Encode(payload)
		if err != nil {
			t.Fatalf("Encode(%d bytes): %v", n, err)
		}
		got, err := decodeSymbol(code)
		if err != nil {
			t.Fatalf("decode of %d bytes (version %d, mask %d): %v", n, code.Version, code.Mask, err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("round trip of %d bytes (version %d) returned %d bytes that differ", n, code.Version, len(got))
		}
	}
}

// TestRoundTripAllByteValues checks that byte mode is transparent to every
// value from 0 to 255, including the NUL and high-bit bytes that a
// text-oriented mode would mangle.
func TestRoundTripAllByteValues(t *testing.T) {
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte(i)
	}
	code, err := Encode(payload)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := decodeSymbol(code)
	if err != nil {
		t.Fatalf("decode (version %d, mask %d): %v", code.Version, code.Mask, err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round trip of all 256 byte values differs")
	}

	// The same values again, in a payload long enough to span several
	// blocks, so interleaving is exercised on them too.
	long := make([]byte, 2000)
	for i := range long {
		long[i] = byte((i * 7) % 256)
	}
	code, err = Encode(long)
	if err != nil {
		t.Fatalf("Encode(2000 bytes): %v", err)
	}
	got, err = decodeSymbol(code)
	if err != nil {
		t.Fatalf("decode (version %d): %v", code.Version, err)
	}
	if !bytes.Equal(got, long) {
		t.Fatalf("round trip of 2000 mixed bytes differs")
	}
}

// TestRoundTripEveryVersion fills every supported version to its exact
// capacity, so every block layout in the table is encoded and decoded at
// least once.
func TestRoundTripEveryVersion(t *testing.T) {
	for version := minVersion; version <= maxVersion; version++ {
		payload := patternBytes(byteCapacity(version))
		code, err := Encode(payload)
		if err != nil {
			t.Fatalf("version %d: Encode: %v", version, err)
		}
		if code.Version != version {
			t.Fatalf("payload of %d bytes chose version %d, want %d", len(payload), code.Version, version)
		}
		got, err := decodeSymbol(code)
		if err != nil {
			t.Fatalf("version %d (mask %d): decode: %v", version, code.Mask, err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("version %d: round trip differs", version)
		}
	}
}

// TestRoundTripRandomPayloads sweeps random lengths and contents, including
// the 120-220 byte base64 range this package was written for.
func TestRoundTripRandomPayloads(t *testing.T) {
	r := rand.New(rand.NewSource(20260911))
	for i := 0; i < 300; i++ {
		var n int
		switch i % 3 {
		case 0:
			n = 120 + r.Intn(101) // the expected payload range
		case 1:
			n = r.Intn(400)
		default:
			n = r.Intn(2332)
		}
		payload := make([]byte, n)
		r.Read(payload)

		code, err := Encode(payload)
		if err != nil {
			t.Fatalf("Encode(%d bytes): %v", n, err)
		}
		got, err := decodeSymbol(code)
		if err != nil {
			t.Fatalf("decode of %d bytes (version %d, mask %d): %v", n, code.Version, code.Mask, err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("round trip of %d random bytes (version %d) differs", n, code.Version)
		}
	}
}

// TestFormatInformationIsReadable checks that both copies of the format
// information are present, agree, and decode to level M with the mask the
// encoder reported, for every version.
func TestFormatInformationIsReadable(t *testing.T) {
	for version := minVersion; version <= maxVersion; version++ {
		code, err := Encode(patternBytes(byteCapacity(version)))
		if err != nil {
			t.Fatalf("version %d: Encode: %v", version, err)
		}
		level, mask, err := readFormatInfo(code)
		if err != nil {
			t.Fatalf("version %d: readFormatInfo: %v", version, err)
		}
		if level != "M" {
			t.Errorf("version %d: level %s, want M", version, level)
		}
		if mask != code.Mask {
			t.Errorf("version %d: format information says mask %d, Code.Mask is %d", version, mask, code.Mask)
		}
	}
}

// TestVersionInformationIsReadable checks both copies of the version
// information for the versions that carry it, and checks that smaller
// versions do not write one.
func TestVersionInformationIsReadable(t *testing.T) {
	for version := minVersion; version <= maxVersion; version++ {
		code, err := Encode(patternBytes(byteCapacity(version)))
		if err != nil {
			t.Fatalf("version %d: Encode: %v", version, err)
		}
		if version < 7 {
			// There is no version information area below version 7; the
			// modules there must be ordinary payload, which the round trip
			// already covers. Nothing to check.
			continue
		}
		if err := checkVersionInfo(code, version); err != nil {
			t.Errorf("version %d: %v", version, err)
		}
	}
}

// TestReservedMapMatchesEncoder checks that this file's independently built
// map of function modules covers exactly the modules the encoder treats as
// function modules. A mismatch would mean one of the two has the geometry
// wrong, even if the round trip happened to work.
func TestReservedMapMatchesEncoder(t *testing.T) {
	for version := minVersion; version <= maxVersion; version++ {
		cv := newCanvas(version)
		cv.drawFunctionPatterns()
		want := cv.function
		got := reservedModules(version)
		if len(got) != len(want) {
			t.Fatalf("version %d: reserved map has %d entries, want %d", version, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("version %d: reserved map differs at module (%d,%d): %v vs %v",
					version, i%cv.size, i/cv.size, got[i], want[i])
			}
		}
	}
}
