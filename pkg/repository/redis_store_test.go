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
		f.sets[args[1]] = append(existing, args[2])
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
