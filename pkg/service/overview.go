package service

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	promclient "github.com/goodrain/rainbond-plugin-template/pkg/clients/prometheus"
	"github.com/goodrain/rainbond-plugin-template/pkg/model"
	"github.com/sirupsen/logrus"
)

type OverviewConfig struct {
	Prometheus      PrometheusQueryClient
	Store           routeIndexStore
	RouteGroupStore routeGroupOverviewStore
	Now             func() time.Time
	Logger          *logrus.Logger
}

type routeIndexStore interface {
	GetAppPrometheusRoutes(ctx context.Context, appID string) ([]string, error)
}

type routeGroupOverviewStore interface {
	ListRouteGroups(ctx context.Context, scope model.AggregateScope, window model.Window, limit int, sortBy string) ([]model.RouteGroupItem, error)
	ListRouteGroupBucketPoints(ctx context.Context, scope model.AggregateScope, window model.Window) ([]model.RouteGroupBucketPoint, error)
}

type OverviewService struct {
	prometheus      PrometheusQueryClient
	store           routeIndexStore
	routeGroupStore routeGroupOverviewStore
	now             func() time.Time
	logger          *logrus.Logger
}

const trendRangeStepSeconds = 30

type PrometheusQueryClient interface {
	QueryScalar(ctx context.Context, query string) (float64, error)
	QueryInstant(ctx context.Context, query string) ([]promclient.Sample, error)
	QueryRange(ctx context.Context, query string, start, end int64, stepSeconds int) ([]promclient.RangeSample, error)
}

func NewOverviewService(cfg OverviewConfig) *OverviewService {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	routeGroupStore := cfg.RouteGroupStore
	if routeGroupStore == nil {
		if store, ok := cfg.Store.(routeGroupOverviewStore); ok {
			routeGroupStore = store
		}
	}
	return &OverviewService{prometheus: cfg.Prometheus, store: cfg.Store, routeGroupStore: routeGroupStore, now: cfg.Now, logger: cfg.Logger}
}

func (s *OverviewService) GetPlatformOverview(ctx context.Context, window model.Window) (model.Overview, error) {
	if overview, ok, err := s.realtimeBucketOverview(ctx, model.AggregateScope{Kind: model.ScopePlatform}, window); err != nil {
		return model.Overview{}, err
	} else if ok {
		return overview, nil
	}
	return s.gatewayOverview(ctx, model.AggregateScope{Kind: model.ScopePlatform}, "", window)
}

func (s *OverviewService) GetAppOverview(ctx context.Context, appID string, window model.Window) (model.Overview, error) {
	if overview, ok, err := s.realtimeBucketOverview(ctx, model.AggregateScope{Kind: model.ScopeApp, ID: appID}, window); err != nil {
		return model.Overview{}, err
	} else if ok {
		return overview, nil
	}
	routeMatcher, err := s.appRouteMatcher(ctx, appID)
	if err != nil {
		return model.Overview{}, err
	}
	s.logQueryContext("app overview route matcher resolved", model.AggregateScope{Kind: model.ScopeApp, ID: appID}, window, logrus.Fields{
		"route_matcher": routeMatcher,
	})
	return s.gatewayOverview(ctx, model.AggregateScope{Kind: model.ScopeApp, ID: appID}, routeMatcher, window)
}

