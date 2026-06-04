package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var apisixRouteGVR = schema.GroupVersionResource{
	Group:    "apisix.apache.org",
	Version:  "v2",
	Resource: "apisixroutes",
}

var apisixUpstreamGVR = schema.GroupVersionResource{
	Group:    "apisix.apache.org",
	Version:  "v2",
	Resource: "apisixupstreams",
}

var apisixTLSGVR = schema.GroupVersionResource{
	Group:    "apisix.apache.org",
	Version:  "v2",
	Resource: "apisixtlses",
}

type RouteClient interface {
	List(ctx context.Context, namespace string) ([]*unstructured.Unstructured, error)
	Update(ctx context.Context, namespace string, route *unstructured.Unstructured) error
}

type DynamicRouteClient struct {
	client dynamic.Interface
}

func NewDynamicRouteClient(client dynamic.Interface) *DynamicRouteClient {
	return &DynamicRouteClient{client: client}
}

func (c *DynamicRouteClient) List(ctx context.Context, namespace string) ([]*unstructured.Unstructured, error) {
	list, err := c.client.Resource(apisixRouteGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	routes := make([]*unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		item := list.Items[i]
		routes = append(routes, &item)
	}
	return routes, nil
}

func (c *DynamicRouteClient) Update(ctx context.Context, namespace string, route *unstructured.Unstructured) error {
	_, err := c.client.Resource(apisixRouteGVR).Namespace(namespace).Update(ctx, route, metav1.UpdateOptions{})
	return err
}

func (c *DynamicRouteClient) ListUpstreams(ctx context.Context, namespace string) ([]*unstructured.Unstructured, error) {
	return c.listResource(ctx, namespace, apisixUpstreamGVR)
}

func (c *DynamicRouteClient) ListTLS(ctx context.Context, namespace string) ([]*unstructured.Unstructured, error) {
	return c.listResource(ctx, namespace, apisixTLSGVR)
}

func (c *DynamicRouteClient) listResource(ctx context.Context, namespace string, gvr schema.GroupVersionResource) ([]*unstructured.Unstructured, error) {
	list, err := c.client.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	items := make([]*unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		item := list.Items[i]
		items = append(items, &item)
	}
	return items, nil
}

type HTTPLoggerAttachJob struct {
	Client         RouteClient
	MappingStore   RouteMappingStore
	Namespaces     []string
	AppID          string
	MappingAppID   string
	Metadata       model.RouteMappingMetadata
	ServiceAliases []string
	Config         HTTPLoggerConfig
	Interval       time.Duration
	Logger         *logrus.Logger
}

type RouteMappingStore interface {
	SaveRouteMapping(ctx context.Context, mapping model.RouteMapping, ttl time.Duration) error
}

type AppPrometheusRouteReplacer interface {
	ReplaceAppPrometheusRoutes(ctx context.Context, appID string, routes []string) error
}

func (j *HTTPLoggerAttachJob) RunOnce(ctx context.Context) error {
	if j.Client == nil {
		return fmt.Errorf("route client is required")
	}
	appRoutes := make(map[string][]string)
	appRouteSeen := make(map[string]map[string]struct{})
	if targetAppID := firstNonEmptyString(j.MappingAppID, j.AppID); targetAppID != "" && len(j.ServiceAliases) > 0 {
		appRoutes[targetAppID] = nil
	}
	for _, namespace := range j.Namespaces {
		if j.Logger != nil {
			j.Logger.WithFields(logrus.Fields{
				"namespace":      namespace,
				"match_app_id":   j.AppID,
				"mapping_app_id": j.MappingAppID,
				"collector_uri":  j.Config.URI,
			}).Info("syncing apisix routes for http-logger")
		}
		routes, err := j.Client.List(ctx, namespace)
		if err != nil {
			return fmt.Errorf("list apisix routes in %s: %w", namespace, err)
		}
		if j.Logger != nil {
			j.Logger.WithFields(logrus.Fields{
				"namespace":      namespace,
				"match_app_id":   j.AppID,
				"mapping_app_id": j.MappingAppID,
				"route_count":    len(routes),
			}).Info("scanned apisix routes for http-logger")
		}
		for _, route := range routes {
			routeMatch := j.matchRoute(route)
			managed := IsRainbondManagedRoute(route)
			if j.Logger != nil && route != nil {
				labels := route.GetLabels()
				j.Logger.WithFields(logrus.Fields{
					"namespace":           namespace,
					"route":               route.GetName(),
					"label_app_id":        labels["app_id"],
					"label_creator":       labels["creator"],
					"label_service_id":    firstLabel(labels, "service_id", "component_id"),
					"label_service_alias": findServiceAlias(labels),
					"matched":             routeMatch.matched,
					"match_reason":        routeMatch.reason,
					"rainbond_managed":    managed,
					"service_aliases":     strings.Join(normalizeServiceAliases(j.ServiceAliases), ","),
				}).Info("checked apisix route for http-logger")
			}
			if !routeMatch.matched {
				continue
			}
			changed, err := EnsureHTTPLoggerPlugin(route, j.Config)
			if err != nil {
				return fmt.Errorf("ensure http logger for %s/%s: %w", namespace, route.GetName(), err)
			}
			if j.Logger != nil {
				j.Logger.WithFields(logrus.Fields{
					"namespace":              namespace,
					"route":                  route.GetName(),
					"changed":                changed,
					"collector_uri":          j.Config.URI,
					"http_logger_timeout":    j.Config.Timeout,
					"http_logger_ssl_verify": j.Config.SSLVerify,
					"mapping_app_id":         j.MappingAppID,
				}).Info("ensured route-level http-logger")
			}
			if !changed {
				mappings, err := j.saveMappings(ctx, namespace, route)
				if err != nil {
					return err
				}
				rememberAppPrometheusRoutes(appRoutes, appRouteSeen, mappings)
				continue
			}
			if err := j.Client.Update(ctx, namespace, route); err != nil {
				return fmt.Errorf("update apisix route %s/%s: %w", namespace, route.GetName(), err)
			}
			mappings, err := j.saveMappings(ctx, namespace, route)
			if err != nil {
				return err
			}
			rememberAppPrometheusRoutes(appRoutes, appRouteSeen, mappings)
			if j.Logger != nil {
				j.Logger.WithFields(logrus.Fields{
					"namespace":     namespace,
					"route":         route.GetName(),
					"collector_uri": j.Config.URI,
				}).Info("attached route-level http-logger")
			}
		}
	}
	if err := j.replaceAppPrometheusRoutes(ctx, appRoutes); err != nil {
		return err
	}
	return nil
}

func (j *HTTPLoggerAttachJob) matchesApp(route *unstructured.Unstructured) bool {
	return j.matchRoute(route).matched
}

type routeMatchResult struct {
	matched bool
	reason  string
}

func (j *HTTPLoggerAttachJob) matchRoute(route *unstructured.Unstructured) routeMatchResult {
	if j.AppID == "" {
		return routeMatchResult{matched: true, reason: "all"}
	}
	if route == nil {
		return routeMatchResult{reason: "none"}
	}
	labels := route.GetLabels()
	if labels["app_id"] == j.AppID {
		return routeMatchResult{matched: true, reason: "app_id_label"}
	}
	aliases := normalizeServiceAliases(j.ServiceAliases)
	if len(aliases) == 0 {
		return routeMatchResult{reason: "none"}
	}
	for _, alias := range aliases {
		if labels["service_alias"] == alias || labels[alias] == "service_alias" {
			return routeMatchResult{matched: true, reason: "service_alias_label"}
		}
		if routeNameMatchesServiceAlias(route.GetName(), alias) {
			return routeMatchResult{matched: true, reason: "service_alias_prefix"}
		}
		for _, value := range strings.Split(labels["component_sort"], ",") {
			if strings.TrimSpace(value) == alias {
				return routeMatchResult{matched: true, reason: "component_sort_label"}
			}
		}
	}
	return routeMatchResult{reason: "none"}
}

func (j *HTTPLoggerAttachJob) Start(ctx context.Context) {
	interval := j.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := j.RunOnce(ctx); err != nil && j.Logger != nil {
				j.Logger.WithError(err).Warn("http-logger attach job failed")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (j *HTTPLoggerAttachJob) saveMappings(ctx context.Context, namespace string, route *unstructured.Unstructured) ([]model.RouteMapping, error) {
	if j.MappingStore == nil {
		if j.Logger != nil && route != nil {
			j.Logger.WithFields(logrus.Fields{
				"namespace": namespace,
				"route":     route.GetName(),
			}).Debug("skip saving apisix route mappings because mapping store is nil")
		}
		return nil, nil
	}
	mappings := RouteMappingsFromApisixRoute(namespace, route)
	if j.Logger != nil && route != nil {
		j.Logger.WithFields(logrus.Fields{
			"namespace":     namespace,
			"route":         route.GetName(),
			"mapping_count": len(mappings),
		}).Info("generated apisix route mappings")
	}
	for i, mapping := range mappings {
		if j.MappingAppID != "" {
			mapping.AppID = j.MappingAppID
		}
		mapping = applyRouteMappingMetadata(mapping, j.Metadata)
		if err := j.MappingStore.SaveRouteMapping(ctx, mapping, 10*time.Minute); err != nil {
			return nil, fmt.Errorf("save route mapping %s: %w", mapping.RouteID, err)
		}
		mappings[i] = mapping
		if j.Logger != nil {
			j.Logger.WithFields(logrus.Fields{
				"namespace":        namespace,
				"route_id":         mapping.RouteID,
				"prometheus_route": mapping.PrometheusRoute,
				"team_id":          mapping.TeamID,
				"team_name":        mapping.TeamName,
				"team_alias":       mapping.TeamAlias,
				"app_id":           mapping.AppID,
				"region_app_id":    mapping.RegionAppID,
				"app_name":         mapping.AppName,
				"region_name":      mapping.RegionName,
				"component_id":     mapping.ComponentID,
				"service_alias":    mapping.ServiceAlias,
			}).Info("saved apisix route mapping")
		}
	}
	return mappings, nil
}

func applyRouteMappingMetadata(mapping model.RouteMapping, metadata model.RouteMappingMetadata) model.RouteMapping {
	mapping.RegionName = firstNonEmptyString(metadata.RegionName, mapping.RegionName)
	mapping.RegionAppID = firstNonEmptyString(metadata.RegionAppID, mapping.RegionAppID)
	mapping.TeamName = firstNonEmptyString(metadata.TeamName, mapping.TeamName)
	mapping.TeamAlias = firstNonEmptyString(metadata.TeamAlias, mapping.TeamAlias)
	mapping.AppName = firstNonEmptyString(metadata.AppName, mapping.AppName)
	return mapping
}

func RouteMappingsFromApisixRoute(namespace string, route *unstructured.Unstructured) []model.RouteMapping {
	if route == nil {
		return nil
	}
	labels := route.GetLabels()
	parentPrometheusRoute := prometheusRouteLabel(namespace, route.GetName(), "")
	mapping := model.RouteMapping{
		RouteID:         route.GetName(),
		TeamID:          firstLabel(labels, "team_id", "tenant_id"),
		AppID:           labels["app_id"],
		RegionAppID:     labels["app_id"],
		ComponentID:     firstLabel(labels, "service_id", "component_id"),
		ServiceAlias:    findServiceAlias(labels),
		Namespace:       namespace,
		PrometheusRoute: parentPrometheusRoute,
	}

	result := routeMappingAliases(mapping, route.GetName(), parentPrometheusRoute)
	httpRoutes, ok, _ := unstructured.NestedSlice(route.Object, "spec", "http")
	if !ok {
		return result
	}
	for _, item := range httpRoutes {
		httpRoute, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := httpRoute["name"].(string)
		if name == "" {
			continue
		}
		childPrometheusRoute := prometheusRouteLabel(namespace, route.GetName(), name)
		child := mapping
		child.RouteID = name
		child.PrometheusRoute = childPrometheusRoute
		if backendService := firstHTTPBackendServiceName(httpRoute); backendService != "" {
			child.ComponentID = firstNonEmptyString(child.ComponentID, backendService)
		}
		result = append(result, routeMappingAliases(child, name, prometheusRouteLabel("", route.GetName(), name), childPrometheusRoute)...)
	}
	return result
}

func routeMappingAliases(mapping model.RouteMapping, routeIDs ...string) []model.RouteMapping {
	seen := make(map[string]struct{})
	result := make([]model.RouteMapping, 0, len(routeIDs))
	for _, routeID := range routeIDs {
		routeID = strings.TrimSpace(routeID)
		if routeID == "" {
			continue
		}
		if _, ok := seen[routeID]; ok {
			continue
		}
		seen[routeID] = struct{}{}
		alias := mapping
		alias.RouteID = routeID
		result = append(result, alias)
	}
	return result
}

func rememberAppPrometheusRoutes(appRoutes map[string][]string, seen map[string]map[string]struct{}, mappings []model.RouteMapping) {
	for _, mapping := range mappings {
		if mapping.AppID == "" || mapping.PrometheusRoute == "" {
			continue
		}
		if seen[mapping.AppID] == nil {
			seen[mapping.AppID] = make(map[string]struct{})
		}
		if _, ok := seen[mapping.AppID][mapping.PrometheusRoute]; ok {
			continue
		}
		seen[mapping.AppID][mapping.PrometheusRoute] = struct{}{}
		appRoutes[mapping.AppID] = append(appRoutes[mapping.AppID], mapping.PrometheusRoute)
	}
}

func (j *HTTPLoggerAttachJob) replaceAppPrometheusRoutes(ctx context.Context, appRoutes map[string][]string) error {
	replacer, ok := j.MappingStore.(AppPrometheusRouteReplacer)
	if !ok || replacer == nil {
		return nil
	}
	for appID, routes := range appRoutes {
		if err := replacer.ReplaceAppPrometheusRoutes(ctx, appID, routes); err != nil {
			return fmt.Errorf("replace app prometheus routes for %s: %w", appID, err)
		}
		if j.Logger != nil {
			j.Logger.WithFields(logrus.Fields{
				"app_id":      appID,
				"route_count": len(routes),
				"routes":      strings.Join(routes, ","),
			}).Info("replaced app prometheus route index")
		}
	}
	return nil
}

func prometheusRouteLabel(namespace, routeName, httpRouteName string) string {
	if httpRouteName == "" {
		if namespace == "" {
			return routeName
		}
		return namespace + "_" + routeName
	}
	if namespace == "" {
		return routeName + "_" + httpRouteName
	}
	return namespace + "_" + routeName + "_" + httpRouteName
}

func firstLabel(labels map[string]string, keys ...string) string {
	for _, key := range keys {
		if labels[key] != "" {
			return labels[key]
		}
	}
	return ""
}

func findServiceAlias(labels map[string]string) string {
	if labels["service_alias"] != "" {
		return labels["service_alias"]
	}
	for key, value := range labels {
		if value == "service_alias" {
			return key
		}
	}
	return ""
}

func normalizeServiceAliases(aliases []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		if _, ok := seen[alias]; ok {
			continue
		}
		seen[alias] = struct{}{}
		result = append(result, alias)
	}
	return result
}

func routeNameMatchesServiceAlias(routeName, alias string) bool {
	return routeName == alias || strings.HasPrefix(routeName, alias+"-")
}

func firstHTTPBackendServiceName(httpRoute map[string]interface{}) string {
	backends, ok := httpRoute["backends"].([]interface{})
	if !ok || len(backends) == 0 {
		return ""
	}
	backend, ok := backends[0].(map[string]interface{})
	if !ok {
		return ""
	}
	serviceName, _ := backend["serviceName"].(string)
	return serviceName
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
