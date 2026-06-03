package service

import (
	"context"
	"fmt"
	"time"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
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
}

type InternalRouteCollector struct {
	store           AggregateStore
	mapper          RouteMapper
	routeGroups     *RouteGroupResolver
	routeGroupRules RouteGroupRuleStore
	now             func() time.Time
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
	for _, log := range logs {
		if log.RouteID == "" && log.ServiceID == "" {
			continue
		}
		mapping, err := c.mapper.ResolveRoute(ctx, log.RouteID, log.ServiceID)
		if err != nil {
			mapping = model.RouteMapping{
				RouteID:     log.RouteID,
				TeamID:      "unknown_team",
				AppID:       "unknown_app",
				ComponentID: "unknown_component",
			}
		}
		routeGroup := c.resolveRouteGroup(ctx, mapping, log)
		metric := metricFromLog(routeGroup, mapping, log)
		for _, window := range model.HotWindows() {
			for _, scope := range scopesForMapping(mapping) {
				if err := c.store.AddRouteGroupBucket(ctx, scope, window, bucket, metric); err != nil {
					return fmt.Errorf("write route group bucket: %w", err)
				}
			}
		}
	}
	return nil
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
		{Kind: model.ScopeTeam, ID: valueOrUnknown(mapping.TeamID, "unknown_team")},
		{Kind: model.ScopeApp, ID: valueOrUnknown(mapping.AppID, "unknown_app")},
		{Kind: model.ScopeComponent, ID: valueOrUnknown(mapping.ComponentID, "unknown_component")},
	}
}

func valueOrUnknown(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
