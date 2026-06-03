package service

import (
	"context"
	"fmt"
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

type SLAService struct {
	prometheus PrometheusScalarClient
	store      SLAStore
	target     float64
	logger     *logrus.Logger
}

func NewSLAService(cfg SLAConfig) *SLAService {
	if cfg.Target <= 0 {
		cfg.Target = 0.999
	}
	return &SLAService{prometheus: cfg.Prometheus, store: cfg.Store, target: cfg.Target, logger: cfg.Logger}
}

func (s *SLAService) GetAppSLA(ctx context.Context, appID string, window model.Window) (model.SLAStatus, error) {
	if s.prometheus == nil {
		return model.SLAStatus{}, fmt.Errorf("prometheus client is required")
	}
	target := s.target
	routeMatcher := regexp.QuoteMeta(appID) + ".*"
	if s.store != nil {
		cfg, err := s.store.GetSLAConfig(ctx, appID, s.target)
		if err != nil {
			return model.SLAStatus{}, err
		}
		target = cfg.Target
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
	totalQuery := fmt.Sprintf(`sum(increase(apisix_http_status{route=~"%s"}[%s]))`, routeMatcher, window)
	errorQuery := fmt.Sprintf(`sum(increase(apisix_http_status{route=~"%s",code=~"5.."}[%s]))`, routeMatcher, window)
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
		EvidenceLevel:         "B",
		PrometheusQuerySource: "apisix_http_status",
	}, nil
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
