package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	promclient "github.com/goodrain/rainbond-plugin-template/pkg/clients/prometheus"
	"github.com/goodrain/rainbond-plugin-template/pkg/model"
)

type fakeRouteGroupOverviewStore struct {
	items   []model.RouteGroupItem
	buckets []model.RouteGroupBucketPoint
	scope   model.AggregateScope
	window  model.Window
}

func (f *fakeRouteGroupOverviewStore) ListRouteGroups(_ context.Context, scope model.AggregateScope, _ model.Window, _ int, _ string) ([]model.RouteGroupItem, error) {
	f.scope = scope
	return f.items, nil
}

func (f *fakeRouteGroupOverviewStore) ListRouteGroupBucketPoints(_ context.Context, scope model.AggregateScope, window model.Window) ([]model.RouteGroupBucketPoint, error) {
	f.scope = scope
	f.window = window
	return f.buckets, nil
}

func TestOverviewServiceGetsPlatformOverview(t *testing.T) {
	client := &fakePrometheusClient{values: map[string]float64{
		`sum(increase(apisix_http_status[5m]))`:                     1000,
		`sum(increase(apisix_http_status{code=~"5.."}[5m]))`:        5,
		`sum(rate(apisix_http_latency_sum{type="upstream"}[5m]))`:   200,
		`sum(rate(apisix_http_latency_count{type="upstream"}[5m]))`: 10,
		`sum(rate(apisix_bandwidth{type="egress"}[5m]))`:            2048,
	}}
	service := NewOverviewService(OverviewConfig{Prometheus: client})

	overview, err := service.GetPlatformOverview(context.Background(), model.Window5m)
	if err != nil {
		t.Fatalf("GetPlatformOverview() unexpected error: %v", err)
	}
	if overview.RequestCount != 1000 {
		t.Fatalf("request count = %v; want 1000", overview.RequestCount)
	}
	if overview.ErrorRate != 0.005 {
		t.Fatalf("error rate = %v; want 0.005", overview.ErrorRate)
	}
	if overview.AvgLatencyMs != 20 {
		t.Fatalf("latency = %v; want 20", overview.AvgLatencyMs)
	}
}

func TestOverviewServiceGetsPlatformOverviewFromRealtimeBuckets(t *testing.T) {
	store := &fakeRouteGroupOverviewStore{
		buckets: []model.RouteGroupBucketPoint{
			{
				Timestamp: 1000,
				Metric: model.RouteGroupMetric{
					RequestCount: 30,
					ErrorCount:   3,
					LatencySumMs: 1500,
					LatencyCount: 30,
					EgressBytes:  3000,
				},
			},
			{
				Timestamp: 1005,
				Metric: model.RouteGroupMetric{
					RequestCount: 10,
					ErrorCount:   2,
					LatencySumMs: 1000,
					LatencyCount: 10,
					EgressBytes:  6000,
				},
			},
		},
	}
	service := NewOverviewService(OverviewConfig{RouteGroupStore: store})

	overview, err := service.GetPlatformOverview(context.Background(), model.Window5m)
	if err != nil {
		t.Fatalf("GetPlatformOverview() unexpected error: %v", err)
	}
	if store.scope.Kind != model.ScopePlatform {
		t.Fatalf("scope = %#v; want platform", store.scope)
	}
	if overview.RequestCount != 40 {
		t.Fatalf("request count = %v; want 40", overview.RequestCount)
	}
	if overview.ErrorRate != 0.125 {
		t.Fatalf("error rate = %v; want 0.125", overview.ErrorRate)
	}
	if overview.AvgLatencyMs != 62.5 {
		t.Fatalf("latency = %v; want 62.5", overview.AvgLatencyMs)
	}
	if overview.EgressBytesPerSec != 30 {
		t.Fatalf("egress = %v; want 30", overview.EgressBytesPerSec)
	}
}

