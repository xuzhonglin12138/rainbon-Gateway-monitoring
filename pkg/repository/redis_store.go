package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
)

const bucketTTLSeconds = 35 * 60
const snapshotTTLSeconds = 120
const scopeRegistryKey = "nm:route-group:scopes"

type CommandClient interface {
	Do(ctx context.Context, args ...string) (interface{}, error)
}

type RedisStore struct {
	client CommandClient
	now    func() time.Time
}

func NewRedisStore(client CommandClient) *RedisStore {
	return &RedisStore{
		client: client,
		now:    time.Now,
	}
}

func (s *RedisStore) AddRouteGroupBucket(ctx context.Context, scope model.AggregateScope, window model.Window, bucketUnix int64, metric model.RouteGroupMetric) error {
	if _, err := s.client.Do(ctx, "SADD", scopeRegistryKey, scope.RedisPart()); err != nil {
		return err
	}
	key := routeGroupBucketKey(scope, window, metric.RouteGroup, bucketUnix)
	updates := []struct {
		field string
		value float64
	}{
		{"request_count", float64(metric.RequestCount)},
		{"error_count", float64(metric.ErrorCount)},
		{"upstream_error_count", float64(metric.UpstreamErrorCount)},
		{"latency_sum_ms", metric.LatencySumMs},
		{"latency_count", float64(metric.LatencyCount)},
	}
	for _, update := range updates {
		if update.value == 0 {
			continue
		}
		if _, err := s.client.Do(ctx, "HINCRBYFLOAT", key, update.field, strconv.FormatFloat(update.value, 'f', -1, 64)); err != nil {
			return err
		}
	}
	static := []string{
		"route_group", metric.RouteGroup,
		"team_id", metric.TeamID,
		"app_id", metric.AppID,
		"component_id", metric.ComponentID,
	}
	if _, err := s.client.Do(ctx, append([]string{"HSET", key}, static...)...); err != nil {
		return err
	}
	_, err := s.client.Do(ctx, "EXPIRE", key, strconv.Itoa(bucketTTLSeconds))
	return err
}

