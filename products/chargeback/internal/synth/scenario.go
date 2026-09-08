package synth

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// The showcase (founder direction 2026-09-08). Six customers who were on the
// platform from June to August 2026 and are all gone by 1 September: three
// buying National Cloud resources, three Organizations of this Sovereign on
// catalog plans. The story dates below are fixed; the scenario window clips
// what is generated, it does not move the story.

// DefaultSeed is the seed the command uses unless --seed is given.
const DefaultSeed = 2026

func date(y int, m time.Month, d, h int) time.Time {
	return time.Date(y, m, d, h, 0, 0, 0, time.UTC)
}

// DefaultScenario builds the founder's showcase for the window and seed.
func DefaultScenario(w Window, seed uint64) *Scenario {
	s := &Scenario{Window: w, Seed: seed, CloudBookName: CloudBookName, PlanBookName: PlanBookName}
	s.GlobalDiscounts = []Discount{{
		Name: NamePrefix + "launch campaign 5%", Kind: "percent", Value: 5,
		StartsAt: date(2026, 6, 1, 0), EndsAt: date(2026, 7, 1, 0),
	}}
	s.Customers = []*Customer{
		gulfRetail(seed),
		muscatHealth(seed),
		dhofarLogistics(seed),
		nizwaFintech(seed),
		soharPorts(seed),
		salalahTourism(seed),
	}
	return s
}

// ---------------------------------------------------------------------------
// resource builders
// ---------------------------------------------------------------------------

type builder struct {
	seed uint64
	slug string
	left time.Time
}

// id derives a Huawei-looking identifier: <prefix>-<8 hex>, stable per seed.
func (b builder) id(prefix string, parts ...string) string {
	return fmt.Sprintf("%s-%08x", prefix, uint32(Hash64(b.seed, b.slug, prefix, strings.Join(parts, "/"))))
}

func (b builder) ip(name string) string {
	h := Hash64(b.seed, b.slug, "ip", name)
	return fmt.Sprintf("185.203.%d.%d", 16+h%64, 1+(h>>8)%250)
}

func azOf(region string, idx int) string { return fmt.Sprintf("%s-az%d", region, 1+idx%2) }

// ecs is one Elastic Cloud Server; active decides whether it runs in hour t
// (nil = always while alive).
func (b builder) ecs(name, flavor string, vcpus, ramMB, idx int, from, to time.Time, active func(t time.Time) bool) ResourceSpec {
	region := RegionA
	if idx%2 == 1 {
		region = RegionB
	}
	az := azOf(region, idx/2)
	sku := "ecs." + flavor
	return ResourceSpec{
		ID: b.id("ecs", name), Kind: "ecs", Name: name, Region: region, From: from, To: to,
		Attrs:  map[string]any{"flavor": flavor, "vcpus": vcpus, "ram_mb": ramMB, "status": "ACTIVE", "az": az},
		Labels: map[string]string{"flavor": flavor, "az": az, "status": "ACTIVE"},
		Meter: func(t time.Time, _ Jitter) []Line {
			if active != nil && !active(t) {
				return nil
			}
			return []Line{{SKU: sku, Unit: "instance-hour", Quantity: 1}}
		},
	}
}

// evs is one SSD volume whose size (GB, whole numbers) may change over time;
// present decides whether it exists in hour t (nil = always while alive).
func (b builder) evs(name, attachedTo string, from, to time.Time, sizeGB func(t time.Time) float64, present func(t time.Time) bool) ResourceSpec {
	end := to
	if end.IsZero() {
		end = b.left
	}
	return ResourceSpec{
		ID: b.id("evs", name), Kind: "evs", Name: name, Region: RegionA, From: from, To: to,
		Attrs:  map[string]any{"size_gb": int(math.Round(sizeGB(end.Add(-time.Hour)))), "volume_type": "SSD", "status": "in-use", "attached_to": attachedTo},
		Labels: map[string]string{"volume_type": "SSD", "attached_to": attachedTo},
		Meter: func(t time.Time, _ Jitter) []Line {
			if present != nil && !present(t) {
				return nil
			}
			return []Line{{SKU: "evs.ssd.gb", Unit: "gb-hour", Quantity: math.Round(sizeGB(t))}}
		},
	}
}

