package service

import (
	"context"
	"math"
	"testing"

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
	config  model.SLAConfig
	routes  []string
	buckets []model.RouteGroupBucketPoint
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

func TestSLAServicePrefersRouteGroupBucketsOverPrometheus(t *testing.T) {
	client := &fakePrometheusClient{values: map[string]float64{
		`sum(increase(apisix_http_status{route=~"route-a"}[5m]))`:             123.45,
		`sum(increase(apisix_http_status{route=~"route-a",code=~"5.."}[5m]))`: 6.78,
	}}
	service := NewSLAService(SLAConfig{
		Prometheus: client,
		Store: fakeSLAStore{
			config: model.SLAConfig{AppID: "app-a", Target: 0.999},
			routes: []string{"route-a"},
			buckets: []model.RouteGroupBucketPoint{
				{Timestamp: 1710000001, Metric: model.RouteGroupMetric{RequestCount: 30, ErrorCount: 1}},
				{Timestamp: 1710000002, Metric: model.RouteGroupMetric{RequestCount: 70, ErrorCount: 2}},
			},
		},
		Target: 0.999,
	})

	result, err := service.GetAppSLA(context.Background(), "app-a", model.Window5m)
	if err != nil {
		t.Fatalf("GetAppSLA() unexpected error: %v", err)
	}

	if result.TotalRequests != 100 {
		t.Fatalf("total = %v; want integer bucket total 100", result.TotalRequests)
	}
	if result.ErrorRequests != 3 {
		t.Fatalf("errors = %v; want integer bucket errors 3", result.ErrorRequests)
	}
	if result.Current != 0.97 {
		t.Fatalf("current = %v; want 0.97", result.Current)
	}
	if result.EvidenceLevel != "A" {
		t.Fatalf("evidence = %q; want A", result.EvidenceLevel)
	}
	if result.PrometheusQuerySource != "route_group_bucket" {
		t.Fatalf("source = %q; want route_group_bucket", result.PrometheusQuerySource)
	}
	if len(client.queries) != 0 {
		t.Fatalf("prometheus queries = %#v; want no prometheus query when buckets are available", client.queries)
	}
}

func TestSLAServiceComputesAvailabilityFromGateway5xx(t *testing.T) {
	client := &fakePrometheusClient{values: map[string]float64{
		`sum(increase(apisix_http_status{route=~"route-a|route-b"}[5m]))`:             1000,
		`sum(increase(apisix_http_status{route=~"route-a|route-b",code=~"5.."}[5m]))`: 2,
	}}
	service := NewSLAService(SLAConfig{
		Prometheus: client,
		Store: fakeSLAStore{
			config: model.SLAConfig{AppID: "app-a", Target: 0.999},
			routes: []string{"route-a", "route-b"},
		},
		Target: 0.999,
	})

	result, err := service.GetAppSLA(context.Background(), "app-a", model.Window5m)
	if err != nil {
		t.Fatalf("GetAppSLA() unexpected error: %v", err)
	}
	if result.AppID != "app-a" {
		t.Fatalf("app id = %q; want app-a", result.AppID)
	}
	if result.TotalRequests != 1000 {
		t.Fatalf("total = %v; want 1000", result.TotalRequests)
	}
	if result.ErrorRequests != 2 {
		t.Fatalf("errors = %v; want 2", result.ErrorRequests)
	}
	if result.Current != 0.998 {
		t.Fatalf("current = %v; want 0.998", result.Current)
	}
	if result.MeetingTarget {
		t.Fatal("meeting target = true; want false")
	}
	if math.Abs(result.ErrorBudgetRemaining-(-1)) > 0.000001 {
		t.Fatalf("budget remaining = %v; want -1", result.ErrorBudgetRemaining)
	}
}

func TestSLAServiceEscapesRegexForPrometheusStringLiteral(t *testing.T) {
	route := `gr1ea4bc-8080-9tmvzlgs.14.103.233.199.nip.iop-ps-s`
	routeMatcher := `gr1ea4bc-8080-9tmvzlgs\\.14\\.103\\.233\\.199\\.nip\\.iop-ps-s`
	client := &fakePrometheusClient{values: map[string]float64{
		`sum(increase(apisix_http_status{route=~"` + routeMatcher + `"}[5m]))`:             1000,
		`sum(increase(apisix_http_status{route=~"` + routeMatcher + `",code=~"5.."}[5m]))`: 2,
	}}
	service := NewSLAService(SLAConfig{
		Prometheus: client,
		Store: fakeSLAStore{
			config: model.SLAConfig{AppID: "1023", Target: 0.999},
			routes: []string{route},
		},
		Target: 0.999,
	})

	result, err := service.GetAppSLA(context.Background(), "1023", model.Window5m)
	if err != nil {
		t.Fatalf("GetAppSLA() unexpected error: %v", err)
	}
	if result.TotalRequests != 1000 {
		t.Fatalf("total = %v; want 1000", result.TotalRequests)
	}
}

func TestSLAServiceReturnsFullAvailabilityWithoutTraffic(t *testing.T) {
	client := &fakePrometheusClient{values: map[string]float64{}}
	service := NewSLAService(SLAConfig{
		Prometheus: client,
		Store:      fakeSLAStore{},
		Target:     0.999,
	})

	result, err := service.GetAppSLA(context.Background(), "app-a", model.Window10m)
	if err != nil {
		t.Fatalf("GetAppSLA() unexpected error: %v", err)
	}
	if result.Current != 1 {
		t.Fatalf("current = %v; want 1", result.Current)
	}
	if !result.MeetingTarget {
		t.Fatal("meeting target = false; want true")
	}
}
