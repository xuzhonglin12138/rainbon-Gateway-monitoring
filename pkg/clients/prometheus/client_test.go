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
