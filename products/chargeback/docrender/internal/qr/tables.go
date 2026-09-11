// Code generated from the grounded ISO/IEC 18004 tables; see the source
// citations below. Do not edit by hand.

package qr

// Table provenance
//
// Every number in this file was taken from two independent published sources
// and cross-checked against each other before being written here (all 40
// versions x 4 EC levels agreed exactly, with zero discrepancies):
//
//  A. thonky.com's QR Code Tutorial, which republishes the ISO/IEC 18004
//     tables verbatim:
//       - error-correction blocks per version/level:
//         https://www.thonky.com/qr-code-tutorial/error-correction-table
//       - alignment-pattern centre coordinates:
//         https://www.thonky.com/qr-code-tutorial/alignment-pattern-locations
//       - byte-mode character capacities:
//         https://www.thonky.com/qr-code-tutorial/character-capacities
//  B. Project Nayuki's QR-Code-generator reference implementation,
//     python/qrcodegen.py (tables _ECC_CODEWORDS_PER_BLOCK and
//     _NUM_ERROR_CORRECTION_BLOCKS, and the functions
//     _get_num_raw_data_modules and _get_alignment_pattern_positions):
//       https://github.com/nayuki/QR-Code-generator
//
// The block split within a version is not free-form: given the total data
// codeword count D and the block count B, the short blocks hold D/B
// codewords and the D mod B long blocks hold one more. That derivation was
// checked against every row of source A and agrees everywhere, so the two
// sources confirm each other structurally as well as numerically.

const (
	minVersion = 1
	maxVersion = 40
)

// ecBlockLayout is the error-correction block structure of one version at
// level M: every block carries ecPerBlock error-correction codewords, and
// the data codewords are split into group1Blocks short blocks followed by
// group2Blocks long blocks (one codeword longer).
type ecBlockLayout struct {
	ecPerBlock   int
	group1Blocks int
	group1Data   int
	group2Blocks int
	group2Data   int
}

