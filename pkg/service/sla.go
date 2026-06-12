package service

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
	"github.com/sirupsen/logrus"
)

type PrometheusScalarClient interface {
	QueryScalar(ctx context.Context, query string) (float64, error)
}

type SLAConfig struct {
	Prometheus PrometheusScalarClient
	Store      SLAStore
	Target     float64
	Logger     *logrus.Logger
}

type SLAStore interface {
	GetSLAConfig(ctx context.Context, appID string, defaultTarget float64) (model.SLAConfig, error)
	GetAppPrometheusRoutes(ctx context.Context, appID string) ([]string, error)
}

type SLAHealthAggregateStore interface {
	GetSLAHealthAggregate(ctx context.Context, appID string, since, until time.Time) (model.SLAHealthAggregate, error)
}

type SLABucketStore interface {
	ListRouteGroupBucketPoints(ctx context.Context, scope model.AggregateScope, window model.Window) ([]model.RouteGroupBucketPoint, error)
}

type SLABucketAtStore interface {
	ListRouteGroupBucketPointsAt(ctx context.Context, scope model.AggregateScope, window model.Window, endTime time.Time) ([]model.RouteGroupBucketPoint, error)
}

type SLAService struct {
	prometheus  PrometheusScalarClient
	store       SLAStore
	healthStore SLAHealthAggregateStore
	bucketStore SLABucketStore
	target      float64
	logger      *logrus.Logger
}

func NewSLAService(cfg SLAConfig) *SLAService {
	if cfg.Target <= 0 {
		cfg.Target = 0.999
	}
	service := &SLAService{prometheus: cfg.Prometheus, store: cfg.Store, target: cfg.Target, logger: cfg.Logger}
	if healthStore, ok := cfg.Store.(SLAHealthAggregateStore); ok {
		service.healthStore = healthStore
	}
	if bucketStore, ok := cfg.Store.(SLABucketStore); ok {
		service.bucketStore = bucketStore
	}
	return service
}

func (s *SLAService) GetAppSLA(ctx context.Context, appID string, window model.Window) (model.SLAStatus, error) {
	return s.getAppSLA(ctx, appID, window, time.Time{})
}

func (s *SLAService) GetAppSLAAt(ctx context.Context, appID string, window model.Window, endTime time.Time) (model.SLAStatus, error) {
	return s.getAppSLA(ctx, appID, window, endTime)
}

func (s *SLAService) getAppSLA(ctx context.Context, appID string, window model.Window, endTime time.Time) (model.SLAStatus, error) {
	target := s.target
	cfg := model.SLAConfig{AppID: appID, Target: target}
	if s.store != nil {
		stored, err := s.store.GetSLAConfig(ctx, appID, s.target)
		if err != nil {
			return model.SLAStatus{}, err
		}
		cfg = stored
		target = cfg.Target
	}
	if cfg.Target <= 0 {
		cfg.Target = target
	}
	return s.getAppSLAFromHealthChecks(ctx, appID, window, endTime, cfg)
}

func (s *SLAService) getAppSLAFromHealthChecks(ctx context.Context, appID string, window model.Window, endTime time.Time, cfg model.SLAConfig) (model.SLAStatus, error) {
	status := baseHealthSLAStatus(appID, window, cfg)
	if !cfg.Enabled || strings.TrimSpace(cfg.URL) == "" {
		return status, nil
	}
	if s.healthStore == nil {
		return status, nil
	}
	until := endTime
	if until.IsZero() {
		until = time.Now()
	}
	since := until.Add(-30 * 24 * time.Hour)
	aggregate, err := s.healthStore.GetSLAHealthAggregate(ctx, appID, since, until)
	if err != nil {
		return model.SLAStatus{}, err
	}
	status.TotalChecks = aggregate.TotalChecks
	status.SuccessChecks = aggregate.SuccessChecks
	status.FailureChecks = aggregate.FailureChecks
	status.LastCheckedAt = aggregate.LastCheckedAt
	status.LastStatusCode = aggregate.LastStatusCode
	status.LastErrorType = aggregate.LastErrorType
	if aggregate.TotalChecks > 0 {
		status.Current = float64(aggregate.SuccessChecks) / float64(aggregate.TotalChecks)
		status.MeetingTarget = status.Current >= status.Target
		status.AvgLatencyMs = aggregate.LatencySumMs / float64(aggregate.TotalChecks)
	}
	if s.logger != nil {
		s.logger.WithFields(logrus.Fields{
			"app_id":         appID,
			"configured":     status.Configured,
			"total_checks":   status.TotalChecks,
			"success_checks": status.SuccessChecks,
			"failure_checks": status.FailureChecks,
			"target":         status.Target,
		}).Debug("computed app sla from health checks")
	}
	return status, nil
}

