package repository

import (
	"context"
	"testing"
	"time"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
)

type fakeRedisClient struct {
	calls   [][]string
	keys    []interface{}
	hash    []interface{}
	get     interface{}
	members []interface{}
	sets    map[string]interface{}
}

func (f *fakeRedisClient) Do(_ context.Context, args ...string) (interface{}, error) {
	f.calls = append(f.calls, args)
	switch args[0] {
	case "KEYS":
		return f.keys, nil
	case "HGETALL":
		return f.hash, nil
	case "GET":
		if f.sets != nil {
			if value, ok := f.sets[args[1]]; ok {
				return value, nil
			}
		}
		return f.get, nil
	case "SMEMBERS":
		if f.sets != nil {
			if value, ok := f.sets[args[1]]; ok {
				return value, nil
			}
		}
		return f.members, nil
	case "SET":
		if f.sets == nil {
			f.sets = make(map[string]interface{})
		}
		f.sets[args[1]] = args[2]
		return "OK", nil
	case "SADD":
		if f.sets == nil {
			f.sets = make(map[string]interface{})
		}
		existing, _ := f.sets[args[1]].([]interface{})
		for _, member := range args[2:] {
			existing = append(existing, member)
		}
		f.sets[args[1]] = existing
		return int64(1), nil
	case "DEL":
		if f.sets != nil {
			delete(f.sets, args[1])
		}
		return int64(1), nil
	default:
		return int64(1), nil
	}
}

func TestRedisStoreAddRouteGroupBucketUsesHotBucketTTL(t *testing.T) {
	client := &fakeRedisClient{}
	store := NewRedisStore(client)

	err := store.AddRouteGroupBucket(context.Background(), model.AggregateScope{
		Kind: model.ScopeApp,
		ID:   "app-a",
	}, model.Window5m, 1710000005, model.RouteGroupMetric{
		RouteGroup:         "/api/order/detail/*",
		RequestCount:       1,
		ErrorCount:         1,
		UpstreamErrorCount: 1,
		LatencySumMs:       86,
		LatencyCount:       1,
		AppID:              "app-a",
		TeamID:             "team-a",
		ComponentID:        "svc-a",
	})
	if err != nil {
		t.Fatalf("AddRouteGroupBucket() unexpected error: %v", err)
	}

	var sawExpire bool
	for _, call := range client.calls {
		if len(call) == 3 && call[0] == "EXPIRE" && call[2] == "2100" {
			sawExpire = true
		}
	}
	if !sawExpire {
		t.Fatalf("expected EXPIRE with 35 minute TTL, got %#v", client.calls)
	}

	var sawScopeRegister bool
	for _, call := range client.calls {
		if len(call) == 3 && call[0] == "SADD" && call[1] == "nm:route-group:scopes" && call[2] == "app:app-a" {
			sawScopeRegister = true
		}
	}
	if !sawScopeRegister {
		t.Fatalf("expected scope registration, got %#v", client.calls)
	}
}

func TestRedisStoreRefreshRouteGroupSnapshotsFiltersBucketsByWindow(t *testing.T) {
	client := &fakeRedisClient{
		members: []interface{}{"platform"},
		keys: []interface{}{
			"nm:platform:5m:route-group:_api_old:bucket:1709999000",
			"nm:platform:5m:route-group:_api_new:bucket:1710000005",
		},
		hash: []interface{}{
			"route_group", "/api/new",
			"request_count", "1",
			"latency_count", "1",
			"latency_sum_ms", "10",
		},
	}
	store := NewRedisStore(client)
	store.now = func() time.Time {
		return time.Unix(1710000010, 0)
	}

	err := store.RefreshRouteGroupSnapshots(context.Background())
	if err != nil {
		t.Fatalf("RefreshRouteGroupSnapshots() unexpected error: %v", err)
	}

	var hgetallCount int
	var sawSnapshot bool
	for _, call := range client.calls {
		if call[0] == "HGETALL" {
			hgetallCount++
		}
		if len(call) >= 5 && call[0] == "SET" && call[1] == "nm:platform:5m:route-groups:summary" && call[3] == "EX" && call[4] == "120" {
			sawSnapshot = true
		}
	}
	if hgetallCount != 4 {
		t.Fatalf("HGETALL count = %d; want filtered buckets across 5m/10m/30m windows", hgetallCount)
	}
	if !sawSnapshot {
		t.Fatalf("expected summary snapshot write with TTL, got %#v", client.calls)
	}
}

func TestRedisStoreListRouteGroupsReadsSnapshot(t *testing.T) {
	client := &fakeRedisClient{
		get: `[{"route_group":"/api/new","request_count":3,"error_count":1,"error_rate":0.3333333333333333,"avg_latency_ms":10}]`,
	}
	store := NewRedisStore(client)

	items, err := store.ListRouteGroups(context.Background(), model.AggregateScope{Kind: model.ScopePlatform}, model.Window5m, 50, "errors")
	if err != nil {
		t.Fatalf("ListRouteGroups() unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items length = %d; want 1", len(items))
	}
	if items[0].RouteGroup != "/api/new" {
		t.Fatalf("route group = %q; want /api/new", items[0].RouteGroup)
	}
	for _, call := range client.calls {
		if call[0] == "KEYS" {
			t.Fatalf("ListRouteGroups should read snapshot without KEYS, got %#v", client.calls)
		}
	}
}

