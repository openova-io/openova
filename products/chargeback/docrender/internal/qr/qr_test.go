package qr

import (
	"bytes"
	"errors"
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

// --- known answers --------------------------------------------------------
//
// Every expected value in this section comes from a published source, quoted
// at the test that uses it. Nothing here was derived from this package.

// TestGeneratorPolynomialKnownAnswer checks the Reed-Solomon generator
// polynomial construction against two published polynomials.
//
// Degree 4, from the Wikiversity article "Reed-Solomon codes for coders"
// (https://en.wikiversity.org/wiki/Reed%E2%80%93Solomon_codes_for_coders):
//
//	g_4(x) = 01x^4 + 0fx^3 + 36x^2 + 78x + 40
//
// Degree 7, from thonky.com's generator polynomial tool
// (https://www.thonky.com/qr-code-tutorial/generator-polynomial-tool):
//
//	a^0 x^7 + a^87 x^6 + a^229 x^5 + a^146 x^4 + a^149 x^3 + a^238 x^2 +
//	a^102 x + a^21
//
// The degree-7 case is stated in alpha exponents, so it also pins the
// GF(256) log table, and with it the primitive polynomial 0x11D.
func TestGeneratorPolynomialKnownAnswer(t *testing.T) {
	wantDeg4 := []byte{0x01, 0x0f, 0x36, 0x78, 0x40}
	if got := rsGeneratorPoly(4); !bytes.Equal(got, wantDeg4) {
		t.Errorf("rsGeneratorPoly(4) = % x, want % x", got, wantDeg4)
	}

	wantDeg7Exponents := []int{0, 87, 229, 146, 149, 238, 102, 21}
	got := rsGeneratorPoly(7)
	if len(got) != len(wantDeg7Exponents) {
		t.Fatalf("rsGeneratorPoly(7) has %d coefficients, want %d", len(got), len(wantDeg7Exponents))
	}
	for i, c := range got {
		if c == 0 {
			t.Fatalf("rsGeneratorPoly(7)[%d] is zero, which has no alpha exponent", i)
		}
		if int(gfLog[c]) != wantDeg7Exponents[i] {
			t.Errorf("rsGeneratorPoly(7)[%d] = %#02x (alpha^%d), want alpha^%d",
				i, c, gfLog[c], wantDeg7Exponents[i])
		}
	}
}

// twasBrilligData is a published QR Code version-1 level-M data codeword
// block, used as the worked example in the Wikiversity article
// "Reed-Solomon codes for coders"
// (https://en.wikiversity.org/wiki/Reed%E2%80%93Solomon_codes_for_coders).
//
// Decoded by hand it is byte mode (0100), character count 13, payload
// "'Twas brillig", a four-bit terminator and one 0xEC pad codeword - i.e. it
// is exactly what this encoder must produce for that payload at version 1.
var (
	twasBrilligPayload = []byte("'Twas brillig")
	twasBrilligData    = []byte{
		0x40, 0xd2, 0x75, 0x47, 0x76, 0x17, 0x32, 0x06,
		0x27, 0x26, 0x96, 0xc6, 0xc6, 0x96, 0x70, 0xec,
	}
	// twasBrilligEC is the error correction result published alongside it.
	twasBrilligEC = []byte{0xbc, 0x2a, 0x90, 0x13, 0x6b, 0xaf, 0xef, 0xfd, 0x4b, 0xe0}
)

// TestReedSolomonKnownAnswer checks rsEncode against the published
// error-correction codewords for the worked example above.
func TestReedSolomonKnownAnswer(t *testing.T) {
	got := rsEncode(twasBrilligData, 10)
	if !bytes.Equal(got, twasBrilligEC) {
		t.Errorf("rsEncode(published data, 10) = % x, want % x", got, twasBrilligEC)
	}
}

// TestDataCodewordsKnownAnswer checks the whole data-encoding stage - mode
// indicator, character count, payload, terminator, bit padding and pad
// codewords - against the published codeword block.
func TestDataCodewordsKnownAnswer(t *testing.T) {
	got := encodeDataCodewords(twasBrilligPayload, 1)
	if !bytes.Equal(got, twasBrilligData) {
		t.Fatalf("encodeDataCodewords(%q, 1) = % x, want % x", twasBrilligPayload, got, twasBrilligData)
	}
	if len(got) != dataCodewords(1) {
		t.Errorf("got %d codewords, want %d", len(got), dataCodewords(1))
	}
}

// TestEncodeKnownAnswerEndToEnd encodes the published payload and reads the
// codewords back out of the finished matrix, so the placement, masking and
// format-information stages are all exercised against a published vector.
func TestEncodeKnownAnswerEndToEnd(t *testing.T) {
	code, err := Encode(twasBrilligPayload)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if code.Version != 1 {
		t.Fatalf("version = %d, want 1", code.Version)
	}

	raw, err := readMatrixCodewords(code)
	if err != nil {
		t.Fatalf("readMatrixCodewords: %v", err)
	}
	want := append(append([]byte{}, twasBrilligData...), twasBrilligEC...)
	if !bytes.Equal(raw, want) {
		t.Errorf("codewords read back from the matrix = % x, want % x", raw, want)
	}
}

// publishedFormatStrings is the format information string table from
// https://www.thonky.com/qr-code-tutorial/format-version-tables
// keyed by "<level><mask>", most significant bit first.
var publishedFormatStrings = map[string]string{
	"L0": "111011111000100", "L1": "111001011110011", "L2": "111110110101010", "L3": "111100010011101",
	"L4": "110011000101111", "L5": "110001100011000", "L6": "110110001000001", "L7": "110100101110110",
	"M0": "101010000010010", "M1": "101000100100101", "M2": "101111001111100", "M3": "101101101001011",
	"M4": "100010111111001", "M5": "100000011001110", "M6": "100111110010111", "M7": "100101010100000",
	"Q0": "011010101011111", "Q1": "011000001101000", "Q2": "011111100110001", "Q3": "011101000000110",
	"Q4": "010010010110100", "Q5": "010000110000011", "Q6": "010111011011010", "Q7": "010101111101101",
	"H0": "001011010001001", "H1": "001001110111110", "H2": "001110011100111", "H3": "001100111010000",
	"H4": "000011101100010", "H5": "000001001010101", "H6": "000110100001100", "H7": "000100000111011",
}

// ecLevelBits maps the level names used above onto their two-bit indicators
// (ISO/IEC 18004 Table 12): L=01, M=00, Q=11, H=10.
var ecLevelBits = map[string]int{"L": 0b01, "M": 0b00, "Q": 0b11, "H": 0b10}

// TestFormatInfoKnownAnswer checks the BCH(15,5) format information against
// all 32 published strings. This pins the generator, the XOR mask and the
// error-correction level indicator bits at once.
func TestFormatInfoKnownAnswer(t *testing.T) {
	for key, want := range publishedFormatStrings {
		level, mask := key[:1], int(key[1]-'0')
		got := formatInfoBits(ecLevelBits[level], mask)
		gotStr := pad(strconv.FormatInt(int64(got), 2), 15)
		if gotStr != want {
			t.Errorf("formatInfoBits(%s, %d) = %s, want %s", level, mask, gotStr, want)
		}
	}
	if ecLevelBitsM != ecLevelBits["M"] {
		t.Errorf("ecLevelBitsM = %d, want %d", ecLevelBitsM, ecLevelBits["M"])
	}
}

// publishedVersionStrings is the version information string table from
// https://www.thonky.com/qr-code-tutorial/format-version-tables
// indexed by version, most significant bit first. Versions below 7 carry no
// version information.
var publishedVersionStrings = map[int]string{
	7: "000111110010010100", 8: "001000010110111100", 9: "001001101010011001",
	10: "001010010011010011", 11: "001011101111110110", 12: "001100011101100010",
	13: "001101100001000111", 14: "001110011000001101", 15: "001111100100101000",
	16: "010000101101111000", 17: "010001010001011101", 18: "010010101000010111",
	19: "010011010100110010", 20: "010100100110100110", 21: "010101011010000011",
	22: "010110100011001001", 23: "010111011111101100", 24: "011000111011000100",
	25: "011001000111100001", 26: "011010111110101011", 27: "011011000010001110",
	28: "011100110000011010", 29: "011101001100111111", 30: "011110110101110101",
	31: "011111001001010000", 32: "100000100111010101", 33: "100001011011110000",
	34: "100010100010111010", 35: "100011011110011111", 36: "100100101100001011",
	37: "100101010000101110", 38: "100110101001100100", 39: "100111010101000001",
	40: "101000110001101001",
}

// TestVersionInfoKnownAnswer checks the BCH(18,6) version information for
// every version that carries it.
func TestVersionInfoKnownAnswer(t *testing.T) {
	for version := 7; version <= maxVersion; version++ {
		want, ok := publishedVersionStrings[version]
		if !ok {
			t.Fatalf("no published version string for version %d", version)
		}
		gotStr := pad(strconv.FormatInt(int64(versionInfoBits(version)), 2), 18)
		if gotStr != want {
			t.Errorf("versionInfoBits(%d) = %s, want %s", version, gotStr, want)
		}
	}
}

// publishedByteCapacityM is the byte-mode capacity at level M for versions
// 1..40, from https://www.thonky.com/qr-code-tutorial/character-capacities
var publishedByteCapacityM = []int{
	14, 26, 42, 62, 84, 106, 122, 152, 180, 213,
	251, 287, 331, 362, 412, 450, 504, 560, 624, 666,
	711, 779, 857, 911, 997, 1059, 1125, 1190, 1264, 1370,
	1452, 1538, 1628, 1722, 1809, 1911, 1989, 2099, 2213, 2331,
}

// TestByteCapacityMatchesPublished checks the derived capacity (data
// codewords minus the mode and character-count overhead) against the
// published capacity table for every supported version.
func TestByteCapacityMatchesPublished(t *testing.T) {
	for version := minVersion; version <= maxVersion; version++ {
		if got, want := byteCapacity(version), publishedByteCapacityM[version-1]; got != want {
			t.Errorf("byteCapacity(%d) = %d, want %d", version, got, want)
		}
	}
}

// TestAlignmentTableMatchesFormula re-derives the alignment pattern centres
// from the closed form used by Project Nayuki's reference implementation
// (_get_alignment_pattern_positions) and compares it with the tabulated
// coordinates, so the two published sources have to agree in code as well.
func TestAlignmentTableMatchesFormula(t *testing.T) {
	for version := minVersion; version <= maxVersion; version++ {
		got := alignmentPatternCenters[version]
		if version == 1 {
			if len(got) != 0 {
				t.Errorf("version 1 has %d alignment centres, want none", len(got))
			}
			continue
		}
		size := 17 + 4*version
		n := version/7 + 2
		step := (version*8 + n*3 + 5) / (n*4 - 4) * 2
		want := make([]int, 0, n)
		for i := n - 2; i >= 0; i-- {
			want = append(want, size-7-i*step)
		}
		want = append([]int{6}, want...)
		if len(got) != len(want) {
			t.Errorf("version %d: %v, want %v", version, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("version %d: %v, want %v", version, got, want)
				break
			}
		}
	}
}

// TestECBlockTableConsistency checks the block table against the independent
// raw-module count: data codewords plus error-correction codewords must fill
// the symbol exactly, and the block lengths must follow the required split
// (the D mod B long blocks hold one codeword more than the rest).
func TestECBlockTableConsistency(t *testing.T) {
	for version := minVersion; version <= maxVersion; version++ {
		layout := ecBlocksM[version]
		blocks := layout.group1Blocks + layout.group2Blocks
		data := dataCodewords(version)

		if got, want := data+layout.ecPerBlock*blocks, totalCodewordsByVersion[version]; got != want {
			t.Errorf("version %d: data+ec = %d codewords, want %d", version, got, want)
		}
		if layout.group1Data != data/blocks {
			t.Errorf("version %d: group1Data = %d, want %d", version, layout.group1Data, data/blocks)
		}
		if layout.group2Blocks != data%blocks {
			t.Errorf("version %d: group2Blocks = %d, want %d", version, layout.group2Blocks, data%blocks)
		}
		if layout.group2Blocks > 0 && layout.group2Data != layout.group1Data+1 {
			t.Errorf("version %d: group2Data = %d, want %d", version, layout.group2Data, layout.group1Data+1)
		}
	}
}

// --- Reed-Solomon syndromes ----------------------------------------------

// TestSyndromesAreZero recovers every interleaved block out of a finished
// symbol and evaluates its codeword polynomial at each root of the
// generator. A syndrome that is not zero means the error-correction
// remainder is wrong, and the check does not depend on how the generator
// polynomial was built.
//
// QR Code's first consecutive root is alpha^0, so the roots of a block with
// n error-correction codewords are alpha^0..alpha^(n-1). (The generator is
// the product of (x - alpha^i) for i in [0, n), per the Wikiversity article
// and thonky.com's generator polynomial tool; evaluating at alpha^1..alpha^n
// instead would be off by one and alpha^n is not generally a root - for the
// published vector above the syndrome there is 0xf8, not zero.)
func TestSyndromesAreZero(t *testing.T) {
	for _, size := range []int{1, 13, 50, 120, 200, 300, 700, 1500, 2331} {
		payload := patternBytes(size)
		code, err := Encode(payload)
		if err != nil {
			t.Fatalf("Encode(%d bytes): %v", size, err)
		}
		raw, err := readMatrixCodewords(code)
		if err != nil {
			t.Fatalf("readMatrixCodewords(%d bytes): %v", size, err)
		}
		dataBlocks, ecBlocks := deinterleaveCodewords(raw, code.Version)
		n := ecBlocksM[code.Version].ecPerBlock

		for b := range dataBlocks {
			codeword := append(append([]byte{}, dataBlocks[b]...), ecBlocks[b]...)
			for r := 0; r < n; r++ {
				if s := evaluatePoly(codeword, gfExp[r]); s != 0 {
					t.Errorf("payload %d bytes, version %d, block %d: syndrome at alpha^%d = %#02x, want 0",
						size, code.Version, b, r, s)
					break
				}
			}
		}
	}
}

// evaluatePoly evaluates a polynomial whose coefficients are given highest
// degree first, at x, using Horner's rule over GF(256).
func evaluatePoly(coeffs []byte, x byte) byte {
	var acc byte
	for _, c := range coeffs {
		acc = gfMul(acc, x) ^ c
	}
	return acc
}

// --- structure ------------------------------------------------------------

// TestStructuralInvariants checks the function patterns of every supported
// version: the symbol size, the three finder patterns and their separators,
// the two timing patterns, the dark module, and the alignment patterns at
// the coordinates the published table gives.
func TestStructuralInvariants(t *testing.T) {
	for version := minVersion; version <= maxVersion; version++ {
		payload := patternBytes(byteCapacity(version))
		code, err := Encode(payload)
		if err != nil {
			t.Fatalf("version %d: Encode: %v", version, err)
		}
		if code.Version != version {
			t.Fatalf("payload of %d bytes chose version %d, want %d", len(payload), code.Version, version)
		}
		if want := 17 + 4*version; code.Size != want {
			t.Fatalf("version %d: Size = %d, want %d", version, code.Size, want)
		}
		size := code.Size

		// Finder patterns: 7x7 concentric squares at three corners, each
		// with a light separator along its inner edges.
		corners := [3][2]int{{0, 0}, {size - 7, 0}, {0, size - 7}}
		for _, origin := range corners {
			ox, oy := origin[0], origin[1]
			for dy := 0; dy < 7; dy++ {
				for dx := 0; dx < 7; dx++ {
					ring := max(abs(dx-3), abs(dy-3))
					want := ring != 2
					if got := code.Dark(ox+dx, oy+dy); got != want {
						t.Fatalf("version %d: finder at (%d,%d) module (%d,%d) = %v, want %v",
							version, ox, oy, dx, dy, got, want)
					}
				}
			}
		}
		for i := 0; i < 8; i++ {
			if code.Dark(7, i) || code.Dark(i, 7) {
				t.Fatalf("version %d: top-left separator is not light at index %d", version, i)
			}
			if code.Dark(size-8, i) || code.Dark(size-1-i, 7) {
				t.Fatalf("version %d: top-right separator is not light at index %d", version, i)
			}
			if code.Dark(7, size-1-i) || code.Dark(i, size-8) {
				t.Fatalf("version %d: bottom-left separator is not light at index %d", version, i)
			}
		}

		// Timing patterns alternate dark-light between the finders.
		for i := 8; i < size-8; i++ {
			want := i%2 == 0
			if got := code.Dark(i, 6); got != want {
				t.Fatalf("version %d: horizontal timing module %d = %v, want %v", version, i, got, want)
			}
			if got := code.Dark(6, i); got != want {
				t.Fatalf("version %d: vertical timing module %d = %v, want %v", version, i, got, want)
			}
		}

		// The dark module.
		if !code.Dark(8, 4*version+9) {
			t.Fatalf("version %d: dark module at (8,%d) is light", version, 4*version+9)
		}

		// Alignment patterns: 5x5, dark except for the ring one module out.
		centers := alignmentPatternCenters[version]
		last := len(centers) - 1
		for i, cx := range centers {
			for j, cy := range centers {
				if (i == 0 && j == 0) || (i == 0 && j == last) || (i == last && j == 0) {
					continue
				}
				for dy := -2; dy <= 2; dy++ {
					for dx := -2; dx <= 2; dx++ {
						want := max(abs(dx), abs(dy)) != 1
						if got := code.Dark(cx+dx, cy+dy); got != want {
							t.Fatalf("version %d: alignment pattern at (%d,%d) module (%d,%d) = %v, want %v",
								version, cx, cy, dx, dy, got, want)
						}
					}
				}
			}
		}
	}
}

// TestDataPlacementStartsBottomRight pins the orientation of the zig-zag
// scan. ISO/IEC 18004 places the first data bit at the bottom-right module
// and walks upward in a two-module-wide column, so the first eight bits of
// the first codeword must land on these eight modules in this order.
func TestDataPlacementStartsBottomRight(t *testing.T) {
	code, err := Encode(twasBrilligPayload)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	size := code.Size
	want := []struct{ x, y int }{
		{size - 1, size - 1}, {size - 2, size - 1},
		{size - 1, size - 2}, {size - 2, size - 2},
		{size - 1, size - 3}, {size - 2, size - 3},
		{size - 1, size - 4}, {size - 2, size - 4},
	}
	first := twasBrilligData[0]
	for i, pos := range want {
		bit := first>>uint(7-i)&1 == 1
		got := code.Dark(pos.x, pos.y)
		if maskCondition(code.Mask, pos.y, pos.x) {
			got = !got
		}
		if got != bit {
			t.Errorf("data bit %d at (%d,%d): unmasked module = %v, want %v", i, pos.x, pos.y, got, bit)
		}
	}
}

// TestMaskOrientation guards against transposing row and column in the mask
// formulas, a mistake that leaves a self-consistent but unreadable symbol.
// Mask 1 depends only on the row and mask 2 only on the column.
func TestMaskOrientation(t *testing.T) {
	for row := 0; row < 12; row++ {
		for col := 0; col < 12; col++ {
			if got, want := maskCondition(1, row, col), row%2 == 0; got != want {
				t.Fatalf("mask 1 at row %d col %d = %v, want %v (mask 1 is a row rule)", row, col, got, want)
			}
			if got, want := maskCondition(2, row, col), col%3 == 0; got != want {
				t.Fatalf("mask 2 at row %d col %d = %v, want %v (mask 2 is a column rule)", row, col, got, want)
			}
			if got, want := maskCondition(4, row, col), (row/2+col/3)%2 == 0; got != want {
				t.Fatalf("mask 4 at row %d col %d = %v, want %v", row, col, got, want)
			}
		}
	}
}

// TestMaskSelectionPicksLowestPenalty re-scores all eight masks and checks
// that Encode returned the best one, with ties broken towards the lower mask
// number.
func TestMaskSelectionPicksLowestPenalty(t *testing.T) {
	for _, size := range []int{5, 40, 120, 260, 900} {
		payload := patternBytes(size)
		code, err := Encode(payload)
		if err != nil {
			t.Fatalf("Encode(%d bytes): %v", size, err)
		}

		base := newCanvas(code.Version)
		base.drawFunctionPatterns()
		base.drawCodewords(buildCodewords(payload, code.Version))

		bestMask, bestScore := -1, 0
		for mask := 0; mask < 8; mask++ {
			trial := base.clone()
			trial.applyMask(mask)
			trial.drawFormatInfo(mask)
			if score := trial.penalty(); bestMask < 0 || score < bestScore {
				bestMask, bestScore = mask, score
			}
		}
		if code.Mask != bestMask {
			t.Errorf("payload %d bytes: Mask = %d, want %d (lowest penalty)", size, code.Mask, bestMask)
		}
		if code.Mask < 0 || code.Mask > 7 {
			t.Errorf("payload %d bytes: Mask = %d out of range", size, code.Mask)
		}
	}
}

// TestPenaltyRulesOnSyntheticMatrices checks each penalty rule in isolation
// against a matrix whose score can be counted by hand.
func TestPenaltyRulesOnSyntheticMatrices(t *testing.T) {
	// Rule 1: one run of exactly five dark modules in a row of an otherwise
	// light 21x21 canvas. The five dark modules score N1 once. The light
	// modules around them form runs too: 21-5 = 16 light modules split as a
	// prefix of 8 and a suffix of 8 in that row, each scoring N1+3, and the
	// other 20 rows are runs of 21 light modules scoring N1+16 each. Rather
	// than enumerate all of that, compare against the same canvas without
	// the dark run and check the difference.
	blank := newCanvas(1)
	withRun := newCanvas(1)
	for x := 8; x < 13; x++ {
		withRun.modules[withRun.idx(x, 10)] = true
	}
	// Row 10 changes from one run of 21 light (N1+16) to 8 light, 5 dark,
	// 8 light: (N1+3) + N1 + (N1+3). Five columns each change from a run of
	// 21 light to 10 light, 1 dark, 10 light: (N1+5) + (N1+5), where they
	// had N1+16.
	wantDelta := (2*(penaltyN1+3) + penaltyN1) - (penaltyN1 + 16)
	wantDelta += 5 * ((2 * (penaltyN1 + 5)) - (penaltyN1 + 16))
	if got := withRun.penaltyRule1() - blank.penaltyRule1(); got != wantDelta {
		t.Errorf("penaltyRule1 delta = %d, want %d", got, wantDelta)
	}

	// Rule 2: a blank canvas is one big block of light modules, so every one
	// of the (size-1)^2 2x2 squares scores N2.
	if got, want := blank.penaltyRule2(), 20*20*penaltyN2; got != want {
		t.Errorf("penaltyRule2 on a blank canvas = %d, want %d", got, want)
	}

	// Rule 3: plant the finder-like sequence 1011101 0000 in one row. It
	// matches once horizontally. Its columns are otherwise light, so no
	// vertical match can appear.
	planted := newCanvas(1)
	for i, dark := range finderLikePatterns[0] {
		planted.modules[planted.idx(i, 3)] = dark
	}
	if got, want := planted.penaltyRule3(), penaltyN3; got != want {
		t.Errorf("penaltyRule3 with one planted pattern = %d, want %d", got, want)
	}
	// The mirrored pattern must be recognised too. Planting 0000 1011101 at
	// the start of an otherwise light row scores twice: once for the
	// mirrored pattern at offset 0, and once for the forward pattern at
	// offset 4, whose trailing four light modules are the light run that
	// follows the planted one. Both are genuine finder-like sequences.
	mirrored := newCanvas(1)
	for i, dark := range finderLikePatterns[1] {
		mirrored.modules[mirrored.idx(i, 3)] = dark
	}
	if got, want := mirrored.penaltyRule3(), 2*penaltyN3; got != want {
		t.Errorf("penaltyRule3 with one planted mirrored pattern = %d, want %d", got, want)
	}

	// Rule 4: an all-light canvas is 0% dark, which is ten 5% steps from
	// half, so k is 9 (the largest value the rule allows) scoring 9*N4.
	if got, want := blank.penaltyRule4(), 9*penaltyN4; got != want {
		t.Errorf("penaltyRule4 on an all-light canvas = %d, want %d", got, want)
	}
	// A canvas as close to half dark as an odd-sided symbol can get scores
	// nothing.
	balanced := newCanvas(1)
	for i := 0; i < len(balanced.modules)/2; i++ {
		balanced.modules[i] = true
	}
	if got := balanced.penaltyRule4(); got != 0 {
		t.Errorf("penaltyRule4 on a balanced canvas = %d, want 0", got)
	}
}

// --- API edges ------------------------------------------------------------

// TestPayloadTooLarge checks that an oversized payload is rejected rather
// than silently truncated into a corrupt symbol.
func TestPayloadTooLarge(t *testing.T) {
	limit := byteCapacity(maxVersion)
	if limit != publishedByteCapacityM[maxVersion-1] {
		t.Fatalf("capacity of version %d = %d, want %d", maxVersion, limit, publishedByteCapacityM[maxVersion-1])
	}

	if code, err := Encode(patternBytes(limit)); err != nil {
		t.Errorf("Encode of exactly %d bytes failed: %v", limit, err)
	} else if code.Version != maxVersion {
		t.Errorf("Encode of exactly %d bytes chose version %d, want %d", limit, code.Version, maxVersion)
	}

	for _, over := range []int{limit + 1, limit + 100, limit * 3} {
		code, err := Encode(patternBytes(over))
		if err == nil {
			t.Errorf("Encode of %d bytes returned a symbol (version %d), want an error", over, code.Version)
			continue
		}
		if code != nil {
			t.Errorf("Encode of %d bytes returned both an error and a symbol", over)
		}
		if !errors.Is(err, errTooLong) {
			t.Errorf("Encode of %d bytes: error %v, want one wrapping errTooLong", over, err)
		}
	}
}

// TestEmptyPayload checks the degenerate input.
func TestEmptyPayload(t *testing.T) {
	code, err := Encode(nil)
	if err != nil {
		t.Fatalf("Encode(nil): %v", err)
	}
	if code.Version != 1 {
		t.Errorf("Encode(nil) version = %d, want 1", code.Version)
	}
	got, err := decodeSymbol(code)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("round-tripped empty payload = % x, want empty", got)
	}
}

