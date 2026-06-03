package service

import (
	"context"
	"testing"

	promclient "github.com/goodrain/rainbond-plugin-template/pkg/clients/prometheus"
	"github.com/goodrain/rainbond-plugin-template/pkg/model"
)

func TestOverviewServiceGetsPlatformOverview(t *testing.T) {
	client := &fakePrometheusClient{values: map[string]float64{
		`sum(increase(apisix_http_status[5m]))`:              1000,
		`sum(increase(apisix_http_status{code=~"5.."}[5m]))`: 5,
		`sum(rate(apisix_http_latency_sum[5m]))`:             2,
		`sum(rate(apisix_http_latency_count[5m]))`:           10,
		`sum(rate(apisix_bandwidth{type="egress"}[5m]))`:     2048,
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
	if overview.AvgLatencyMs != 200 {
		t.Fatalf("latency = %v; want 200", overview.AvgLatencyMs)
	}
}

func TestOverviewServiceGetsAppOverviewWithRouteIndex(t *testing.T) {
	client := &fakePrometheusClient{values: map[string]float64{
		`sum(increase(apisix_http_status{route=~"route-a"}[10m]))`:             200,
		`sum(increase(apisix_http_status{route=~"route-a",code=~"5.."}[10m]))`: 1,
		`sum(rate(apisix_http_latency_sum{route=~"route-a"}[10m]))`:            1,
		`sum(rate(apisix_http_latency_count{route=~"route-a"}[10m]))`:          5,
		`sum(rate(apisix_bandwidth{route=~"route-a",type="egress"}[10m]))`:     512,
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
}

func TestOverviewServiceGetsComponentOverview(t *testing.T) {
	client := &fakePrometheusClient{values: map[string]float64{
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
	if overview.AvgLatencyMs != 86 {
		t.Fatalf("latency = %v; want 86", overview.AvgLatencyMs)
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
