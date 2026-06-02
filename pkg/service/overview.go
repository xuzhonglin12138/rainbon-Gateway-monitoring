package service

import (
	"context"
	"fmt"
	"regexp"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
)

type OverviewConfig struct {
	Prometheus PrometheusScalarClient
	Store      routeIndexStore
}

type routeIndexStore interface {
	GetAppPrometheusRoutes(ctx context.Context, appID string) ([]string, error)
}

type OverviewService struct {
	prometheus PrometheusScalarClient
	store      routeIndexStore
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
