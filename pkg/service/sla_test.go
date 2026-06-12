package service

import (
	"context"
	"testing"
	"time"

	promclient "github.com/goodrain/rainbond-plugin-template/pkg/clients/prometheus"
	"github.com/goodrain/rainbond-plugin-template/pkg/model"
)

type fakePrometheusClient struct {
	values        map[string]float64
	vectors       map[string][]promclient.Sample
	ranges        map[string][]promclient.RangeSample
	queries       []string
	rangesQueries []rangeQueryCall
}

type rangeQueryCall struct {
	Query       string
	Start       int64
	End         int64
	StepSeconds int
}

func (f *fakePrometheusClient) QueryScalar(ctx context.Context, query string) (float64, error) {
	f.queries = append(f.queries, query)
	return f.values[query], nil
}

func (f *fakePrometheusClient) QueryInstant(ctx context.Context, query string) ([]promclient.Sample, error) {
	f.queries = append(f.queries, query)
	return f.vectors[query], nil
}

func (f *fakePrometheusClient) QueryRange(ctx context.Context, query string, start, end int64, stepSeconds int) ([]promclient.RangeSample, error) {
	f.queries = append(f.queries, query)
	f.rangesQueries = append(f.rangesQueries, rangeQueryCall{
		Query:       query,
		Start:       start,
		End:         end,
		StepSeconds: stepSeconds,
	})
	return f.ranges[query], nil
}

type fakeSLAStore struct {
	config    model.SLAConfig
	routes    []string
	buckets   []model.RouteGroupBucketPoint
	aggregate model.SLAHealthAggregate
	since     time.Time
	until     time.Time
}

func (f fakeSLAStore) GetSLAConfig(ctx context.Context, appID string, defaultTarget float64) (model.SLAConfig, error) {
	if f.config.AppID == "" {
		return model.SLAConfig{AppID: appID, Target: defaultTarget}, nil
	}
	return f.config, nil
}

func (f fakeSLAStore) GetAppPrometheusRoutes(ctx context.Context, appID string) ([]string, error) {
	return f.routes, nil
}

func (f fakeSLAStore) ListRouteGroupBucketPoints(ctx context.Context, scope model.AggregateScope, window model.Window) ([]model.RouteGroupBucketPoint, error) {
	return f.buckets, nil
}

func (f *fakeSLAStore) GetSLAHealthAggregate(ctx context.Context, appID string, since, until time.Time) (model.SLAHealthAggregate, error) {
	f.since = since
	f.until = until
	return f.aggregate, nil
}

func TestSLAServiceReturnsUnconfiguredHealthStatus(t *testing.T) {
	service := NewSLAService(SLAConfig{
		Store:  fakeSLAStore{},
		Target: 0.99,
	})

	result, err := service.GetAppSLA(context.Background(), "app-a", model.Window5m)
	if err != nil {
		t.Fatalf("GetAppSLA() unexpected error: %v", err)
	}

	if result.Configured {
		t.Fatalf("configured = true; want false")
	}
	if result.Target != 0.99 || result.Current != 1 || !result.MeetingTarget {
		t.Fatalf("status = %#v; want unconfigured target 0.99 with full availability", result)
	}
	if result.PrometheusQuerySource != "sla_health_check" {
		t.Fatalf("source = %q; want sla_health_check", result.PrometheusQuerySource)
	}
}

func TestSLAServiceComputesAvailabilityFromHealthChecks(t *testing.T) {
	store := &fakeSLAStore{
		config: model.SLAConfig{
			AppID:            "app-a",
			Enabled:          true,
			URL:              "https://example.com/healthz",
			Target:           0.99,
			IntervalSeconds:  10,
			TimeoutSeconds:   3,
			SuccessStatusMin: 200,
			SuccessStatusMax: 399,
		},
		aggregate: model.SLAHealthAggregate{
			TotalChecks:    100,
			SuccessChecks:  98,
			FailureChecks:  2,
			LatencySumMs:   2500,
			LastCheckedAt:  1710000060,
			LastStatusCode: 500,
			LastErrorType:  "status_code_5xx",
		},
	}
	service := NewSLAService(SLAConfig{Store: store, Target: 0.99})

	result, err := service.GetAppSLAAt(context.Background(), "app-a", model.Window10m, time.Unix(1710000100, 0))
	if err != nil {
		t.Fatalf("GetAppSLA() unexpected error: %v", err)
	}
	if !result.Configured {
		t.Fatalf("configured = false; want true")
	}
	if result.Current != 0.98 || result.MeetingTarget {
		t.Fatalf("availability = %v meeting=%v; want 0.98 and false", result.Current, result.MeetingTarget)
	}
	if result.TotalChecks != 100 || result.SuccessChecks != 98 || result.FailureChecks != 2 {
		t.Fatalf("checks = %#v", result)
	}
	if result.AvgLatencyMs != 25 {
		t.Fatalf("avg latency = %v; want 25", result.AvgLatencyMs)
	}
	if result.LastErrorType != "status_code_5xx" || result.LastStatusCode != 500 {
		t.Fatalf("last failure = %#v", result)
	}
	if got := store.until.Unix(); got != 1710000100 {
		t.Fatalf("aggregate until = %d; want 1710000100", got)
	}
}