func TestRedisStoreListRouteGroupsNormalizesNullSnapshotToEmptyList(t *testing.T) {
	client := &fakeRedisClient{get: `null`}
	store := NewRedisStore(client)

	items, err := store.ListRouteGroups(context.Background(), model.AggregateScope{Kind: model.ScopeApp, ID: "app-a"}, model.Window5m, 50, "summary")
	if err != nil {
		t.Fatalf("ListRouteGroups() unexpected error: %v", err)
	}
	if items == nil {
		t.Fatal("items is nil; want empty slice")
	}
	if len(items) != 0 {
		t.Fatalf("items length = %d; want 0", len(items))
	}
}

func TestRedisStoreListsAppComponentSummariesFromHotBuckets(t *testing.T) {
	client := &fakeRedisClient{
		keys: []interface{}{
			"nm:app:app-a:5m:route-group:_api_orders:bucket:1710000005",
			"nm:app:app-a:5m:route-group:_api_pay:bucket:1710000005",
		},
		hash: []interface{}{
			"route_group", "/api/orders/*",
			"request_count", "3",
			"error_count", "1",
			"latency_count", "3",
			"latency_sum_ms", "90",
			"component_id", "svc-a",
			"service_alias", "orders",
		},
	}
	store := NewRedisStore(client)
	store.now = func() time.Time {
		return time.Unix(1710000010, 0)
	}

	items, err := store.ListAppComponentSummaries(context.Background(), "app-a", model.Window5m, 50)
	if err != nil {
		t.Fatalf("ListAppComponentSummaries() unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items length = %d; want 1", len(items))
	}
	if items[0].ComponentID != "svc-a" || items[0].Name != "orders" {
		t.Fatalf("component identity = %#v; want svc-a/orders", items[0])
	}
	if items[0].RequestCount != 6 {
		t.Fatalf("request count = %d; want 6", items[0].RequestCount)
	}
	if items[0].ErrorCount != 2 {
		t.Fatalf("error count = %d; want 2", items[0].ErrorCount)
	}
	if items[0].ErrorRate != float64(2)/float64(6) {
		t.Fatalf("error rate = %v; want %v", items[0].ErrorRate, float64(2)/float64(6))
	}
	if items[0].AvgLatencyMs != 30 {
		t.Fatalf("avg latency = %v; want 30", items[0].AvgLatencyMs)
	}
}

func TestRedisStoreListsAppsFromHotBuckets(t *testing.T) {
	client := &fakeRedisClient{
		keys: []interface{}{
			"nm:platform:5m:route-group:_api_orders:bucket:1710000005",
			"nm:platform:5m:route-group:_api_pay:bucket:1710000005",
		},
		hash: []interface{}{
			"route_group", "/api/orders/*",
			"request_count", "6",
			"error_count", "2",
			"latency_count", "6",
			"latency_sum_ms", "180",
			"team_id", "team-a",
			"app_id", "app-a",
			"region_app_id", "region-app-a",
			"app_name", "订单系统",
			"team_name", "team-a",
			"team_alias", "研发团队",
			"region_name", "cn-east",
			"component_id", "svc-a",
		},
	}
	store := NewRedisStore(client)
	store.now = func() time.Time {
		return time.Unix(1710000010, 0)
	}

	items, err := store.ListApps(context.Background(), model.AggregateScope{Kind: model.ScopePlatform}, model.Window5m, 50, "throughput")
	if err != nil {
		t.Fatalf("ListApps() unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items length = %d; want 1", len(items))
	}
	if items[0].AppID != "app-a" || items[0].TeamID != "team-a" {
		t.Fatalf("app identity = %#v; want app-a/team-a", items[0])
	}
	if items[0].RegionAppID != "region-app-a" || items[0].AppName != "订单系统" {
		t.Fatalf("app display identity = %#v; want region-app-a and app name", items[0])
	}
	if items[0].TeamName != "team-a" || items[0].TeamAlias != "研发团队" || items[0].RegionName != "cn-east" {
		t.Fatalf("team display identity = %#v; want team and region display metadata", items[0])
	}
	if items[0].RequestCount != 12 {
		t.Fatalf("request count = %d; want 12", items[0].RequestCount)
	}
	if items[0].ErrorCount != 4 {
		t.Fatalf("error count = %d; want 4", items[0].ErrorCount)
	}
	if items[0].AvgLatencyMs != 30 {
		t.Fatalf("avg latency = %v; want 30", items[0].AvgLatencyMs)
	}
	if items[0].ThroughputPerSecond != 0.04 {
		t.Fatalf("throughput = %v; want 0.04", items[0].ThroughputPerSecond)
	}
}

