package service

import (
	"context"
	"math"
	"testing"

	promclient "github.com/goodrain/rainbond-plugin-template/pkg/clients/prometheus"
	"github.com/goodrain/rainbond-plugin-template/pkg/model"
)

type fakePrometheusClient struct {
	values  map[string]float64
	vectors map[string][]promclient.Sample
	queries []string
}

func (f *fakePrometheusClient) QueryScalar(ctx context.Context, query string) (float64, error) {
	f.queries = append(f.queries, query)
	return f.values[query], nil
}

func (f *fakePrometheusClient) QueryInstant(ctx context.Context, query string) ([]promclient.Sample, error) {
	f.queries = append(f.queries, query)
	return f.vectors[query], nil
}

type fakeSLAStore struct {
	config model.SLAConfig
	routes []string
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
