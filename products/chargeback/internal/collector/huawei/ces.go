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

// Datapoint is one hourly CES average.
type Datapoint struct {
	Average   float64 `json:"average"`
	Timestamp int64   `json:"timestamp"` // epoch milliseconds, start of the period
	Unit      string  `json:"unit"`
}

// CPUUtilHourly fetches GET ces /V1.0/{pid}/metric-data?namespace=SYS.ECS&
// metric_name=cpu_util&dim.0=instance_id,<id>&from&to&period=3600&filter=average.
func (c *Client) CPUUtilHourly(ctx context.Context, creds Credentials, region, instanceID string, from, to time.Time) ([]Datapoint, error) {
	q := url.Values{
		"namespace":   {"SYS.ECS"},
		"metric_name": {"cpu_util"},
		"dim.0":       {"instance_id," + instanceID},
		"from":        {strconv.FormatInt(from.UnixMilli(), 10)},
		"to":          {strconv.FormatInt(to.UnixMilli(), 10)},
		"period":      {"3600"},
		"filter":      {"average"},
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
