package service

import (
	"context"
	"testing"

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