func TestOverviewServiceGetsAppOverviewWithRouteIndex(t *testing.T) {
	client := &fakePrometheusClient{values: map[string]float64{
		`sum(increase(apisix_http_status{route=~"route-a"}[10m]))`:                    200,
		`sum(increase(apisix_http_status{route=~"route-a",code=~"5.."}[10m]))`:        1,
		`sum(rate(apisix_http_latency_sum{route=~"route-a",type="upstream"}[10m]))`:   100,
		`sum(rate(apisix_http_latency_count{route=~"route-a",type="upstream"}[10m]))`: 5,
		`sum(rate(apisix_bandwidth{route=~"route-a",type="egress"}[10m]))`:            512,
	}}
	service := NewOverviewService(OverviewConfig{
		Prometheus: client,
		Store:      fakeSLAStore{routes: []string{"route-a"}},
	})

	overview, err := service.GetAppOverview(context.Background(), "app-a", model.Window10m)
	if err != nil {
		t.Fatalf("GetAppOverview() unexpected error: %v", err)
	}
	if overview.Scope.Kind != model.ScopeApp || overview.Scope.ID != "app-a" {
		t.Fatalf("scope = %#v", overview.Scope)
	}
	if overview.RequestCount != 200 {
		t.Fatalf("request count = %v; want 200", overview.RequestCount)
	}
	if overview.AvgLatencyMs != 20 {
		t.Fatalf("latency = %v; want 20", overview.AvgLatencyMs)
	}
}

func TestOverviewServiceGetsAppOverviewFromRealtimeBucketsWithoutRouteIndex(t *testing.T) {
	store := &fakeRouteGroupOverviewStore{
		buckets: []model.RouteGroupBucketPoint{
			{
				Timestamp: 1000,
				Metric: model.RouteGroupMetric{
					RequestCount: 12,
					ErrorCount:   1,
					LatencySumMs: 480,
					LatencyCount: 12,
					EgressBytes:  2400,
				},
			},
		},
	}
	service := NewOverviewService(OverviewConfig{RouteGroupStore: store})

	overview, err := service.GetAppOverview(context.Background(), "app-a", model.Window5m)
	if err != nil {
		t.Fatalf("GetAppOverview() unexpected error: %v", err)
	}
	if store.scope.Kind != model.ScopeApp || store.scope.ID != "app-a" {
		t.Fatalf("scope = %#v; want app/app-a", store.scope)
	}
	if overview.RequestCount != 12 {
		t.Fatalf("request count = %v; want 12", overview.RequestCount)
	}
	if overview.ErrorRate != float64(1)/float64(12) {
		t.Fatalf("error rate = %v; want %v", overview.ErrorRate, float64(1)/float64(12))
	}
	if overview.AvgLatencyMs != 40 {
		t.Fatalf("latency = %v; want 40", overview.AvgLatencyMs)
	}
	if overview.EgressBytesPerSec != 8 {
		t.Fatalf("egress = %v; want 8", overview.EgressBytesPerSec)
	}
}

func TestOverviewServiceEscapesRegexForPrometheusStringLiteral(t *testing.T) {
	route := `gr1ea4bc-8080-9tmvzlgs.14.103.233.199.nip.iop-ps-s`
	routeMatcher := `gr1ea4bc-8080-9tmvzlgs\\.14\\.103\\.233\\.199\\.nip\\.iop-ps-s`
	client := &fakePrometheusClient{values: map[string]float64{
		`sum(increase(apisix_http_status{route=~"` + routeMatcher + `"}[5m]))`:                    200,
		`sum(increase(apisix_http_status{route=~"` + routeMatcher + `",code=~"5.."}[5m]))`:        1,
		`sum(rate(apisix_http_latency_sum{route=~"` + routeMatcher + `",type="upstream"}[5m]))`:   100,
		`sum(rate(apisix_http_latency_count{route=~"` + routeMatcher + `",type="upstream"}[5m]))`: 5,
		`sum(rate(apisix_bandwidth{route=~"` + routeMatcher + `",type="egress"}[5m]))`:            512,
	}}
	service := NewOverviewService(OverviewConfig{
		Prometheus: client,
		Store:      fakeSLAStore{routes: []string{route}},
	})

	overview, err := service.GetAppOverview(context.Background(), "1023", model.Window5m)
	if err != nil {
		t.Fatalf("GetAppOverview() unexpected error: %v", err)
	}
	if overview.RequestCount != 200 {
		t.Fatalf("request count = %v; want 200", overview.RequestCount)
	}
}

