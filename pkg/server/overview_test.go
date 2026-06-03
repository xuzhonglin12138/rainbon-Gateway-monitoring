package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
)

type fakeOverviewService struct {
	scope  model.AggregateScope
	window model.Window
}

func (f *fakeOverviewService) GetPlatformOverview(_ context.Context, window model.Window) (model.Overview, error) {
	f.scope = model.AggregateScope{Kind: model.ScopePlatform}
	f.window = window
	return model.Overview{Scope: f.scope, Window: window, RequestCount: 10}, nil
}

func (f *fakeOverviewService) GetAppOverview(_ context.Context, appID string, window model.Window) (model.Overview, error) {
	f.scope = model.AggregateScope{Kind: model.ScopeApp, ID: appID}
	f.window = window
	return model.Overview{Scope: f.scope, Window: window, RequestCount: 20}, nil
}

func (f *fakeOverviewService) GetComponentOverview(_ context.Context, componentID string, window model.Window) (model.Overview, error) {
	f.scope = model.AggregateScope{Kind: model.ScopeComponent, ID: componentID}
	f.window = window
	return model.Overview{Scope: f.scope, Window: window, ThroughputPerSecond: 3}, nil
}

func (f *fakeOverviewService) GetPlatformNodeSummaries(_ context.Context, window model.Window) ([]model.PlatformNodeSummary, error) {
	f.scope = model.AggregateScope{Kind: model.ScopePlatform}
	f.window = window
	return []model.PlatformNodeSummary{{
		Name:              "node-a",
		Cluster:           "cluster-a",
		RequestCount:      100,
		P50LatencyMs:      35,
		ErrorCount:        2,
		EgressBytesPerSec: 2048,
	}}, nil
}

func (f *fakeOverviewService) GetPlatformNodeDetail(_ context.Context, nodeName string, window model.Window) (model.PlatformNodeDetail, error) {
	f.scope = model.AggregateScope{Kind: model.ScopePlatform, ID: nodeName}
	f.window = window
	return model.PlatformNodeDetail{
		Name:               nodeName,
		Cluster:            "cluster-a",
		Status:             "ready",
		CPUUsagePercent:    42,
		MemoryUsagePercent: 63,
	}, nil
}

func TestServerHandlesOverviewRoutes(t *testing.T) {
	overview := &fakeOverviewService{}
	s := New(Config{OverviewService: overview})

	tests := []struct {
		path string
		want string
	}{
		{path: "/api/v1/platform/overview?window=5m", want: `"request_count":10`},
		{path: "/api/v1/apps/app-a/overview?window=10m", want: `"request_count":20`},
		{path: "/api/v1/components/svc-a/overview?window=5m", want: `"throughput_per_second":3`},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		s.httpServer.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", tt.path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), tt.want) {
			t.Fatalf("%s body = %s; want contains %s", tt.path, rec.Body.String(), tt.want)
		}
	}
}

func TestServerHandlesPlatformNodeRoutes(t *testing.T) {
	overview := &fakeOverviewService{}
	s := New(Config{OverviewService: overview})

	tests := []struct {
		path string
		want string
	}{
		{path: "/api/v1/platform/nodes/summary?window=5m", want: `"p50_latency_ms":35`},
		{path: "/api/v1/platform/nodes/node-a/detail?window=10m", want: `"cpu_usage_percent":42`},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		s.httpServer.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", tt.path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), tt.want) {
			t.Fatalf("%s body = %s; want contains %s", tt.path, rec.Body.String(), tt.want)
		}
	}
}
