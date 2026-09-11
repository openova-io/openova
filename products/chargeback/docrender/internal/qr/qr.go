// Package qr encodes a byte payload as a QR Code matrix (ISO/IEC 18004),
// byte mode, error-correction level M. No dependencies.
//
// The pipeline follows ISO/IEC 18004 directly: mode and character-count
// indicators, terminator, 0xEC/0x11 padding, Reed-Solomon error correction
// over GF(256) with primitive polynomial 0x11D, block splitting and
// interleaving, function patterns, the eight data masks scored by the four
// standard penalty rules, BCH-coded format information and, from version 7
// up, BCH-coded version information.
//
// The numeric tables live in tables.go, with their published sources cited
// there. The structural rules used here are grounded as follows:
//
//   - mask pattern formulas:
//     https://www.thonky.com/qr-code-tutorial/mask-patterns
//   - penalty rules 1-3 (including the two finder-like 11-module patterns):
//     https://www.thonky.com/qr-code-tutorial/data-masking
//   - format and version information placement, BCH generators and the
//     format XOR mask:
//     https://www.thonky.com/qr-code-tutorial/format-version-information
//   - the zig-zag codeword placement order, the finder/alignment/timing
//     module geometry and the exact-integer form of penalty rule 4, from
//     Project Nayuki's QR-Code-generator reference implementation
//     (_draw_codewords, _draw_finder_pattern, _draw_alignment_pattern,
//     _draw_format_bits, _draw_version, _get_penalty_score):
//     https://github.com/nayuki/QR-Code-generator
package qr

import (
	"errors"
	"fmt"
)

// Code is an encoded symbol.
type Code struct {
	Version int // 1..N
	Mask    int // 0..7
	Size    int // modules per side = 17 + 4*Version

	// modules is the symbol in row-major order, len == Size*Size.
	modules []bool
}

// errTooLong is returned when no supported version can hold the payload.
var errTooLong = errors.New("qr: payload too long")

// Encode encodes data, choosing the smallest version that fits.
// It returns an error when the payload does not fit the supported versions.
func Encode(data []byte) (*Code, error) {
	version := 0
	for v := minVersion; v <= maxVersion; v++ {
		if len(data) <= byteCapacity(v) {
			version = v
			break
		}
	}
	if version == 0 {
		return nil, fmt.Errorf("%w: %d bytes exceeds the %d-byte byte-mode capacity of version %d at level M",
			errTooLong, len(data), byteCapacity(maxVersion), maxVersion)
	}

	codewords := buildCodewords(data, version)

	base := newCanvas(version)
	base.drawFunctionPatterns()
	base.drawCodewords(codewords)

	bestMask := 0
	bestScore := -1
	var best *canvas
	for mask := 0; mask < 8; mask++ {
		trial := base.clone()
		trial.applyMask(mask)
		trial.drawFormatInfo(mask)
		if score := trial.penalty(); bestScore < 0 || score < bestScore {
			bestMask, bestScore, best = mask, score, trial
		}
	}

	return &Code{
		Version: version,
		Mask:    bestMask,
		Size:    best.size,
		modules: best.modules,
	}, nil
}

// Dark reports whether the module at column x, row y is dark.
// Out-of-range coordinates report false.
func (c *Code) Dark(x, y int) bool {
	if c == nil || x < 0 || y < 0 || x >= c.Size || y >= c.Size {
		return false
	}
	return c.modules[y*c.Size+x]
}

// --- capacity -------------------------------------------------------------

// byteModeIndicator is the 4-bit mode indicator for byte mode (ISO/IEC 18004
// Table 2).
const byteModeIndicator = 0b0100

// charCountBits is the width of the character-count indicator for byte mode:
// 8 bits for versions 1-9 and 16 bits for versions 10-40 (ISO/IEC 18004
// Table 3, as republished at
// https://www.thonky.com/qr-code-tutorial/data-encoding).
func charCountBits(version int) int {
	if version <= 9 {
		return 8
	}
	return 16
}

// dataCodewords is the number of 8-bit data codewords a version holds at
// level M, i.e. the sum over its error-correction blocks.
func dataCodewords(version int) int {
	b := ecBlocksM[version]
	return b.group1Blocks*b.group1Data + b.group2Blocks*b.group2Data
}

