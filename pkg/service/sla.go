package service

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"

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

type SLABucketStore interface {
	ListRouteGroupBucketPoints(ctx context.Context, scope model.AggregateScope, window model.Window) ([]model.RouteGroupBucketPoint, error)
}

type SLAService struct {
	prometheus  PrometheusScalarClient
	store       SLAStore
	bucketStore SLABucketStore
	target      float64
	logger      *logrus.Logger
}

func NewSLAService(cfg SLAConfig) *SLAService {
	if cfg.Target <= 0 {
		cfg.Target = 0.999
	}
	service := &SLAService{prometheus: cfg.Prometheus, store: cfg.Store, target: cfg.Target, logger: cfg.Logger}
	if bucketStore, ok := cfg.Store.(SLABucketStore); ok {
		service.bucketStore = bucketStore
	}
	return service
}

func (s *SLAService) GetAppSLA(ctx context.Context, appID string, window model.Window) (model.SLAStatus, error) {
	target := s.target
	routeMatcher := regexp.QuoteMeta(appID) + ".*"
	if s.store != nil {
		cfg, err := s.store.GetSLAConfig(ctx, appID, s.target)
		if err != nil {
			return model.SLAStatus{}, err
		}
		target = cfg.Target
	}

	if status, ok, err := s.getAppSLAFromBuckets(ctx, appID, window, target); err != nil {
		return model.SLAStatus{}, err
	} else if ok {
		return status, nil
	}

	if s.prometheus == nil {
		return model.SLAStatus{}, fmt.Errorf("prometheus client is required")
	}

	if s.store != nil {
		routes, err := s.store.GetAppPrometheusRoutes(ctx, appID)
		if err != nil {
			return model.SLAStatus{}, err
		}
		if len(routes) > 0 {
			routeMatcher = prometheusRouteMatcher(routes)
		}
		if s.logger != nil {
			s.logger.WithFields(logrus.Fields{
				"app_id":        appID,
				"window":        window,
				"route_count":   len(routes),
				"routes":        strings.Join(routes, ","),
				"route_matcher": routeMatcher,
				"target":        target,
			}).Info("resolved app sla route matcher")
		}
	} else if s.logger != nil {
		s.logger.WithFields(logrus.Fields{
			"app_id":        appID,
			"window":        window,
			"route_matcher": routeMatcher,
			"target":        target,
		}).Info("using fallback app sla route matcher because store is nil")
	}
	routeMatcherLiteral := prometheusStringLiteralValue(routeMatcher)
	totalQuery := fmt.Sprintf(`sum(increase(apisix_http_status{route=~"%s"}[%s]))`, routeMatcherLiteral, window)
	errorQuery := fmt.Sprintf(`sum(increase(apisix_http_status{route=~"%s",code=~"5.."}[%s]))`, routeMatcherLiteral, window)
	if s.logger != nil {
		s.logger.WithFields(logrus.Fields{
			"app_id":      appID,
			"window":      window,
			"total_query": totalQuery,
			"error_query": errorQuery,
		}).Debug("querying app sla prometheus metrics")
	}

	total, err := s.prometheus.QueryScalar(ctx, totalQuery)
	if err != nil {
		return model.SLAStatus{}, err
	}
	errors, err := s.prometheus.QueryScalar(ctx, errorQuery)
	if err != nil {
		return model.SLAStatus{}, err
	}
	total = math.Round(total)
	errors = math.Round(errors)

	return slaStatusFromCounts(appID, window, target, total, errors, "B", "apisix_http_status"), nil
}

func (s *SLAService) getAppSLAFromBuckets(ctx context.Context, appID string, window model.Window, target float64) (model.SLAStatus, bool, error) {
	if s.bucketStore == nil {
		return model.SLAStatus{}, false, nil
	}

	points, err := s.bucketStore.ListRouteGroupBucketPoints(ctx, model.AggregateScope{Kind: model.ScopeApp, ID: appID}, window)
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