func baseHealthSLAStatus(appID string, window model.Window, cfg model.SLAConfig) model.SLAStatus {
	target := cfg.Target
	if target <= 0 {
		target = 0.99
	}
	if cfg.IntervalSeconds <= 0 {
		cfg.IntervalSeconds = 10
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 3
	}
	if cfg.SuccessStatusMin <= 0 {
		cfg.SuccessStatusMin = 200
	}
	if cfg.SuccessStatusMax <= 0 {
		cfg.SuccessStatusMax = 399
	}
	current := 1.0
	return model.SLAStatus{
		AppID:                 appID,
		Window:                window,
		HealthWindow:          "30d",
		Configured:            cfg.Enabled && strings.TrimSpace(cfg.URL) != "",
		Current:               current,
		Target:                target,
		MeetingTarget:         current >= target,
		IntervalSeconds:       cfg.IntervalSeconds,
		TimeoutSeconds:        cfg.TimeoutSeconds,
		SuccessStatusRange:    fmt.Sprintf("%d-%d", cfg.SuccessStatusMin, cfg.SuccessStatusMax),
		EvidenceLevel:         "A",
		PrometheusQuerySource: "sla_health_check",
	}
}

func (s *SLAService) getAppSLAFromBuckets(ctx context.Context, appID string, window model.Window, target float64) (model.SLAStatus, bool, error) {
	return s.getAppSLAFromBucketsAt(ctx, appID, window, time.Time{}, target)
}

func (s *SLAService) getAppSLAFromBucketsAt(ctx context.Context, appID string, window model.Window, endTime time.Time, target float64) (model.SLAStatus, bool, error) {
	if s.bucketStore == nil {
		return model.SLAStatus{}, false, nil
	}

	var (
		points []model.RouteGroupBucketPoint
		err    error
	)
	if !endTime.IsZero() {
		atStore, ok := s.bucketStore.(SLABucketAtStore)
		if !ok {
			points, err = s.bucketStore.ListRouteGroupBucketPoints(ctx, model.AggregateScope{Kind: model.ScopeApp, ID: appID}, window)
		} else {
			points, err = atStore.ListRouteGroupBucketPointsAt(ctx, model.AggregateScope{Kind: model.ScopeApp, ID: appID}, window, endTime)
		}
	} else {
		points, err = s.bucketStore.ListRouteGroupBucketPoints(ctx, model.AggregateScope{Kind: model.ScopeApp, ID: appID}, window)
	}
	if err != nil {
		return model.SLAStatus{}, false, err
	}
	if len(points) == 0 {
		return model.SLAStatus{}, false, nil
	}

	var total float64
	var errors float64
	for _, point := range points {
		total += float64(point.Metric.RequestCount)
		errors += float64(point.Metric.ErrorCount)
	}
	if s.logger != nil {
		s.logger.WithFields(logrus.Fields{
			"app_id":       appID,
			"window":       window,
			"bucket_count": len(points),
			"total":        total,
			"errors":       errors,
			"target":       target,
		}).Debug("computed app sla from route group buckets")
	}
	return slaStatusFromCounts(appID, window, target, total, errors, "A", "route_group_bucket"), true, nil
}

func slaStatusFromCounts(appID string, window model.Window, target, total, errors float64, evidenceLevel, querySource string) model.SLAStatus {
	current := 1.0
	if total > 0 {
		current = (total - errors) / total
	}
	errorBudget := total * (1 - target)
	return model.SLAStatus{
		AppID:                 appID,
		Window:                window,
		Current:               current,
		Target:                target,
		MeetingTarget:         current >= target,
		TotalRequests:         total,
		ErrorRequests:         errors,
		ErrorBudget:           errorBudget,
		ErrorBudgetRemaining:  errorBudget - errors,
		EvidenceLevel:         evidenceLevel,
		PrometheusQuerySource: querySource,
	}
}

func prometheusRouteMatcher(routes []string) string {
	escaped := make([]string, 0, len(routes))
	for _, route := range routes {
		if route == "" {
			continue
		}
		escaped = append(escaped, regexp.QuoteMeta(route))
	}
	if len(escaped) == 0 {
		return ""
	}
	return strings.Join(escaped, "|")
}
