package model

import (
	"encoding/json"
	"testing"
)

func TestApisixAccessLogAcceptsStringNumberFields(t *testing.T) {
	var log ApisixAccessLog
	err := json.Unmarshal([]byte(`{
		"route_id":"r1",
		"status":"503",
		"request_time":"0.086",
		"upstream_status":"502",
		"upstream_response_time":"0.081"
	}`), &log)
	if err != nil {
		t.Fatalf("UnmarshalJSON() unexpected error: %v", err)
	}
	if log.Status != 503 {
		t.Fatalf("status = %d; want 503", log.Status)
	}
	if log.RequestTime != 0.086 {
		t.Fatalf("request_time = %v; want 0.086", log.RequestTime)
	}
	if log.UpstreamStatus != 502 {
		t.Fatalf("upstream_status = %d; want 502", log.UpstreamStatus)
	}
}

func TestApisixAccessLogAcceptsDefaultHTTPLoggerPayload(t *testing.T) {
	var log ApisixAccessLog
	err := json.Unmarshal([]byte(`{
		"route_id":"r1",
		"service_id":"svc-a",
		"request":{"method":"GET","uri":"/api/order/detail/123"},
		"response":{"status":503},
		"latency":86,
		"upstream_latency":81,
		"client_ip":"10.0.0.1"
	}`), &log)
	if err != nil {
		t.Fatalf("UnmarshalJSON() unexpected error: %v", err)
	}
	if log.URI != "/api/order/detail/123" {
		t.Fatalf("uri = %q; want /api/order/detail/123", log.URI)
	}
	if log.Method != "GET" {
		t.Fatalf("method = %q; want GET", log.Method)
	}
	if log.Status != 503 {
		t.Fatalf("status = %d; want 503", log.Status)
	}
	if log.RequestTime != 0.086 {
		t.Fatalf("request_time = %v; want 0.086", log.RequestTime)
	}
	if log.UpstreamResponseTime != 0.081 {
		t.Fatalf("upstream_response_time = %v; want 0.081", log.UpstreamResponseTime)
	}
}
