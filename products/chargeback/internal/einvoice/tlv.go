package einvoice

import (
	"encoding/base64"
	"fmt"
)

// TLV — the tag-length-value encoding the Gulf tax authorities specify for
// the QR code on a tax invoice, implemented GENERICALLY here and given its
// tag numbers by the profile (oman.go).
//
// The encoding itself is three rules and nothing else:
//
//	tag    one byte, the field number
//	length one byte, the BYTE length of the value (not its character count —
//	       an Arabic seller name is several bytes per character)
//	value  the value's UTF-8 bytes
//
// The fields are concatenated with no separator and the whole stream is
// base64-encoded into the QR. A value longer than 255 bytes cannot be
// expressed by a one-byte length and is an error rather than a truncation:
// a QR that decodes to a clipped seller name is worse than no QR.

// TLVField is one field of the stream.
type TLVField struct {
	Tag   byte
	Value string
}

// MaxTLVValueBytes is what a one-byte length can express.
const MaxTLVValueBytes = 255

// EncodeTLV concatenates the fields, in the order given.
func EncodeTLV(fields []TLVField) ([]byte, error) {
	out := make([]byte, 0, 64)
	for _, f := range fields {
		v := []byte(f.Value)
		if len(v) > MaxTLVValueBytes {
			return nil, fmt.Errorf("tlv tag %d: value is %d bytes, longer than the %d a one-byte length can express", f.Tag, len(v), MaxTLVValueBytes)
		}
		out = append(out, f.Tag, byte(len(v)))
		out = append(out, v...)
	}
	return out, nil
}

// EncodeTLVBase64 is EncodeTLV followed by standard base64 — what goes into
// the QR.
func EncodeTLVBase64(fields []TLVField) (string, error) {
	raw, err := EncodeTLV(fields)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// DecodeTLV reads a stream back. It exists so the encoder is testable
// against its own inverse rather than against a recorded blob, and so an
// operator debugging a scanner can be shown what their QR actually says.
func DecodeTLV(raw []byte) ([]TLVField, error) {
	var out []TLVField
	for i := 0; i < len(raw); {
		if i+2 > len(raw) {
			return nil, fmt.Errorf("tlv: truncated at byte %d, a tag and a length need two bytes", i)
		}
		tag, length := raw[i], int(raw[i+1])
		i += 2
		if i+length > len(raw) {
			return nil, fmt.Errorf("tlv: tag %d claims %d bytes but only %d remain", tag, length, len(raw)-i)
		}
		out = append(out, TLVField{Tag: tag, Value: string(raw[i : i+length])})
		i += length
	}
	return out, nil
}

// DecodeTLVBase64 is the inverse of EncodeTLVBase64.
func DecodeTLVBase64(s string) ([]TLVField, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("tlv: not base64: %w", err)
	}
	return DecodeTLV(raw)
}
