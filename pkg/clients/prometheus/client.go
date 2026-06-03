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

type Point struct {
	Timestamp int64
	Value     float64
}

type RangeSample struct {
	Metric map[string]string
	Values []Point
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

func (c *Client) QueryRange(ctx context.Context, query string, start, end int64, stepSeconds int) ([]RangeSample, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("prometheus base URL is required")
	}
	endpoint, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse prometheus base URL: %w", err)
	}
	endpoint.Path = stringsTrimRight(endpoint.Path, "/") + "/api/v1/query_range"
	values := endpoint.Query()
	values.Set("query", query)
	values.Set("start", strconv.FormatInt(start, 10))
	values.Set("end", strconv.FormatInt(end, 10))
	values.Set("step", strconv.Itoa(stepSeconds))
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

	var payload rangeQueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Status != "success" {
		return nil, fmt.Errorf("prometheus range query failed: %s", payload.Error)
	}
	result := make([]RangeSample, 0, len(payload.Data.Result))
	for _, item := range payload.Data.Result {
		points, err := item.points()
		if err != nil {
			return nil, err
		}
		result = append(result, RangeSample{Metric: item.Metric, Values: points})
	}
	return result, nil
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

type rangeQueryResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []rangeQueryResult `json:"result"`
	} `json:"data"`
}

type rangeQueryResult struct {
	Metric map[string]string `json:"metric"`
	Values [][]interface{}   `json:"values"`
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

func (r rangeQueryResult) points() ([]Point, error) {
	points := make([]Point, 0, len(r.Values))
	for _, raw := range r.Values {
		if len(raw) < 2 {
			return nil, fmt.Errorf("prometheus range result has no value")
		}
		timestamp, err := floatFromPromValue(raw[0])
		if err != nil {
			return nil, err
		}
		value, err := floatFromPromValue(raw[1])
		if err != nil {
			return nil, err
		}
		points = append(points, Point{Timestamp: int64(timestamp), Value: value})
	}
	return points, nil
}

func floatFromPromValue(value interface{}) (float64, error) {
	switch typed := value.(type) {
	case string:
		return strconv.ParseFloat(typed, 64)
	case float64:
		return typed, nil
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
