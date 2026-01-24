package influxx

import (
	"context"
	"errors"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api"

	"swarm-agv-arm-scheduling-system/shared/config"
)

type Client struct {
	client influxdb2.Client
	org    string
	bucket string
}

func New(cfg config.Config) (*Client, error) {
	if cfg.InfluxURL == "" || cfg.InfluxToken == "" || cfg.InfluxOrg == "" || cfg.InfluxBucket == "" {
		return nil, errors.New("INFLUX_URL/INFLUX_TOKEN/INFLUX_ORG/INFLUX_BUCKET are required")
	}
	opts := influxdb2.DefaultOptions().
		SetHTTPRequestTimeout(uint(cfg.InfluxTimeoutMS))
	client := influxdb2.NewClientWithOptions(cfg.InfluxURL, cfg.InfluxToken, opts)
	return &Client{client: client, org: cfg.InfluxOrg, bucket: cfg.InfluxBucket}, nil
}

func (c *Client) WritePoint(ctx context.Context, measurement string, tags map[string]string, fields map[string]any, ts time.Time) error {
	if c == nil || c.client == nil {
		return errors.New("influx client not initialized")
	}
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	p := influxdb2.NewPoint(measurement, tags, fields, ts)
	writeAPI := c.client.WriteAPIBlocking(c.org, c.bucket)
	return writeAPI.WritePoint(ctx, p)
}

func (c *Client) Query(ctx context.Context, flux string) (*api.QueryTableResult, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("influx client not initialized")
	}
	return c.client.QueryAPI(c.org).Query(ctx, flux)
}

func (c *Client) Close() {
	if c == nil || c.client == nil {
		return
	}
	c.client.Close()
}
