package synth

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"strconv"
	"time"
)

// CSVHeader is the column layout of a usage chunk the seeding command
// writes (and offers to a source import endpoint, when the product has
// one): one hourly record per row, labels as a JSON object.
var CSVHeader = []string{"resource_id", "resource_kind", "sku", "unit", "quantity", "window_start", "window_end", "region", "labels"}

// WriteCSV writes records in CSVHeader order. Output is byte-deterministic
// for equal input: quantities at 6 decimals, RFC3339 UTC times, labels with
// sorted keys (encoding/json sorts map keys).
func WriteCSV(w io.Writer, recs []Record) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(CSVHeader); err != nil {
		return err
	}
	for _, r := range recs {
		labels, err := json.Marshal(r.Labels)
		if err != nil {
			return err
		}
		row := []string{
			r.ResourceID, r.ResourceKind, r.SKU, r.Unit,
			strconv.FormatFloat(r.Quantity, 'f', 6, 64),
			r.Start.UTC().Format(time.RFC3339), r.End.UTC().Format(time.RFC3339),
			r.Region, string(labels),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// InPeriod keeps the records whose window starts in the YYYY-MM period.
func InPeriod(recs []Record, period string) []Record {
	var out []Record
	for _, r := range recs {
		if r.Start.UTC().Format("2006-01") == period {
			out = append(out, r)
		}
	}
	return out
}
