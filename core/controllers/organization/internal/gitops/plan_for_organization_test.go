package gitops

import "testing"

func TestPlanForOrganization(t *testing.T) {
	cases := []struct{ kind, plan, want string }{
		{"internal", "", "xl"},
		{"Internal", " ", "xl"},
		{"internal", "m", "m"},
		{"customer", "", ""},
		{"", "", ""},
		{"customer", "l", "l"},
	}
	for _, c := range cases {
		if got := PlanForOrganization(c.kind, c.plan); got != c.want {
			t.Errorf("PlanForOrganization(%q,%q)=%q want %q", c.kind, c.plan, got, c.want)
		}
	}
	if planQuota(PlanForOrganization("customer", "")).Slug != "s" {
		t.Errorf("a customer with no plan must still be capped at s")
	}
	if planQuota(PlanForOrganization("internal", "")).Slug != "xl" {
		t.Errorf("an internal Organization with no plan must be sized at xl")
	}
}
