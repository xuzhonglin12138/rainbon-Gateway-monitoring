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
