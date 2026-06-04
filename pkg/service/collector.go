package service

import (
	"context"
	"fmt"
	"time"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
	"github.com/sirupsen/logrus"
)

type AggregateStore interface {
	AddRouteGroupBucket(ctx context.Context, scope model.AggregateScope, window model.Window, bucketUnix int64, metric model.RouteGroupMetric) error
}

type RouteMapper interface {
	ResolveRoute(ctx context.Context, routeID, serviceID string) (model.RouteMapping, error)
}

type RouteGroupRuleStore interface {
	GetRouteGroupRules(ctx context.Context, appID string) ([]model.RouteGroupRule, error)
}

type CollectorConfig struct {
	Store           AggregateStore
	Mapper          RouteMapper
	RouteGroups     *RouteGroupResolver
	RouteGroupRules RouteGroupRuleStore
	Now             func() time.Time
	Logger          *logrus.Logger
}

type InternalRouteCollector struct {
	store           AggregateStore
	mapper          RouteMapper
	routeGroups     *RouteGroupResolver
	routeGroupRules RouteGroupRuleStore
	now             func() time.Time
	logger          *logrus.Logger
}

func NewInternalRouteCollector(cfg CollectorConfig) *InternalRouteCollector {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RouteGroups == nil {
		cfg.RouteGroups = NewRouteGroupResolver(RouteGroupConfig{})
	}
	return &InternalRouteCollector{
		store:           cfg.Store,
		mapper:          cfg.Mapper,
		routeGroups:     cfg.RouteGroups,
		routeGroupRules: cfg.RouteGroupRules,
		now:             cfg.Now,
		logger:          cfg.Logger,
	}
}

func (c *InternalRouteCollector) Collect(ctx context.Context, logs []model.ApisixAccessLog) error {
	if c.store == nil {
		return fmt.Errorf("collector store is required")
	}
	if c.mapper == nil {
		return fmt.Errorf("collector route mapper is required")
	}

	bucket := model.AlignBucket(c.now())
	if c.logger != nil {
		c.logger.WithFields(logrus.Fields{
			"log_count":   len(logs),
			"bucket_unix": bucket,
		}).Info("collecting apisix access logs")
	}
	var skippedMissingRoute, resolvedCount, unknownMappingCount int
	for _, log := range logs {
		if log.RouteID == "" && log.RouteName == "" && log.ServiceID == "" {
			skippedMissingRoute++
			if c.logger != nil {
				c.logger.WithFields(logrus.Fields{
					"uri":    chooseURI(log),
					"status": log.Status,
				}).Debug("skip apisix access log without route_id and service_id")
			}
			continue
		}
		mapping, err := c.resolveMapping(ctx, log)
		if err != nil {
			if c.logger != nil {
				c.logger.WithError(err).WithFields(logrus.Fields{
					"route_id":   log.RouteID,
					"route_name": log.RouteName,
					"service_id": log.ServiceID,
					"uri":        chooseURI(log),
				}).Warn("resolve apisix route mapping failed")
			}
			mapping = model.RouteMapping{
				RouteID:     log.RouteID,
				TeamID:      "unknown_team",
				AppID:       "unknown_app",
				ComponentID: "unknown_component",
			}
			unknownMappingCount++
		} else {
			resolvedCount++
		}
		routeGroup := c.resolveRouteGroup(ctx, mapping, log)
		metric := metricFromLog(routeGroup, mapping, log)
		if c.logger != nil {
			c.logger.WithFields(logrus.Fields{
				"route_id":        log.RouteID,
				"route_name":      log.RouteName,
				"service_id":      log.ServiceID,
				"route_group":     routeGroup,
				"team_id":         mapping.TeamID,
				"app_id":          mapping.AppID,
				"component_id":    mapping.ComponentID,
				"service_alias":   mapping.ServiceAlias,
				"status":          log.Status,
				"upstream_status": log.UpstreamStatus,
				"request_time":    log.RequestTime,
			}).Debug("mapped apisix access log")
		}
		for _, window := range model.HotWindows() {
			for _, scope := range scopesForMapping(mapping) {
				if err := c.store.AddRouteGroupBucket(ctx, scope, window, bucket, metric); err != nil {
					return fmt.Errorf("write route group bucket: %w", err)
				}
			}
		}
	}
	if c.logger != nil {
		c.logger.WithFields(logrus.Fields{
			"log_count":             len(logs),
			"skipped_missing_route": skippedMissingRoute,
			"mapped_count":          resolvedCount,
			"unknown_mapping_count": unknownMappingCount,
		}).Info("collected apisix access logs")
	}
	return nil
}

func (c *InternalRouteCollector) resolveMapping(ctx context.Context, log model.ApisixAccessLog) (model.RouteMapping, error) {
	if log.RouteID != "" {
		mapping, err := c.mapper.ResolveRoute(ctx, log.RouteID, log.ServiceID)
		if err == nil {
			return mapping, nil
		}
		if log.RouteName == "" || log.RouteName == log.RouteID {
			return model.RouteMapping{}, err
		}
	}
	return c.mapper.ResolveRoute(ctx, log.RouteName, log.ServiceID)
}

func (c *InternalRouteCollector) resolveRouteGroup(ctx context.Context, mapping model.RouteMapping, log model.ApisixAccessLog) string {
	input := RouteGroupInput{
		AppID:       mapping.AppID,
		ComponentID: mapping.ComponentID,
		URI:         chooseURI(log),
	}
	if c.routeGroupRules == nil || mapping.AppID == "" {
		return c.routeGroups.Resolve(input)
	}
	rules, err := c.routeGroupRules.GetRouteGroupRules(ctx, mapping.AppID)
	if err != nil || len(rules) == 0 {
		return c.routeGroups.Resolve(input)
	}
	return c.routeGroups.ResolveWithUserRules(input, serviceRouteGroupRules(rules))
}

func serviceRouteGroupRules(rules []model.RouteGroupRule) []RouteGroupRule {
	result := make([]RouteGroupRule, 0, len(rules))
	for _, rule := range rules {
		result = append(result, RouteGroupRule{
			Prefix: rule.Prefix,
			Group:  rule.Group,
		})
	}
	return result
}

func chooseURI(log model.ApisixAccessLog) string {
	if log.URI != "" {
		return log.URI
	}
	return log.RequestURI
}

func metricFromLog(routeGroup string, mapping model.RouteMapping, log model.ApisixAccessLog) model.RouteGroupMetric {
	metric := model.RouteGroupMetric{
		RouteGroup:   routeGroup,
		RequestCount: 1,
		LatencySumMs: log.RequestTime * 1000,
		LatencyCount: 1,
		TeamID:       mapping.TeamID,
		AppID:        mapping.AppID,
		Namespace:    mapping.Namespace,
		ComponentID:  mapping.ComponentID,
		ServiceAlias: mapping.ServiceAlias,
	}
	if log.Status >= 500 {
		metric.ErrorCount = 1
	}
	if log.UpstreamStatus >= 500 {
		metric.UpstreamErrorCount = 1
	}
	return metric
}

func scopesForMapping(mapping model.RouteMapping) []model.AggregateScope {
	return []model.AggregateScope{
		{Kind: model.ScopePlatform},
		{Kind: model.ScopeTeam, ID: valueOrUnknown(firstNonEmptyString(mapping.TeamID, mapping.Namespace), "unknown_team")},
		{Kind: model.ScopeApp, ID: valueOrUnknown(mapping.AppID, "unknown_app")},
		{Kind: model.ScopeComponent, ID: valueOrUnknown(mapping.ComponentID, "unknown_component")},
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func valueOrUnknown(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