func (s *OverviewService) GetComponentOverview(ctx context.Context, componentID string, window model.Window) (model.Overview, error) {
	if overview, ok, err := s.realtimeBucketOverview(ctx, model.AggregateScope{Kind: model.ScopeComponent, ID: componentID}, window); err != nil {
		return model.Overview{}, err
	} else if ok {
		return overview, nil
	}
	if s.routeGroupStore != nil {
		items, err := s.routeGroupStore.ListRouteGroups(ctx, model.AggregateScope{Kind: model.ScopeComponent, ID: componentID}, window, 200, "summary")
		if err != nil {
			return model.Overview{}, err
		}
		if len(items) > 0 {
			return overviewFromRouteGroups(componentID, window, items), nil
		}
	}
	if s.prometheus == nil {
		return model.Overview{}, fmt.Errorf("prometheus client is required")
	}
	requests, err := s.prometheus.QueryScalar(ctx, fmt.Sprintf(`sum(increase(app_request{service_id="%s",method="total"}[%s]))`, componentID, window))
	if err != nil {
		return model.Overview{}, err
	}
	throughput, err := s.prometheus.QueryScalar(ctx, fmt.Sprintf(`sum(rate(app_request{service_id="%s",method="total"}[%s]))`, componentID, window))
	if err != nil {
		return model.Overview{}, err
	}
	latency, err := s.prometheus.QueryScalar(ctx, fmt.Sprintf(`avg(app_requesttime{service_id="%s",mode="avg"})`, componentID))
	if err != nil {
		return model.Overview{}, err
	}
	receive, err := s.prometheus.QueryScalar(ctx, fmt.Sprintf(`sum(rate(container_network_receive_bytes_total{service_id="%s"}[%s]))`, componentID, window))
	if err != nil {
		return model.Overview{}, err
	}
	transmit, err := s.prometheus.QueryScalar(ctx, fmt.Sprintf(`sum(rate(container_network_transmit_bytes_total{service_id="%s"}[%s]))`, componentID, window))
	if err != nil {
		return model.Overview{}, err
	}
	return model.Overview{
		Scope:               model.AggregateScope{Kind: model.ScopeComponent, ID: componentID},
		Window:              window,
		RequestCount:        requests,
		EgressBytesPerSec:   transmit,
		ThroughputPerSecond: throughput,
		AvgLatencyMs:        latency,
		NetworkReceiveBps:   receive,
		NetworkTransmitBps:  transmit,
		EvidenceLevel:       "A",
	}, nil
}

func (s *OverviewService) realtimeBucketOverview(ctx context.Context, scope model.AggregateScope, window model.Window) (model.Overview, bool, error) {
	if s.routeGroupStore == nil {
		return model.Overview{}, false, nil
	}
	buckets, err := s.routeGroupStore.ListRouteGroupBucketPoints(ctx, scope, window)
	if err != nil {
		return model.Overview{}, false, err
	}
	if len(buckets) == 0 {
		return model.Overview{}, false, nil
	}
	return overviewFromRouteMetrics(scope, window, routeMetricsFromBuckets(buckets)), true, nil
}

func overviewFromRouteGroups(componentID string, window model.Window, items []model.RouteGroupItem) model.Overview {
	scope := model.AggregateScope{Kind: model.ScopeComponent, ID: componentID}
	metrics := make([]model.RouteGroupMetric, 0, len(items))
	for _, item := range items {
		metrics = append(metrics, model.RouteGroupMetric{
			RequestCount: item.RequestCount,
			ErrorCount:   item.ErrorCount,
			LatencySumMs: item.AvgLatencyMs * float64(item.RequestCount),
			LatencyCount: item.RequestCount,
			EgressBytes:  item.EgressBytes,
		})
	}
	return overviewFromRouteMetrics(scope, window, metrics)
}

func routeMetricsFromBuckets(buckets []model.RouteGroupBucketPoint) []model.RouteGroupMetric {
	metrics := make([]model.RouteGroupMetric, 0, len(buckets))
	for _, bucket := range buckets {
		metrics = append(metrics, bucket.Metric)
	}
	return metrics
}

func overviewFromRouteMetrics(scope model.AggregateScope, window model.Window, metrics []model.RouteGroupMetric) model.Overview {
	var requestCount float64
	var errorCount float64
	var latencySum float64
	var latencyCount float64
	var egressBytes float64
	for _, metric := range metrics {
		requests := float64(metric.RequestCount)
		requestCount += requests
		errorCount += float64(metric.ErrorCount)
		latencySum += metric.LatencySumMs
		latencyCount += float64(metric.LatencyCount)
		egressBytes += float64(metric.EgressBytes)
	}
	var errorRate float64
	if requestCount > 0 {
		errorRate = errorCount / requestCount
	}
	var avgLatency float64
	if latencyCount > 0 {
		avgLatency = latencySum / latencyCount
	}
	windowSeconds := window.Duration().Seconds()
	if windowSeconds <= 0 {
		windowSeconds = 1
	}
	egressBytesPerSecond := egressBytes / windowSeconds
	return model.Overview{
		Scope:               scope,
		Window:              window,
		RequestCount:        requestCount,
		ErrorCount:          errorCount,
		ErrorRate:           errorRate,
		AvgLatencyMs:        avgLatency,
		EgressBytesPerSec:   egressBytesPerSecond,
		NetworkTransmitBps:  egressBytesPerSecond,
		ThroughputPerSecond: requestCount / windowSeconds,
		EvidenceLevel:       "A",
	}
}

