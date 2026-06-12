package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
)

func TestSLAHealthCheckerRecordsSuccessfulCheck(t *testing.T) {
	checker := NewSLAHealthChecker(nil, nil)
	checker.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       http.NoBody,
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}
	checker.now = fixedClock(time.Unix(1710000000, 0), time.Unix(1710000000, int64(25*time.Millisecond)))

	sample := checker.check(context.Background(), model.SLAConfig{
		AppID:            "app-a",
		URL:              "https://example.com/healthz",
		SuccessStatusMin: 200,
		SuccessStatusMax: 399,
	})

	if !sample.Success || sample.StatusCode != http.StatusNoContent {
		t.Fatalf("sample = %#v; want successful 204", sample)
	}
	if sample.LatencyMs != 25 {
		t.Fatalf("latency = %v; want 25", sample.LatencyMs)
	}
}

func TestSLAHealthCheckerClassifiesStatusCodeFailure(t *testing.T) {
	checker := NewSLAHealthChecker(nil, nil)
	checker.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       http.NoBody,
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}
	checker.now = fixedClock(time.Unix(1710000000, 0), time.Unix(1710000000, int64(12*time.Millisecond)))

	sample := checker.check(context.Background(), model.SLAConfig{
		AppID:            "app-a",
		URL:              "https://example.com/healthz",
		SuccessStatusMin: 200,
		SuccessStatusMax: 399,
	})

	if sample.Success || sample.ErrorType != "status_code_5xx" {
		t.Fatalf("sample = %#v; want status_code_5xx failure", sample)
	}
}

func TestValidateSLAHealthURL(t *testing.T) {
	for _, rawURL := range []string{"https://example.com/healthz", "http://127.0.0.1:8080/healthz"} {
		if err := ValidateSLAHealthURL(rawURL); err != nil {
			t.Fatalf("ValidateSLAHealthURL(%q) unexpected error: %v", rawURL, err)
		}
	}
	for _, rawURL := range []string{"", "/healthz", "ftp://example.com/healthz"} {
		if err := ValidateSLAHealthURL(rawURL); err == nil {
			t.Fatalf("ValidateSLAHealthURL(%q) = nil; want error", rawURL)
		}
	}
}

func fixedClock(values ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		if index >= len(values) {
			return values[len(values)-1]
		}
		value := values[index]
		index++
		return value
	}
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