// TestDarkOutOfRange checks the accessor's bounds behaviour.
func TestDarkOutOfRange(t *testing.T) {
	code, err := Encode([]byte("bounds"))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	size := code.Size
	for _, p := range [][2]int{{-1, 0}, {0, -1}, {size, 0}, {0, size}, {size, size}, {-5, -5}, {1 << 20, 3}} {
		if code.Dark(p[0], p[1]) {
			t.Errorf("Dark(%d,%d) = true, want false for an out-of-range coordinate", p[0], p[1])
		}
	}
	if !code.Dark(0, 0) {
		t.Error("Dark(0,0) = false, want true (top-left finder corner)")
	}
	var nilCode *Code
	if nilCode.Dark(0, 0) {
		t.Error("Dark on a nil *Code = true, want false")
	}
}

// TestVersionSelectionIsSmallest checks that every payload length picks the
// smallest version that can hold it, at both sides of each boundary.
func TestVersionSelectionIsSmallest(t *testing.T) {
	for version := minVersion; version <= maxVersion; version++ {
		capacity := byteCapacity(version)
		code, err := Encode(patternBytes(capacity))
		if err != nil {
			t.Fatalf("Encode(%d bytes): %v", capacity, err)
		}
		if code.Version != version {
			t.Errorf("%d bytes chose version %d, want %d", capacity, code.Version, version)
		}
		if version < maxVersion {
			next, err := Encode(patternBytes(capacity + 1))
			if err != nil {
				t.Fatalf("Encode(%d bytes): %v", capacity+1, err)
			}
			if next.Version != version+1 {
				t.Errorf("%d bytes chose version %d, want %d", capacity+1, next.Version, version+1)
			}
		}
	}
}

// --- helpers --------------------------------------------------------------

// patternBytes builds a deterministic payload of n bytes that cycles through
// every byte value, so padding and block boundaries are exercised with
// non-trivial content.
func patternBytes(n int) []byte {
	out := make([]byte, n)
	r := rand.New(rand.NewSource(int64(n)))
	for i := range out {
		if i%3 == 0 {
			out[i] = byte(i)
		} else {
			out[i] = byte(r.Intn(256))
		}
	}
	return out
}

// pad left-pads a binary string with zeros to the given width.
func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return strings.Repeat("0", width-len(s)) + s
}