func componentTrendPointsFromBuckets(buckets []model.RouteGroupBucketPoint, window model.Window, now time.Time) []model.OverviewTrendPoint {
	bucketCount := window.BucketCount()
	if bucketCount <= 0 {
		bucketCount = model.Window5m.BucketCount()
	}
	points := make([]model.OverviewTrendPoint, 0, bucketCount)
	bucketSeconds := model.BucketSize.Seconds()
	if bucketSeconds <= 0 {
		bucketSeconds = 1
	}
	metricsByTimestamp := make(map[int64]model.RouteGroupMetric, len(buckets))
	for _, bucket := range buckets {
		current := metricsByTimestamp[bucket.Timestamp]
		current.RequestCount += bucket.Metric.RequestCount
		current.ErrorCount += bucket.Metric.ErrorCount
		current.UpstreamErrorCount += bucket.Metric.UpstreamErrorCount
		current.LatencySumMs += bucket.Metric.LatencySumMs
		current.LatencyCount += bucket.Metric.LatencyCount
		current.EgressBytes += bucket.Metric.EgressBytes
		metricsByTimestamp[bucket.Timestamp] = current
	}
	latestBucket := model.AlignBucket(now)
	startBucket := latestBucket - int64(bucketCount-1)*int64(model.BucketSize/time.Second)
	for index := 0; index < bucketCount; index++ {
		timestamp := startBucket + int64(index)*int64(model.BucketSize/time.Second)
		metric := metricsByTimestamp[timestamp]
		var errorRate float64
		if metric.RequestCount > 0 {
			errorRate = float64(metric.ErrorCount) / float64(metric.RequestCount)
		}
		points = append(points, model.OverviewTrendPoint{
			Timestamp:         timestamp,
			RequestPerSecond:  float64(metric.RequestCount) / bucketSeconds,
			ErrorRate:         errorRate,
			AvgLatencyMs:      metric.AvgLatencyMs(),
			EgressBytesPerSec: float64(metric.EgressBytes) / bucketSeconds,
		})
	}
	return points
}

func (s *OverviewService) GetPlatformRealtimeTrend(ctx context.Context, window model.Window) (model.OverviewTrend, error) {
	if trend, ok, err := s.realtimeBucketTrend(ctx, model.AggregateScope{Kind: model.ScopePlatform}, window); err != nil {
		return model.OverviewTrend{}, err
	} else if ok {
		return trend, nil
	}
	return s.gatewayRealtimeTrend(ctx, model.AggregateScope{Kind: model.ScopePlatform}, "", window)
}

func (s *OverviewService) GetAppRealtimeTrend(ctx context.Context, appID string, window model.Window) (model.OverviewTrend, error) {
	if trend, ok, err := s.realtimeBucketTrend(ctx, model.AggregateScope{Kind: model.ScopeApp, ID: appID}, window); err != nil {
		return model.OverviewTrend{}, err
	} else if ok {
		return trend, nil
	}
	routeMatcher, err := s.appRouteMatcher(ctx, appID)
	if err != nil {
		return model.OverviewTrend{}, err
	}
	return s.gatewayRealtimeTrend(ctx, model.AggregateScope{Kind: model.ScopeApp, ID: appID}, routeMatcher, window)
}

