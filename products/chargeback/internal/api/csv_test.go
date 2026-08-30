package api

import (
	"strings"
	"testing"
)

func TestParseCustomerImportCSV(t *testing.T) {
	csvText := "slug,name,admin_email,region,project_ids,price_book,billing_mode,start_date\n" +
		"acme,Acme Trading,ops@acme.example,me-east-215,p1;p2,NC list 2026,chargeback,2026-08-01\n" +
		"beta-co,Beta Co,finance@beta.example,me-east-215,p3,,showback,\n" +
		"Bad Slug,Broken,x@y.example,me-east-215,p4,,,\n" +
		"gamma,Gamma,not-an-email,me-east-215,,,,\n" +
		"delta,Delta,d@d.example,,p9,,,\n" +
		"epsilon,Epsilon,e@e.example,r,p1,,weird,\n" +
		"zeta,Zeta,z@z.example,r,p1,,,31-08-2026\n" +
		"\n"
	rows, errs, err := ParseCustomerImportCSV(strings.NewReader(csvText))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d: %+v", len(rows), rows)
	}
	a := rows[0]
	if a.Slug != "acme" || a.Name != "Acme Trading" || a.AdminEmail != "ops@acme.example" || a.Region != "me-east-215" ||
		len(a.ProjectIDs) != 2 || a.ProjectIDs[1] != "p2" || a.PriceBook != "NC list 2026" || a.BillingMode != "chargeback" || a.StartDate != "2026-08-01" || a.Line != 2 {
		t.Fatalf("acme row = %+v", a)
	}
	if b := rows[1]; b.Slug != "beta-co" || len(b.ProjectIDs) != 1 || b.BillingMode != "showback" || b.StartDate != "" {
		t.Fatalf("beta row = %+v", b)
	}
	wantErrLines := []int{4, 5, 6, 7, 8}
	if len(errs) != len(wantErrLines) {
		t.Fatalf("errors = %+v", errs)
	}
	for i, e := range errs {
		if e.Line != wantErrLines[i] {
			t.Errorf("error %d at line %d (%s), want line %d", i, e.Line, e.Message, wantErrLines[i])
		}
	}
	if !strings.Contains(errs[0].Message, "slug") || !strings.Contains(errs[1].Message, "admin_email") ||
		!strings.Contains(errs[2].Message, "region") || !strings.Contains(errs[3].Message, "billing_mode") || !strings.Contains(errs[4].Message, "start_date") {
		t.Fatalf("error messages = %+v", errs)
	}
}

func TestParseCustomerImportCSVHeaderVariants(t *testing.T) {
	// Column order is free; a UTF-8 BOM and mixed-case headers are tolerated.
	csvText := "\uFEFFName,SLUG,Admin_Email\nAcme,acme,ops@acme.example\n"
	rows, errs, err := ParseCustomerImportCSV(strings.NewReader(csvText))
	if err != nil || len(errs) != 0 || len(rows) != 1 || rows[0].Slug != "acme" {
		t.Fatalf("rows=%+v errs=%+v err=%v", rows, errs, err)
	}
	if _, _, err := ParseCustomerImportCSV(strings.NewReader("slug,name\nacme,Acme\n")); err == nil {
		t.Fatal("missing admin_email column accepted")
	}
}

func TestParseCustomerImportJSON(t *testing.T) {
	body := `[{"slug":"ACME","name":"Acme","admin_email":"Ops@Acme.example","region":"r","project_ids":["p1;p2","p3"]},
	          {"slug":"xy","name":"","admin_email":"a@b.example"}]`
	rows, errs, err := ParseCustomerImportJSON(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Slug != "acme" || rows[0].AdminEmail != "ops@acme.example" || len(rows[0].ProjectIDs) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	if len(errs) != 1 || errs[0].Line != 2 || !strings.Contains(errs[0].Message, "name") {
		t.Fatalf("errs = %+v", errs)
	}
	if _, _, err := ParseCustomerImportJSON(strings.NewReader(`{"slug":"x"}`)); err == nil {
		t.Fatal("object accepted where array expected")
	}
}