// eip is one Elastic IP of the given bandwidth whose metered bandwidth
// follows traffic(t, j) ∈ [0, ∞) as a share of the provisioned size.
func (b builder) eip(name string, mbps float64, traffic func(t time.Time, j Jitter) float64) ResourceSpec {
	return ResourceSpec{
		ID: b.id("eip", name), Kind: "eip", Name: name, Region: RegionA,
		Attrs:  map[string]any{"bandwidth_mbps": mbps, "public_ip_address": b.ip(name), "type": "5_bgp", "status": "ACTIVE"},
		Labels: map[string]string{"bandwidth_mbps": fmt.Sprint(mbps)},
		Meter: func(t time.Time, j Jitter) []Line {
			return []Line{
				{SKU: "eip", Unit: "hour", Quantity: 1},
				{SKU: "eip.bandwidth_mbps", Unit: "mbps-hour", Quantity: mbps * traffic(t, j)},
			}
		},
	}
}

// fixed is a resource billed one unit per hour: ELB, NAT gateway, VPC.
func (b builder) fixed(kind, name, sku string, attrs map[string]any) ResourceSpec {
	a := map[string]any{"status": "ACTIVE"}
	for k, v := range attrs {
		a[k] = v
	}
	return ResourceSpec{
		ID: b.id(kind, name), Kind: kind, Name: name, Region: RegionA, Attrs: a,
		Meter: func(time.Time, Jitter) []Line {
			return []Line{{SKU: sku, Unit: "hour", Quantity: 1}}
		},
	}
}

// podSuffix draws a Kubernetes-looking name suffix from the hash.
func podSuffix(h uint64, n int) string {
	const alphabet = "bcdfghjklmnpqrstvwxz2456789"
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteByte(alphabet[h%uint64(len(alphabet))])
		h /= uint64(len(alphabet))
	}
	return sb.String()
}

// pod is replica idx of a workload: <deploy>-<replicaset>-<5 chars>, metered
// by its CPU and memory requests while running(t).
func (b builder) pod(ns, deploy string, idx int, cpu, memGiB float64, running func(t time.Time) bool) ResourceSpec {
	rs := podSuffix(Hash64(b.seed, ns, deploy, "rs"), 10)
	name := fmt.Sprintf("%s-%s-%s", deploy, rs, podSuffix(Hash64(b.seed, ns, deploy, fmt.Sprint(idx)), 5))
	return ResourceSpec{
		ID: "pod/" + ns + "/" + name, Kind: "k8s-pod", Name: name, Region: "",
		Attrs:  map[string]any{"namespace": ns, "workload": deploy, "cpu_request": cpu, "memory_request_gib": memGiB, "status": "Running"},
		Labels: map[string]string{"name": ns + "/" + name, "namespace": ns, "kind": "pod"},
		Meter: func(t time.Time, j Jitter) []Line {
			if running != nil && !running(t) {
				return nil
			}
			return []Line{
				{SKU: "k8s.vcpu", Unit: "vcpu-hour", Quantity: cpu * j(0.05)},
				{SKU: "k8s.mem_gb", Unit: "gib-hour", Quantity: memGiB * j(0.05)},
			}
		},
	}
}

// pvc is one persistent volume claim of sizeGB(t) gigabytes while present(t).
func (b builder) pvc(ns, name string, sizeGB func(t time.Time) float64, present func(t time.Time) bool) ResourceSpec {
	return ResourceSpec{
		ID: "pvc/" + ns + "/" + name, Kind: "k8s-pvc", Name: name, Region: "",
		Attrs:  map[string]any{"namespace": ns, "storage_class": "cnpg-ssd", "status": "Bound"},
		Labels: map[string]string{"name": ns + "/" + name, "namespace": ns, "kind": "pvc"},
		Meter: func(t time.Time, _ Jitter) []Line {
			if present != nil && !present(t) {
				return nil
			}
			return []Line{{SKU: "k8s.pvc_gb", Unit: "gb-hour", Quantity: math.Round(sizeGB(t))}}
		},
	}
}

// plan is the catalog plan line for one slug: one plan-hour in every hour the
// slug is the plan in force. A suspended Organization still pays its plan.
func plan(slug string, switches []PlanSwitch) ResourceSpec {
	return ResourceSpec{
		ID: "plan/" + slug, Kind: "plan", Name: strings.ToUpper(slug) + " plan", Region: "",
		Attrs:  map[string]any{"plan": slug},
		Labels: map[string]string{"plan": slug},
		Meter: func(t time.Time, _ Jitter) []Line {
			if PlanAt(t, switches) != slug {
				return nil
			}
			return []Line{{SKU: "plan." + slug, Unit: "plan-hour", Quantity: 1}}
		},
	}
}

