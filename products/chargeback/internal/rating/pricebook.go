package rating

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// PriceBookCSVTemplate is the import layout: sku,unit,annual_price,description.
// The mapping from a cloud catalogue to these SKUs is the operator's work.
const PriceBookCSVTemplate = `sku,unit,annual_price,description
ecs.s6.large.2,instance-hour,876.00,General computing 2 vCPU 4 GB (annual list)
ecs.c7.xlarge.2,instance-hour,2190.00,Compute 4 vCPU 8 GB (annual list)
evs.ssd.gb,gb-hour,1.20,SSD block storage per GB (annual list)
evs.hdd.gb,gb-hour,0.40,HDD block storage per GB (annual list)
eip,hour,43.80,Elastic IP (annual list)
eip.bandwidth_mbps,mbps-hour,87.60,EIP bandwidth per Mbps (annual list)
elb,hour,262.80,Elastic load balancer (annual list)
nat.1,hour,438.00,NAT gateway small (annual list)
obs.gb,gb-hour,0.20,Object storage per GB (annual list)
`

// ImportError is one rejected CSV row.
type ImportError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// ParsePriceBookCSV reads sku,unit,annual_price[,description][,unit_price]
// rows and derives unit_price = annual_price / divisor. A row may give
// unit_price directly (annual_price empty). Header names are matched
// case-insensitively in any column order.
func ParsePriceBookCSV(r io.Reader, divisor int) ([]store.PriceItem, []ImportError, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("read header: %w", err)
	}
	idx := map[string]int{}
	for i, h := range header {
		idx[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF")))] = i
	}
	for _, need := range []string{"sku", "unit"} {
		if _, ok := idx[need]; !ok {
			return nil, nil, fmt.Errorf("missing column %q", need)
		}
	}
	if _, ok := idx["annual_price"]; !ok {
		if _, ok2 := idx["unit_price"]; !ok2 {
			return nil, nil, fmt.Errorf("missing column annual_price (or unit_price)")
		}
	}
	get := func(rec []string, name string) string {
		i, ok := idx[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}
	var items []store.PriceItem
	var errs []ImportError
	seen := map[string]bool{}
	line := 1
	for {
		rec, err := cr.Read()
		line++
		if err == io.EOF {
			break
		}
		if err != nil {
			errs = append(errs, ImportError{Line: line, Message: err.Error()})
			continue
		}
		if len(rec) == 0 || (len(rec) == 1 && strings.TrimSpace(rec[0]) == "") {
			continue
		}
		sku := get(rec, "sku")
		unit := get(rec, "unit")
		if sku == "" || unit == "" {
			errs = append(errs, ImportError{Line: line, Message: "sku and unit are required"})
			continue
		}
		if seen[sku] {
			errs = append(errs, ImportError{Line: line, Message: "duplicate sku " + sku})
			continue
		}
		item := store.PriceItem{SKU: sku, Unit: unit, Description: get(rec, "description")}
		annual := get(rec, "annual_price")
		direct := get(rec, "unit_price")
		switch {
		case annual != "":
			up, err := UnitPrice(annual, divisor)
			if err != nil {
				errs = append(errs, ImportError{Line: line, Message: "annual_price: " + err.Error()})
				continue
			}
			a := store.Decimal(annual)
			item.AnnualPrice = &a
			item.UnitPrice = up
		case direct != "":
			if _, err := parseRat(direct); err != nil {
				errs = append(errs, ImportError{Line: line, Message: "unit_price: " + err.Error()})
				continue
			}
			item.UnitPrice = store.Decimal(direct)
		default:
			errs = append(errs, ImportError{Line: line, Message: "annual_price or unit_price is required"})
			continue
		}
		seen[sku] = true
		items = append(items, item)
	}
	return items, errs, nil
}