// byteCapacity is the largest byte-mode payload a version holds at level M.
// The value is derived rather than tabulated, and the derivation is checked
// against the published capacity table in TestByteCapacityMatchesPublished.
func byteCapacity(version int) int {
	return (dataCodewords(version)*8 - 4 - charCountBits(version)) / 8
}

// --- bit buffer -----------------------------------------------------------

// bitBuffer accumulates a big-endian bit stream.
type bitBuffer struct {
	data []byte
	n    int
}

// appendBits appends the low bits of value, most significant first.
func (b *bitBuffer) appendBits(value uint32, bits int) {
	for i := bits - 1; i >= 0; i-- {
		if b.n%8 == 0 {
			b.data = append(b.data, 0)
		}
		if (value>>uint(i))&1 == 1 {
			b.data[b.n/8] |= 1 << uint(7-b.n%8)
		}
		b.n++
	}
}

// --- data encoding --------------------------------------------------------

// padCodewords are the alternating pad codewords 0b11101100 and 0b00010001
// (ISO/IEC 18004 8.4.9).
var padCodewords = [2]byte{0xEC, 0x11}

// encodeDataCodewords builds the data codeword sequence for a payload: mode
// indicator, character count, payload bytes, terminator, bit padding to a
// codeword boundary, then alternating pad codewords up to capacity.
func encodeDataCodewords(data []byte, version int) []byte {
	capacity := dataCodewords(version)
	capacityBits := capacity * 8

	var b bitBuffer
	b.appendBits(byteModeIndicator, 4)
	b.appendBits(uint32(len(data)), charCountBits(version))
	for _, d := range data {
		b.appendBits(uint32(d), 8)
	}

	// Terminator: up to four 0 bits, truncated if capacity runs out.
	terminator := 4
	if remaining := capacityBits - b.n; remaining < terminator {
		terminator = remaining
	}
	b.appendBits(0, terminator)

	// Pad with 0 bits up to the next codeword boundary.
	if rem := b.n % 8; rem != 0 {
		b.appendBits(0, 8-rem)
	}

	out := b.data
	for i := 0; len(out) < capacity; i++ {
		out = append(out, padCodewords[i%2])
	}
	return out
}

// buildCodewords produces the full interleaved codeword sequence (data then
// error correction) that is written into the symbol.
func buildCodewords(data []byte, version int) []byte {
	return interleave(encodeDataCodewords(data, version), version)
}

// splitBlocks divides the data codewords into the version's error-correction
// blocks and computes each block's error-correction codewords.
func splitBlocks(dataCW []byte, version int) (blocks, ecs [][]byte) {
	layout := ecBlocksM[version]
	offset := 0
	add := func(count, size int) {
		for i := 0; i < count; i++ {
			block := dataCW[offset : offset+size]
			offset += size
			blocks = append(blocks, block)
			ecs = append(ecs, rsEncode(block, layout.ecPerBlock))
		}
	}
	add(layout.group1Blocks, layout.group1Data)
	add(layout.group2Blocks, layout.group2Data)
	return blocks, ecs
}

// interleave orders the block codewords as they are written into the symbol:
// the i-th data codeword of every block in turn (short blocks dropping out
// early), then the i-th error-correction codeword of every block in turn
// (ISO/IEC 18004 8.6).
func interleave(dataCW []byte, version int) []byte {
	blocks, ecs := splitBlocks(dataCW, version)

	out := make([]byte, 0, totalCodewordsByVersion[version])
	longest := 0
	for _, b := range blocks {
		if len(b) > longest {
			longest = len(b)
		}
	}
	for i := 0; i < longest; i++ {
		for _, b := range blocks {
			if i < len(b) {
				out = append(out, b[i])
			}
		}
	}
	for i := 0; i < ecBlocksM[version].ecPerBlock; i++ {
		for _, e := range ecs {
			out = append(out, e[i])
		}
	}
	return out
}

// --- GF(256) and Reed-Solomon --------------------------------------------

// gfPrimitive is the primitive polynomial x^8+x^4+x^3+x^2+1 used by QR Code
// (ISO/IEC 18004 8.5.2).
const gfPrimitive = 0x11D

var (
	// gfExp[i] is alpha^i; doubled so index sums need no reduction.
	gfExp [512]byte
	// gfLog[v] is the i with alpha^i == v; gfLog[0] is meaningless.
	gfLog [256]byte
)

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		gfExp[i] = byte(x)
		gfLog[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= gfPrimitive
		}
	}
	for i := 255; i < len(gfExp); i++ {
		gfExp[i] = gfExp[i-255]
	}
}