func (s *OverviewService) GetComponentRealtimeTrend(ctx context.Context, componentID string, window model.Window) (model.OverviewTrend, error) {
	if trend, ok, err := s.realtimeBucketTrend(ctx, model.AggregateScope{Kind: model.ScopeComponent, ID: componentID}, window); err != nil {
		return model.OverviewTrend{}, err
	} else if ok {
		return trend, nil
	}
	if s.prometheus == nil {
		return model.OverviewTrend{}, fmt.Errorf("prometheus client is required")
	}
	start, end := alignedTrendRange(window, s.now(), trendRangeStepSeconds)
	requests, err := s.prometheus.QueryRange(ctx, fmt.Sprintf(`sum(rate(app_request{service_id="%s",method="total"}[1m]))`, componentID), start, end, trendRangeStepSeconds)
	if err != nil {
		return model.OverviewTrend{}, err
	}
	latencies, err := s.prometheus.QueryRange(ctx, fmt.Sprintf(`avg(app_requesttime{service_id="%s",mode="avg"})`, componentID), start, end, trendRangeStepSeconds)
	if err != nil {
		return model.OverviewTrend{}, err
	}
	receive, err := s.prometheus.QueryRange(ctx, fmt.Sprintf(`sum(rate(container_network_receive_bytes_total{service_id="%s"}[1m]))`, componentID), start, end, trendRangeStepSeconds)
	if err != nil {
		return model.OverviewTrend{}, err
	}
	transmit, err := s.prometheus.QueryRange(ctx, fmt.Sprintf(`sum(rate(container_network_transmit_bytes_total{service_id="%s"}[1m]))`, componentID), start, end, trendRangeStepSeconds)
	if err != nil {
		return model.OverviewTrend{}, err
	}

	requestValues := rangeValuesByTimestamp(requests)
	latencyValues := rangeValuesByTimestamp(latencies)
	receiveValues := rangeValuesByTimestamp(receive)
	transmitValues := rangeValuesByTimestamp(transmit)
	timestamps := sortedTimestamps(requestValues, latencyValues, receiveValues, transmitValues)
	points := make([]model.OverviewTrendPoint, 0, len(timestamps))
	for _, timestamp := range timestamps {
		points = append(points, model.OverviewTrendPoint{
			Timestamp:         timestamp,
			RequestPerSecond:  requestValues[timestamp],
			AvgLatencyMs:      latencyValues[timestamp],
			EgressBytesPerSec: transmitValues[timestamp] + receiveValues[timestamp],
		})
	}
	return model.OverviewTrend{
		Scope:  model.AggregateScope{Kind: model.ScopeComponent, ID: componentID},
		Window: window,
		Points: points,
	}, nil
}

func (s *OverviewService) realtimeBucketTrend(ctx context.Context, scope model.AggregateScope, window model.Window) (model.OverviewTrend, bool, error) {
	if s.routeGroupStore == nil {
		return model.OverviewTrend{}, false, nil
	}
	buckets, err := s.routeGroupStore.ListRouteGroupBucketPoints(ctx, scope, window)
	if err != nil {
		return model.OverviewTrend{}, false, err
	}
	if len(buckets) == 0 {
		return model.OverviewTrend{}, false, nil
	}
	return model.OverviewTrend{
		Scope:  scope,
		Window: window,
		Points: componentTrendPointsFromBuckets(buckets, window, s.now()),
	}, true, nil
}

func (s *OverviewService) GetPlatformNodeSummaries(ctx context.Context, window model.Window) ([]model.PlatformNodeSummary, error) {
	if s.prometheus == nil {
		return nil, fmt.Errorf("prometheus client is required")
	}
	requests, err := s.prometheus.QueryInstant(ctx, fmt.Sprintf(`sum by (instance) (increase(apisix_http_status[%s]))`, window))
	if err != nil {
		return nil, err
	}
	latencies, err := s.prometheus.QueryInstant(ctx, fmt.Sprintf(`histogram_quantile(0.50, sum by (instance, le) (rate(apisix_http_latency_bucket[%s]))) * 1000`, window))
	if err != nil {
		return nil, err
	}
	errors, err := s.prometheus.QueryInstant(ctx, fmt.Sprintf(`sum by (instance) (increase(apisix_http_status{code=~"5.."}[%s]))`, window))
	if err != nil {
		return nil, err
	}
	egress, err := s.prometheus.QueryInstant(ctx, fmt.Sprintf(`sum by (instance) (rate(apisix_bandwidth{type="egress"}[%s]))`, window))
	if err != nil {
		return nil, err
	}

	nodes := map[string]*model.PlatformNodeSummary{}
	ensure := func(sample promclient.Sample) *model.PlatformNodeSummary {
		name := nodeNameFromSample(sample)
		if name == "" {
			name = "unknown"
		}
		if nodes[name] == nil {
			nodes[name] = &model.PlatformNodeSummary{Name: name, Cluster: sample.Metric["cluster"]}
		}
		if nodes[name].Cluster == "" {
			nodes[name].Cluster = sample.Metric["cluster"]
		}
		return nodes[name]
	}
	for _, sample := range requests {
		ensure(sample).RequestCount = sample.Value
	}
	for _, sample := range latencies {
		ensure(sample).P50LatencyMs = sample.Value
	}
	for _, sample := range errors {
		ensure(sample).ErrorCount = sample.Value
	}
	for _, sample := range egress {
		ensure(sample).EgressBytesPerSec = sample.Value
	}

	result := make([]model.PlatformNodeSummary, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, *node)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].RequestCount > result[j].RequestCount
	})
	return result, nil
}

