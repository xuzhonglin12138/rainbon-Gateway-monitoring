package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
)

type fakeRedisClient struct {
	calls         [][]string
	batchCalls    [][][]string
	keys          []interface{}
	keysByPattern map[string][]interface{}
	zrangeByKey   map[string][]interface{}
	hash          []interface{}
	hashByKey     map[string][]interface{}
	get           interface{}
	members       []interface{}
	sets          map[string]interface{}
}

func (f *fakeRedisClient) DoBatch(ctx context.Context, commands ...[]string) ([]interface{}, error) {
	f.batchCalls = append(f.batchCalls, commands)
	values := make([]interface{}, 0, len(commands))
	for _, command := range commands {
		value, err := f.Do(ctx, command...)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (f *fakeRedisClient) Do(_ context.Context, args ...string) (interface{}, error) {
	f.calls = append(f.calls, args)
	switch args[0] {
	case "KEYS":
		if f.keysByPattern != nil {
			if value, ok := f.keysByPattern[args[1]]; ok {
				return value, nil
			}
		}
		return f.keys, nil
	case "ZRANGEBYSCORE":
		if f.zrangeByKey != nil {
			if value, ok := f.zrangeByKey[args[1]]; ok {
				return value, nil
			}
		}
		return []interface{}{}, nil
	case "HGETALL":
		if f.hashByKey != nil {
			if value, ok := f.hashByKey[args[1]]; ok {
				return value, nil
			}
		}
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
	case "SREM":
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

func TestRedisStoreSaveSLAConfigRegistersEnabledHealthCheck(t *testing.T) {
	client := &fakeRedisClient{}
	store := NewRedisStore(client)
	store.now = func() time.Time { return time.Unix(1710000000, 0) }

	err := store.SaveSLAConfig(context.Background(), model.SLAConfig{
		AppID:   "app-a",
		Enabled: true,
		URL:     "https://example.com/healthz",
	})
	if err != nil {
		t.Fatalf("SaveSLAConfig() unexpected error: %v", err)
	}

	var sawRegister bool
	raw, ok := client.sets["nm:app:app-a:sla-config"].(string)
	if !ok || raw == "" {
		t.Fatalf("saved config payload = %#v", client.sets["nm:app:app-a:sla-config"])
	}
	if !strings.Contains(raw, `"target":0.99`) || !strings.Contains(raw, `"interval_seconds":10`) {
		t.Fatalf("saved config payload = %s; want fixed defaults", raw)
	}
	for _, call := range client.calls {
		if len(call) == 3 && call[0] == "SADD" && call[1] == "nm:sla-health:apps" && call[2] == "app-a" {
			sawRegister = true
		}
	}
	if !sawRegister {
		t.Fatalf("expected SLA app registration, calls=%#v", client.calls)
	}
}

func TestRedisStoreRecordSLAHealthCheckUsesBatchAndRecentFailure(t *testing.T) {
	client := &fakeRedisClient{}
	store := NewRedisStore(client)

	err := store.RecordSLAHealthCheck(context.Background(), model.SLAHealthSample{
		AppID:      "app-a",
		CheckedAt:  1710000061,
		Success:    false,
		StatusCode: 500,
		LatencyMs:  25.5,
		ErrorType:  "status_code_5xx",
	})
	if err != nil {
		t.Fatalf("RecordSLAHealthCheck() unexpected error: %v", err)
	}
	if len(client.batchCalls) != 1 {
		t.Fatalf("batchCalls = %#v; want one batch", client.batchCalls)
	}
	commands := client.batchCalls[0]
	if len(commands) < 10 {
		t.Fatalf("commands = %#v; want bucket and recent failure commands", commands)
	}
	if commands[0][0] != "ZADD" || commands[0][1] != "nm:app:app-a:sla-health:index" || commands[0][2] != "1710000060" {
		t.Fatalf("first command = %#v; want ZADD minute index", commands[0])
	}
	var sawRecent bool
	for _, command := range commands {
		if len(command) >= 2 && command[0] == "LPUSH" && command[1] == "nm:app:app-a:sla-health:recent-failures" {
			sawRecent = true
		}
	}
	if !sawRecent {
		t.Fatalf("commands = %#v; want recent failure write", commands)
	}
}

func TestRedisStoreGetSLAHealthAggregateUsesTimeIndex(t *testing.T) {
	client := &fakeRedisClient{
		zrangeByKey: map[string][]interface{}{
			"nm:app:app-a:sla-health:index": {"1710000000", "1710000060"},
		},
		hashByKey: map[string][]interface{}{
			"nm:app:app-a:sla-health:bucket:1710000000": {
				"success_count", "4",
				"failure_count", "1",
				"latency_sum_ms", "100",
				"last_status_code", "200",
				"last_checked_at", "1710000050",
			},
			"nm:app:app-a:sla-health:bucket:1710000060": {
				"success_count", "5",
				"failure_count", "0",
				"latency_sum_ms", "150",
				"last_status_code", "204",
				"last_checked_at", "1710000068",
			},
		},
	}
	store := NewRedisStore(client)

	aggregate, err := store.GetSLAHealthAggregate(context.Background(), "app-a", time.Unix(1710000000, 0), time.Unix(1710000090, 0))
	if err != nil {
		t.Fatalf("GetSLAHealthAggregate() unexpected error: %v", err)
	}
	if aggregate.TotalChecks != 10 || aggregate.SuccessChecks != 9 || aggregate.FailureChecks != 1 {
		t.Fatalf("aggregate = %#v; want 9/10 success", aggregate)
	}
	if aggregate.LatencySumMs != 250 || aggregate.LastStatusCode != 204 || aggregate.LastCheckedAt != 1710000068 {
		t.Fatalf("aggregate metadata = %#v", aggregate)
	}
	if countCommand(client.calls, "KEYS") != 0 {
		t.Fatalf("calls = %#v; want no KEYS", client.calls)
	}
	if countCommand(client.calls, "ZRANGEBYSCORE") != 1 {
		t.Fatalf("calls = %#v; want ZRANGEBYSCORE", client.calls)
	}
}

func countCommand(calls [][]string, command string) int {
	count := 0
	for _, call := range calls {
		if len(call) > 0 && call[0] == command {
			count++
		}
	}
	return count
}

func stringArgsContainPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
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
		EgressBytes:        4096,
		AppID:              "app-a",
		TeamID:             "team-a",
		ComponentID:        "svc-a",
	})
	if err != nil {
		t.Fatalf("AddRouteGroupBucket() unexpected error: %v", err)
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

	var evalCall []string
	for _, call := range client.calls {
		if len(call) > 0 && call[0] == "EVAL" {
			evalCall = call
		}
	}
	if evalCall == nil {
		t.Fatalf("expected route group bucket Lua write, got %#v", client.calls)
	}
	if len(evalCall) < 8 {
		t.Fatalf("unexpected EVAL call: %#v", evalCall)
	}
	if evalCall[2] != "2" {
		t.Fatalf("EVAL key count = %s; want 2", evalCall[2])
	}
	if evalCall[3] != "nm:app:app-a:5m:route-group:/api/order/detail/*:bucket:1710000005" {
		t.Fatalf("EVAL bucket key = %s", evalCall[3])
	}
	if evalCall[4] != "nm:app:app-a:route-group-buckets" {
		t.Fatalf("EVAL index key = %s", evalCall[4])
	}
	if evalCall[5] != "1710000005" || evalCall[6] != "2100" {
		t.Fatalf("EVAL bucket metadata = %#v; want bucket=1710000005 ttl=2100", evalCall[5:7])
	}
	for _, pair := range [][2]string{
		{"request_count", "1"},
		{"egress_bytes", "4096"},
		{"route_group", "/api/order/detail/*"},
		{"app_id", "app-a"},
		{"component_id", "svc-a"},
	} {
		if !stringArgsContainPair(evalCall, pair[0], pair[1]) {
			t.Fatalf("EVAL args missing %s=%s: %#v", pair[0], pair[1], evalCall)
		}
	}
}

func TestRedisStoreAppBucketKeyStaysAggregatedByRoute(t *testing.T) {
	client := &fakeRedisClient{}
	store := NewRedisStore(client)
	first := model.RouteGroupMetric{
		RouteGroup:   "/api/ping",
		RequestCount: 30,
		LatencySumMs: 300,
		LatencyCount: 30,
		AppID:        "1023",
		ComponentID:  "gr1ea4bc",
	}
	second := model.RouteGroupMetric{
		RouteGroup:   "/api/ping",
		RequestCount: 40,
		LatencySumMs: 400,
		LatencyCount: 40,
		AppID:        "1023",
		ComponentID:  "gr707edd",
	}

	err := store.AddRouteGroupBucket(context.Background(), model.AggregateScope{
		Kind: model.ScopeApp,
		ID:   "1023",
	}, model.Window5m, 1710000005, first)
	if err != nil {
		t.Fatalf("AddRouteGroupBucket(first) unexpected error: %v", err)
	}
	err = store.AddRouteGroupBucket(context.Background(), model.AggregateScope{
		Kind: model.ScopeApp,
		ID:   "1023",
	}, model.Window5m, 1710000005, second)
	if err != nil {
		t.Fatalf("AddRouteGroupBucket(second) unexpected error: %v", err)
	}

	var bucketKeys []string
	for _, call := range client.calls {
		if len(call) > 3 && call[0] == "EVAL" {
			bucketKeys = append(bucketKeys, call[3])
		}
	}
	if len(bucketKeys) != 2 {
		t.Fatalf("bucket key writes = %#v; want two app-scope writes", bucketKeys)
	}
	if bucketKeys[0] != bucketKeys[1] {
		t.Fatalf("bucket keys = %#v; want same app route bucket to preserve app-level aggregation", bucketKeys)
	}
	if strings.Contains(bucketKeys[0], ":component:") {
		t.Fatalf("bucket key = %s; app-level key must not include component dimension", bucketKeys[0])
	}
}

func TestRedisStoreAddRouteGroupBucketSkipsEmptyStaticDisplayFields(t *testing.T) {
	client := &fakeRedisClient{}
	store := NewRedisStore(client)

	err := store.AddRouteGroupBucket(context.Background(), model.AggregateScope{
		Kind: model.ScopePlatform,
	}, model.Window5m, 1710000005, model.RouteGroupMetric{
		RouteGroup:   "/api/order/detail/*",
		RequestCount: 1,
		AppID:        "app-a",
		Namespace:    "team-ns",
		TeamAlias:    "",
		AppName:      "",
	})
	if err != nil {
		t.Fatalf("AddRouteGroupBucket() unexpected error: %v", err)
	}

	var evalCall []string
	for _, call := range client.calls {
		if len(call) > 0 && call[0] == "EVAL" {
			evalCall = call
		}
	}
	if evalCall == nil {
		t.Fatalf("expected route group bucket Lua write, got %#v", client.calls)
	}
	if stringArgsContainPair(evalCall, "team_alias", "") {
		t.Fatalf("EVAL args include empty team_alias static field: %#v", evalCall)
	}
	if stringArgsContainPair(evalCall, "app_name", "") {
		t.Fatalf("EVAL args include empty app_name static field: %#v", evalCall)
	}
	if !stringArgsContainPair(evalCall, "namespace", "team-ns") {
		t.Fatalf("EVAL args = %#v; want non-empty namespace static field", evalCall)
	}
}

func TestRedisStoreRefreshRouteGroupSnapshotsFiltersBucketsByWindow(t *testing.T) {
	client := &fakeRedisClient{
		members: []interface{}{"platform"},
		zrangeByKey: map[string][]interface{}{
			"nm:platform:route-group-buckets": {
				"nm:platform:5m:route-group:_api_old:bucket:1709999000",
				"nm:platform:5m:route-group:_api_new:bucket:1710000005",
			},
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

	var sawSnapshot bool
	for _, call := range client.calls {
		if len(call) >= 5 && call[0] == "SET" && call[1] == "nm:platform:5m:route-groups:summary" && call[3] == "EX" && call[4] == "120" {
			sawSnapshot = true
		}
		if call[0] == "KEYS" {
			t.Fatalf("RefreshRouteGroupSnapshots should use route bucket indexes without KEYS, got %#v", client.calls)
		}
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
		zrangeByKey: map[string][]interface{}{
			"nm:app:app-a:route-group-buckets": {
				"nm:app:app-a:5m:route-group:_api_orders:bucket:1710000005",
				"nm:app:app-a:5m:route-group:_api_pay:bucket:1710000005",
			},
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
	if len(client.batchCalls) == 0 {
		t.Fatalf("expected bucket HGETALL commands to be pipelined, got calls %#v", client.calls)
	}
}

func TestRedisStoreAppComponentSummariesUseComponentScopesWhenAppBucketMergesSameRoute(t *testing.T) {
	client := &fakeRedisClient{
		zrangeByKey: map[string][]interface{}{
			"nm:app:1023:route-group-buckets": {
				"nm:app:1023:5m:route-group:_api_ping:bucket:1710000005",
			},
			"nm:component:gr1ea4bc:route-group-buckets": {
				"nm:component:gr1ea4bc:5m:route-group:_api_ping:bucket:1710000005",
			},
			"nm:component:gr707edd:route-group-buckets": {
				"nm:component:gr707edd:5m:route-group:_api_ping:bucket:1710000005",
			},
		},
		hashByKey: map[string][]interface{}{
			"nm:app:1023:5m:route-group:_api_ping:bucket:1710000005": {
				"route_group", "/api/ping",
				"request_count", "70",
				"latency_count", "70",
				"latency_sum_ms", "700",
				"app_id", "1023",
				"component_id", "gr1ea4bc",
				"service_alias", "gr1ea4bc",
			},
			"nm:component:gr1ea4bc:5m:route-group:_api_ping:bucket:1710000005": {
				"route_group", "/api/ping",
				"request_count", "30",
				"latency_count", "30",
				"latency_sum_ms", "300",
				"app_id", "1023",
				"component_id", "gr1ea4bc",
				"service_alias", "gr1ea4bc",
			},
			"nm:component:gr707edd:5m:route-group:_api_ping:bucket:1710000005": {
				"route_group", "/api/ping",
				"request_count", "40",
				"latency_count", "40",
				"latency_sum_ms", "400",
				"app_id", "1023",
				"component_id", "gr707edd",
				"service_alias", "gr707edd",
			},
		},
		sets: map[string]interface{}{
			scopeRegistryKey: []interface{}{
				"component:gr1ea4bc",
				"component:gr707edd",
			},
		},
	}
	store := NewRedisStore(client)
	store.now = func() time.Time {
		return time.Unix(1710000010, 0)
	}

	items, err := store.ListAppComponentSummaries(context.Background(), "1023", model.Window5m, 50)
	if err != nil {
		t.Fatalf("ListAppComponentSummaries() unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items length = %d; want summaries for both components", len(items))
	}
	countByComponent := map[string]int64{}
	for _, item := range items {
		countByComponent[item.ComponentID] = item.RequestCount
	}
	if countByComponent["gr1ea4bc"] != 30 || countByComponent["gr707edd"] != 40 {
		t.Fatalf("component counts = %#v; want gr1ea4bc=30 and gr707edd=40", countByComponent)
	}
}

func TestRedisStoreListAppsReadsSnapshotForPlatformScope(t *testing.T) {
	client := &fakeRedisClient{
		sets: map[string]interface{}{
			appTrafficSnapshotKey(model.AggregateScope{Kind: model.ScopePlatform}, model.Window5m, "throughput"): `[{"app_id":"app-a","name":"orders","request_count":10,"error_count":2,"avg_latency_ms":30,"throughput_per_second":0.03333333333333333}]`,
		},
		zrangeByKey: map[string][]interface{}{
			"nm:app:app-a:route-group-buckets": {
				"nm:app:app-a:5m:route-group:_api_orders:bucket:1710000005",
			},
		},
		hashByKey: map[string][]interface{}{
			"nm:app:app-a:5m:route-group:_api_orders:bucket:1710000005": {
				"route_group", "/api/orders/*",
				"request_count", "999",
				"app_id", "app-a",
			},
		},
	}
	store := NewRedisStore(client)

	items, err := store.ListApps(context.Background(), model.AggregateScope{Kind: model.ScopePlatform}, model.Window5m, 50, "throughput")
	if err != nil {
		t.Fatalf("ListApps() unexpected error: %v", err)
	}
	if len(items) != 1 || items[0].RequestCount != 10 {
		t.Fatalf("items = %#v; want snapshot result", items)
	}
	if countCommand(client.calls, "ZRANGEBYSCORE") != 0 {
		t.Fatalf("ListApps platform scope should read app traffic snapshot without scanning buckets, got %#v", client.calls)
	}
}

func TestRedisStoreListsAppsFromHotBuckets(t *testing.T) {
	client := &fakeRedisClient{
		members: []interface{}{"app:app-a", "app:unknown_app"},
		zrangeByKey: map[string][]interface{}{
			"nm:app:app-a:route-group-buckets": []interface{}{
				"nm:app:app-a:5m:route-group:_api_orders:bucket:1710000005",
				"nm:app:app-a:5m:route-group:_api_pay:bucket:1710000005",
			},
			"nm:app:unknown_app:route-group-buckets": []interface{}{
				"nm:app:unknown_app:5m:route-group:_api_unmapped:bucket:1710000005",
			},
		},
		hashByKey: map[string][]interface{}{
			"nm:app:app-a:5m:route-group:_api_orders:bucket:1710000005": {
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
			"nm:app:app-a:5m:route-group:_api_pay:bucket:1710000005": {
				"route_group", "/api/pay/*",
				"request_count", "4",
				"error_count", "3",
				"latency_count", "4",
				"latency_sum_ms", "320",
				"team_id", "team-a",
				"app_id", "app-a",
				"region_app_id", "region-app-a",
				"app_name", "订单系统",
				"team_name", "team-a",
				"team_alias", "研发团队",
				"region_name", "cn-east",
				"component_id", "svc-a",
			},
			"nm:app:unknown_app:5m:route-group:_api_unmapped:bucket:1710000005": {
				"route_group", "/api/unmapped/*",
				"request_count", "100",
				"error_count", "50",
				"latency_count", "100",
				"latency_sum_ms", "10000",
				"team_id", "unknown_team",
				"app_id", "unknown_app",
				"component_id", "unknown_component",
			},
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
	if items[0].RequestCount != 10 {
		t.Fatalf("request count = %d; want 10", items[0].RequestCount)
	}
	if items[0].ErrorCount != 5 {
		t.Fatalf("error count = %d; want 5", items[0].ErrorCount)
	}
	if items[0].AvgLatencyMs != 50 {
		t.Fatalf("avg latency = %v; want 50", items[0].AvgLatencyMs)
	}
	if items[0].ThroughputPerSecond != float64(10)/model.Window5m.Duration().Seconds() {
		t.Fatalf("throughput = %v; want %v", items[0].ThroughputPerSecond, float64(10)/model.Window5m.Duration().Seconds())
	}
	if items[0].TopErrorRouteGroup != "/api/pay/*" || items[0].TopErrorRouteErrors != 3 {
		t.Fatalf("top error route = %q/%d; want /api/pay/*/3", items[0].TopErrorRouteGroup, items[0].TopErrorRouteErrors)
	}
	if items[0].TopLatencyRouteGroup != "/api/pay/*" || items[0].TopLatencyRouteAvgMs != 80 {
		t.Fatalf("top latency route = %q/%v; want /api/pay/*/80", items[0].TopLatencyRouteGroup, items[0].TopLatencyRouteAvgMs)
	}
}

func TestRedisStoreListAppsCanonicalizesRegionAppID(t *testing.T) {
	client := &fakeRedisClient{
		members: []interface{}{"app:region-app-a"},
		zrangeByKey: map[string][]interface{}{
			"nm:app:region-app-a:route-group-buckets": []interface{}{
				"nm:app:region-app-a:5m:route-group:_api_pay:bucket:1710000005",
			},
		},
		hashByKey: map[string][]interface{}{
			"nm:app:region-app-a:5m:route-group:_api_pay:bucket:1710000005": {
				"route_group", "/api/pay/*",
				"request_count", "7",
				"latency_count", "7",
				"latency_sum_ms", "140",
				"app_id", "region-app-a",
				"region_app_id", "region-app-a",
				"team_id", "team-ns",
			},
		},
		sets: map[string]interface{}{
			appCanonicalKey("region-app-a"): "1023",
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
	if items[0].AppID != "1023" || items[0].RegionAppID != "region-app-a" {
		t.Fatalf("app ids = %#v; want console app id with region app id retained", items[0])
	}
}

func TestRedisStoreListAppsUsesPlatformRouteBucketsWithoutScanningAppScopes(t *testing.T) {
	client := &fakeRedisClient{
		members: []interface{}{"platform", "app:app-a", "app:app-b"},
		zrangeByKey: map[string][]interface{}{
			"nm:app:app-a:route-group-buckets": []interface{}{
				"nm:app:app-a:5m:route-group:_api_same:bucket:1710000005",
				"nm:app:app-a:5m:route-group:_api_same:bucket:1709999100",
			},
			"nm:app:app-b:route-group-buckets": []interface{}{
				"nm:app:app-b:5m:route-group:_api_same:bucket:1710000005",
				"nm:app:app-b:5m:route-group:_api_same:bucket:1709999100",
			},
			"nm:platform:route-group-buckets": []interface{}{
				"nm:platform:5m:route-group:_api_a:bucket:1710000005",
				"nm:platform:5m:route-group:_api_b:bucket:1710000005",
				"nm:platform:5m:route-group:_api_b_old:bucket:1709999100",
			},
		},
		hashByKey: map[string][]interface{}{
			"nm:platform:5m:route-group:_api_a:bucket:1710000005": {
				"route_group", "/api/a",
				"request_count", "10",
				"latency_count", "10",
				"latency_sum_ms", "100",
				"app_id", "app-a",
				"team_id", "team-a",
			},
			"nm:platform:5m:route-group:_api_b:bucket:1710000005": {
				"route_group", "/api/b",
				"request_count", "20",
				"latency_count", "20",
				"latency_sum_ms", "400",
				"app_id", "app-b",
				"team_id", "team-b",
			},
			"nm:platform:5m:route-group:_api_b_old:bucket:1709999100": {
				"route_group", "/api/b-old",
				"request_count", "20",
				"latency_count", "20",
				"latency_sum_ms", "400",
				"app_id", "app-b",
				"team_id", "team-b",
			},
			"nm:app:app-a:5m:route-group:_api_same:bucket:1710000005": {
				"route_group", "/api/same",
				"request_count", "999",
				"latency_count", "10",
				"latency_sum_ms", "100",
				"app_id", "app-a",
				"team_id", "team-a",
			},
			"nm:app:app-b:5m:route-group:_api_same:bucket:1710000005": {
				"route_group", "/api/same",
				"request_count", "20",
				"latency_count", "20",
				"latency_sum_ms", "400",
				"app_id", "app-b",
				"team_id", "team-b",
			},
			"nm:app:app-a:5m:route-group:_api_same:bucket:1709999100": {
				"route_group", "/api/same",
				"request_count", "20",
				"latency_count", "20",
				"latency_sum_ms", "200",
				"app_id", "app-a",
				"team_id", "team-a",
			},
			"nm:app:app-b:5m:route-group:_api_same:bucket:1709999100": {
				"route_group", "/api/same",
				"request_count", "20",
				"latency_count", "20",
				"latency_sum_ms", "400",
				"app_id", "app-b",
				"team_id", "team-b",
			},
		},
	}
	store := NewRedisStore(client)
	store.now = func() time.Time {
		return time.Unix(1710000010, 0)
	}

	items5m, err := store.ListApps(context.Background(), model.AggregateScope{Kind: model.ScopePlatform}, model.Window5m, 50, "throughput")
	if err != nil {
		t.Fatalf("ListApps(5m) unexpected error: %v", err)
	}
	if len(items5m) != 2 {
		t.Fatalf("5m items length = %d; want 2", len(items5m))
	}
	if items5m[0].AppID != "app-b" || items5m[0].RequestCount != 20 {
		t.Fatalf("5m top item = %#v; want app-b with 20 requests from platform scope", items5m[0])
	}
	items30m, err := store.ListApps(context.Background(), model.AggregateScope{Kind: model.ScopePlatform}, model.Window30m, 50, "throughput")
	if err != nil {
		t.Fatalf("ListApps(30m) unexpected error: %v", err)
	}
	if len(items30m) != 2 {
		t.Fatalf("30m items length = %d; want 2", len(items30m))
	}
	if items30m[0].AppID != "app-b" || items30m[0].RequestCount != 40 {
		t.Fatalf("30m top item = %#v; want app-b with current and older raw buckets from selected window", items30m[0])
	}
	if countCommand(client.calls, "SMEMBERS") != 0 {
		t.Fatalf("ListApps platform scope should not scan registered app scopes, calls=%#v", client.calls)
	}
}

func TestRedisStoreListAppsRequestRankingUsesOnlySelectedWindowBuckets(t *testing.T) {
	client := &fakeRedisClient{
		zrangeByKey: map[string][]interface{}{
			"nm:platform:route-group-buckets": {
				"nm:platform:5m:route-group:_api_old:bucket:1709999400",
				"nm:platform:5m:route-group:_api_current:bucket:1710000000",
				"nm:platform:5m:route-group:_api_future:bucket:1710000360",
			},
		},
		hashByKey: map[string][]interface{}{
			"nm:platform:5m:route-group:_api_old:bucket:1709999400": {
				"route_group", "/api/old",
				"request_count", "1000",
				"latency_count", "1000",
				"latency_sum_ms", "1000",
				"app_id", "app-a",
			},
			"nm:platform:5m:route-group:_api_current:bucket:1710000000": {
				"route_group", "/api/current",
				"request_count", "200",
				"latency_count", "200",
				"latency_sum_ms", "2000",
				"app_id", "app-a",
			},
			"nm:platform:5m:route-group:_api_future:bucket:1710000360": {
				"route_group", "/api/future",
				"request_count", "5000",
				"latency_count", "5000",
				"latency_sum_ms", "5000",
				"app_id", "app-a",
			},
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
	if items[0].RequestCount != 200 {
		t.Fatalf("request ranking count = %d; want only requests inside the selected 5m window", items[0].RequestCount)
	}
	if items[0].TopErrorRouteGroup == "/api/old" || items[0].TopLatencyRouteGroup == "/api/future" {
		t.Fatalf("top routes = %#v; want out-of-window buckets excluded", items[0])
	}
}

func TestRedisStoreListAppsAtUsesExplicitWindowEndTime(t *testing.T) {
	client := &fakeRedisClient{
		zrangeByKey: map[string][]interface{}{
			"nm:platform:route-group-buckets": {
				"nm:platform:5m:route-group:_api_inside:bucket:1710000000",
				"nm:platform:5m:route-group:_api_after_end:bucket:1710000005",
			},
		},
		hashByKey: map[string][]interface{}{
			"nm:platform:5m:route-group:_api_inside:bucket:1710000000": {
				"route_group", "/api/inside",
				"request_count", "70",
				"latency_count", "70",
				"latency_sum_ms", "700",
				"app_id", "app-a",
			},
			"nm:platform:5m:route-group:_api_after_end:bucket:1710000005": {
				"route_group", "/api/after-end",
				"request_count", "900",
				"latency_count", "900",
				"latency_sum_ms", "9000",
				"app_id", "app-a",
			},
		},
	}
	store := NewRedisStore(client)
	store.now = func() time.Time {
		return time.Unix(1710000010, 0)
	}

	items, err := store.ListAppsAt(context.Background(), model.AggregateScope{Kind: model.ScopePlatform}, model.Window5m, time.Unix(1710000000, 0), 50, "throughput")
	if err != nil {
		t.Fatalf("ListAppsAt() unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items length = %d; want 1", len(items))
	}
	if items[0].RequestCount != 70 {
		t.Fatalf("request count = %d; want only buckets up to explicit end_time", items[0].RequestCount)
	}
	if items[0].TopLatencyRouteGroup == "/api/after-end" {
		t.Fatalf("top routes = %#v; want buckets after explicit end_time excluded", items[0])
	}
}

func TestRedisStoreListAppsDeduplicatesCanonicalAliasBuckets(t *testing.T) {
	client := &fakeRedisClient{
		zrangeByKey: map[string][]interface{}{
			"nm:platform:route-group-buckets": {
				"nm:platform:5m:route-group:_api_ping:bucket:1710000005",
			},
		},
		hashByKey: map[string][]interface{}{
			"nm:platform:5m:route-group:_api_ping:bucket:1710000005": {
				"route_group", "/api/ping",
				"request_count", "50",
				"latency_count", "50",
				"latency_sum_ms", "500",
				"app_id", "1023",
				"region_app_id", "region-app-a",
				"team_id", "team-a",
			},
		},
		sets: map[string]interface{}{
			appAliasesKey("1023"):           []interface{}{"region-app-a"},
			appCanonicalKey("region-app-a"): "1023",
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
	if items[0].AppID != "1023" || items[0].RequestCount != 50 {
		t.Fatalf("item = %#v; want canonical app counted once", items[0])
	}
}

func TestRedisStoreListRouteGroupsForAppIncludesRegionAliasBuckets(t *testing.T) {
	client := &fakeRedisClient{
		zrangeByKey: map[string][]interface{}{
			"nm:app:1023:route-group-buckets": []interface{}{
				"nm:app:1023:5m:route-group:_api_pay:bucket:1710000005",
			},
			"nm:app:region-app-a:route-group-buckets": []interface{}{
				"nm:app:region-app-a:5m:route-group:_api_pay:bucket:1710000010",
			},
		},
		hashByKey: map[string][]interface{}{
			"nm:app:region-app-a:5m:route-group:_api_pay:bucket:1710000010": {
				"route_group", "/api/pay/*",
				"request_count", "5",
				"error_count", "2",
				"latency_count", "5",
				"latency_sum_ms", "250",
				"app_id", "region-app-a",
				"region_app_id", "region-app-a",
			},
			"nm:app:1023:5m:route-group:_api_pay:bucket:1710000005": {
				"route_group", "/api/pay/*",
				"request_count", "3",
				"latency_count", "3",
				"latency_sum_ms", "60",
				"app_id", "1023",
				"region_app_id", "region-app-a",
			},
		},
		sets: map[string]interface{}{
			appAliasesKey("1023"): []interface{}{"region-app-a"},
		},
	}
	store := NewRedisStore(client)
	store.now = func() time.Time {
		return time.Unix(1710000010, 0)
	}

	items, err := store.ListRouteGroups(context.Background(), model.AggregateScope{Kind: model.ScopeApp, ID: "1023"}, model.Window5m, 50, "requests")
	if err != nil {
		t.Fatalf("ListRouteGroups() unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items length = %d; want 1", len(items))
	}
	if items[0].RequestCount != 8 || items[0].ErrorCount != 2 {
		t.Fatalf("item = %#v; want merged console and region app buckets", items[0])
	}
	if items[0].AvgLatencyMs != 38.75 {
		t.Fatalf("avg latency = %v; want 38.75", items[0].AvgLatencyMs)
	}
}

func TestRedisStoreListRouteGroupsForAppDeduplicatesCanonicalAliasBuckets(t *testing.T) {
	client := &fakeRedisClient{
		zrangeByKey: map[string][]interface{}{
			"nm:app:1023:route-group-buckets": {
				"nm:app:1023:5m:route-group:_api_ping:bucket:1710000005",
			},
			"nm:app:region-app-a:route-group-buckets": {
				"nm:app:region-app-a:5m:route-group:_api_ping:bucket:1710000005",
			},
		},
		hashByKey: map[string][]interface{}{
			"nm:app:1023:5m:route-group:_api_ping:bucket:1710000005": {
				"route_group", "/api/ping",
				"request_count", "50",
				"latency_count", "50",
				"latency_sum_ms", "500",
				"app_id", "1023",
				"region_app_id", "region-app-a",
			},
			"nm:app:region-app-a:5m:route-group:_api_ping:bucket:1710000005": {
				"route_group", "/api/ping",
				"request_count", "50",
				"latency_count", "50",
				"latency_sum_ms", "500",
				"app_id", "region-app-a",
				"region_app_id", "region-app-a",
			},
		},
		sets: map[string]interface{}{
			appAliasesKey("1023"):           []interface{}{"region-app-a"},
			appCanonicalKey("region-app-a"): "1023",
		},
	}
	store := NewRedisStore(client)
	store.now = func() time.Time {
		return time.Unix(1710000010, 0)
	}

	items, err := store.ListRouteGroups(context.Background(), model.AggregateScope{Kind: model.ScopeApp, ID: "1023"}, model.Window5m, 50, "requests")
	if err != nil {
		t.Fatalf("ListRouteGroups() unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items length = %d; want 1", len(items))
	}
	if items[0].RequestCount != 50 {
		t.Fatalf("request count = %d; want canonical alias duplicate counted once", items[0].RequestCount)
	}
}

func TestRedisStoreListRouteGroupsForAppRequestRankingUsesOnlySelectedWindowBuckets(t *testing.T) {
	client := &fakeRedisClient{
		zrangeByKey: map[string][]interface{}{
			"nm:app:app-a:route-group-buckets": {
				"nm:app:app-a:5m:route-group:_api_old:bucket:1709999400",
				"nm:app:app-a:5m:route-group:_api_current:bucket:1710000000",
				"nm:app:app-a:5m:route-group:_api_future:bucket:1710000360",
			},
		},
		hashByKey: map[string][]interface{}{
			"nm:app:app-a:5m:route-group:_api_old:bucket:1709999400": {
				"route_group", "/api/old",
				"request_count", "1000",
				"latency_count", "1000",
				"latency_sum_ms", "1000",
				"app_id", "app-a",
			},
			"nm:app:app-a:5m:route-group:_api_current:bucket:1710000000": {
				"route_group", "/api/current",
				"request_count", "200",
				"latency_count", "200",
				"latency_sum_ms", "2000",
				"app_id", "app-a",
			},
			"nm:app:app-a:5m:route-group:_api_future:bucket:1710000360": {
				"route_group", "/api/future",
				"request_count", "5000",
				"latency_count", "5000",
				"latency_sum_ms", "5000",
				"app_id", "app-a",
			},
		},
	}
	store := NewRedisStore(client)
	store.now = func() time.Time {
		return time.Unix(1710000010, 0)
	}

	items, err := store.ListRouteGroups(context.Background(), model.AggregateScope{Kind: model.ScopeApp, ID: "app-a"}, model.Window5m, 50, "requests")
	if err != nil {
		t.Fatalf("ListRouteGroups() unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items length = %d; want 1", len(items))
	}
	if items[0].RouteGroup != "/api/current" || items[0].RequestCount != 200 {
		t.Fatalf("request ranking item = %#v; want only selected-window route", items[0])
	}
}

func TestRedisStoreListsRouteGroupBucketPoints(t *testing.T) {
	client := &fakeRedisClient{
		zrangeByKey: map[string][]interface{}{
			"nm:component:svc-a:route-group-buckets": {
				"nm:component:svc-a:5m:route-group:_api_ping:bucket:1710000005",
				"nm:component:svc-a:5m:route-group:_api_order:bucket:1710000005",
				"nm:component:svc-a:5m:route-group:_api_ping:bucket:1710000010",
			},
		},
		hashByKey: map[string][]interface{}{
			"nm:component:svc-a:5m:route-group:_api_ping:bucket:1710000005": {
				"route_group", "/api/ping",
				"request_count", "2",
				"error_count", "1",
				"latency_count", "2",
				"latency_sum_ms", "40",
				"egress_bytes", "200",
			},
			"nm:component:svc-a:5m:route-group:_api_order:bucket:1710000005": {
				"route_group", "/api/order",
				"request_count", "3",
				"latency_count", "3",
				"latency_sum_ms", "90",
				"egress_bytes", "300",
			},
			"nm:component:svc-a:5m:route-group:_api_ping:bucket:1710000010": {
				"route_group", "/api/ping",
				"request_count", "4",
				"error_count", "2",
				"latency_count", "4",
				"latency_sum_ms", "200",
				"egress_bytes", "800",
			},
		},
	}
	store := NewRedisStore(client)
	store.now = func() time.Time {
		return time.Unix(1710000015, 0)
	}

	points, err := store.ListRouteGroupBucketPoints(context.Background(), model.AggregateScope{Kind: model.ScopeComponent, ID: "svc-a"}, model.Window5m)
	if err != nil {
		t.Fatalf("ListRouteGroupBucketPoints() unexpected error: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("points length = %d; want 2", len(points))
	}
	if points[0].Timestamp != 1710000005 || points[0].Metric.RequestCount != 5 || points[0].Metric.ErrorCount != 1 {
		t.Fatalf("first point = %#v", points[0])
	}
	if points[0].Metric.EgressBytes != 500 {
		t.Fatalf("first egress bytes = %d; want 500", points[0].Metric.EgressBytes)
	}
	if points[1].Timestamp != 1710000010 || points[1].Metric.RequestCount != 4 || points[1].Metric.ErrorCount != 2 {
		t.Fatalf("second point = %#v", points[1])
	}
	if points[1].Metric.EgressBytes != 800 {
		t.Fatalf("second egress bytes = %d; want 800", points[1].Metric.EgressBytes)
	}
}

func TestRedisStoreListRouteGroupBucketPointsForAppDeduplicatesCanonicalAliasScopes(t *testing.T) {
	client := &fakeRedisClient{
		zrangeByKey: map[string][]interface{}{
			"nm:app:1023:route-group-buckets": {
				"nm:app:1023:5m:route-group:_api_ping:bucket:1710000005",
			},
			"nm:app:region-app-a:route-group-buckets": {
				"nm:app:region-app-a:5m:route-group:_api_ping:bucket:1710000005",
			},
		},
		hashByKey: map[string][]interface{}{
			"nm:app:1023:5m:route-group:_api_ping:bucket:1710000005": {
				"route_group", "/api/ping",
				"request_count", "50",
				"latency_count", "50",
				"latency_sum_ms", "500",
				"app_id", "1023",
				"region_app_id", "region-app-a",
			},
			"nm:app:region-app-a:5m:route-group:_api_ping:bucket:1710000005": {
				"route_group", "/api/ping",
				"request_count", "50",
				"latency_count", "50",
				"latency_sum_ms", "500",
				"app_id", "region-app-a",
				"region_app_id", "region-app-a",
			},
		},
		sets: map[string]interface{}{
			appAliasesKey("1023"):           []interface{}{"region-app-a"},
			appCanonicalKey("region-app-a"): "1023",
		},
	}
	store := NewRedisStore(client)
	store.now = func() time.Time {
		return time.Unix(1710000010, 0)
	}

	points, err := store.ListRouteGroupBucketPoints(context.Background(), model.AggregateScope{Kind: model.ScopeApp, ID: "1023"}, model.Window5m)
	if err != nil {
		t.Fatalf("ListRouteGroupBucketPoints() unexpected error: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("points length = %d; want 1", len(points))
	}
	if points[0].Metric.RequestCount != 50 {
		t.Fatalf("request count = %d; want canonical alias duplicate counted once", points[0].Metric.RequestCount)
	}
}

func TestRedisStoreListRouteGroupBucketPointsReadsRawBucketsForEveryWindow(t *testing.T) {
	client := &fakeRedisClient{
		zrangeByKey: map[string][]interface{}{
			"nm:app:app-a:route-group-buckets": {
				"nm:app:app-a:5m:route-group:_api_ping:bucket:1710000005",
			},
		},
		hashByKey: map[string][]interface{}{
			"nm:app:app-a:5m:route-group:_api_ping:bucket:1710000005": {
				"route_group", "/api/ping",
				"request_count", "70",
				"latency_count", "70",
				"latency_sum_ms", "700",
				"app_id", "app-a",
			},
		},
	}
	store := NewRedisStore(client)
	store.now = func() time.Time {
		return time.Unix(1710000010, 0)
	}

	points5m, err := store.ListRouteGroupBucketPoints(context.Background(), model.AggregateScope{Kind: model.ScopeApp, ID: "app-a"}, model.Window5m)
	if err != nil {
		t.Fatalf("ListRouteGroupBucketPoints(5m) unexpected error: %v", err)
	}
	points10m, err := store.ListRouteGroupBucketPoints(context.Background(), model.AggregateScope{Kind: model.ScopeApp, ID: "app-a"}, model.Window10m)
	if err != nil {
		t.Fatalf("ListRouteGroupBucketPoints(10m) unexpected error: %v", err)
	}
	if len(points5m) != 1 || len(points10m) != 1 {
		t.Fatalf("points length 5m=%d 10m=%d; want both windows to read the same raw bucket", len(points5m), len(points10m))
	}
	if points5m[0].Timestamp != points10m[0].Timestamp || points5m[0].Metric.RequestCount != points10m[0].Metric.RequestCount {
		t.Fatalf("points differ: 5m=%#v 10m=%#v; want same raw bucket value", points5m[0], points10m[0])
	}
	for _, call := range client.calls {
		if call[0] == "KEYS" {
			t.Fatalf("ListRouteGroupBucketPoints used KEYS: %#v", client.calls)
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

	err := store.SaveSLAConfig(context.Background(), model.SLAConfig{AppID: "app-a", Enabled: true, URL: "https://example.com/healthz", Target: 0.995})
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
	if !config.Enabled || config.URL != "https://example.com/healthz" {
		t.Fatalf("health config = %#v; want enabled URL", config)
	}
	if config.IntervalSeconds != 10 || config.TimeoutSeconds != 3 || config.SuccessStatusMin != 200 || config.SuccessStatusMax != 399 {
		t.Fatalf("fixed health defaults = %#v", config)
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
	if config.Enabled || config.URL != "" {
		t.Fatalf("default health config = %#v; want disabled empty URL", config)
	}
	if config.IntervalSeconds != 10 || config.TimeoutSeconds != 3 {
		t.Fatalf("default timing = %#v; want fixed health defaults", config)
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

func TestRedisStoreIndexesRegionAppAliasByConsoleApp(t *testing.T) {
	client := &fakeRedisClient{}
	store := NewRedisStore(client)

	err := store.SaveRouteMapping(context.Background(), model.RouteMapping{
		RouteID:         "route-a",
		AppID:           "1023",
		RegionAppID:     "region-app-a",
		PrometheusRoute: "prom-route-a",
	}, 0)
	if err != nil {
		t.Fatalf("SaveRouteMapping() unexpected error: %v", err)
	}
	aliases, err := store.appScopes(context.Background(), "1023")
	if err != nil {
		t.Fatalf("appScopes() unexpected error: %v", err)
	}
	if len(aliases) != 2 || aliases[0].ID != "1023" || aliases[1].ID != "region-app-a" {
		t.Fatalf("app scopes = %#v; want console and region app ids", aliases)
	}
	canonical, err := store.appScopes(context.Background(), "region-app-a")
	if err != nil {
		t.Fatalf("appScopes(region) unexpected error: %v", err)
	}
	if len(canonical) < 2 || canonical[0].ID != "region-app-a" || canonical[1].ID != "1023" {
		t.Fatalf("canonical app scopes = %#v; want region and console app ids", canonical)
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
	platformRoutes, err := store.GetPlatformPrometheusRoutes(context.Background())
	if err != nil {
		t.Fatalf("GetPlatformPrometheusRoutes() unexpected error: %v", err)
	}
	if len(platformRoutes) != 2 || platformRoutes[0] != "route-a" || platformRoutes[1] != "route-b" {
		t.Fatalf("platform routes = %#v; want route-a and route-b", platformRoutes)
	}
}

func TestRedisStoreSaveRouteMappingIndexesPlatformPrometheusRoute(t *testing.T) {
	client := &fakeRedisClient{}
	store := NewRedisStore(client)

	err := store.SaveRouteMapping(context.Background(), model.RouteMapping{
		RouteID:         "route-id",
		AppID:           "app-a",
		PrometheusRoute: "route-a",
	}, time.Minute)
	if err != nil {
		t.Fatalf("SaveRouteMapping() unexpected error: %v", err)
	}
	routes, err := store.GetPlatformPrometheusRoutes(context.Background())
	if err != nil {
		t.Fatalf("GetPlatformPrometheusRoutes() unexpected error: %v", err)
	}
	if len(routes) != 1 || routes[0] != "route-a" {
		t.Fatalf("platform routes = %#v; want route-a", routes)
	}
}