// ecBlocksM is indexed by version; index 0 is unused padding.
//
// Columns: EC codewords per block, group-1 block count x data codewords,
// group-2 block count x data codewords. Source A + B (see above).
var ecBlocksM = [maxVersion + 1]ecBlockLayout{
	{}, // version 0 is not a thing
	{ecPerBlock: 10, group1Blocks: 1, group1Data: 16, group2Blocks: 0, group2Data: 0},    // v1: 16 data codewords, 14 byte-mode chars
	{ecPerBlock: 16, group1Blocks: 1, group1Data: 28, group2Blocks: 0, group2Data: 0},    // v2: 28 data codewords, 26 byte-mode chars
	{ecPerBlock: 26, group1Blocks: 1, group1Data: 44, group2Blocks: 0, group2Data: 0},    // v3: 44 data codewords, 42 byte-mode chars
	{ecPerBlock: 18, group1Blocks: 2, group1Data: 32, group2Blocks: 0, group2Data: 0},    // v4: 64 data codewords, 62 byte-mode chars
	{ecPerBlock: 24, group1Blocks: 2, group1Data: 43, group2Blocks: 0, group2Data: 0},    // v5: 86 data codewords, 84 byte-mode chars
	{ecPerBlock: 16, group1Blocks: 4, group1Data: 27, group2Blocks: 0, group2Data: 0},    // v6: 108 data codewords, 106 byte-mode chars
	{ecPerBlock: 18, group1Blocks: 4, group1Data: 31, group2Blocks: 0, group2Data: 0},    // v7: 124 data codewords, 122 byte-mode chars
	{ecPerBlock: 22, group1Blocks: 2, group1Data: 38, group2Blocks: 2, group2Data: 39},   // v8: 154 data codewords, 152 byte-mode chars
	{ecPerBlock: 22, group1Blocks: 3, group1Data: 36, group2Blocks: 2, group2Data: 37},   // v9: 182 data codewords, 180 byte-mode chars
	{ecPerBlock: 26, group1Blocks: 4, group1Data: 43, group2Blocks: 1, group2Data: 44},   // v10: 216 data codewords, 213 byte-mode chars
	{ecPerBlock: 30, group1Blocks: 1, group1Data: 50, group2Blocks: 4, group2Data: 51},   // v11: 254 data codewords, 251 byte-mode chars
	{ecPerBlock: 22, group1Blocks: 6, group1Data: 36, group2Blocks: 2, group2Data: 37},   // v12: 290 data codewords, 287 byte-mode chars
	{ecPerBlock: 22, group1Blocks: 8, group1Data: 37, group2Blocks: 1, group2Data: 38},   // v13: 334 data codewords, 331 byte-mode chars
	{ecPerBlock: 24, group1Blocks: 4, group1Data: 40, group2Blocks: 5, group2Data: 41},   // v14: 365 data codewords, 362 byte-mode chars
	{ecPerBlock: 24, group1Blocks: 5, group1Data: 41, group2Blocks: 5, group2Data: 42},   // v15: 415 data codewords, 412 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 7, group1Data: 45, group2Blocks: 3, group2Data: 46},   // v16: 453 data codewords, 450 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 10, group1Data: 46, group2Blocks: 1, group2Data: 47},  // v17: 507 data codewords, 504 byte-mode chars
	{ecPerBlock: 26, group1Blocks: 9, group1Data: 43, group2Blocks: 4, group2Data: 44},   // v18: 563 data codewords, 560 byte-mode chars
	{ecPerBlock: 26, group1Blocks: 3, group1Data: 44, group2Blocks: 11, group2Data: 45},  // v19: 627 data codewords, 624 byte-mode chars
	{ecPerBlock: 26, group1Blocks: 3, group1Data: 41, group2Blocks: 13, group2Data: 42},  // v20: 669 data codewords, 666 byte-mode chars
	{ecPerBlock: 26, group1Blocks: 17, group1Data: 42, group2Blocks: 0, group2Data: 0},   // v21: 714 data codewords, 711 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 17, group1Data: 46, group2Blocks: 0, group2Data: 0},   // v22: 782 data codewords, 779 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 4, group1Data: 47, group2Blocks: 14, group2Data: 48},  // v23: 860 data codewords, 857 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 6, group1Data: 45, group2Blocks: 14, group2Data: 46},  // v24: 914 data codewords, 911 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 8, group1Data: 47, group2Blocks: 13, group2Data: 48},  // v25: 1000 data codewords, 997 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 19, group1Data: 46, group2Blocks: 4, group2Data: 47},  // v26: 1062 data codewords, 1059 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 22, group1Data: 45, group2Blocks: 3, group2Data: 46},  // v27: 1128 data codewords, 1125 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 3, group1Data: 45, group2Blocks: 23, group2Data: 46},  // v28: 1193 data codewords, 1190 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 21, group1Data: 45, group2Blocks: 7, group2Data: 46},  // v29: 1267 data codewords, 1264 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 19, group1Data: 47, group2Blocks: 10, group2Data: 48}, // v30: 1373 data codewords, 1370 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 2, group1Data: 46, group2Blocks: 29, group2Data: 47},  // v31: 1455 data codewords, 1452 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 10, group1Data: 46, group2Blocks: 23, group2Data: 47}, // v32: 1541 data codewords, 1538 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 14, group1Data: 46, group2Blocks: 21, group2Data: 47}, // v33: 1631 data codewords, 1628 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 14, group1Data: 46, group2Blocks: 23, group2Data: 47}, // v34: 1725 data codewords, 1722 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 12, group1Data: 47, group2Blocks: 26, group2Data: 48}, // v35: 1812 data codewords, 1809 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 6, group1Data: 47, group2Blocks: 34, group2Data: 48},  // v36: 1914 data codewords, 1911 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 29, group1Data: 46, group2Blocks: 14, group2Data: 47}, // v37: 1992 data codewords, 1989 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 13, group1Data: 46, group2Blocks: 32, group2Data: 47}, // v38: 2102 data codewords, 2099 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 40, group1Data: 47, group2Blocks: 7, group2Data: 48},  // v39: 2216 data codewords, 2213 byte-mode chars
	{ecPerBlock: 28, group1Blocks: 18, group1Data: 47, group2Blocks: 31, group2Data: 48}, // v40: 2334 data codewords, 2331 byte-mode chars
}