func (s *OverviewService) GetPlatformNodeDetail(ctx context.Context, nodeName string, window model.Window) (model.PlatformNodeDetail, error) {
	if s.prometheus == nil {
		return model.PlatformNodeDetail{}, fmt.Errorf("prometheus client is required")
	}
	ready, err := s.prometheus.QueryInstant(ctx, `kube_node_status_condition{condition="Ready",status="true"}`)
	if err != nil {
		return model.PlatformNodeDetail{}, err
	}
	cpu, err := s.prometheus.QueryInstant(ctx, fmt.Sprintf(`100 * (1 - avg by (instance) (rate(node_cpu_seconds_total{mode="idle"}[%s])))`, window))
	if err != nil {
		return model.PlatformNodeDetail{}, err
	}
	memory, err := s.prometheus.QueryInstant(ctx, `100 * (1 - (node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes))`)
	if err != nil {
		return model.PlatformNodeDetail{}, err
	}

	detail := model.PlatformNodeDetail{Name: nodeName, Status: "unknown"}
	for _, sample := range ready {
		if nodeNameFromSample(sample) == nodeName {
			detail.Cluster = sample.Metric["cluster"]
			if sample.Value > 0 {
				detail.Status = "ready"
			} else {
				detail.Status = "not_ready"
			}
			break
		}
	}
	for _, sample := range cpu {
		if nodeNameFromSample(sample) == nodeName {
			detail.CPUUsagePercent = sample.Value
			break
		}
	}
	for _, sample := range memory {
		if nodeNameFromSample(sample) == nodeName {
			detail.MemoryUsagePercent = sample.Value
			break
		}
	}
	return detail, nil
}