// deployment adds maxReplicas pod specs for a workload whose live replica
// count is replicas(t); replica i runs while i < replicas(t) and running(t).
func (b builder) deployment(ns, name string, cpu, memGiB float64, maxReplicas int, replicas func(t time.Time) int, running func(t time.Time) bool) []ResourceSpec {
	out := make([]ResourceSpec, 0, maxReplicas)
	for i := 0; i < maxReplicas; i++ {
		idx := i
		out = append(out, b.pod(ns, name, idx, cpu, memGiB, func(t time.Time) bool {
			if running != nil && !running(t) {
				return false
			}
			return idx < replicas(t)
		}))
	}
	return out
}

func constant(v float64) func(time.Time) float64 { return func(time.Time) float64 { return v } }

func fixedReplicas(n int) func(time.Time) int { return func(time.Time) int { return n } }

// stepped rounds v down to a multiple of step (volume resizes happen in
// whole increments, not byte by byte).
func stepped(v, step float64) float64 { return math.Floor(v/step) * step }

// ---------------------------------------------------------------------------
// cloud-layer customers (National Cloud list, billing mode real, OMR)
// ---------------------------------------------------------------------------

func cloudCustomer(slug, name, email string, joined, left time.Time, note string) *Customer {
	return &Customer{
		Slug: SlugPrefix + slug, Name: name, AdminEmail: email,
		Layer: LayerCloud, Kind: "external", BillingMode: "real", Currency: "OMR",
		Source: Source{Name: SlugPrefix + slug, Kind: SourceKindFile, Layer: LayerCloud, Region: RegionA, Book: CloudBookName},
		Joined: joined, Left: left, DecommissionNote: note,
	}
}

// Gulf Retail Group — steady e-commerce. Worker pool 6 → 10 m7n.xlarge.8 with
// the working-week rhythm, three EIPs whose bandwidth follows the daily
// traffic curve (promo 14–16 July at 180 %, the 22 August anomaly at ×3 for
// eight hours), SSD growing 2 TB → 3.5 TB, one ELB, one NAT, one VPC. Usage
// stops 31 August 23:00; the customer moved to its own tenancy.
func gulfRetail(seed uint64) *Customer {
	c := cloudCustomer("gulf-retail", "Gulf Retail Group", "billing@gulfretail.example",
		date(2026, 6, 1, 0), date(2026, 8, 31, 23), "moved to own tenancy 2026-09-01")
	b := builder{seed, c.Slug, c.Left}
	jun1, sep1 := date(2026, 6, 1, 0), date(2026, 9, 1, 0)

	growth := []Step{{date(2026, 6, 22, 0), 7}, {date(2026, 7, 6, 0), 8}, {date(2026, 7, 27, 0), 9}, {date(2026, 8, 17, 0), 10}}
	nodes := func(t time.Time) int {
		n := RoundInt(StepAt(t, 6, growth)*WeekdayFactor(t), 4)
		if n > 12 {
			n = 12
		}
		return n
	}
	for i := 0; i < 12; i++ {
		idx := i
		c.Resources = append(c.Resources, b.ecs(fmt.Sprintf("gr-worker-%02d", i+1), "m7n.xlarge.8", 4, 32768, i, time.Time{}, time.Time{},
			func(t time.Time) bool { return idx < nodes(t) }))
	}
	for i := 0; i < 4; i++ {
		c.Resources = append(c.Resources, b.evs(fmt.Sprintf("gr-data-%02d", i+1), b.id("ecs", fmt.Sprintf("gr-worker-%02d", i+1)), time.Time{}, time.Time{},
			func(t time.Time) float64 { return stepped(Linear(t, jun1, sep1, 2048, 3584)/4, 8) }, nil))
	}
	promoFrom, promoTo := date(2026, 7, 14, 0), date(2026, 7, 17, 0)
	traffic := func(t time.Time, j Jitter) float64 {
		f := DailyTraffic(t) * j(0.05)
		switch t.Weekday() {
		case time.Friday:
			f *= 0.8
		case time.Saturday:
			f *= 0.9
		}
		if InRange(t, promoFrom, promoTo) {
			f *= 1.8
		}
		if t.Year() == 2026 && t.Month() == time.August && t.Day() == 22 && HourIn(t, 12, 20) {
			f *= 3
		}
		return f
	}
	for i := 0; i < 3; i++ {
		// 12 Mbps each (36 Mbps of egress across the three). The size is what
		// keeps the month totals on the right side of the 1,800 budget: the
		// founder's own compute and storage shape already costs 1,181 → 1,746
		// OMR a month at National Cloud list, so a larger pipe would push July
		// past 100 % and destroy the 50 → 80 → 100 % escalation the showcase
		// is meant to demonstrate.
		c.Resources = append(c.Resources, b.eip(fmt.Sprintf("gr-eip-%02d", i+1), 12, traffic))
	}
	c.Resources = append(c.Resources,
		b.fixed("elb", "gr-elb-01", "elb", nil),
		b.fixed("nat", "gr-nat-01", "nat.1", map[string]any{"spec": "1"}),
		b.fixed("vpc", "gr-vpc", "vpc", map[string]any{"cidr": "10.20.0.0/16"}),
	)
	c.Budgets = []Budget{{Name: NamePrefix + "Gulf Retail monthly cap", Amount: 1800, Thresholds: []int{50, 80, 100}}}
	return c
}