// gfMul multiplies two GF(256) elements.
func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

// rsGeneratorPoly returns the generator polynomial of the given degree, the
// product of (x - alpha^i) for i in [0, degree). Coefficients are ordered
// with the leading (highest-degree) term first, so the result has
// degree+1 entries and starts with 1.
//
// QR Code's first consecutive root is alpha^0, not alpha^1; the degree-4 and
// degree-7 polynomials this produces are checked against published values in
// TestGeneratorPolynomialKnownAnswer.
func rsGeneratorPoly(degree int) []byte {
	poly := []byte{1}
	for i := 0; i < degree; i++ {
		next := make([]byte, len(poly)+1)
		for j, c := range poly {
			next[j] ^= c                    // c * x
			next[j+1] ^= gfMul(c, gfExp[i]) // c * alpha^i
		}
		poly = next
	}
	return poly
}

// rsEncode returns the ecCount error-correction codewords for a block: the
// remainder of the data polynomial shifted by ecCount, divided by the
// generator polynomial.
func rsEncode(data []byte, ecCount int) []byte {
	gen := rsGeneratorPoly(ecCount)
	rem := make([]byte, ecCount)
	for _, d := range data {
		factor := d ^ rem[0]
		copy(rem, rem[1:])
		rem[ecCount-1] = 0
		for i := 0; i < ecCount; i++ {
			rem[i] ^= gfMul(gen[i+1], factor)
		}
	}
	return rem
}

// --- matrix ---------------------------------------------------------------

// canvas is a symbol under construction: the module colours plus a map of
// which modules are function patterns (and so are never masked or used for
// codewords).
type canvas struct {
	version  int
	size     int
	modules  []bool
	function []bool
}

func newCanvas(version int) *canvas {
	size := 17 + 4*version
	return &canvas{
		version:  version,
		size:     size,
		modules:  make([]bool, size*size),
		function: make([]bool, size*size),
	}
}

func (c *canvas) clone() *canvas {
	out := &canvas{
		version:  c.version,
		size:     c.size,
		modules:  make([]bool, len(c.modules)),
		function: make([]bool, len(c.function)),
	}
	copy(out.modules, c.modules)
	copy(out.function, c.function)
	return out
}

func (c *canvas) idx(x, y int) int { return y*c.size + x }

func (c *canvas) get(x, y int) bool { return c.modules[c.idx(x, y)] }

// setFunction paints a function module, ignoring out-of-range coordinates so
// that patterns drawn near an edge can be clipped.
func (c *canvas) setFunction(x, y int, dark bool) {
	if x < 0 || y < 0 || x >= c.size || y >= c.size {
		return
	}
	c.modules[c.idx(x, y)] = dark
	c.function[c.idx(x, y)] = true
}

// drawFunctionPatterns lays down every module that is not payload: timing
// patterns, the three finders with their separators, the alignment patterns,
// and the reserved format and version information areas.
func (c *canvas) drawFunctionPatterns() {
	for i := 0; i < c.size; i++ {
		c.setFunction(6, i, i%2 == 0)
		c.setFunction(i, 6, i%2 == 0)
	}

	c.drawFinder(3, 3)
	c.drawFinder(c.size-4, 3)
	c.drawFinder(3, c.size-4)

	centers := alignmentPatternCenters[c.version]
	last := len(centers) - 1
	for i, cx := range centers {
		for j, cy := range centers {
			// The three corners are occupied by finder patterns.
			if (i == 0 && j == 0) || (i == 0 && j == last) || (i == last && j == 0) {
				continue
			}
			c.drawAlignment(cx, cy)
		}
	}

	// Reserve the format and version areas. The format bits are rewritten
	// once the mask is known; the version bits are already final.
	c.drawFormatInfo(0)
	c.drawVersionInfo()
}

// drawFinder draws a 9x9 finder pattern plus its separator, centred at
// (x, y). A module is dark when its Chebyshev distance from the centre is
// not 2 (the light ring) and not 4 (the separator).
func (c *canvas) drawFinder(x, y int) {
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			d := max(abs(dx), abs(dy))
			c.setFunction(x+dx, y+dy, d != 2 && d != 4)
		}
	}
}