func (s *OverviewService) gatewayOverview(ctx context.Context, scope model.AggregateScope, routeMatcher string, window model.Window) (model.Overview, error) {
	if s.prometheus == nil {
		return model.Overview{}, fmt.Errorf("prometheus client is required")
	}
	routeLabel := prometheusRouteLabel(routeMatcher)
	selector := metricSelector(routeLabel)
	selectorWithCode := metricSelector(routeLabel, `code=~"5.."`)
	latencySelector := metricSelector(routeLabel, `type="upstream"`)
	egressSelector := metricSelector(routeLabel, `type="egress"`)
	totalQuery := fmt.Sprintf(`sum(increase(apisix_http_status%s[%s]))`, selector, window)
	errorQuery := fmt.Sprintf(`sum(increase(apisix_http_status%s[%s]))`, selectorWithCode, window)
	latencySumQuery := fmt.Sprintf(`sum(rate(apisix_http_latency_sum%s[%s]))`, latencySelector, window)
	latencyCountQuery := fmt.Sprintf(`sum(rate(apisix_http_latency_count%s[%s]))`, latencySelector, window)
	egressQuery := fmt.Sprintf(`sum(rate(apisix_bandwidth%s[%s]))`, egressSelector, window)
	s.logQueryContext("querying gateway overview prometheus metrics", scope, window, logrus.Fields{
		"route_matcher": routeMatcher,
		"selector":      selector,
	})
	s.logPrometheusQuery("gateway overview request count query", scope, totalQuery)
	total, err := s.prometheus.QueryScalar(ctx, totalQuery)
	if err != nil {
		return model.Overview{}, err
	}
	s.logPrometheusQuery("gateway overview error count query", scope, errorQuery)
	errors, err := s.prometheus.QueryScalar(ctx, errorQuery)
	if err != nil {
		return model.Overview{}, err
	}
	s.logPrometheusQuery("gateway overview latency sum query", scope, latencySumQuery)
	latencySum, err := s.prometheus.QueryScalar(ctx, latencySumQuery)
	if err != nil {
		return model.Overview{}, err
	}
	s.logPrometheusQuery("gateway overview latency count query", scope, latencyCountQuery)
	latencyCount, err := s.prometheus.QueryScalar(ctx, latencyCountQuery)
	if err != nil {
		return model.Overview{}, err
	}
	s.logPrometheusQuery("gateway overview egress query", scope, egressQuery)
	egress, err := s.prometheus.QueryScalar(ctx, egressQuery)
	if err != nil {
		return model.Overview{}, err
	}
	errorRate := 0.0
	if total > 0 {
		errorRate = errors / total
	}
	latency := 0.0
	if latencyCount > 0 {
		latency = latencySum / latencyCount
	}
	return model.Overview{
		Scope:             scope,
		Window:            window,
		RequestCount:      total,
		ErrorCount:        errors,
		ErrorRate:         errorRate,
		AvgLatencyMs:      latency,
		EgressBytesPerSec: egress,
		EvidenceLevel:     "A",
	}, nil
}

func (s *OverviewService) gatewayRealtimeTrend(ctx context.Context, scope model.AggregateScope, routeMatcher string, window model.Window) (model.OverviewTrend, error) {
	if s.prometheus == nil {
		return model.OverviewTrend{}, fmt.Errorf("prometheus client is required")
	}
	start, end := alignedTrendRange(window, s.now(), trendRangeStepSeconds)
	routeLabel := prometheusRouteLabel(routeMatcher)
	selector := metricSelector(routeLabel)
	selectorWithCode := metricSelector(routeLabel, `code=~"5.."`)
	latencySelector := metricSelector(routeLabel, `type="upstream"`)
	egressSelector := metricSelector(routeLabel, `type="egress"`)

	requests, err := s.prometheus.QueryRange(ctx, fmt.Sprintf(`sum(rate(apisix_http_status%s[1m]))`, selector), start, end, trendRangeStepSeconds)
	if err != nil {
		return model.OverviewTrend{}, err
	}
	errors, err := s.prometheus.QueryRange(ctx, fmt.Sprintf(`sum(rate(apisix_http_status%s[1m]))`, selectorWithCode), start, end, trendRangeStepSeconds)
	if err != nil {
		return model.OverviewTrend{}, err
	}
	latencies, err := s.prometheus.QueryRange(ctx, fmt.Sprintf(`sum(rate(apisix_http_latency_sum%s[1m])) / sum(rate(apisix_http_latency_count%s[1m]))`, latencySelector, latencySelector), start, end, trendRangeStepSeconds)
	if err != nil {
		return model.OverviewTrend{}, err
	}
	egress, err := s.prometheus.QueryRange(ctx, fmt.Sprintf(`sum(rate(apisix_bandwidth%s[1m]))`, egressSelector), start, end, trendRangeStepSeconds)
	if err != nil {
		return model.OverviewTrend{}, err
	}

	requestValues := rangeValuesByTimestamp(requests)
	errorValues := rangeValuesByTimestamp(errors)
	latencyValues := rangeValuesByTimestamp(latencies)
	egressValues := rangeValuesByTimestamp(egress)
	timestamps := sortedTimestamps(requestValues, errorValues, latencyValues, egressValues)
	points := make([]model.OverviewTrendPoint, 0, len(timestamps))
	for _, timestamp := range timestamps {
		errorRate := 0.0
		if requestValues[timestamp] > 0 {
			errorRate = errorValues[timestamp] / requestValues[timestamp]
		}
		points = append(points, model.OverviewTrendPoint{
			Timestamp:         timestamp,
			RequestPerSecond:  requestValues[timestamp],
			ErrorRate:         errorRate,
			AvgLatencyMs:      latencyValues[timestamp],
			EgressBytesPerSec: egressValues[timestamp],
		})
	}
	return model.OverviewTrend{Scope: scope, Window: window, Points: points}, nil
}

