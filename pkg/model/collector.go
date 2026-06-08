package model

import (
	"encoding/json"
	"strconv"
)

type ApisixAccessLog struct {
	Timestamp            string  `json:"timestamp"`
	RouteID              string  `json:"route_id"`
	RouteName            string  `json:"route_name"`
	ServiceID            string  `json:"service_id"`
	Host                 string  `json:"host"`
	Method               string  `json:"method"`
	URI                  string  `json:"uri"`
	RequestURI           string  `json:"request_uri"`
	Status               int     `json:"status"`
	RequestTime          float64 `json:"request_time"`
	UpstreamStatus       int     `json:"upstream_status"`
	UpstreamResponseTime float64 `json:"upstream_response_time"`
	BodyBytesSent        int64   `json:"body_bytes_sent"`
	BytesSent            int64   `json:"bytes_sent"`
	ResponseSize         int64   `json:"response_size"`
	ClientIP             string  `json:"client_ip"`
}

func (l *ApisixAccessLog) UnmarshalJSON(data []byte) error {
	type rawLog struct {
		Timestamp            string      `json:"timestamp"`
		RouteID              string      `json:"route_id"`
		RouteName            string      `json:"route_name"`
		ServiceID            string      `json:"service_id"`
		Host                 string      `json:"host"`
		Method               string      `json:"method"`
		URI                  string      `json:"uri"`
		RequestURI           string      `json:"request_uri"`
		Status               interface{} `json:"status"`
		RequestTime          interface{} `json:"request_time"`
		UpstreamStatus       interface{} `json:"upstream_status"`
		UpstreamResponseTime interface{} `json:"upstream_response_time"`
		Latency              interface{} `json:"latency"`
		UpstreamLatency      interface{} `json:"upstream_latency"`
		BodyBytesSent        interface{} `json:"body_bytes_sent"`
		BytesSent            interface{} `json:"bytes_sent"`
		ResponseSize         interface{} `json:"response_size"`
		ResponseBytes        interface{} `json:"response_bytes"`
		ClientIP             string      `json:"client_ip"`
		Request              struct {
			Method string `json:"method"`
			URI    string `json:"uri"`
		} `json:"request"`
		Response struct {
			Status interface{} `json:"status"`
			Size   interface{} `json:"size"`
			Bytes  interface{} `json:"bytes"`
		} `json:"response"`
	}
	var raw rawLog
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	l.Timestamp = raw.Timestamp
	l.RouteID = raw.RouteID
	l.RouteName = raw.RouteName
	l.ServiceID = raw.ServiceID
	l.Host = raw.Host
	l.Method = raw.Method
	l.URI = raw.URI
	l.RequestURI = raw.RequestURI
	l.Status = flexibleInt(raw.Status)
	l.RequestTime = flexibleFloat(raw.RequestTime)
	l.UpstreamStatus = flexibleInt(raw.UpstreamStatus)
	l.UpstreamResponseTime = flexibleFloat(raw.UpstreamResponseTime)
	l.BodyBytesSent = flexibleInt64(raw.BodyBytesSent)
	l.BytesSent = flexibleInt64(raw.BytesSent)
	l.ResponseSize = flexibleInt64(raw.ResponseSize)
	l.ClientIP = raw.ClientIP
	if l.Method == "" {
		l.Method = raw.Request.Method
	}
	if l.URI == "" {
		l.URI = raw.Request.URI
	}
	if l.Status == 0 {
		l.Status = flexibleInt(raw.Response.Status)
	}
	if l.RequestTime == 0 {
		l.RequestTime = millisecondsToSeconds(flexibleFloat(raw.Latency))
	}
	if l.UpstreamResponseTime == 0 {
		l.UpstreamResponseTime = millisecondsToSeconds(flexibleFloat(raw.UpstreamLatency))
	}
	if l.ResponseSize == 0 {
		l.ResponseSize = flexibleInt64(raw.ResponseSize)
	}
	if l.ResponseSize == 0 {
		l.ResponseSize = flexibleInt64(raw.ResponseBytes)
	}
	if l.ResponseSize == 0 {
		l.ResponseSize = flexibleInt64(raw.Response.Size)
	}
	if l.ResponseSize == 0 {
		l.ResponseSize = flexibleInt64(raw.Response.Bytes)
	}
	return nil
}

