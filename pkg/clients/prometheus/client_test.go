package prometheus

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClientQueryInstantParsesVectorValues(t *testing.T) {
	client := NewClient(Config{BaseURL: "http://prometheus.example", Timeout: time.Second})
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/query" {
			t.Fatalf("path = %q; want /api/v1/query", r.URL.Path)
		}
		if r.URL.Query().Get("query") != "sum(apisix_http_status)" {
			t.Fatalf("query = %q", r.URL.Query().Get("query"))
		}
		return jsonResponse(`{
			"status":"success",
			"data":{
				"resultType":"vector",
				"result":[
					{"metric":{"code":"200"},"value":[1710000000.0,"42"]}
				]
			}
		}`), nil
	})

	results, err := client.QueryInstant(context.Background(), "sum(apisix_http_status)")
	if err != nil {
		t.Fatalf("QueryInstant() unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results length = %d; want 1", len(results))
	}
	if results[0].Value != 42 {
		t.Fatalf("value = %v; want 42", results[0].Value)
	}
	if results[0].Metric["code"] != "200" {
		t.Fatalf("metric code = %q; want 200", results[0].Metric["code"])
	}
}

func TestClientQueryInstantReturnsPrometheusErrors(t *testing.T) {
	client := NewClient(Config{BaseURL: "http://prometheus.example", Timeout: time.Second})
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{"status":"error","errorType":"bad_data","error":"parse error"}`), nil
	})
	_, err := client.QueryInstant(context.Background(), "bad")
	if err == nil {
		t.Fatal("QueryInstant() expected error")
	}
}

func TestClientQueryRangeParsesMatrixValues(t *testing.T) {
	client := NewClient(Config{BaseURL: "http://prometheus.example", Timeout: time.Second})
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/query_range" {
			t.Fatalf("path = %q; want /api/v1/query_range", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("query") != "sum(rate(apisix_http_status[1m]))" {
			t.Fatalf("query = %q", query.Get("query"))
		}
		if query.Get("start") != "1710000000" || query.Get("end") != "1710000300" || query.Get("step") != "30" {
			t.Fatalf("range params = start:%s end:%s step:%s", query.Get("start"), query.Get("end"), query.Get("step"))
		}
		return jsonResponse(`{
			"status":"success",
			"data":{
				"resultType":"matrix",
				"result":[
					{"metric":{"job":"apisix"},"values":[[1710000000.0,"2"],[1710000030.0,"3"]]}
				]
			}
		}`), nil
	})

	results, err := client.QueryRange(context.Background(), "sum(rate(apisix_http_status[1m]))", 1710000000, 1710000300, 30)
	if err != nil {
		t.Fatalf("QueryRange() unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results length = %d; want 1", len(results))
	}
	if results[0].Metric["job"] != "apisix" {
		t.Fatalf("metric job = %q; want apisix", results[0].Metric["job"])
	}
	if len(results[0].Values) != 2 {
		t.Fatalf("points length = %d; want 2", len(results[0].Values))
	}
	if results[0].Values[1].Timestamp != 1710000030 || results[0].Values[1].Value != 3 {
		t.Fatalf("second point = %#v", results[0].Values[1])
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
