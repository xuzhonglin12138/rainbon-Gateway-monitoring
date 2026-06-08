package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
	logtest "github.com/sirupsen/logrus/hooks/test"
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
	err     error
}

func (f fakeRouteMapper) ResolveRoute(_ context.Context, routeID, serviceID string) (model.RouteMapping, error) {
	if f.err != nil {
		return model.RouteMapping{}, f.err
	}
	got := f.mapping
	got.RouteID = routeID
	if got.ComponentID == "" {
		got.ComponentID = serviceID
	}
	return got, nil
}

type fakeRouteMapperByID map[string]model.RouteMapping

func (f fakeRouteMapperByID) ResolveRoute(_ context.Context, routeID, serviceID string) (model.RouteMapping, error) {
	mapping, ok := f[routeID]
	if !ok {
		return model.RouteMapping{}, errors.New("mapping missing")
	}
	mapping.RouteID = routeID
	if mapping.ComponentID == "" {
		mapping.ComponentID = serviceID
	}
	return mapping, nil
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
			RouteID:       "route-a",
			ServiceID:     "service-a",
			URI:           "/api/order/detail/123",
			Status:        200,
			RequestTime:   0.086,
			BodyBytesSent: 1024,
		},
		{
			RouteID:        "route-a",
			ServiceID:      "service-a",
			URI:            "/api/order/detail/456",
			Status:         503,
			RequestTime:    0.114,
			UpstreamStatus: 502,
			BytesSent:      2048,
		},
	})
	if err != nil {
		t.Fatalf("Collect() unexpected error: %v", err)
	}

	wantWrites := 4 * 3
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
			platform5m.EgressBytes += write.Metric.EgressBytes
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
	if platform5m.EgressBytes != 3072 {
		t.Fatalf("platform 5m egress bytes = %d; want 3072", platform5m.EgressBytes)
	}
}

func TestCollectorKeepsDistinctBucketsSeparateWhenBatchAggregating(t *testing.T) {
	store := &fakeAggregateStore{}
	collector := NewInternalRouteCollector(CollectorConfig{
		Store: store,
		Mapper: fakeRouteMapper{mapping: model.RouteMapping{
			TeamID:      "team-a",
			AppID:       "app-a",
			ComponentID: "svc-a",
		}},
		Now: func() time.Time {
			return time.Unix(1710000010, 0)
		},
	})

	err := collector.Collect(context.Background(), []model.ApisixAccessLog{
		{
			Timestamp:   "2024-03-09T16:00:07Z",
			RouteID:     "route-a",
			URI:         "/api/ping",
			Status:      200,
			RequestTime: 0.01,
		},
		{
			Timestamp:   "2024-03-09T16:00:11Z",
			RouteID:     "route-a",
			URI:         "/api/ping",
			Status:      200,
			RequestTime: 0.01,
		},
	})
	if err != nil {
		t.Fatalf("Collect() unexpected error: %v", err)
	}

	wantWrites := 4 * 3 * 2
	if len(store.writes) != wantWrites {
		t.Fatalf("writes = %d; want %d", len(store.writes), wantWrites)
	}
	seenBuckets := map[int64]bool{}
	for _, write := range store.writes {
		seenBuckets[write.BucketUnix] = true
		if write.Metric.RequestCount != 1 {
			t.Fatalf("request count = %d; want 1 for distinct bucket write", write.Metric.RequestCount)
		}
	}
	if !seenBuckets[1710000005] || !seenBuckets[1710000010] {
		t.Fatalf("seen buckets = %#v; want 1710000005 and 1710000010", seenBuckets)
	}
}

func TestCollectorUsesAccessLogTimestampForBucket(t *testing.T) {
	store := &fakeAggregateStore{}
	collector := NewInternalRouteCollector(CollectorConfig{
		Store: store,
		Mapper: fakeRouteMapper{mapping: model.RouteMapping{
			TeamID:      "team-a",
			AppID:       "app-a",
			ComponentID: "svc-a",
		}},
		Now: func() time.Time {
			return time.Unix(1710000060, 0)
		},
	})

	err := collector.Collect(context.Background(), []model.ApisixAccessLog{
		{
			Timestamp:   "2024-03-09T16:00:07Z",
			RouteID:     "route-a",
			ServiceID:   "service-a",
			URI:         "/api/ping",
			Status:      200,
			RequestTime: 0.086,
		},
	})
	if err != nil {
		t.Fatalf("Collect() unexpected error: %v", err)
	}
	if len(store.writes) == 0 {
		t.Fatal("writes length = 0; want > 0")
	}
	for _, write := range store.writes {
		if write.BucketUnix != 1710000005 {
			t.Fatalf("bucket = %d; want 1710000005", write.BucketUnix)
		}
	}
}