// Muscat Health Systems — a migration: four m7n.2xlarge.8 until 17 July,
// replaced over 15–17 July by eight m7n.xlarge.8 (both generations run
// 15–17 July), 6 TB SSD constant, low daytime traffic. 10 % off the new
// generation's compute from 1 August. Decommissioned 1 September.
func muscatHealth(seed uint64) *Customer {
	c := cloudCustomer("muscat-health", "Muscat Health Systems", "finance@muscathealth.example",
		date(2026, 6, 1, 0), date(2026, 9, 1, 0), "decommissioned 2026-09-01 after migration to the national health cloud")
	b := builder{seed, c.Slug, c.Left}
	retire := date(2026, 7, 18, 0)
	for i := 0; i < 4; i++ {
		c.Resources = append(c.Resources, b.ecs(fmt.Sprintf("mhs-app-%02d", i+1), "m7n.2xlarge.8", 8, 65536, i, time.Time{}, retire, nil))
	}
	arrivals := []time.Time{date(2026, 7, 15, 6), date(2026, 7, 16, 6), date(2026, 7, 17, 6)}
	for i := 0; i < 8; i++ {
		from := arrivals[i/3]
		c.Resources = append(c.Resources, b.ecs(fmt.Sprintf("mhs-node-%02d", i+1), "m7n.xlarge.8", 4, 32768, i, from, time.Time{}, nil))
	}
	for i := 0; i < 3; i++ {
		c.Resources = append(c.Resources, b.evs(fmt.Sprintf("mhs-pacs-%02d", i+1), b.id("ecs", fmt.Sprintf("mhs-app-%02d", i+1)), time.Time{}, time.Time{}, constant(2048), nil))
	}
	traffic := func(t time.Time, j Jitter) float64 {
		f := OfficeTraffic(t) * j(0.05)
		switch t.Weekday() {
		case time.Friday:
			f *= 0.5
		case time.Saturday:
			f *= 0.7
		}
		return f
	}
	c.Resources = append(c.Resources,
		b.eip("mhs-eip-01", 5, traffic),
		b.fixed("elb", "mhs-elb-01", "elb", nil),
		b.fixed("nat", "mhs-nat-01", "nat.1", map[string]any{"spec": "1"}),
		b.fixed("vpc", "mhs-vpc", "vpc", map[string]any{"cidr": "10.30.0.0/16"}),
	)
	c.Discounts = []Discount{{
		Name: NamePrefix + "Muscat Health ECS 10%", Kind: "percent", Value: 10,
		SKU: "ecs.m7n.xlarge.8", StartsAt: date(2026, 8, 1, 0),
	}}
	return c
}