func TestRedisStoreReturnsRouteGroupSnapshotMeta(t *testing.T) {
	client := &fakeRedisClient{}
	store := NewRedisStore(client)
	store.now = func() time.Time {
		return time.Unix(1710000012, 0)
	}

	if err := store.saveSnapshot(context.Background(), model.AggregateScope{Kind: model.ScopePlatform}, model.Window5m, "errors", nil); err != nil {
		t.Fatalf("saveSnapshot() unexpected error: %v", err)
	}
	meta, err := store.GetRouteGroupSnapshotMeta(context.Background(), model.AggregateScope{Kind: model.ScopePlatform}, model.Window5m, "errors")
	if err != nil {
		t.Fatalf("GetRouteGroupSnapshotMeta() unexpected error: %v", err)
	}
	if meta.Window != model.Window5m {
		t.Fatalf("window = %q; want 5m", meta.Window)
	}
	if meta.FreshnessSeconds != 0 || meta.Stale {
		t.Fatalf("fresh meta = %#v; want freshness 0 and stale false", meta)
	}

	store.now = func() time.Time {
		return time.Unix(1710000045, 0)
	}
	meta, err = store.GetRouteGroupSnapshotMeta(context.Background(), model.AggregateScope{Kind: model.ScopePlatform}, model.Window5m, "errors")
	if err != nil {
		t.Fatalf("GetRouteGroupSnapshotMeta() unexpected error: %v", err)
	}
	if meta.FreshnessSeconds != 33 || !meta.Stale {
		t.Fatalf("stale meta = %#v; want freshness 33 and stale true", meta)
	}
}

func TestRedisStoreSavesAndLoadsSLAConfig(t *testing.T) {
	client := &fakeRedisClient{}
	store := NewRedisStore(client)

	err := store.SaveSLAConfig(context.Background(), model.SLAConfig{AppID: "app-a", Target: 0.995})
	if err != nil {
		t.Fatalf("SaveSLAConfig() unexpected error: %v", err)
	}

	config, err := store.GetSLAConfig(context.Background(), "app-a", 0.999)
	if err != nil {
		t.Fatalf("GetSLAConfig() unexpected error: %v", err)
	}
	if config.Target != 0.995 {
		t.Fatalf("target = %v; want 0.995", config.Target)
	}
}

func TestRedisStoreReturnsDefaultSLAConfigWhenMissing(t *testing.T) {
	store := NewRedisStore(&fakeRedisClient{})

	config, err := store.GetSLAConfig(context.Background(), "app-a", 0.999)
	if err != nil {
		t.Fatalf("GetSLAConfig() unexpected error: %v", err)
	}
	if config.Target != 0.999 {
		t.Fatalf("target = %v; want default 0.999", config.Target)
	}
}

func TestRedisStoreSavesRouteGroupRules(t *testing.T) {
	client := &fakeRedisClient{}
	store := NewRedisStore(client)
	rules := []model.RouteGroupRule{
		{Prefix: "/api/orders/", Group: "/api/orders/*"},
	}

	if err := store.SaveRouteGroupRules(context.Background(), "app-a", rules); err != nil {
		t.Fatalf("SaveRouteGroupRules() unexpected error: %v", err)
	}
	got, err := store.GetRouteGroupRules(context.Background(), "app-a")
	if err != nil {
		t.Fatalf("GetRouteGroupRules() unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Group != "/api/orders/*" {
		t.Fatalf("rules = %#v", got)
	}
}

func TestRedisStoreIndexesPrometheusRoutesByApp(t *testing.T) {
	client := &fakeRedisClient{}
	store := NewRedisStore(client)

	if err := store.SaveRouteMapping(context.Background(), model.RouteMapping{
		RouteID:         "route-a",
		AppID:           "app-a",
		PrometheusRoute: "prom-route-a",
	}, time.Minute); err != nil {
		t.Fatalf("SaveRouteMapping() unexpected error: %v", err)
	}

	routes, err := store.GetAppPrometheusRoutes(context.Background(), "app-a")
	if err != nil {
		t.Fatalf("GetAppPrometheusRoutes() unexpected error: %v", err)
	}
	if len(routes) != 1 || routes[0] != "prom-route-a" {
		t.Fatalf("routes = %#v", routes)
	}
}

func TestRedisStoreReplacesAppPrometheusRoutes(t *testing.T) {
	client := &fakeRedisClient{
		sets: map[string]interface{}{
			"nm:app:app-a:prometheus-routes": []interface{}{"old-route"},
		},
	}
	store := NewRedisStore(client)

	err := store.ReplaceAppPrometheusRoutes(context.Background(), "app-a", []string{"route-a", "route-b"})
	if err != nil {
		t.Fatalf("ReplaceAppPrometheusRoutes() unexpected error: %v", err)
	}

	routes, err := store.GetAppPrometheusRoutes(context.Background(), "app-a")
	if err != nil {
		t.Fatalf("GetAppPrometheusRoutes() unexpected error: %v", err)
	}
	if len(routes) != 2 || routes[0] != "route-a" || routes[1] != "route-b" {
		t.Fatalf("routes = %#v; want route-a and route-b", routes)
	}
}
