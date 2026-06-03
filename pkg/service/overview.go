package service

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	promclient "github.com/goodrain/rainbond-plugin-template/pkg/clients/prometheus"
	"github.com/goodrain/rainbond-plugin-template/pkg/model"
)

type OverviewConfig struct {
	Prometheus PrometheusQueryClient
	Store      routeIndexStore
}

type routeIndexStore interface {
	GetAppPrometheusRoutes(ctx context.Context, appID string) ([]string, error)
}

type OverviewService struct {
	prometheus PrometheusQueryClient
	store      routeIndexStore
}

type PrometheusQueryClient interface {
	QueryScalar(ctx context.Context, query string) (float64, error)
	QueryInstant(ctx context.Context, query string) ([]promclient.Sample, error)
}

func NewOverviewService(cfg OverviewConfig) *OverviewService {
	return &OverviewService{prometheus: cfg.Prometheus, store: cfg.Store}
}

func (s *OverviewService) GetPlatformOverview(ctx context.Context, window model.Window) (model.Overview, error) {
	return s.gatewayOverview(ctx, model.AggregateScope{Kind: model.ScopePlatform}, "", window)
}

func (s *OverviewService) GetAppOverview(ctx context.Context, appID string, window model.Window) (model.Overview, error) {
	routeMatcher := regexp.QuoteMeta(appID) + ".*"
	if s.store != nil {
		routes, err := s.store.GetAppPrometheusRoutes(ctx, appID)
		if err != nil {
			return model.Overview{}, err
		}
		if len(routes) > 0 {
			routeMatcher = prometheusRouteMatcher(routes)
		}
	}
	return s.gatewayOverview(ctx, model.AggregateScope{Kind: model.ScopeApp, ID: appID}, routeMatcher, window)
}

func (s *OverviewService) GetComponentOverview(ctx context.Context, componentID string, window model.Window) (model.Overview, error) {
	if s.prometheus == nil {
		return model.Overview{}, fmt.Errorf("prometheus client is required")
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
		ThroughputPerSecond: throughput,
		AvgLatencyMs:        latency,
		NetworkReceiveBps:   receive,
		NetworkTransmitBps:  transmit,
		EvidenceLevel:       "A",
	}, nil
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
	selector := ""
	if routeMatcher != "" {
		selector = fmt.Sprintf(`{route=~"%s"}`, routeMatcher)
	}
	selectorWithCode := `{code=~"5.."}`
	if routeMatcher != "" {
		selectorWithCode = fmt.Sprintf(`{route=~"%s",code=~"5.."}`, routeMatcher)
	}
	egressSelector := `{type="egress"}`
	if routeMatcher != "" {
		egressSelector = fmt.Sprintf(`{route=~"%s",type="egress"}`, routeMatcher)
	}
	total, err := s.prometheus.QueryScalar(ctx, fmt.Sprintf(`sum(increase(apisix_http_status%s[%s]))`, selector, window))
	if err != nil {
		return model.Overview{}, err
	}
	errors, err := s.prometheus.QueryScalar(ctx, fmt.Sprintf(`sum(increase(apisix_http_status%s[%s]))`, selectorWithCode, window))
	if err != nil {
		return model.Overview{}, err
	}
	latencySum, err := s.prometheus.QueryScalar(ctx, fmt.Sprintf(`sum(rate(apisix_http_latency_sum%s[%s]))`, selector, window))
	if err != nil {
		return model.Overview{}, err
	}
	latencyCount, err := s.prometheus.QueryScalar(ctx, fmt.Sprintf(`sum(rate(apisix_http_latency_count%s[%s]))`, selector, window))
	if err != nil {
		return model.Overview{}, err
	}
	egress, err := s.prometheus.QueryScalar(ctx, fmt.Sprintf(`sum(rate(apisix_bandwidth%s[%s]))`, egressSelector, window))
	if err != nil {
		return model.Overview{}, err
	}
	errorRate := 0.0
	if total > 0 {
		errorRate = errors / total
	}
	latency := 0.0
	if latencyCount > 0 {
		latency = latencySum / latencyCount * 1000
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
