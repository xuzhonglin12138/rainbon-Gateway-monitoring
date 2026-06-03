package gateway

import (
	"context"
	"fmt"
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
	Client       RouteClient
	MappingStore RouteMappingStore
	Namespaces   []string
	AppID        string
	MappingAppID string
	Config       HTTPLoggerConfig
	Interval     time.Duration
	Logger       *logrus.Logger
}

type RouteMappingStore interface {
	SaveRouteMapping(ctx context.Context, mapping model.RouteMapping, ttl time.Duration) error
}

func (j *HTTPLoggerAttachJob) RunOnce(ctx context.Context) error {
	if j.Client == nil {
		return fmt.Errorf("route client is required")
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
			matched := j.matchesApp(route)
			managed := IsRainbondManagedRoute(route)
			if j.Logger != nil && route != nil {
				labels := route.GetLabels()
				j.Logger.WithFields(logrus.Fields{
					"namespace":        namespace,
					"route":            route.GetName(),
					"label_app_id":     labels["app_id"],
					"label_creator":    labels["creator"],
					"label_service_id": firstLabel(labels, "service_id", "component_id"),
					"matched":          matched,
					"rainbond_managed": managed,
				}).Info("checked apisix route for http-logger")
			}
			if !matched {
				continue
			}
			changed, err := EnsureHTTPLoggerPlugin(route, j.Config)
			if err != nil {
				return fmt.Errorf("ensure http logger for %s/%s: %w", namespace, route.GetName(), err)
			}
			if j.Logger != nil {
				j.Logger.WithFields(logrus.Fields{
					"namespace":      namespace,
					"route":          route.GetName(),
					"changed":        changed,
					"collector_uri":  j.Config.URI,
					"mapping_app_id": j.MappingAppID,
				}).Info("ensured route-level http-logger")
			}
			if !changed {
				if err := j.saveMappings(ctx, namespace, route); err != nil {
					return err
				}
				continue
			}
			if err := j.Client.Update(ctx, namespace, route); err != nil {
				return fmt.Errorf("update apisix route %s/%s: %w", namespace, route.GetName(), err)
			}
			if err := j.saveMappings(ctx, namespace, route); err != nil {
				return err
			}
			if j.Logger != nil {
				j.Logger.WithFields(logrus.Fields{
					"namespace":     namespace,
					"route":         route.GetName(),
					"collector_uri": j.Config.URI,
				}).Info("attached route-level http-logger")
			}
		}
	}
	return nil
}

func (j *HTTPLoggerAttachJob) matchesApp(route *unstructured.Unstructured) bool {
	if j.AppID == "" {
		return true
	}
	if route == nil {
		return false
	}
	return route.GetLabels()["app_id"] == j.AppID
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

func (j *HTTPLoggerAttachJob) saveMappings(ctx context.Context, namespace string, route *unstructured.Unstructured) error {
	if j.MappingStore == nil {
		if j.Logger != nil && route != nil {
			j.Logger.WithFields(logrus.Fields{
				"namespace": namespace,
				"route":     route.GetName(),
			}).Debug("skip saving apisix route mappings because mapping store is nil")
		}
		return nil
	}
	mappings := RouteMappingsFromApisixRoute(namespace, route)
	if j.Logger != nil && route != nil {
		j.Logger.WithFields(logrus.Fields{
			"namespace":     namespace,
			"route":         route.GetName(),
			"mapping_count": len(mappings),
		}).Info("generated apisix route mappings")
	}
	for _, mapping := range mappings {
		if j.MappingAppID != "" {
			mapping.AppID = j.MappingAppID
		}
		if err := j.MappingStore.SaveRouteMapping(ctx, mapping, 10*time.Minute); err != nil {
			return fmt.Errorf("save route mapping %s: %w", mapping.RouteID, err)
		}
		if j.Logger != nil {
			j.Logger.WithFields(logrus.Fields{
				"namespace":        namespace,
				"route_id":         mapping.RouteID,
				"prometheus_route": mapping.PrometheusRoute,
				"team_id":          mapping.TeamID,
				"app_id":           mapping.AppID,
				"component_id":     mapping.ComponentID,
				"service_alias":    mapping.ServiceAlias,
			}).Info("saved apisix route mapping")
		}
	}
	return nil
}

func RouteMappingsFromApisixRoute(namespace string, route *unstructured.Unstructured) []model.RouteMapping {
	if route == nil {
		return nil
	}
	labels := route.GetLabels()
	mapping := model.RouteMapping{
		RouteID:         route.GetName(),
		TeamID:          firstLabel(labels, "team_id", "tenant_id"),
		AppID:           labels["app_id"],
		ComponentID:     firstLabel(labels, "service_id", "component_id"),
		ServiceAlias:    findServiceAlias(labels),
		Namespace:       namespace,
		PrometheusRoute: route.GetName(),
	}

	result := []model.RouteMapping{mapping}
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
		child := mapping
		child.RouteID = name
		if backendService := firstHTTPBackendServiceName(httpRoute); backendService != "" {
			child.ComponentID = firstNonEmptyString(child.ComponentID, backendService)
		}
		result = append(result, child)
	}
	return result
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