func TestOverviewServiceGetsGatewayRealtimeTrend(t *testing.T) {
	client := &fakePrometheusClient{
		ranges: map[string][]promclient.RangeSample{
			`sum(rate(apisix_http_status[1m]))`: {
				{Values: []promclient.Point{{Timestamp: 100, Value: 2}, {Timestamp: 130, Value: 3}}},
			},
			`sum(rate(apisix_http_status{code=~"5.."}[1m]))`: {
				{Values: []promclient.Point{{Timestamp: 100, Value: 1}, {Timestamp: 130, Value: 0}}},
			},
			`sum(rate(apisix_http_latency_sum{type="upstream"}[1m])) / sum(rate(apisix_http_latency_count{type="upstream"}[1m]))`: {
				{Values: []promclient.Point{{Timestamp: 100, Value: 18}, {Timestamp: 130, Value: 20}}},
			},
			`sum(rate(apisix_bandwidth{type="egress"}[1m]))`: {
				{Values: []promclient.Point{{Timestamp: 100, Value: 2048}, {Timestamp: 130, Value: 4096}}},
			},
		},
	}
	service := NewOverviewService(OverviewConfig{Prometheus: client, Now: func() time.Time {
		return time.Unix(400, 0)
	}})

	trend, err := service.GetPlatformRealtimeTrend(context.Background(), model.Window5m)
	if err != nil {
		t.Fatalf("GetPlatformRealtimeTrend() unexpected error: %v", err)
	}
	if len(trend.Points) != 2 {
		t.Fatalf("points length = %d; want 2", len(trend.Points))
	}
	if trend.Points[0].RequestPerSecond != 2 || trend.Points[0].ErrorRate != 0.5 {
		t.Fatalf("first point = %#v", trend.Points[0])
	}
	if math.Abs(trend.Points[1].AvgLatencyMs-20) > 0.000001 {
		t.Fatalf("latency = %v; want 20", trend.Points[1].AvgLatencyMs)
	}
}

func TestOverviewServiceSanitizesNonFiniteRealtimeTrendValues(t *testing.T) {
	client := &fakePrometheusClient{
		ranges: map[string][]promclient.RangeSample{
			`sum(rate(apisix_http_status[1m]))`: {
				{Values: []promclient.Point{{Timestamp: 100, Value: 2}}},
			},
			`sum(rate(apisix_http_status{code=~"5.."}[1m]))`: {
				{Values: []promclient.Point{{Timestamp: 100, Value: 0}}},
			},
			`sum(rate(apisix_http_latency_sum{type="upstream"}[1m])) / sum(rate(apisix_http_latency_count{type="upstream"}[1m]))`: {
				{Values: []promclient.Point{{Timestamp: 100, Value: math.Inf(1)}}},
			},
			`sum(rate(apisix_bandwidth{type="egress"}[1m]))`: {
				{Values: []promclient.Point{{Timestamp: 100, Value: math.NaN()}}},
			},
		},
	}
	service := NewOverviewService(OverviewConfig{Prometheus: client, Now: func() time.Time {
		return time.Unix(400, 0)
	}})

	trend, err := service.GetPlatformRealtimeTrend(context.Background(), model.Window5m)
	if err != nil {
		t.Fatalf("GetPlatformRealtimeTrend() unexpected error: %v", err)
	}
	if len(trend.Points) != 1 {
		t.Fatalf("points length = %d; want 1", len(trend.Points))
	}
	if trend.Points[0].AvgLatencyMs != 0 {
		t.Fatalf("latency = %v; want sanitized 0", trend.Points[0].AvgLatencyMs)
	}
	if trend.Points[0].EgressBytesPerSec != 0 {
		t.Fatalf("egress = %v; want sanitized 0", trend.Points[0].EgressBytesPerSec)
	}
	if _, err := json.Marshal(trend); err != nil {
		t.Fatalf("marshal trend unexpected error: %v", err)
	}
}