func alignedTrendRange(window model.Window, now time.Time, stepSeconds int64) (int64, int64) {
	if stepSeconds <= 0 {
		stepSeconds = trendRangeStepSeconds
	}
	end := now.Unix()
	end -= end % stepSeconds
	start := end - int64(window.Duration()/time.Second)
	return start, end
}

func (s *OverviewService) appRouteMatcher(ctx context.Context, appID string) (string, error) {
	routeMatcher := regexp.QuoteMeta(appID) + ".*"
	if s.store == nil {
		s.logQueryContext("using fallback app route matcher because route index store is nil", model.AggregateScope{Kind: model.ScopeApp, ID: appID}, "", logrus.Fields{
			"route_matcher": routeMatcher,
		})
		return routeMatcher, nil
	}
	routes, err := s.store.GetAppPrometheusRoutes(ctx, appID)
	if err != nil {
		return "", err
	}
	s.logQueryContext("loaded app prometheus routes from route index", model.AggregateScope{Kind: model.ScopeApp, ID: appID}, "", logrus.Fields{
		"route_count": len(routes),
		"routes":      strings.Join(routes, ","),
	})
	if len(routes) > 0 {
		routeMatcher = prometheusRouteMatcher(routes)
	}
	return routeMatcher, nil
}

func (s *OverviewService) logQueryContext(message string, scope model.AggregateScope, window model.Window, fields logrus.Fields) {
	if s.logger == nil {
		return
	}
	entry := s.logger.WithFields(logrus.Fields{
		"scope_kind": scope.Kind,
		"scope_id":   scope.ID,
	})
	if window != "" {
		entry = entry.WithField("window", window)
	}
	entry.WithFields(fields).Info(message)
}

func (s *OverviewService) logPrometheusQuery(message string, scope model.AggregateScope, query string) {
	if s.logger == nil {
		return
	}
	s.logger.WithFields(logrus.Fields{
		"scope_kind": scope.Kind,
		"scope_id":   scope.ID,
		"query":      query,
	}).Debug(message)
}

func prometheusRouteLabel(routeMatcher string) string {
	if routeMatcher == "" {
		return ""
	}
	return fmt.Sprintf(`route=~"%s"`, prometheusStringLiteralValue(routeMatcher))
}

func prometheusStringLiteralValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

func metricSelector(labels ...string) string {
	filtered := make([]string, 0, len(labels))
	for _, label := range labels {
		if label != "" {
			filtered = append(filtered, label)
		}
	}
	if len(filtered) == 0 {
		return ""
	}
	return "{" + strings.Join(filtered, ",") + "}"
}

func rangeValuesByTimestamp(samples []promclient.RangeSample) map[int64]float64 {
	values := map[int64]float64{}
	for _, sample := range samples {
		for _, point := range sample.Values {
			if !finiteFloat(point.Value) {
				continue
			}
			values[point.Timestamp] += point.Value
		}
	}
	return values
}

func finiteFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func sortedTimestamps(series ...map[int64]float64) []int64 {
	seen := map[int64]struct{}{}
	for _, values := range series {
		for timestamp := range values {
			seen[timestamp] = struct{}{}
		}
	}
	timestamps := make([]int64, 0, len(seen))
	for timestamp := range seen {
		timestamps = append(timestamps, timestamp)
	}
	sort.Slice(timestamps, func(i, j int) bool {
		return timestamps[i] < timestamps[j]
	})
	return timestamps
}

func nodeNameFromSample(sample promclient.Sample) string {
	for _, key := range []string{"node", "nodename", "kubernetes_node", "kubernetes_io_hostname"} {
		if value := sample.Metric[key]; value != "" {
			return value
		}
	}
	instance := sample.Metric["instance"]
	if instance == "" {
		return ""
	}
	if strings.Contains(instance, ":") {
		return strings.Split(instance, ":")[0]
	}
	return instance
}