// drawAlignment draws a 5x5 alignment pattern centred at (x, y): dark except
// for the ring at Chebyshev distance 1.
func (c *canvas) drawAlignment(x, y int) {
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			c.setFunction(x+dx, y+dy, max(abs(dx), abs(dy)) != 1)
		}
	}
}

// drawCodewords writes the interleaved codeword bit stream into the symbol in
// the standard zig-zag order: two-module-wide columns walked right to left,
// alternating upward and downward, skipping the vertical timing column and
// every function module. Any leftover remainder modules stay light.
func (c *canvas) drawCodewords(codewords []byte) {
	totalBits := len(codewords) * 8
	bit := 0
	upward := true
	for col := c.size - 1; col > 0; col -= 2 {
		if col == 6 {
			// Column 6 is the vertical timing pattern; step past it so the
			// column pairs stay two payload columns wide.
			col--
		}
		for i := 0; i < c.size; i++ {
			row := i
			if upward {
				row = c.size - 1 - i
			}
			for j := 0; j < 2; j++ {
				x := col - j
				if c.function[c.idx(x, row)] || bit >= totalBits {
					continue
				}
				if (codewords[bit/8]>>uint(7-bit%8))&1 == 1 {
					c.modules[c.idx(x, row)] = true
				}
				bit++
			}
		}
		upward = !upward
	}
}

// maskCondition reports whether mask inverts the module at the given row and
// column. Formulas from
// https://www.thonky.com/qr-code-tutorial/mask-patterns
func maskCondition(mask, row, col int) bool {
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
		return false
	}
}

// applyMask inverts every non-function module the mask selects.
func (c *canvas) applyMask(mask int) {
	for row := 0; row < c.size; row++ {
		for col := 0; col < c.size; col++ {
			i := c.idx(col, row)
			if !c.function[i] && maskCondition(mask, row, col) {
				c.modules[i] = !c.modules[i]
			}
		}
	}
}

// --- format and version information --------------------------------------

const (
	// formatGenerator is the BCH(15,5) generator x^10+x^8+x^5+x^4+x^2+x+1.
	formatGenerator = 0b10100110111
	// formatMaskXOR is applied to the finished format string so that it is
	// never all zero.
	formatMaskXOR = 0b101010000010010
	// ecLevelBitsM is the level-M error-correction indicator.
	ecLevelBitsM = 0b00
	// versionGenerator is the BCH(18,6) generator
	// x^12+x^11+x^10+x^9+x^8+x^5+x^2+1.
	versionGenerator = 0b1111100100101
)

// formatInfoBits returns the 15-bit format information for an error-
// correction level and mask: five data bits, ten BCH check bits, XORed with
// the format mask. Checked against the published table in
// TestFormatInfoKnownAnswer.
func formatInfoBits(ecLevelBits, mask int) int {
	data := ecLevelBits<<3 | mask
	rem := data << 10
	for bit := 14; bit >= 10; bit-- {
		if rem&(1<<uint(bit)) != 0 {
			rem ^= formatGenerator << uint(bit-10)
		}
	}
	return (data<<10 | rem) ^ formatMaskXOR
}

// versionInfoBits returns the 18-bit version information for versions 7 and
// up: six data bits and twelve BCH check bits, with no mask. Checked against
// the published table in TestVersionInfoKnownAnswer.
func versionInfoBits(version int) int {
	rem := version << 12
	for bit := 17; bit >= 12; bit-- {
		if rem&(1<<uint(bit)) != 0 {
			rem ^= versionGenerator << uint(bit-12)
		}
	}
	return version<<12 | rem
}

// drawFormatInfo writes both copies of the format information, plus the
// always-dark module at (8, 4*version+9).
func (c *canvas) drawFormatInfo(mask int) {
	bits := formatInfoBits(ecLevelBitsM, mask)
	bitAt := func(i int) bool { return (bits>>uint(i))&1 == 1 }

	// First copy: down the left of the top-left finder, then back along the
	// row beneath it. Bit 0 is the least significant bit.
	for i := 0; i <= 5; i++ {
		c.setFunction(8, i, bitAt(i))
	}
	c.setFunction(8, 7, bitAt(6))
	c.setFunction(8, 8, bitAt(7))
	c.setFunction(7, 8, bitAt(8))
	for i := 9; i <= 14; i++ {
		c.setFunction(14-i, 8, bitAt(i))
	}

	// Second copy: along the bottom of the top-right finder and up the right
	// of the bottom-left finder.
	for i := 0; i <= 7; i++ {
		c.setFunction(c.size-1-i, 8, bitAt(i))
	}
	for i := 8; i <= 14; i++ {
		c.setFunction(8, c.size-15+i, bitAt(i))
	}

	// The dark module, always set, at (8, 4*version+9) == (8, size-8).
	c.setFunction(8, c.size-8, true)
}