func TestCollectorFallsBackToRouteNameWhenApisixRouteIDIsInternalID(t *testing.T) {
	store := &fakeAggregateStore{}
	collector := NewInternalRouteCollector(CollectorConfig{
		Store: store,
		Mapper: fakeRouteMapperByID{
			"xuzl_gr1ea4bc-8080-route_db59856e": {
				TeamID:       "team-a",
				AppID:        "1023",
				ComponentID:  "gr1ea4bc",
				ServiceAlias: "gr1ea4bc",
			},
		},
		Now: func() time.Time {
			return time.Unix(1710000007, 0)
		},
	})

	err := collector.Collect(context.Background(), []model.ApisixAccessLog{{
		RouteID:        "19963474",
		RouteName:      "xuzl_gr1ea4bc-8080-route_db59856e",
		URI:            "/api/orders/123",
		Status:         200,
		RequestTime:    0.01,
		UpstreamStatus: 200,
	}})
	if err != nil {
		t.Fatalf("Collect() unexpected error: %v", err)
	}
	if len(store.writes) == 0 {
		t.Fatal("writes length = 0; want route group bucket writes")
	}
	if store.writes[0].Metric.AppID != "1023" {
		t.Fatalf("metric app id = %q; want 1023", store.writes[0].Metric.AppID)
	}
}

func TestCollectorUsesNamespaceAsTeamScopeFallback(t *testing.T) {
	store := &fakeAggregateStore{}
	collector := NewInternalRouteCollector(CollectorConfig{
		Store: store,
		Mapper: fakeRouteMapper{mapping: model.RouteMapping{
			AppID:       "app-a",
			Namespace:   "team-ns",
			ComponentID: "svc-a",
		}},
		Now: func() time.Time {
			return time.Unix(1710000007, 0)
		},
	})

	err := collector.Collect(context.Background(), []model.ApisixAccessLog{{
		RouteID:     "route-a",
		URI:         "/api/orders/123",
		Status:      200,
		RequestTime: 0.1,
	}})
	if err != nil {
		t.Fatalf("Collect() unexpected error: %v", err)
	}

	for _, write := range store.writes {
		if write.Scope.Kind == model.ScopeTeam && write.Scope.ID == "team-ns" {
			return
		}
	}
	t.Fatalf("missing team scope fallback to namespace; writes=%#v", store.writes)
}

func TestCollectorCopiesDisplayMetadataToMetrics(t *testing.T) {
	store := &fakeAggregateStore{}
	collector := NewInternalRouteCollector(CollectorConfig{
		Store: store,
		Mapper: fakeRouteMapper{mapping: model.RouteMapping{
			AppID:       "console-app-a",
			RegionAppID: "region-app-a",
			AppName:     "订单系统",
			TeamName:    "team-a",
			TeamAlias:   "研发团队",
			RegionName:  "cn-east",
			Namespace:   "team-ns",
			ComponentID: "svc-a",
		}},
		Now: func() time.Time {
			return time.Unix(1710000007, 0)
		},
	})

	err := collector.Collect(context.Background(), []model.ApisixAccessLog{{
		RouteID:     "route-a",
		URI:         "/api/orders/123",
		Status:      200,
		RequestTime: 0.1,
	}})
	if err != nil {
		t.Fatalf("Collect() unexpected error: %v", err)
	}
	if len(store.writes) == 0 {
		t.Fatal("no metric writes")
	}
	metric := store.writes[0].Metric
	if metric.AppID != "console-app-a" || metric.RegionAppID != "region-app-a" || metric.AppName != "订单系统" {
		t.Fatalf("app metadata = %#v; want console id, region id and name", metric)
	}
	if metric.TeamName != "team-a" || metric.TeamAlias != "研发团队" || metric.RegionName != "cn-east" {
		t.Fatalf("team metadata = %#v; want team and region metadata", metric)
	}
}

func TestCollectorLogsSummaryForDiagnostics(t *testing.T) {
	store := &fakeAggregateStore{}
	logger, hook := logtest.NewNullLogger()
	collector := NewInternalRouteCollector(CollectorConfig{
		Store: store,
		Mapper: fakeRouteMapper{
			mapping: model.RouteMapping{AppID: "app-a", ComponentID: "svc-a"},
			err:     errors.New("mapping missing"),
		},
		Logger: logger,
		Now: func() time.Time {
			return time.Unix(1710000007, 0)
		},
	})

	err := collector.Collect(context.Background(), []model.ApisixAccessLog{
		{URI: "/skip", Status: 200},
		{RouteID: "route-a", URI: "/unknown", Status: 200, RequestTime: 0.01},
	})
	if err != nil {
		t.Fatalf("Collect() unexpected error: %v", err)
	}

	var sawSummary bool
	for _, entry := range hook.Entries {
		if entry.Message != "collected apisix access logs" {
			continue
		}
		sawSummary = true
		if entry.Data["log_count"] != 2 {
			t.Fatalf("log_count = %v; want 2", entry.Data["log_count"])
		}
		if entry.Data["skipped_missing_route"] != 1 {
			t.Fatalf("skipped_missing_route = %v; want 1", entry.Data["skipped_missing_route"])
		}
		if entry.Data["unknown_mapping_count"] != 1 {
			t.Fatalf("unknown_mapping_count = %v; want 1", entry.Data["unknown_mapping_count"])
		}
	}
	if !sawSummary {
		t.Fatalf("missing collector summary log; entries=%#v", hook.Entries)
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