// Dhofar Logistics — bursty batch: two core servers plus 6–14 spot-like
// servers between 02:00 and 06:00 UTC every day, scratch volumes that come
// and go, an archive volume growing 2 → 4 TB. Joined 20 June, left 25 August.
func dhofarLogistics(seed uint64) *Customer {
	c := cloudCustomer("dhofar-logistics", "Dhofar Logistics", "accounts@dhofarlogistics.example",
		date(2026, 6, 20, 0), date(2026, 8, 25, 0), "contract ended 2026-08-25; workloads moved off the National Cloud")
	b := builder{seed, c.Slug, c.Left}
	for i := 0; i < 2; i++ {
		c.Resources = append(c.Resources, b.ecs(fmt.Sprintf("dl-core-%02d", i+1), "m7n.xlarge.8", 4, 32768, i, time.Time{}, time.Time{}, nil))
	}
	batch := func(t time.Time) int { return 6 + int(DayFraction(seed, "dl-batch", t)*9) } // 6..14
	for i := 0; i < 14; i++ {
		idx := i
		c.Resources = append(c.Resources, b.ecs(fmt.Sprintf("dl-batch-%02d", i+1), "m7n.xlarge.8", 4, 32768, i, time.Time{}, time.Time{},
			func(t time.Time) bool { return HourIn(t, 2, 6) && idx < batch(t) }))
	}
	c.Resources = append(c.Resources, b.evs("dl-archive-01", b.id("ecs", "dl-core-01"), time.Time{}, time.Time{},
		func(t time.Time) float64 { return stepped(Linear(t, c.Joined, c.Left, 2048, 4096), 64) }, nil))
	for i := 0; i < 6; i++ {
		key := fmt.Sprintf("dl-scratch-%02d", i+1)
		c.Resources = append(c.Resources, b.evs(key, b.id("ecs", "dl-core-02"), time.Time{}, time.Time{}, constant(1024),
			func(t time.Time) bool { return DayFraction(seed, key, t) < 0.6 }))
	}
	traffic := func(t time.Time, j Jitter) float64 {
		if HourIn(t, 2, 6) {
			return 1.0 * j(0.05)
		}
		return 0.25 * j(0.05)
	}
	c.Resources = append(c.Resources,
		b.eip("dl-eip-01", 10, traffic),
		b.eip("dl-eip-02", 10, traffic),
		b.fixed("nat", "dl-nat-01", "nat.1", map[string]any{"spec": "1"}),
		b.fixed("vpc", "dl-vpc", "vpc", map[string]any{"cidr": "10.40.0.0/16"}),
	)
	return c
}

// ---------------------------------------------------------------------------
// platform-layer Organizations ("OpenOva plans" book)
// ---------------------------------------------------------------------------

func platformOrg(slug, name, email string, switches []PlanSwitch, note string) *Customer {
	c := &Customer{
		Slug: SlugPrefix + slug, Name: name, AdminEmail: email,
		Layer: LayerPlatform, Kind: "organization", OrgSlug: SlugPrefix + slug,
		// chargeback, not real: an issued statement of a real-billing
		// Organization debits credits through the billing hook (ADR-0014
		// D6), and a showcase must never move real money.
		BillingMode: "chargeback", Currency: "OMR",
		Source: Source{Name: SlugPrefix + slug, Kind: SourceKindOrg, Layer: LayerPlatform, Region: "", Book: PlanBookName},
		Joined: date(2026, 6, 1, 0), Left: date(2026, 9, 1, 0),
		PlanSwitches: switches, PlanSlug: switches[len(switches)-1].Slug,
		DecommissionNote: note,
	}
	for _, sw := range switches {
		dup := false
		for _, r := range c.Resources {
			if r.ID == "plan/"+sw.Slug {
				dup = true
			}
		}
		if !dup {
			c.Resources = append(c.Resources, plan(sw.Slug, switches))
		}
	}
	return c
}