func (s *RedisStore) ListRouteGroups(ctx context.Context, scope model.AggregateScope, window model.Window, limit int, sortBy string) ([]model.RouteGroupItem, error) {
	if limit <= 0 {
		limit = 50
	}
	value, err := s.client.Do(ctx, "GET", routeGroupSnapshotKey(scope, window, sortBy))
	if err != nil {
		return nil, err
	}
	raw, ok := value.(string)
	if !ok || raw == "" {
		return []model.RouteGroupItem{}, nil
	}
	var items []model.RouteGroupItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, err
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *RedisStore) RefreshRouteGroupSnapshots(ctx context.Context) error {
	scopesValue, err := s.client.Do(ctx, "SMEMBERS", scopeRegistryKey)
	if err != nil {
		return err
	}
	for _, scopePart := range stringSlice(scopesValue) {
		scope := scopeFromRedisPart(scopePart)
		for _, window := range model.HotWindows() {
			items, err := s.aggregateRouteGroups(ctx, scope, window, 200, "requests")
			if err != nil {
				return err
			}
			if err := s.saveSnapshot(ctx, scope, window, "requests", items); err != nil {
				return err
			}
			errorItems := append([]model.RouteGroupItem(nil), items...)
			sortRouteGroupItems(errorItems, "errors")
			if err := s.saveSnapshot(ctx, scope, window, "errors", errorItems); err != nil {
				return err
			}
			latencyItems := append([]model.RouteGroupItem(nil), items...)
			sortRouteGroupItems(latencyItems, "latency")
			if err := s.saveSnapshot(ctx, scope, window, "latency", latencyItems); err != nil {
				return err
			}
			if err := s.saveSnapshot(ctx, scope, window, "summary", items); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *RedisStore) aggregateRouteGroups(ctx context.Context, scope model.AggregateScope, window model.Window, limit int, sortBy string) ([]model.RouteGroupItem, error) {
	keysValue, err := s.client.Do(ctx, "KEYS", routeGroupBucketPattern(scope, window))
	if err != nil {
		return nil, err
	}
	keys := stringSlice(keysValue)
	minBucket := model.AlignBucket(s.now().Add(-window.Duration() + model.BucketSize))
	metrics := make(map[string]model.RouteGroupMetric)
	for _, key := range keys {
		bucketUnix, ok := bucketUnixFromKey(key)
		if !ok || bucketUnix < minBucket {
			continue
		}
		values, err := s.client.Do(ctx, "HGETALL", key)
		if err != nil {
			return nil, err
		}
		metric := metricFromHash(values)
		if metric.RouteGroup == "" {
			continue
		}
		current := metrics[metric.RouteGroup]
		current.RouteGroup = metric.RouteGroup
		current.RequestCount += metric.RequestCount
		current.ErrorCount += metric.ErrorCount
		current.UpstreamErrorCount += metric.UpstreamErrorCount
		current.LatencySumMs += metric.LatencySumMs
		current.LatencyCount += metric.LatencyCount
		current.TeamID = firstNonEmpty(current.TeamID, metric.TeamID)
		current.AppID = firstNonEmpty(current.AppID, metric.AppID)
		current.ComponentID = firstNonEmpty(current.ComponentID, metric.ComponentID)
		metrics[metric.RouteGroup] = current
	}

	items := make([]model.RouteGroupItem, 0, len(metrics))
	for _, metric := range metrics {
		items = append(items, model.NewRouteGroupItem(metric))
	}
	sortRouteGroupItems(items, sortBy)
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *RedisStore) saveSnapshot(ctx context.Context, scope model.AggregateScope, window model.Window, sortBy string, items []model.RouteGroupItem) error {
	payload, err := json.Marshal(items)
	if err != nil {
		return err
	}
	if _, err := s.client.Do(ctx, "SET", routeGroupSnapshotKey(scope, window, sortBy), string(payload), "EX", strconv.Itoa(snapshotTTLSeconds)); err != nil {
		return err
	}
	meta := map[string]int64{"updated_unix": s.now().Unix()}
	metaPayload, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	_, err = s.client.Do(ctx, "SET", routeGroupSnapshotMetaKey(scope, window, sortBy), string(metaPayload), "EX", strconv.Itoa(snapshotTTLSeconds))
	return err
}

func (s *RedisStore) GetRouteGroupSnapshotMeta(ctx context.Context, scope model.AggregateScope, window model.Window, sortBy string) (model.QueryMeta, error) {
	meta := model.QueryMeta{Window: window}
	value, err := s.client.Do(ctx, "GET", routeGroupSnapshotMetaKey(scope, window, sortBy))
	if err != nil {
		return meta, err
	}
	raw, ok := value.(string)
	if !ok || raw == "" {
		meta.Partial = true
		meta.Stale = true
		return meta, nil
	}
	var payload struct {
		UpdatedUnix int64 `json:"updated_unix"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return meta, err
	}
	if payload.UpdatedUnix <= 0 {
		meta.Partial = true
		meta.Stale = true
		return meta, nil
	}
	meta.FreshnessSeconds = s.now().Unix() - payload.UpdatedUnix
	if meta.FreshnessSeconds < 0 {
		meta.FreshnessSeconds = 0
	}
	if meta.FreshnessSeconds > 15 {
		meta.Stale = true
	}
	return meta, nil
}

func (s *RedisStore) SaveRouteMapping(ctx context.Context, mapping model.RouteMapping, ttl time.Duration) error {
	if mapping.RouteID == "" {
		return fmt.Errorf("route_id is required")
	}
	payload, err := json.Marshal(mapping)
	if err != nil {
		return err
	}
	key := routeMappingKey(mapping.RouteID)
	if _, err := s.client.Do(ctx, "SET", key, string(payload)); err != nil {
		return err
	}
	if ttl > 0 {
		_, err = s.client.Do(ctx, "EXPIRE", key, strconv.Itoa(int(ttl.Seconds())))
		if err != nil {
			return err
		}
	}
	if mapping.AppID != "" && mapping.PrometheusRoute != "" {
		if _, err := s.client.Do(ctx, "SADD", appPrometheusRoutesKey(mapping.AppID), mapping.PrometheusRoute); err != nil {
			return err
		}
	}
	return nil
}

func (s *RedisStore) ResolveRoute(ctx context.Context, routeID, serviceID string) (model.RouteMapping, error) {
	if routeID == "" {
		return model.RouteMapping{ComponentID: serviceID}, nil
	}
	value, err := s.client.Do(ctx, "GET", routeMappingKey(routeID))
	if err != nil {
		return model.RouteMapping{}, err
	}
	raw, ok := value.(string)
	if !ok || raw == "" {
		return model.RouteMapping{}, fmt.Errorf("route mapping %s not found", routeID)
	}
	var mapping model.RouteMapping
	if err := json.Unmarshal([]byte(raw), &mapping); err != nil {
		return model.RouteMapping{}, err
	}
	if mapping.ComponentID == "" {
		mapping.ComponentID = serviceID
	}
	return mapping, nil
}

func (s *RedisStore) SaveSLAConfig(ctx context.Context, cfg model.SLAConfig) error {
	if cfg.AppID == "" {
		return fmt.Errorf("app_id is required")
	}
	payload, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = s.client.Do(ctx, "SET", slaConfigKey(cfg.AppID), string(payload))
	return err
}

func (s *RedisStore) GetSLAConfig(ctx context.Context, appID string, defaultTarget float64) (model.SLAConfig, error) {
	value, err := s.client.Do(ctx, "GET", slaConfigKey(appID))
	if err != nil {
		return model.SLAConfig{}, err
	}
	raw, ok := value.(string)
	if !ok || raw == "" {
		return model.SLAConfig{AppID: appID, Target: defaultTarget}, nil
	}
	var cfg model.SLAConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return model.SLAConfig{}, err
	}
	if cfg.Target <= 0 {
		cfg.Target = defaultTarget
	}
	return cfg, nil
}

func (s *RedisStore) SaveRouteGroupRules(ctx context.Context, appID string, rules []model.RouteGroupRule) error {
	if appID == "" {
		return fmt.Errorf("app_id is required")
	}
	payload, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	_, err = s.client.Do(ctx, "SET", routeGroupRulesKey(appID), string(payload))
	return err
}

func (s *RedisStore) GetRouteGroupRules(ctx context.Context, appID string) ([]model.RouteGroupRule, error) {
	value, err := s.client.Do(ctx, "GET", routeGroupRulesKey(appID))
	if err != nil {
		return nil, err
	}
	raw, ok := value.(string)
	if !ok || raw == "" {
		return nil, nil
	}
	var rules []model.RouteGroupRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, err
	}
	return rules, nil
}

func (s *RedisStore) GetAppPrometheusRoutes(ctx context.Context, appID string) ([]string, error) {
	value, err := s.client.Do(ctx, "SMEMBERS", appPrometheusRoutesKey(appID))
	if err != nil {
		return nil, err
	}
	return stringSlice(value), nil
}

func routeGroupBucketKey(scope model.AggregateScope, window model.Window, routeGroup string, bucketUnix int64) string {
	return fmt.Sprintf("nm:%s:%s:route-group:%s:bucket:%d", scope.RedisPart(), window, sanitizeKeyPart(routeGroup), bucketUnix)
}

func routeGroupBucketPattern(scope model.AggregateScope, window model.Window) string {
	return fmt.Sprintf("nm:%s:%s:route-group:*:bucket:*", scope.RedisPart(), window)
}

func routeGroupSnapshotKey(scope model.AggregateScope, window model.Window, sortBy string) string {
	switch sortBy {
	case "errors":
		return fmt.Sprintf("nm:%s:%s:route-groups:error-top", scope.RedisPart(), window)
	case "latency":
		return fmt.Sprintf("nm:%s:%s:route-groups:latency-top", scope.RedisPart(), window)
	case "summary":
		return fmt.Sprintf("nm:%s:%s:route-groups:summary", scope.RedisPart(), window)
	default:
		return fmt.Sprintf("nm:%s:%s:route-groups:request-top", scope.RedisPart(), window)
	}
}

func routeGroupSnapshotMetaKey(scope model.AggregateScope, window model.Window, sortBy string) string {
	return routeGroupSnapshotKey(scope, window, sortBy) + ":meta"
}

func scopeFromRedisPart(value string) model.AggregateScope {
	if value == "platform" {
		return model.AggregateScope{Kind: model.ScopePlatform}
	}
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 {
		return model.AggregateScope{Kind: model.ScopePlatform}
	}
	return model.AggregateScope{Kind: model.ScopeKind(parts[0]), ID: parts[1]}
}

func bucketUnixFromKey(key string) (int64, bool) {
	parts := strings.Split(key, ":bucket:")
	if len(parts) != 2 {
		return 0, false
	}
	value, err := strconv.ParseInt(parts[1], 10, 64)
	return value, err == nil
}

func routeMappingKey(routeID string) string {
	return "nm:route:" + sanitizeKeyPart(routeID) + ":mapping"
}

func slaConfigKey(appID string) string {
	return "nm:app:" + sanitizeKeyPart(appID) + ":sla-config"
}

func routeGroupRulesKey(appID string) string {
	return "nm:app:" + sanitizeKeyPart(appID) + ":route-group-rules"
}

func appPrometheusRoutesKey(appID string) string {
	return "nm:app:" + sanitizeKeyPart(appID) + ":prometheus-routes"
}

func sanitizeKeyPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer(" ", "_", "\n", "_", "\r", "_", ":", "_")
	return replacer.Replace(value)
}

func stringSlice(value interface{}) []string {
	values, ok := value.([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, item := range values {
		if str, ok := item.(string); ok {
			result = append(result, str)
		}
	}
	return result
}

func metricFromHash(value interface{}) model.RouteGroupMetric {
	values, ok := value.([]interface{})
	if !ok {
		return model.RouteGroupMetric{}
	}
	fields := make(map[string]string)
	for i := 0; i+1 < len(values); i += 2 {
		key, _ := values[i].(string)
		val, _ := values[i+1].(string)
		fields[key] = val
	}
	return model.RouteGroupMetric{
		RouteGroup:         fields["route_group"],
		RequestCount:       parseInt(fields["request_count"]),
		ErrorCount:         parseInt(fields["error_count"]),
		UpstreamErrorCount: parseInt(fields["upstream_error_count"]),
		LatencySumMs:       parseFloat(fields["latency_sum_ms"]),
		LatencyCount:       parseInt(fields["latency_count"]),
		TeamID:             fields["team_id"],
		AppID:              fields["app_id"],
		ComponentID:        fields["component_id"],
	}
}

func parseInt(value string) int64 {
	if value == "" {
		return 0
	}
	if strings.Contains(value, ".") {
		return int64(parseFloat(value))
	}
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}

func parseFloat(value string) float64 {
	parsed, _ := strconv.ParseFloat(value, 64)
	return parsed
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func sortRouteGroupItems(items []model.RouteGroupItem, sortBy string) {
	sort.Slice(items, func(i, j int) bool {
		switch sortBy {
		case "latency":
			return items[i].AvgLatencyMs > items[j].AvgLatencyMs
		case "errors":
			if items[i].ErrorCount == items[j].ErrorCount {
				return items[i].ErrorRate > items[j].ErrorRate
			}
			return items[i].ErrorCount > items[j].ErrorCount
		default:
			return items[i].RequestCount > items[j].RequestCount
		}
	})
}
