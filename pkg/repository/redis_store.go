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

type routeGroupBucketMetric struct {
	timestamp int64
	metric    model.RouteGroupMetric
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
		{"egress_bytes", float64(metric.EgressBytes)},
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
		"team_name", metric.TeamName,
		"team_alias", metric.TeamAlias,
		"app_id", metric.AppID,
		"namespace", metric.Namespace,
		"region_app_id", metric.RegionAppID,
		"app_name", metric.AppName,
		"region_name", metric.RegionName,
		"component_id", metric.ComponentID,
		"service_alias", metric.ServiceAlias,
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
	if scope.Kind == model.ScopeApp {
		return s.aggregateRouteGroupsForApp(ctx, scope.ID, window, limit, sortBy)
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
	if items == nil {
		items = []model.RouteGroupItem{}
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *RedisStore) ListAppComponentSummaries(ctx context.Context, appID string, window model.Window, limit int) ([]model.AppComponentSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	metrics, err := s.aggregateComponentMetricsForApp(ctx, appID, window)
	if err != nil {
		return nil, err
	}
	items := make([]model.AppComponentSummary, 0, len(metrics))
	for componentID, metric := range metrics {
		name := metric.ServiceAlias
		if name == "" {
			name = componentID
		}
		items = append(items, model.AppComponentSummary{
			ComponentID:  componentID,
			ServiceAlias: metric.ServiceAlias,
			Name:         name,
			RequestCount: metric.RequestCount,
			ErrorCount:   metric.ErrorCount,
			ErrorRate:    metric.ErrorRate(),
			AvgLatencyMs: metric.AvgLatencyMs(),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].RequestCount > items[j].RequestCount
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *RedisStore) ListApps(ctx context.Context, scope model.AggregateScope, window model.Window, limit int, sortBy string) ([]model.AppTrafficItem, error) {
	if limit <= 0 {
		limit = 50
	}
	metrics, routeMetrics, err := s.aggregateAppMetrics(ctx, scope, window)
	if err != nil {
		return nil, err
	}
	items := make([]model.AppTrafficItem, 0, len(metrics))
	windowSeconds := window.Duration().Seconds()
	if windowSeconds <= 0 {
		windowSeconds = 1
	}
	for appID, metric := range metrics {
		name := firstNonEmpty(metric.AppName, appID)
		if appID == "" {
			name = "unknown_app"
		}
		errorRoute := topErrorRouteGroup(routeMetrics[appID])
		latencyRoute := topLatencyRouteGroup(routeMetrics[appID])
		items = append(items, model.AppTrafficItem{
			AppID:                appID,
			TeamID:               metric.TeamID,
			TeamName:             metric.TeamName,
			TeamAlias:            metric.TeamAlias,
			Namespace:            metric.Namespace,
			RegionAppID:          metric.RegionAppID,
			AppName:              metric.AppName,
			RegionName:           metric.RegionName,
			Name:                 name,
			RequestCount:         metric.RequestCount,
			ErrorCount:           metric.ErrorCount,
			ErrorRate:            metric.ErrorRate(),
			UpstreamErrorCount:   metric.UpstreamErrorCount,
			UpstreamErrorRate:    metric.UpstreamErrorRate(),
			AvgLatencyMs:         metric.AvgLatencyMs(),
			ThroughputPerSecond:  float64(metric.RequestCount) / windowSeconds,
			TopErrorRouteGroup:   errorRoute.RouteGroup,
			TopErrorRouteErrors:  errorRoute.ErrorCount,
			TopLatencyRouteGroup: latencyRoute.RouteGroup,
			TopLatencyRouteAvgMs: latencyRoute.AvgLatencyMs(),
		})
	}
	sortAppTrafficItems(items, sortBy)
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *RedisStore) ListRouteGroupBucketPoints(ctx context.Context, scope model.AggregateScope, window model.Window) ([]model.RouteGroupBucketPoint, error) {
	if scope.Kind == model.ScopeApp {
		return s.listRouteGroupBucketPointsForApp(ctx, scope.ID, window)
	}
	return s.listRouteGroupBucketPointsForScope(ctx, scope, window)
}

func (s *RedisStore) listRouteGroupBucketPointsForScope(ctx context.Context, scope model.AggregateScope, window model.Window) ([]model.RouteGroupBucketPoint, error) {
	metrics, err := s.listRouteGroupBucketMetricsForScope(ctx, scope, window)
	if err != nil {
		return nil, err
	}
	return aggregateRouteGroupBucketPoints(metrics), nil
}

func (s *RedisStore) listRouteGroupBucketMetricsForScope(ctx context.Context, scope model.AggregateScope, window model.Window) ([]routeGroupBucketMetric, error) {
	keysValue, err := s.client.Do(ctx, "KEYS", routeGroupBucketPattern(scope, window))
	if err != nil {
		return nil, err
	}
	keys := stringSlice(keysValue)
	now := s.now()
	metrics := make([]routeGroupBucketMetric, 0, len(keys))
	for _, key := range keys {
		bucketUnix, ok := bucketUnixFromKey(key)
		if !ok || !bucketInWindow(bucketUnix, now, window) {
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
		metrics = append(metrics, routeGroupBucketMetric{
			timestamp: bucketUnix,
			metric:    metric,
		})
	}
	return metrics, nil
}

func (s *RedisStore) listRouteGroupBucketPointsForApp(ctx context.Context, appID string, window model.Window) ([]model.RouteGroupBucketPoint, error) {
	scopes, err := s.appScopes(ctx, appID)
	if err != nil {
		return nil, err
	}
	metrics := make([]routeGroupBucketMetric, 0)
	for _, scope := range scopes {
		scopeMetrics, err := s.listRouteGroupBucketMetricsForScope(ctx, scope, window)
		if err != nil {
			return nil, err
		}
		metrics = append(metrics, scopeMetrics...)
	}
	return aggregateRouteGroupBucketPoints(dedupeRouteGroupBucketMetrics(metrics)), nil
}

func aggregateRouteGroupBucketPoints(bucketMetrics []routeGroupBucketMetric) []model.RouteGroupBucketPoint {
	pointsByBucket := make(map[int64]model.RouteGroupMetric)
	for _, bucketMetric := range bucketMetrics {
		current := pointsByBucket[bucketMetric.timestamp]
		mergeRouteGroupMetric(&current, bucketMetric.metric)
		pointsByBucket[bucketMetric.timestamp] = current
	}
	timestamps := make([]int64, 0, len(pointsByBucket))
	for timestamp := range pointsByBucket {
		timestamps = append(timestamps, timestamp)
	}
	sort.Slice(timestamps, func(i, j int) bool { return timestamps[i] < timestamps[j] })
	points := make([]model.RouteGroupBucketPoint, 0, len(timestamps))
	for _, timestamp := range timestamps {
		points = append(points, model.RouteGroupBucketPoint{
			Timestamp: timestamp,
			Metric:    pointsByBucket[timestamp],
		})
	}
	return points
}

func aggregateRouteGroupMetrics(bucketMetrics []routeGroupBucketMetric) map[string]model.RouteGroupMetric {
	metrics := make(map[string]model.RouteGroupMetric)
	for _, bucketMetric := range bucketMetrics {
		metric := bucketMetric.metric
		if metric.RouteGroup == "" {
			continue
		}
		current := metrics[metric.RouteGroup]
		mergeRouteGroupMetric(&current, metric)
		current.RouteGroup = metric.RouteGroup
		metrics[metric.RouteGroup] = current
	}
	return metrics
}

func dedupeRouteGroupBucketMetrics(bucketMetrics []routeGroupBucketMetric) []routeGroupBucketMetric {
	byBucketAndRoute := make(map[string]routeGroupBucketMetric)
	for _, bucketMetric := range bucketMetrics {
		if bucketMetric.metric.RouteGroup == "" {
			continue
		}
		key := fmt.Sprintf("%d\x00%s", bucketMetric.timestamp, bucketMetric.metric.RouteGroup)
		current, ok := byBucketAndRoute[key]
		if !ok || bucketMetric.metric.RequestCount > current.metric.RequestCount {
			byBucketAndRoute[key] = bucketMetric
		}
	}
	deduped := make([]routeGroupBucketMetric, 0, len(byBucketAndRoute))
	for _, bucketMetric := range byBucketAndRoute {
		deduped = append(deduped, bucketMetric)
	}
	return deduped
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
	bucketMetrics, err := s.listRouteGroupBucketMetricsForScope(ctx, scope, window)
	if err != nil {
		return nil, err
	}
	metrics := aggregateRouteGroupMetrics(bucketMetrics)

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

func (s *RedisStore) aggregateRouteGroupsForApp(ctx context.Context, appID string, window model.Window, limit int, sortBy string) ([]model.RouteGroupItem, error) {
	scopes, err := s.appScopes(ctx, appID)
	if err != nil {
		return nil, err
	}
	metrics := make(map[string]model.RouteGroupMetric)
	bucketMetrics := make([]routeGroupBucketMetric, 0)
	for _, scope := range scopes {
		scopeMetrics, err := s.listRouteGroupBucketMetricsForScope(ctx, scope, window)
		if err != nil {
			return nil, err
		}
		bucketMetrics = append(bucketMetrics, scopeMetrics...)
	}
	metrics = aggregateRouteGroupMetrics(dedupeRouteGroupBucketMetrics(bucketMetrics))
	for routeGroup, metric := range metrics {
		metric.AppID = firstNonEmpty(appID, metric.AppID)
		if metric.RegionAppID == "" && metric.AppID != "" && metric.AppID != appID {
			metric.RegionAppID = metric.AppID
		}
		metrics[routeGroup] = metric
	}
	items := make([]model.RouteGroupItem, 0, len(metrics))
	for _, metric := range metrics {
		items = append(items, model.NewRouteGroupItem(metric))
	}
	sortRouteGroupItems(items, sortBy)
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *RedisStore) aggregateAppMetrics(ctx context.Context, scope model.AggregateScope, window model.Window) (map[string]model.RouteGroupMetric, map[string]map[string]model.RouteGroupMetric, error) {
	if scope.Kind == model.ScopePlatform || scope.Kind == model.ScopeTeam {
		return s.aggregateAppMetricsFromRegisteredAppScopes(ctx, scope, window)
	}
	if scope.Kind == model.ScopeApp {
		return s.aggregateAppMetricsForApp(ctx, scope.ID, window)
	}
	return s.aggregateAppMetricsFromScope(ctx, scope, window)
}

func (s *RedisStore) aggregateAppMetricsFromRegisteredAppScopes(ctx context.Context, scope model.AggregateScope, window model.Window) (map[string]model.RouteGroupMetric, map[string]map[string]model.RouteGroupMetric, error) {
	scopesValue, err := s.client.Do(ctx, "SMEMBERS", scopeRegistryKey)
	if err != nil {
		return nil, nil, err
	}
	metrics := make(map[string]model.RouteGroupMetric)
	routeMetrics := make(map[string]map[string]model.RouteGroupMetric)
	visitedApps := make(map[string]struct{})
	for _, scopePart := range stringSlice(scopesValue) {
		appScope := scopeFromRedisPart(scopePart)
		if appScope.Kind != model.ScopeApp || appScope.ID == "" {
			continue
		}
		appID, err := s.canonicalAppIDForID(ctx, appScope.ID)
		if err != nil {
			return nil, nil, err
		}
		if _, ok := visitedApps[appID]; ok {
			continue
		}
		visitedApps[appID] = struct{}{}
		appMetrics, appRouteMetrics, err := s.aggregateAppMetricsForApp(ctx, appScope.ID, window)
		if err != nil {
			return nil, nil, err
		}
		for appID, metric := range appMetrics {
			if scope.Kind == model.ScopeTeam && !appMetricBelongsToTeam(metric, scope.ID) {
				continue
			}
			current := metrics[appID]
			mergeRouteGroupMetric(&current, metric)
			current.AppID = firstNonEmpty(current.AppID, appID, metric.AppID)
			metrics[appID] = current
			if routeMetrics[appID] == nil {
				routeMetrics[appID] = make(map[string]model.RouteGroupMetric)
			}
			for routeGroup, routeMetric := range appRouteMetrics[appID] {
				currentRoute := routeMetrics[appID][routeGroup]
				mergeRouteGroupMetric(&currentRoute, routeMetric)
				currentRoute.RouteGroup = firstNonEmpty(currentRoute.RouteGroup, routeGroup, routeMetric.RouteGroup)
				routeMetrics[appID][routeGroup] = currentRoute
			}
		}
	}
	return metrics, routeMetrics, nil
}

func (s *RedisStore) aggregateAppMetricsForApp(ctx context.Context, appID string, window model.Window) (map[string]model.RouteGroupMetric, map[string]map[string]model.RouteGroupMetric, error) {
	scopes, err := s.appScopes(ctx, appID)
	if err != nil {
		return nil, nil, err
	}
	bucketMetrics := make([]routeGroupBucketMetric, 0)
	for _, scope := range scopes {
		scopeMetrics, err := s.listRouteGroupBucketMetricsForScope(ctx, scope, window)
		if err != nil {
			return nil, nil, err
		}
		bucketMetrics = append(bucketMetrics, scopeMetrics...)
	}
	bucketMetrics = dedupeRouteGroupBucketMetrics(bucketMetrics)
	metrics := make(map[string]model.RouteGroupMetric)
	routeMetrics := make(map[string]map[string]model.RouteGroupMetric)
	for _, bucketMetric := range bucketMetrics {
		metric := bucketMetric.metric
		if metric.AppID == "" || metric.AppID == "unknown_app" {
			continue
		}
		metricAppID, err := s.canonicalAppID(ctx, metric)
		if err != nil {
			return nil, nil, err
		}
		current := metrics[metricAppID]
		mergeRouteGroupMetric(&current, metric)
		current.AppID = firstNonEmpty(current.AppID, metricAppID, metric.AppID)
		current.RegionAppID = firstNonEmpty(current.RegionAppID, metric.RegionAppID, alternateRegionAppID(metric, metricAppID))
		metrics[metricAppID] = current
		if metric.RouteGroup == "" {
			continue
		}
		if routeMetrics[metricAppID] == nil {
			routeMetrics[metricAppID] = make(map[string]model.RouteGroupMetric)
		}
		currentRoute := routeMetrics[metricAppID][metric.RouteGroup]
		mergeRouteGroupMetric(&currentRoute, metric)
		currentRoute.RouteGroup = metric.RouteGroup
		routeMetrics[metricAppID][metric.RouteGroup] = currentRoute
	}
	return metrics, routeMetrics, nil
}

func (s *RedisStore) aggregateAppMetricsFromScope(ctx context.Context, scope model.AggregateScope, window model.Window) (map[string]model.RouteGroupMetric, map[string]map[string]model.RouteGroupMetric, error) {
	keysValue, err := s.client.Do(ctx, "KEYS", routeGroupBucketPattern(scope, window))
	if err != nil {
		return nil, nil, err
	}
	keys := stringSlice(keysValue)
	now := s.now()
	metrics := make(map[string]model.RouteGroupMetric)
	routeMetrics := make(map[string]map[string]model.RouteGroupMetric)
	for _, key := range keys {
		bucketUnix, ok := bucketUnixFromKey(key)
		if !ok || !bucketInWindow(bucketUnix, now, window) {
			continue
		}
		values, err := s.client.Do(ctx, "HGETALL", key)
		if err != nil {
			return nil, nil, err
		}
		metric := metricFromHash(values)
		if metric.AppID == "" || metric.AppID == "unknown_app" {
			continue
		}
		appID, err := s.canonicalAppID(ctx, metric)
		if err != nil {
			return nil, nil, err
		}
		current := metrics[appID]
		current.AppID = appID
		current.RequestCount += metric.RequestCount
		current.ErrorCount += metric.ErrorCount
		current.UpstreamErrorCount += metric.UpstreamErrorCount
		current.LatencySumMs += metric.LatencySumMs
		current.LatencyCount += metric.LatencyCount
		current.EgressBytes += metric.EgressBytes
		current.TeamID = firstNonEmpty(current.TeamID, metric.TeamID)
		current.TeamName = firstNonEmpty(current.TeamName, metric.TeamName)
		current.TeamAlias = firstNonEmpty(current.TeamAlias, metric.TeamAlias)
		current.Namespace = firstNonEmpty(current.Namespace, metric.Namespace)
		current.RegionAppID = firstNonEmpty(current.RegionAppID, metric.RegionAppID, alternateRegionAppID(metric, appID))
		current.AppName = firstNonEmpty(current.AppName, metric.AppName)
		current.RegionName = firstNonEmpty(current.RegionName, metric.RegionName)
		metrics[appID] = current
		if metric.RouteGroup != "" {
			if routeMetrics[appID] == nil {
				routeMetrics[appID] = make(map[string]model.RouteGroupMetric)
			}
			currentRoute := routeMetrics[appID][metric.RouteGroup]
			mergeRouteGroupMetric(&currentRoute, metric)
			currentRoute.RouteGroup = metric.RouteGroup
			routeMetrics[appID][metric.RouteGroup] = currentRoute
		}
	}
	return metrics, routeMetrics, nil
}

func (s *RedisStore) aggregateComponentMetrics(ctx context.Context, scope model.AggregateScope, window model.Window) (map[string]model.RouteGroupMetric, error) {
	keysValue, err := s.client.Do(ctx, "KEYS", routeGroupBucketPattern(scope, window))
	if err != nil {
		return nil, err
	}
	keys := stringSlice(keysValue)
	now := s.now()
	metrics := make(map[string]model.RouteGroupMetric)
	for _, key := range keys {
		bucketUnix, ok := bucketUnixFromKey(key)
		if !ok || !bucketInWindow(bucketUnix, now, window) {
			continue
		}
		values, err := s.client.Do(ctx, "HGETALL", key)
		if err != nil {
			return nil, err
		}
		metric := metricFromHash(values)
		if metric.ComponentID == "" {
			continue
		}
		current := metrics[metric.ComponentID]
		current.ComponentID = metric.ComponentID
		current.ServiceAlias = firstNonEmpty(current.ServiceAlias, metric.ServiceAlias)
		current.RequestCount += metric.RequestCount
		current.ErrorCount += metric.ErrorCount
		current.UpstreamErrorCount += metric.UpstreamErrorCount
		current.LatencySumMs += metric.LatencySumMs
		current.LatencyCount += metric.LatencyCount
		current.EgressBytes += metric.EgressBytes
		current.TeamID = firstNonEmpty(current.TeamID, metric.TeamID)
		current.AppID = firstNonEmpty(current.AppID, metric.AppID)
		current.TeamName = firstNonEmpty(current.TeamName, metric.TeamName)
		current.TeamAlias = firstNonEmpty(current.TeamAlias, metric.TeamAlias)
		current.Namespace = firstNonEmpty(current.Namespace, metric.Namespace)
		current.RegionAppID = firstNonEmpty(current.RegionAppID, metric.RegionAppID)
		current.AppName = firstNonEmpty(current.AppName, metric.AppName)
		current.RegionName = firstNonEmpty(current.RegionName, metric.RegionName)
		metrics[metric.ComponentID] = current
	}
	return metrics, nil
}

func (s *RedisStore) aggregateComponentMetricsForApp(ctx context.Context, appID string, window model.Window) (map[string]model.RouteGroupMetric, error) {
	scopes, err := s.appScopes(ctx, appID)
	if err != nil {
		return nil, err
	}
	metrics := make(map[string]model.RouteGroupMetric)
	for _, scope := range scopes {
		scopeMetrics, err := s.aggregateComponentMetrics(ctx, scope, window)
		if err != nil {
			return nil, err
		}
		for componentID, metric := range scopeMetrics {
			current := metrics[componentID]
			current.ComponentID = componentID
			current.ServiceAlias = firstNonEmpty(current.ServiceAlias, metric.ServiceAlias)
			current.RequestCount += metric.RequestCount
			current.ErrorCount += metric.ErrorCount
			current.UpstreamErrorCount += metric.UpstreamErrorCount
			current.LatencySumMs += metric.LatencySumMs
			current.LatencyCount += metric.LatencyCount
			current.EgressBytes += metric.EgressBytes
			current.TeamID = firstNonEmpty(current.TeamID, metric.TeamID)
			current.AppID = firstNonEmpty(current.AppID, appID, metric.AppID)
			current.TeamName = firstNonEmpty(current.TeamName, metric.TeamName)
			current.TeamAlias = firstNonEmpty(current.TeamAlias, metric.TeamAlias)
			current.Namespace = firstNonEmpty(current.Namespace, metric.Namespace)
			current.RegionAppID = firstNonEmpty(current.RegionAppID, metric.RegionAppID, alternateRegionAppID(metric, appID))
			current.AppName = firstNonEmpty(current.AppName, metric.AppName)
			current.RegionName = firstNonEmpty(current.RegionName, metric.RegionName)
			metrics[componentID] = current
		}
	}
	return metrics, nil
}

func (s *RedisStore) saveSnapshot(ctx context.Context, scope model.AggregateScope, window model.Window, sortBy string, items []model.RouteGroupItem) error {
	if items == nil {
		items = []model.RouteGroupItem{}
	}
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
	if mapping.AppID != "" && mapping.RegionAppID != "" && mapping.AppID != mapping.RegionAppID {
		if _, err := s.client.Do(ctx, "SADD", appAliasesKey(mapping.AppID), mapping.RegionAppID); err != nil {
			return err
		}
		if _, err := s.client.Do(ctx, "SET", appCanonicalKey(mapping.RegionAppID), mapping.AppID); err != nil {
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

func (s *RedisStore) ReplaceAppPrometheusRoutes(ctx context.Context, appID string, routes []string) error {
	if appID == "" {
		return fmt.Errorf("app_id is required")
	}
	key := appPrometheusRoutesKey(appID)
	if _, err := s.client.Do(ctx, "DEL", key); err != nil {
		return err
	}
	if len(routes) == 0 {
		return nil
	}
	args := append([]string{"SADD", key}, routes...)
	_, err := s.client.Do(ctx, args...)
	return err
}

func routeGroupBucketKey(scope model.AggregateScope, window model.Window, routeGroup string, bucketUnix int64) string {
	return fmt.Sprintf("nm:%s:%s:route-group:%s:bucket:%d", scope.RedisPart(), routeGroupBucketStorageWindow(window), sanitizeKeyPart(routeGroup), bucketUnix)
}

func routeGroupBucketPattern(scope model.AggregateScope, window model.Window) string {
	return fmt.Sprintf("nm:%s:%s:route-group:*:bucket:*", scope.RedisPart(), routeGroupBucketStorageWindow(window))
}

func routeGroupBucketStorageWindow(_ model.Window) model.Window {
	return model.Window5m
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

func bucketInWindow(bucketUnix int64, now time.Time, window model.Window) bool {
	minBucket := model.AlignBucket(now.Add(-window.Duration() + model.BucketSize))
	maxBucket := model.AlignBucket(now)
	return bucketUnix >= minBucket && bucketUnix <= maxBucket
}

func routeMappingKey(routeID string) string {
	return "nm:route:" + sanitizeKeyPart(routeID) + ":mapping"
}

func appAliasesKey(appID string) string {
	return "nm:app:" + sanitizeKeyPart(appID) + ":aliases"
}

func appCanonicalKey(appID string) string {
	return "nm:app:" + sanitizeKeyPart(appID) + ":canonical"
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
		EgressBytes:        parseInt(fields["egress_bytes"]),
		TeamID:             fields["team_id"],
		TeamName:           fields["team_name"],
		TeamAlias:          fields["team_alias"],
		AppID:              fields["app_id"],
		Namespace:          fields["namespace"],
		RegionAppID:        fields["region_app_id"],
		AppName:            fields["app_name"],
		RegionName:         fields["region_name"],
		ComponentID:        fields["component_id"],
		ServiceAlias:       fields["service_alias"],
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

func mergeRouteGroupMetric(target *model.RouteGroupMetric, source model.RouteGroupMetric) {
	target.RequestCount += source.RequestCount
	target.ErrorCount += source.ErrorCount
	target.UpstreamErrorCount += source.UpstreamErrorCount
	target.LatencySumMs += source.LatencySumMs
	target.LatencyCount += source.LatencyCount
	target.EgressBytes += source.EgressBytes
	target.TeamID = firstNonEmpty(target.TeamID, source.TeamID)
	target.TeamName = firstNonEmpty(target.TeamName, source.TeamName)
	target.TeamAlias = firstNonEmpty(target.TeamAlias, source.TeamAlias)
	target.Namespace = firstNonEmpty(target.Namespace, source.Namespace)
	target.AppID = firstNonEmpty(target.AppID, source.AppID)
	target.RegionAppID = firstNonEmpty(target.RegionAppID, source.RegionAppID)
	target.AppName = firstNonEmpty(target.AppName, source.AppName)
	target.RegionName = firstNonEmpty(target.RegionName, source.RegionName)
	target.ComponentID = firstNonEmpty(target.ComponentID, source.ComponentID)
	target.ServiceAlias = firstNonEmpty(target.ServiceAlias, source.ServiceAlias)
	target.RouteGroup = firstNonEmpty(target.RouteGroup, source.RouteGroup)
}

func appMetricBelongsToTeam(metric model.RouteGroupMetric, teamID string) bool {
	if teamID == "" {
		return true
	}
	return metric.TeamID == teamID || metric.TeamName == teamID || metric.Namespace == teamID
}

func (s *RedisStore) appScopes(ctx context.Context, appID string) ([]model.AggregateScope, error) {
	seen := map[string]struct{}{}
	ids := make([]string, 0, 2)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		ids = append(ids, value)
	}
	add(appID)
	canonicalValue, err := s.client.Do(ctx, "GET", appCanonicalKey(appID))
	if err != nil {
		return nil, err
	}
	if canonical, ok := canonicalValue.(string); ok {
		add(canonical)
	}
	aliasesValue, err := s.client.Do(ctx, "SMEMBERS", appAliasesKey(appID))
	if err != nil {
		return nil, err
	}
	for _, alias := range stringSlice(aliasesValue) {
		add(alias)
	}
	scopes := make([]model.AggregateScope, 0, len(ids))
	for _, id := range ids {
		scopes = append(scopes, model.AggregateScope{Kind: model.ScopeApp, ID: id})
	}
	return scopes, nil
}

func (s *RedisStore) canonicalAppID(ctx context.Context, metric model.RouteGroupMetric) (string, error) {
	if metric.RegionAppID != "" && metric.AppID != "" && metric.RegionAppID != metric.AppID {
		return metric.AppID, nil
	}
	return s.canonicalAppIDForID(ctx, metric.AppID)
}

func (s *RedisStore) canonicalAppIDForID(ctx context.Context, appID string) (string, error) {
	value, err := s.client.Do(ctx, "GET", appCanonicalKey(appID))
	if err != nil {
		return "", err
	}
	if canonical, ok := value.(string); ok && strings.TrimSpace(canonical) != "" {
		return strings.TrimSpace(canonical), nil
	}
	return appID, nil
}

func alternateRegionAppID(metric model.RouteGroupMetric, appID string) string {
	if metric.RegionAppID != "" {
		return metric.RegionAppID
	}
	if metric.AppID != "" && metric.AppID != appID {
		return metric.AppID
	}
	return ""
}

func alternateRegionAppIDFromItem(item model.RouteGroupItem, appID string) string {
	if item.RegionAppID != "" {
		return item.RegionAppID
	}
	if item.AppID != "" && item.AppID != appID {
		return item.AppID
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

func sortAppTrafficItems(items []model.AppTrafficItem, sortBy string) {
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
			return items[i].ThroughputPerSecond > items[j].ThroughputPerSecond
		}
	})
}

func topErrorRouteGroup(routes map[string]model.RouteGroupMetric) model.RouteGroupMetric {
	var top model.RouteGroupMetric
	for _, route := range routes {
		if route.ErrorCount > top.ErrorCount {
			top = route
			continue
		}
		if route.ErrorCount == top.ErrorCount && route.ErrorRate() > top.ErrorRate() {
			top = route
		}
	}
	return top
}

func topLatencyRouteGroup(routes map[string]model.RouteGroupMetric) model.RouteGroupMetric {
	var top model.RouteGroupMetric
	for _, route := range routes {
		if route.AvgLatencyMs() > top.AvgLatencyMs() {
			top = route
			continue
		}
		if route.AvgLatencyMs() == top.AvgLatencyMs() && route.RequestCount > top.RequestCount {
			top = route
		}
	}
	return top
}