func millisecondsToSeconds(value float64) float64 {
	if value == 0 {
		return 0
	}
	return value / 1000
}

func flexibleInt(value interface{}) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case string:
		parsed, _ := strconv.Atoi(typed)
		return parsed
	default:
		return 0
	}
}

func flexibleInt64(value interface{}) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case string:
		parsed, _ := strconv.ParseInt(typed, 10, 64)
		return parsed
	default:
		return 0
	}
}

func flexibleFloat(value interface{}) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case string:
		parsed, _ := strconv.ParseFloat(typed, 64)
		return parsed
	default:
		return 0
	}
}

type ScopeKind string

const (
	ScopePlatform  ScopeKind = "platform"
	ScopeTeam      ScopeKind = "team"
	ScopeApp       ScopeKind = "app"
	ScopeComponent ScopeKind = "component"
)

type AggregateScope struct {
	Kind ScopeKind
	ID   string
}

func (s AggregateScope) RedisPart() string {
	if s.Kind == ScopePlatform {
		return "platform"
	}
	return string(s.Kind) + ":" + s.ID
}

type RouteMapping struct {
	RouteID         string `json:"route_id"`
	TeamID          string `json:"team_id"`
	TeamName        string `json:"team_name,omitempty"`
	TeamAlias       string `json:"team_alias,omitempty"`
	AppID           string `json:"app_id"`
	RegionAppID     string `json:"region_app_id,omitempty"`
	AppName         string `json:"app_name,omitempty"`
	RegionName      string `json:"region_name,omitempty"`
	ComponentID     string `json:"component_id"`
	ServiceAlias    string `json:"service_alias"`
	Namespace       string `json:"namespace"`
	PrometheusRoute string `json:"prometheus_route"`
}

type RouteMappingMetadata struct {
	RegionName  string `json:"region_name,omitempty"`
	RegionAppID string `json:"region_app_id,omitempty"`
	AppID       string `json:"app_id,omitempty"`
	TeamName    string `json:"team_name,omitempty"`
	TeamAlias   string `json:"team_alias,omitempty"`
	AppName     string `json:"app_name,omitempty"`
}

type RouteGroupMetric struct {
	RouteGroup         string  `json:"route_group"`
	RequestCount       int64   `json:"request_count"`
	ErrorCount         int64   `json:"error_count"`
	UpstreamErrorCount int64   `json:"upstream_error_count"`
	LatencySumMs       float64 `json:"latency_sum_ms"`
	LatencyCount       int64   `json:"latency_count"`
	EgressBytes        int64   `json:"egress_bytes"`
	AppID              string  `json:"app_id,omitempty"`
	TeamID             string  `json:"team_id,omitempty"`
	TeamName           string  `json:"team_name,omitempty"`
	TeamAlias          string  `json:"team_alias,omitempty"`
	Namespace          string  `json:"namespace,omitempty"`
	RegionAppID        string  `json:"region_app_id,omitempty"`
	AppName            string  `json:"app_name,omitempty"`
	RegionName         string  `json:"region_name,omitempty"`
	ComponentID        string  `json:"component_id,omitempty"`
	ServiceAlias       string  `json:"service_alias,omitempty"`
}

func (m RouteGroupMetric) ErrorRate() float64 {
	if m.RequestCount == 0 {
		return 0
	}
	return float64(m.ErrorCount) / float64(m.RequestCount)
}

func (m RouteGroupMetric) UpstreamErrorRate() float64 {
	if m.RequestCount == 0 {
		return 0
	}
	return float64(m.UpstreamErrorCount) / float64(m.RequestCount)
}

func (m RouteGroupMetric) AvgLatencyMs() float64 {
	if m.LatencyCount == 0 {
		return 0
	}
	return m.LatencySumMs / float64(m.LatencyCount)
}