func TestOverviewServiceAlignsGatewayTrendRangeQueries(t *testing.T) {
	client := &fakePrometheusClient{ranges: map[string][]promclient.RangeSample{}}
	service := NewOverviewService(OverviewConfig{
		Prometheus: client,
		Now: func() time.Time {
			return time.Unix(401, 0)
		},
	})

	if _, err := service.GetPlatformRealtimeTrend(context.Background(), model.Window5m); err != nil {
		t.Fatalf("GetPlatformRealtimeTrend() unexpected error: %v", err)
	}
	if len(client.rangesQueries) != 4 {
		t.Fatalf("range query count = %d; want 4", len(client.rangesQueries))
	}
	for _, call := range client.rangesQueries {
		if call.Start != 90 || call.End != 390 || call.StepSeconds != 30 {
			t.Fatalf("range query = %#v; want start=90 end=390 step=30", call)
		}
	}

	client.rangesQueries = nil
	service.now = func() time.Time {
		return time.Unix(419, 0)
	}
	if _, err := service.GetPlatformRealtimeTrend(context.Background(), model.Window5m); err != nil {
		t.Fatalf("GetPlatformRealtimeTrend() unexpected error: %v", err)
	}
	for _, call := range client.rangesQueries {
		if call.Start != 90 || call.End != 390 || call.StepSeconds != 30 {
			t.Fatalf("range query after refresh = %#v; want start=90 end=390 step=30", call)
		}
	}
}

func TestOverviewServiceGetsComponentOverview(t *testing.T) {
	client := &fakePrometheusClient{values: map[string]float64{
		`sum(increase(app_request{service_id="svc-a",method="total"}[5m]))`:         3600,
		`sum(rate(app_request{service_id="svc-a",method="total"}[5m]))`:             12,
		`avg(app_requesttime{service_id="svc-a",mode="avg"})`:                       86,
		`sum(rate(container_network_receive_bytes_total{service_id="svc-a"}[5m]))`:  1024,
		`sum(rate(container_network_transmit_bytes_total{service_id="svc-a"}[5m]))`: 2048,
	}}
	service := NewOverviewService(OverviewConfig{Prometheus: client})

	overview, err := service.GetComponentOverview(context.Background(), "svc-a", model.Window5m)
	if err != nil {
		t.Fatalf("GetComponentOverview() unexpected error: %v", err)
	}
	if overview.ThroughputPerSecond != 12 {
		t.Fatalf("throughput = %v; want 12", overview.ThroughputPerSecond)
	}
	if overview.RequestCount != 3600 {
		t.Fatalf("request count = %v; want 3600", overview.RequestCount)
	}
	if overview.AvgLatencyMs != 86 {
		t.Fatalf("latency = %v; want 86", overview.AvgLatencyMs)
	}
	if overview.EgressBytesPerSec != 2048 {
		t.Fatalf("egress = %v; want 2048", overview.EgressBytesPerSec)
	}
}

func TestOverviewServiceGetsComponentOverviewFromRouteGroups(t *testing.T) {
	store := &fakeRouteGroupOverviewStore{
		buckets: []model.RouteGroupBucketPoint{
			{
				Timestamp: 1000,
				Metric: model.RouteGroupMetric{
					RequestCount: 10,
					ErrorCount:   2,
					LatencySumMs: 280,
					LatencyCount: 10,
					EgressBytes:  6000,
				},
			},
		},
		items: []model.RouteGroupItem{
			{RouteGroup: "/api/ping", RequestCount: 8, ErrorCount: 1, AvgLatencyMs: 20, EgressBytes: 3000},
			{RouteGroup: "/api/order", RequestCount: 2, ErrorCount: 1, AvgLatencyMs: 60, EgressBytes: 1500},
		},
	}
	service := NewOverviewService(OverviewConfig{RouteGroupStore: store})

	overview, err := service.GetComponentOverview(context.Background(), "svc-a", model.Window5m)
	if err != nil {
		t.Fatalf("GetComponentOverview() unexpected error: %v", err)
	}
	if store.scope.Kind != model.ScopeComponent || store.scope.ID != "svc-a" {
		t.Fatalf("scope = %#v", store.scope)
	}
	if overview.RequestCount != 10 {
		t.Fatalf("request count = %v; want 10", overview.RequestCount)
	}
	if overview.ErrorCount != 2 {
		t.Fatalf("error count = %v; want 2", overview.ErrorCount)
	}
	if overview.ErrorRate != 0.2 {
		t.Fatalf("error rate = %v; want 0.2", overview.ErrorRate)
	}
	if overview.AvgLatencyMs != 28 {
		t.Fatalf("latency = %v; want 28", overview.AvgLatencyMs)
	}
	if overview.ThroughputPerSecond != float64(10)/model.Window5m.Duration().Seconds() {
		t.Fatalf("throughput = %v; want %v", overview.ThroughputPerSecond, float64(10)/model.Window5m.Duration().Seconds())
	}
	if overview.EgressBytesPerSec != 20 {
		t.Fatalf("egress = %v; want 20", overview.EgressBytesPerSec)
	}
	if overview.NetworkTransmitBps != 20 {
		t.Fatalf("network transmit = %v; want 20", overview.NetworkTransmitBps)
	}
}