// alignmentPatternCenters gives the row/column coordinates of the alignment
// pattern centres for each version; a pattern is drawn at every (x, y)
// combination except the three that would collide with a finder pattern.
// Version 1 has none. Source A + B (see above).
var alignmentPatternCenters = [maxVersion + 1][]int{
	nil,                            // version 0 is not a thing
	nil,                            // v1 has no alignment patterns
	{6, 18},                        // v2
	{6, 22},                        // v3
	{6, 26},                        // v4
	{6, 30},                        // v5
	{6, 34},                        // v6
	{6, 22, 38},                    // v7
	{6, 24, 42},                    // v8
	{6, 26, 46},                    // v9
	{6, 28, 50},                    // v10
	{6, 30, 54},                    // v11
	{6, 32, 58},                    // v12
	{6, 34, 62},                    // v13
	{6, 26, 46, 66},                // v14
	{6, 26, 48, 70},                // v15
	{6, 26, 50, 74},                // v16
	{6, 30, 54, 78},                // v17
	{6, 30, 56, 82},                // v18
	{6, 30, 58, 86},                // v19
	{6, 34, 62, 90},                // v20
	{6, 28, 50, 72, 94},            // v21
	{6, 26, 50, 74, 98},            // v22
	{6, 30, 54, 78, 102},           // v23
	{6, 28, 54, 80, 106},           // v24
	{6, 32, 58, 84, 110},           // v25
	{6, 30, 58, 86, 114},           // v26
	{6, 34, 62, 90, 118},           // v27
	{6, 26, 50, 74, 98, 122},       // v28
	{6, 30, 54, 78, 102, 126},      // v29
	{6, 26, 52, 78, 104, 130},      // v30
	{6, 30, 56, 82, 108, 134},      // v31
	{6, 34, 60, 86, 112, 138},      // v32
	{6, 30, 58, 86, 114, 142},      // v33
	{6, 34, 62, 90, 118, 146},      // v34
	{6, 30, 54, 78, 102, 126, 150}, // v35
	{6, 24, 50, 76, 102, 128, 154}, // v36
	{6, 28, 54, 80, 106, 132, 158}, // v37
	{6, 32, 58, 84, 110, 136, 162}, // v38
	{6, 26, 54, 82, 110, 138, 166}, // v39
	{6, 30, 58, 86, 114, 142, 170}, // v40
}

// totalCodewordsByVersion is the total number of 8-bit codewords (data plus
// error correction) a symbol of each version holds, i.e. the number of data
// modules divided by 8, discarding the 0-7 remainder bits. Derived from
// source B's _get_num_raw_data_modules and confirmed against source A
// (data codewords + ecPerBlock * blocks) for all 40 versions.
var totalCodewordsByVersion = [maxVersion + 1]int{
	0,    // version 0 is not a thing
	26,   // v1 (208 data modules)
	44,   // v2 (359 data modules)
	70,   // v3 (567 data modules)
	100,  // v4 (807 data modules)
	134,  // v5 (1079 data modules)
	172,  // v6 (1383 data modules)
	196,  // v7 (1568 data modules)
	242,  // v8 (1936 data modules)
	292,  // v9 (2336 data modules)
	346,  // v10 (2768 data modules)
	404,  // v11 (3232 data modules)
	466,  // v12 (3728 data modules)
	532,  // v13 (4256 data modules)
	581,  // v14 (4651 data modules)
	655,  // v15 (5243 data modules)
	733,  // v16 (5867 data modules)
	815,  // v17 (6523 data modules)
	901,  // v18 (7211 data modules)
	991,  // v19 (7931 data modules)
	1085, // v20 (8683 data modules)
	1156, // v21 (9252 data modules)
	1258, // v22 (10068 data modules)
	1364, // v23 (10916 data modules)
	1474, // v24 (11796 data modules)
	1588, // v25 (12708 data modules)
	1706, // v26 (13652 data modules)
	1828, // v27 (14628 data modules)
	1921, // v28 (15371 data modules)
	2051, // v29 (16411 data modules)
	2185, // v30 (17483 data modules)
	2323, // v31 (18587 data modules)
	2465, // v32 (19723 data modules)
	2611, // v33 (20891 data modules)
	2761, // v34 (22091 data modules)
	2876, // v35 (23008 data modules)
	3034, // v36 (24272 data modules)
	3196, // v37 (25568 data modules)
	3362, // v38 (26896 data modules)
	3532, // v39 (28256 data modules)
	3706, // v40 (29648 data modules)
}