type RouteGroupItem struct {
	RouteGroup         string  `json:"route_group"`
	RequestCount       int64   `json:"request_count"`
	ErrorCount         int64   `json:"error_count"`
	ErrorRate          float64 `json:"error_rate"`
	UpstreamErrorCount int64   `json:"upstream_error_count"`
	UpstreamErrorRate  float64 `json:"upstream_error_rate"`
	AvgLatencyMs       float64 `json:"avg_latency_ms"`
	EgressBytes        int64   `json:"egress_bytes,omitempty"`
	AppID              string  `json:"app_id,omitempty"`
	TeamID             string  `json:"team_id,omitempty"`
	TeamName           string  `json:"team_name,omitempty"`
	TeamAlias          string  `json:"team_alias,omitempty"`
	Namespace          string  `json:"namespace,omitempty"`
	RegionAppID        string  `json:"region_app_id,omitempty"`
	AppName            string  `json:"app_name,omitempty"`
	RegionName         string  `json:"region_name,omitempty"`
	ComponentID        string  `json:"component_id,omitempty"`
	ServiceAlias       string  `json:"service_alias,omitempty"`
}

type RouteGroupBucketPoint struct {
	Timestamp int64
	Metric    RouteGroupMetric
}

func NewRouteGroupItem(metric RouteGroupMetric) RouteGroupItem {
	return RouteGroupItem{
		RouteGroup:         metric.RouteGroup,
		RequestCount:       metric.RequestCount,
		ErrorCount:         metric.ErrorCount,
		ErrorRate:          metric.ErrorRate(),
		UpstreamErrorCount: metric.UpstreamErrorCount,
		UpstreamErrorRate:  metric.UpstreamErrorRate(),
		AvgLatencyMs:       metric.AvgLatencyMs(),
		EgressBytes:        metric.EgressBytes,
		AppID:              metric.AppID,
		TeamID:             metric.TeamID,
		TeamName:           metric.TeamName,
		TeamAlias:          metric.TeamAlias,
		Namespace:          metric.Namespace,
		RegionAppID:        metric.RegionAppID,
		AppName:            metric.AppName,
		RegionName:         metric.RegionName,
		ComponentID:        metric.ComponentID,
		ServiceAlias:       metric.ServiceAlias,
	}
}

type AppTrafficItem struct {
	AppID                string  `json:"app_id"`
	TeamID               string  `json:"team_id,omitempty"`
	TeamName             string  `json:"team_name,omitempty"`
	TeamAlias            string  `json:"team_alias,omitempty"`
	Namespace            string  `json:"namespace,omitempty"`
	RegionAppID          string  `json:"region_app_id,omitempty"`
	AppName              string  `json:"app_name,omitempty"`
	RegionName           string  `json:"region_name,omitempty"`
	Name                 string  `json:"name"`
	RequestCount         int64   `json:"request_count"`
	ErrorCount           int64   `json:"error_count"`
	ErrorRate            float64 `json:"error_rate"`
	UpstreamErrorCount   int64   `json:"upstream_error_count"`
	UpstreamErrorRate    float64 `json:"upstream_error_rate"`
	AvgLatencyMs         float64 `json:"avg_latency_ms"`
	ThroughputPerSecond  float64 `json:"throughput_per_second"`
	TopErrorRouteGroup   string  `json:"top_error_route_group,omitempty"`
	TopErrorRouteErrors  int64   `json:"top_error_route_errors,omitempty"`
	TopLatencyRouteGroup string  `json:"top_latency_route_group,omitempty"`
	TopLatencyRouteAvgMs float64 `json:"top_latency_route_avg_ms,omitempty"`
}

type AppComponentSummary struct {
	ComponentID  string  `json:"component_id"`
	ServiceAlias string  `json:"service_alias,omitempty"`
	Name         string  `json:"name"`
	RequestCount int64   `json:"request_count"`
	ErrorCount   int64   `json:"error_count"`
	ErrorRate    float64 `json:"error_rate"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

type QueryMeta struct {
	Window           Window `json:"window"`
	Partial          bool   `json:"partial"`
	Stale            bool   `json:"stale"`
	FreshnessSeconds int64  `json:"freshness_seconds"`
}