func TestOverviewServiceGetsComponentTrendFromRouteGroups(t *testing.T) {
	store := &fakeRouteGroupOverviewStore{
		buckets: []model.RouteGroupBucketPoint{
			{
				Timestamp: 1000,
				Metric: model.RouteGroupMetric{
					RequestCount: 30,
					ErrorCount:   3,
					LatencySumMs: 1500,
					LatencyCount: 30,
					EgressBytes:  3000,
				},
			},
			{
				Timestamp: 1005,
				Metric: model.RouteGroupMetric{
					RequestCount: 10,
					ErrorCount:   2,
					LatencySumMs: 800,
					LatencyCount: 10,
					EgressBytes:  2000,
				},
			},
		},
	}
	service := NewOverviewService(OverviewConfig{
		RouteGroupStore: store,
		Now: func() time.Time {
			return time.Unix(1005, 0)
		},
	})

	trend, err := service.GetComponentRealtimeTrend(context.Background(), "svc-a", model.Window5m)
	if err != nil {
		t.Fatalf("GetComponentRealtimeTrend() unexpected error: %v", err)
	}
	if len(trend.Points) != model.Window5m.BucketCount() {
		t.Fatalf("points length = %d; want %d", len(trend.Points), model.Window5m.BucketCount())
	}
	firstActual := trend.Points[len(trend.Points)-2]
	lastActual := trend.Points[len(trend.Points)-1]
	if firstActual.Timestamp != 1000 || lastActual.Timestamp != 1005 {
		t.Fatalf("timestamps = %#v", trend.Points)
	}
	if firstActual.RequestPerSecond != float64(30)/model.BucketSize.Seconds() {
		t.Fatalf("request per second = %v", firstActual.RequestPerSecond)
	}
	if firstActual.ErrorRate != 0.1 {
		t.Fatalf("error rate = %v; want 0.1", firstActual.ErrorRate)
	}
	if firstActual.AvgLatencyMs != 50 {
		t.Fatalf("latency = %v; want 50", firstActual.AvgLatencyMs)
	}
	if firstActual.EgressBytesPerSec != 600 {
		t.Fatalf("egress = %v; want 600", firstActual.EgressBytesPerSec)
	}
	if lastActual.ErrorRate != 0.2 || lastActual.AvgLatencyMs != 80 {
		t.Fatalf("second point = %#v", lastActual)
	}
	if lastActual.EgressBytesPerSec != 400 {
		t.Fatalf("second egress = %v; want 400", lastActual.EgressBytesPerSec)
	}
}

func TestOverviewServicePadsRouteGroupTrendToRequestedWindow(t *testing.T) {
	store := &fakeRouteGroupOverviewStore{
		buckets: []model.RouteGroupBucketPoint{
			{
				Timestamp: 1005,
				Metric: model.RouteGroupMetric{
					RequestCount: 10,
				},
			},
		},
	}
	service := NewOverviewService(OverviewConfig{
		RouteGroupStore: store,
		Now: func() time.Time {
			return time.Unix(1005, 0)
		},
	})

	trend, err := service.GetPlatformRealtimeTrend(context.Background(), model.Window30m)
	if err != nil {
		t.Fatalf("GetPlatformRealtimeTrend() unexpected error: %v", err)
	}
	if store.window != model.Window30m {
		t.Fatalf("store window = %s; want %s", store.window, model.Window30m)
	}
	if trend.Window != model.Window30m {
		t.Fatalf("trend window = %s; want %s", trend.Window, model.Window30m)
	}
	if len(trend.Points) != model.Window30m.BucketCount() {
		t.Fatalf("points length = %d; want %d", len(trend.Points), model.Window30m.BucketCount())
	}
	wantFirst := int64(1005) - int64(model.Window30m.BucketCount()-1)*int64(model.BucketSize/time.Second)
	if trend.Points[0].Timestamp != wantFirst {
		t.Fatalf("first timestamp = %d; want %d", trend.Points[0].Timestamp, wantFirst)
	}
	if trend.Points[len(trend.Points)-1].Timestamp != 1005 {
		t.Fatalf("last timestamp = %d; want 1005", trend.Points[len(trend.Points)-1].Timestamp)
	}
}

