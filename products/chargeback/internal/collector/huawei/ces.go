package huawei

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

// SKUCPUUtil is the informational (never rated) utilisation SKU.
const SKUCPUUtil = "ecs.cpu_util"

// UnitCPUUtil is its unit: the hourly average CPU utilisation in percent.
const UnitCPUUtil = "pct-hour-avg"

// The Elastic-IP traffic meter (#6867).
//
// An address billed by TRAFFIC reserves no pipe: what it costs is the bytes
// that left through it. That is a real meter and it is written per hour like
// any other, so it can be priced and rated exactly like a reserved size.
//
// The same measurement on a RESERVATION-billed address is not a meter — the
// cloud charges nothing for it — so it is written under a separate,
// never-rated SKU, the way ecs.cpu_util is. Keeping them apart is what makes
// "an address bills either its reservation or its traffic, never both" hold
// even after the operator adds a rate for the traffic meter: the rate can
// only ever reach addresses the cloud really bills by traffic.
const (
	// SKUEIPTraffic is the BILLABLE outbound-traffic meter of a
	// traffic-billed address (or shared pipe).
	SKUEIPTraffic = "eip.traffic_gb"
	// UnitEIPTraffic is its unit: gigabytes out in the hour.
	UnitEIPTraffic = "gb"
	// SKUEIPTrafficObserved is the SAME measurement on a reservation-billed
	// address: a metric, never a meter, never rated. The oversized-
	// reservation recommendation reads it to compare what was reserved
	// against what was actually used.
	SKUEIPTrafficObserved = "eip.traffic_gb.observed"
	// UnitEIPTrafficObserved names it as a metric so no aggregate can mix
	// the two.
	UnitEIPTrafficObserved = "gb-hour-out"
)

// CES metric identity for the traffic meter, stated once so the code says
// exactly what it reads and in which direction.
const (
	// cesVPCNamespace is where the cloud monitoring service publishes
	// bandwidth metrics.
	cesVPCNamespace = "SYS.VPC"
	// MetricOutboundTraffic is Huawei's "Outbound Traffic" on a bandwidth:
	// the number of BYTES that left through the pipe in the period.
	// OUTBOUND is the billed direction — inbound (down_stream) is free —
	// so it is the only one read here.
	MetricOutboundTraffic = "up_stream"
	// cesPeriod is the aggregation period asked of CES: one hour, so one
	// datapoint is one billing hour.
	cesPeriod = 3600
	// cesRawInterval is the interval SYS.VPC publishes raw points at
	// (1 minute), so one hourly period aggregates cesPeriod/cesRawInterval
	// of them.
	cesRawInterval = 60
	// bytesPerGB — network traffic is sold per decimal gigabyte (10^9
	// bytes), not per GiB. Using 2^30 here would under-report every hour by
	// about 7 %.
	bytesPerGB = 1e9
)

// Datapoint is one hourly CES aggregate. Which field is populated depends on
// the `filter` the call asked for: `average` for a mean (cpu_util),
// `sum` for a total over the period (up_stream).
type Datapoint struct {
	Average   float64 `json:"average"`
	Sum       float64 `json:"sum"`
	Timestamp int64   `json:"timestamp"` // epoch milliseconds, start of the period
	Unit      string  `json:"unit"`
}

// CPUUtilHourly fetches GET ces /V1.0/{pid}/metric-data?namespace=SYS.ECS&
// metric_name=cpu_util&dim.0=instance_id,<id>&from&to&period=3600&filter=average.
func (c *Client) CPUUtilHourly(ctx context.Context, creds Credentials, region, instanceID string, from, to time.Time) ([]Datapoint, error) {
	return c.metricData(ctx, creds, region, "SYS.ECS", "cpu_util", "instance_id,"+instanceID, "average", from, to)
}

// OutboundTrafficHourly fetches the hourly OUTBOUND traffic of one bandwidth:
// GET ces /V1.0/{pid}/metric-data?namespace=SYS.VPC&metric_name=up_stream&
// dim.0=bandwidth_id,<id>&from&to&period=3600&filter=sum.
//
// The dimension is the BANDWIDTH, not the address, because that is the
// object the cloud meters and bills: a dedicated pipe is exactly one
// address, and a shared pipe is metered once for all of them — which is the
// same "count the pipe once" rule the reservation follows.
func (c *Client) OutboundTrafficHourly(ctx context.Context, creds Credentials, region, bandwidthID string, from, to time.Time) ([]Datapoint, error) {
	return c.metricData(ctx, creds, region, cesVPCNamespace, MetricOutboundTraffic, "bandwidth_id,"+bandwidthID, "sum", from, to)
}

func (c *Client) metricData(ctx context.Context, creds Credentials, region, namespace, metric, dim, filter string, from, to time.Time) ([]Datapoint, error) {
	q := url.Values{
		"namespace":   {namespace},
		"metric_name": {metric},
		"dim.0":       {dim},
		"from":        {strconv.FormatInt(from.UnixMilli(), 10)},
		"to":          {strconv.FormatInt(to.UnixMilli(), 10)},
		"period":      {strconv.Itoa(cesPeriod)},
		"filter":      {filter},
	}
	var resp struct {
		Datapoints []Datapoint `json:"datapoints"`
		MetricName string      `json:"metric_name"`
	}
	if err := c.Get(ctx, creds, "ces", region, "/V1.0/"+creds.ProjectID+"/metric-data", q, &resp); err != nil {
		return nil, err
	}
	return resp.Datapoints, nil
}

// TrafficGB converts one hourly SYS.VPC up_stream datapoint into the
// gigabytes that hour carried outbound.
//
// up_stream is NOT a cumulative counter: each raw point is the number of
// bytes that went out during its own one-minute interval, so the hour's
// total is the SUM of the raw points inside it and no delta between
// successive readings is taken. `filter=sum` asks CES for exactly that, and
// the answer is divided by 10^9.
//
// A gateway that answers with only an `average` reports the MEAN of those
// per-minute byte totals, and the hour is then that mean multiplied by the
// number of raw intervals in the period (60). The conversion is spelled out
// rather than left implicit because reading a mean as a total under-reports
// the hour by a factor of 60 — a silent under-bill on the one meter that is
// charged per gigabyte.
func TrafficGB(p Datapoint) float64 {
	bytes := p.Sum
	if bytes == 0 && p.Average > 0 {
		bytes = p.Average * (cesPeriod / cesRawInterval)
	}
	if bytes <= 0 {
		return 0
	}
	return bytes / bytesPerGB
}
