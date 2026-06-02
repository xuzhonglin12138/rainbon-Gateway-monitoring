package service

import (
	"context"
	"testing"
	"time"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
)

type collectorWrite struct {
	Scope      model.AggregateScope
	Window     model.Window
	BucketUnix int64
	Metric     model.RouteGroupMetric
}

type fakeAggregateStore struct {
	writes []collectorWrite
}

func (f *fakeAggregateStore) AddRouteGroupBucket(_ context.Context, scope model.AggregateScope, window model.Window, bucketUnix int64, metric model.RouteGroupMetric) error {
	f.writes = append(f.writes, collectorWrite{Scope: scope, Window: window, BucketUnix: bucketUnix, Metric: metric})
	return nil
}

type fakeRouteMapper struct {
	mapping model.RouteMapping
}

func (f fakeRouteMapper) ResolveRoute(_ context.Context, routeID, serviceID string) (model.RouteMapping, error) {
	got := f.mapping
	got.RouteID = routeID
	if got.ComponentID == "" {
		got.ComponentID = serviceID
	}
	return got, nil
}

type fakeCollectorRuleStore struct {
	rules []model.RouteGroupRule
}

func (f fakeCollectorRuleStore) GetRouteGroupRules(_ context.Context, _ string) ([]model.RouteGroupRule, error) {
	return f.rules, nil
}

func TestCollectorAggregatesApisixLogsIntoAllHotWindowsAndScopes(t *testing.T) {
	store := &fakeAggregateStore{}
	collector := NewInternalRouteCollector(CollectorConfig{
		Store: store,
		Mapper: fakeRouteMapper{mapping: model.RouteMapping{
			TeamID:      "team-a",
			AppID:       "app-a",
			ComponentID: "svc-a",
		}},
		RouteGroups: NewRouteGroupResolver(RouteGroupConfig{
			TemplateRules: []RouteGroupRule{{Prefix: "/api/order/detail/", Group: "/api/order/detail/*"}},
		}),
		Now: func() time.Time {
			return time.Unix(1710000007, 0)
		},
	})

	err := collector.Collect(context.Background(), []model.ApisixAccessLog{
		{
			RouteID:        "route-a",
			ServiceID:      "service-a",
			URI:            "/api/order/detail/123",
			Status:         200,
			RequestTime:    0.086,
			UpstreamStatus: 200,
		},
		{
			RouteID:        "route-a",
			ServiceID:      "service-a",
			URI:            "/api/order/detail/456",
			Status:         503,
			RequestTime:    0.114,
			UpstreamStatus: 502,
		},
	})
	if err != nil {
		t.Fatalf("Collect() unexpected error: %v", err)
	}

	wantWrites := 4 * 3 * 2
	if len(store.writes) != wantWrites {
		t.Fatalf("writes = %d; want %d", len(store.writes), wantWrites)
	}

	first := store.writes[0]
	if first.BucketUnix != 1710000005 {
		t.Fatalf("bucket = %d; want 1710000005", first.BucketUnix)
	}
	if first.Metric.RouteGroup != "/api/order/detail/*" {
		t.Fatalf("route group = %q; want /api/order/detail/*", first.Metric.RouteGroup)
	}

	var platform5m model.RouteGroupMetric
	for _, write := range store.writes {
		if write.Scope.Kind == model.ScopePlatform && write.Window == model.Window5m {
			platform5m.RequestCount += write.Metric.RequestCount
			platform5m.ErrorCount += write.Metric.ErrorCount
			platform5m.UpstreamErrorCount += write.Metric.UpstreamErrorCount
			platform5m.LatencySumMs += write.Metric.LatencySumMs
			platform5m.LatencyCount += write.Metric.LatencyCount
		}
	}
	if platform5m.RequestCount != 2 {
		t.Fatalf("platform 5m request count = %d; want 2", platform5m.RequestCount)
	}
	if platform5m.ErrorCount != 1 {
		t.Fatalf("platform 5m error count = %d; want 1", platform5m.ErrorCount)
	}
	if platform5m.UpstreamErrorCount != 1 {
		t.Fatalf("platform 5m upstream error count = %d; want 1", platform5m.UpstreamErrorCount)
	}
	if platform5m.LatencySumMs != 200 {
		t.Fatalf("platform 5m latency sum = %v; want 200", platform5m.LatencySumMs)
	}
}

func TestCollectorUsesAppRouteGroupRulesFromStore(t *testing.T) {
	store := &fakeAggregateStore{}
	collector := NewInternalRouteCollector(CollectorConfig{
		Store: store,
		Mapper: fakeRouteMapper{mapping: model.RouteMapping{
			TeamID:      "team-a",
			AppID:       "app-a",
			ComponentID: "svc-a",
		}},
		RouteGroupRules: fakeCollectorRuleStore{rules: []model.RouteGroupRule{
			{Prefix: "/api/orders/", Group: "/api/orders/*"},
		}},
		Now: func() time.Time {
			return time.Unix(1710000007, 0)
		},
	})

	err := collector.Collect(context.Background(), []model.ApisixAccessLog{
		{
			RouteID:     "route-a",
			URI:         "/api/orders/ABC-123",
			Status:      200,
			RequestTime: 0.01,
		},
	})
	if err != nil {
		t.Fatalf("Collect() unexpected error: %v", err)
	}
	if len(store.writes) == 0 {
		t.Fatal("writes length = 0; want route group bucket writes")
	}
	if store.writes[0].Metric.RouteGroup != "/api/orders/*" {
		t.Fatalf("route group = %q; want /api/orders/*", store.writes[0].Metric.RouteGroup)
	}
}