func TestOverviewServiceGetsPlatformNodeSummaries(t *testing.T) {
	client := &fakePrometheusClient{
		vectors: map[string][]promclient.Sample{
			`sum by (instance) (increase(apisix_http_status[5m]))`: {
				{Metric: map[string]string{"instance": "node-a:9091", "cluster": "cluster-a"}, Value: 1200},
			},
			`histogram_quantile(0.50, sum by (instance, le) (rate(apisix_http_latency_bucket[5m]))) * 1000`: {
				{Metric: map[string]string{"instance": "node-a:9091"}, Value: 36},
			},
			`sum by (instance) (increase(apisix_http_status{code=~"5.."}[5m]))`: {
				{Metric: map[string]string{"instance": "node-a:9091"}, Value: 7},
			},
			`sum by (instance) (rate(apisix_bandwidth{type="egress"}[5m]))`: {
				{Metric: map[string]string{"instance": "node-a:9091"}, Value: 4096},
			},
		},
	}
	service := NewOverviewService(OverviewConfig{Prometheus: client})

	nodes, err := service.GetPlatformNodeSummaries(context.Background(), model.Window5m)
	if err != nil {
		t.Fatalf("GetPlatformNodeSummaries() unexpected error: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes length = %d; want 1", len(nodes))
	}
	if nodes[0].Name != "node-a" {
		t.Fatalf("node name = %q; want node-a", nodes[0].Name)
	}
	if nodes[0].RequestCount != 1200 {
		t.Fatalf("request count = %v; want 1200", nodes[0].RequestCount)
	}
	if nodes[0].P50LatencyMs != 36 {
		t.Fatalf("p50 = %v; want 36", nodes[0].P50LatencyMs)
	}
	if nodes[0].ErrorCount != 7 {
		t.Fatalf("error count = %v; want 7", nodes[0].ErrorCount)
	}
	if nodes[0].EgressBytesPerSec != 4096 {
		t.Fatalf("egress = %v; want 4096", nodes[0].EgressBytesPerSec)
	}
	if nodes[0].Cluster != "cluster-a" {
		t.Fatalf("cluster = %q; want cluster-a", nodes[0].Cluster)
	}
}

func TestOverviewServiceGetsPlatformNodeDetail(t *testing.T) {
	client := &fakePrometheusClient{
		vectors: map[string][]promclient.Sample{
			`kube_node_status_condition{condition="Ready",status="true"}`: {
				{Metric: map[string]string{"node": "node-a", "cluster": "cluster-a"}, Value: 1},
			},
			`100 * (1 - avg by (instance) (rate(node_cpu_seconds_total{mode="idle"}[5m])))`: {
				{Metric: map[string]string{"instance": "node-a:9100"}, Value: 42.5},
			},
			`100 * (1 - (node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes))`: {
				{Metric: map[string]string{"instance": "node-a:9100"}, Value: 63.25},
			},
		},
	}
	service := NewOverviewService(OverviewConfig{Prometheus: client})

	detail, err := service.GetPlatformNodeDetail(context.Background(), "node-a", model.Window5m)
	if err != nil {
		t.Fatalf("GetPlatformNodeDetail() unexpected error: %v", err)
	}
	if detail.Status != "ready" {
		t.Fatalf("status = %q; want ready", detail.Status)
	}
	if detail.Cluster != "cluster-a" {
		t.Fatalf("cluster = %q; want cluster-a", detail.Cluster)
	}
	if detail.CPUUsagePercent != 42.5 {
		t.Fatalf("cpu = %v; want 42.5", detail.CPUUsagePercent)
	}
	if detail.MemoryUsagePercent != 63.25 {
		t.Fatalf("memory = %v; want 63.25", detail.MemoryUsagePercent)
	}
}
