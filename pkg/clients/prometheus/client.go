package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Config struct {
	BaseURL string
	Timeout time.Duration
}

type Client struct {
	baseURL string
	http    *http.Client
}

type Sample struct {
	Metric map[string]string
	Value  float64
}

func NewClient(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 3 * time.Second
	}
	return &Client{
		baseURL: cfg.BaseURL,
		http: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

func (c *Client) QueryInstant(ctx context.Context, query string) ([]Sample, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("prometheus base URL is required")
	}
	endpoint, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse prometheus base URL: %w", err)
	}
	endpoint.Path = stringsTrimRight(endpoint.Path, "/") + "/api/v1/query"
	values := endpoint.Query()
	values.Set("query", query)
	endpoint.RawQuery = values.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var payload queryResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Status != "success" {
		return nil, fmt.Errorf("prometheus query failed: %s", payload.Error)
	}
	result := make([]Sample, 0, len(payload.Data.Result))
	for _, item := range payload.Data.Result {
		value, err := item.floatValue()
		if err != nil {
			return nil, err
		}
		result = append(result, Sample{Metric: item.Metric, Value: value})
	}
	return result, nil
}

func (c *Client) QueryScalar(ctx context.Context, query string) (float64, error) {
	samples, err := c.QueryInstant(ctx, query)
	if err != nil || len(samples) == 0 {
		return 0, err
	}
	return samples[0].Value, nil
}

type queryResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []queryResult `json:"result"`
	} `json:"data"`
}

type queryResult struct {
	Metric map[string]string `json:"metric"`
	Value  []interface{}     `json:"value"`
}

func (r queryResult) floatValue() (float64, error) {
	if len(r.Value) < 2 {
		return 0, fmt.Errorf("prometheus result has no value")
	}
	switch value := r.Value[1].(type) {
	case string:
		return strconv.ParseFloat(value, 64)
	case float64:
		return value, nil
	default:
		return 0, fmt.Errorf("unsupported prometheus value type %T", value)
	}
}

func stringsTrimRight(value, suffix string) string {
	for len(value) >= len(suffix) && suffix != "" && value[len(value)-len(suffix):] == suffix {
		value = value[:len(value)-len(suffix)]
	}
	return value
}