// Nizwa Fintech — growth: plan S in June, M from 1 July, L from 10 August
// 09:00; pods 12 → 40 vCPU with the working-week rhythm; PVC 200 → 900 GB.
func nizwaFintech(seed uint64) *Customer {
	c := platformOrg("nizwa-fintech", "Nizwa Fintech", "ops@nizwafintech.example",
		[]PlanSwitch{{date(2026, 6, 1, 0), "s"}, {date(2026, 7, 1, 0), "m"}, {date(2026, 8, 10, 9), "l"}},
		"Organization deleted 2026-09-01 (moved to a dedicated Sovereign)")
	b := builder{seed, c.Slug, c.Left}
	ns := c.Slug
	grow := func(r0, r1 float64, weekly bool) func(time.Time) int {
		return func(t time.Time) int {
			v := Linear(t, c.Joined, c.Left, r0, r1)
			if weekly {
				v *= WeekdayFactor(t)
			}
			return RoundInt(v, 1)
		}
	}
	c.Resources = append(c.Resources, b.deployment(ns, "payments-api", 1, 2, 13, grow(4, 12, true), nil)...)
	c.Resources = append(c.Resources, b.deployment(ns, "ledger-worker", 2, 4, 9, grow(2, 8, true), nil)...)
	c.Resources = append(c.Resources, b.deployment(ns, "web", 0.5, 1, 9, grow(2, 8, true), nil)...)
	c.Resources = append(c.Resources, b.deployment(ns, "ml-scoring", 1, 2, 6, grow(1, 5, true), nil)...)
	c.Resources = append(c.Resources, b.deployment(ns, "postgres", 2, 8, 1, fixedReplicas(1), nil)...)
	c.Resources = append(c.Resources, b.deployment(ns, "redis", 0.5, 1, 2, fixedReplicas(2), nil)...)
	c.Resources = append(c.Resources,
		b.pvc(ns, "data-postgres-0", func(t time.Time) float64 { return stepped(Linear(t, c.Joined, c.Left, 150, 650), 50) }, nil),
		b.pvc(ns, "data-redis-0", func(t time.Time) float64 { return stepped(Linear(t, c.Joined, c.Left, 50, 250), 25) }, nil),
	)
	return c
}

// Sohar Ports Analytics — plan M throughout; nightly ETL 00:00–04:00 UTC
// triples the vCPU; suspended 18–24 July (no pods, plan still billed).
func soharPorts(seed uint64) *Customer {
	c := platformOrg("sohar-ports", "Sohar Ports Analytics", "it@soharports.example",
		[]PlanSwitch{{date(2026, 6, 1, 0), "m"}},
		"Organization deleted 2026-09-01 (project closed)")
	b := builder{seed, c.Slug, c.Left}
	ns := c.Slug
	suspFrom, suspTo := date(2026, 7, 18, 0), date(2026, 7, 25, 0)
	up := func(t time.Time) bool { return !InRange(t, suspFrom, suspTo) }
	night := func(t time.Time) bool { return up(t) && HourIn(t, 0, 4) }
	c.Resources = append(c.Resources, b.deployment(ns, "api", 1, 2, 2, fixedReplicas(2), up)...)
	c.Resources = append(c.Resources, b.deployment(ns, "dashboard", 0.5, 1, 2, fixedReplicas(2), up)...)
	c.Resources = append(c.Resources, b.deployment(ns, "postgres", 2, 8, 1, fixedReplicas(1), up)...)
	c.Resources = append(c.Resources, b.deployment(ns, "etl-runner", 2, 4, 5, fixedReplicas(5), night)...)
	c.Resources = append(c.Resources,
		b.pvc(ns, "data-postgres-0", constant(300), up),
		b.pvc(ns, "data-lake", func(t time.Time) float64 { return stepped(Linear(t, c.Joined, c.Left, 500, 800), 100) }, up),
	)
	return c
}

// Salalah Tourism Board — plan XL June–July, downgraded to M on 1 August;
// steady 9 vCPU; decommissioned 1 September.
func salalahTourism(seed uint64) *Customer {
	c := platformOrg("salalah-tourism", "Salalah Tourism Board", "digital@salalahtourism.example",
		[]PlanSwitch{{date(2026, 6, 1, 0), "xl"}, {date(2026, 8, 1, 0), "m"}},
		"Organization deleted 2026-09-01 (season over)")
	b := builder{seed, c.Slug, c.Left}
	ns := c.Slug
	c.Resources = append(c.Resources, b.deployment(ns, "web", 0.5, 1, 6, fixedReplicas(6), nil)...)
	c.Resources = append(c.Resources, b.deployment(ns, "cms", 1, 2, 2, fixedReplicas(2), nil)...)
	c.Resources = append(c.Resources, b.deployment(ns, "postgres", 2, 8, 1, fixedReplicas(1), nil)...)
	c.Resources = append(c.Resources, b.deployment(ns, "search", 1, 2, 2, fixedReplicas(2), nil)...)
	c.Resources = append(c.Resources,
		b.pvc(ns, "data-postgres-0", constant(200), nil),
		b.pvc(ns, "media", constant(300), nil),
	)
	return c
}