// drawVersionInfo writes both 6x3 copies of the version information for
// versions 7 and up.
func (c *canvas) drawVersionInfo() {
	if c.version < 7 {
		return
	}
	bits := versionInfoBits(c.version)
	for i := 0; i < 18; i++ {
		dark := (bits>>uint(i))&1 == 1
		a := c.size - 11 + i%3
		b := i / 3
		c.setFunction(a, b, dark) // top-right block
		c.setFunction(b, a, dark) // bottom-left block
	}
}

// --- penalty scoring ------------------------------------------------------

// Penalty weights from ISO/IEC 18004 Table 24.
const (
	penaltyN1 = 3
	penaltyN2 = 3
	penaltyN3 = 40
	penaltyN4 = 10
)

// finderLikePatterns are the two 11-module sequences penalty rule 3 looks
// for: the 1:1:3:1:1 finder ratio with four light modules on one side.
// https://www.thonky.com/qr-code-tutorial/data-masking
var finderLikePatterns = [2][11]bool{
	{true, false, true, true, true, false, true, false, false, false, false},
	{false, false, false, false, true, false, true, true, true, false, true},
}

func (c *canvas) penalty() int {
	return c.penaltyRule1() + c.penaltyRule2() + c.penaltyRule3() + c.penaltyRule4()
}

// penaltyRule1 charges N1 for each run of five same-coloured modules in a row
// or column, plus one more for every module beyond five.
func (c *canvas) penaltyRule1() int {
	score := 0
	scan := func(at func(i int) bool) {
		run := 1
		for i := 1; i < c.size; i++ {
			if at(i) == at(i-1) {
				run++
				switch {
				case run == 5:
					score += penaltyN1
				case run > 5:
					score++
				}
			} else {
				run = 1
			}
		}
	}
	for y := 0; y < c.size; y++ {
		scan(func(x int) bool { return c.get(x, y) })
	}
	for x := 0; x < c.size; x++ {
		scan(func(y int) bool { return c.get(x, y) })
	}
	return score
}

// penaltyRule2 charges N2 for every 2x2 block of one colour.
func (c *canvas) penaltyRule2() int {
	score := 0
	for y := 0; y < c.size-1; y++ {
		for x := 0; x < c.size-1; x++ {
			v := c.get(x, y)
			if c.get(x+1, y) == v && c.get(x, y+1) == v && c.get(x+1, y+1) == v {
				score += penaltyN2
			}
		}
	}
	return score
}

// penaltyRule3 charges N3 for each finder-like 11-module sequence in a row or
// column.
func (c *canvas) penaltyRule3() int {
	score := 0
	line := make([]bool, c.size)
	count := func() {
		for start := 0; start+11 <= c.size; start++ {
			for _, pattern := range finderLikePatterns {
				match := true
				for k := 0; k < 11; k++ {
					if line[start+k] != pattern[k] {
						match = false
						break
					}
				}
				if match {
					score += penaltyN3
				}
			}
		}
	}
	for y := 0; y < c.size; y++ {
		for x := 0; x < c.size; x++ {
			line[x] = c.get(x, y)
		}
		count()
	}
	for x := 0; x < c.size; x++ {
		for y := 0; y < c.size; y++ {
			line[y] = c.get(x, y)
		}
		count()
	}
	return score
}

// penaltyRule4 charges N4 for every 5% the proportion of dark modules strays
// from half. k is the smallest integer >= 0 with
// (45-5k)% <= dark/total <= (55+5k)%, computed in exact integer arithmetic
// after Project Nayuki's _get_penalty_score.
func (c *canvas) penaltyRule4() int {
	dark := 0
	for _, m := range c.modules {
		if m {
			dark++
		}
	}
	total := c.size * c.size
	k := (abs(dark*20-total*10)+total-1)/total - 1
	if k < 0 {
		k = 0
	}
	return k * penaltyN4
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
