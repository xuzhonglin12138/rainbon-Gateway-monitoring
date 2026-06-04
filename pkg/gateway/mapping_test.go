package gateway

import (
	"testing"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRouteMappingsFromApisixRouteUsesRainbondLabelsAndHTTPRouteNames(t *testing.T) {
	route := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name": "domain-p-p",
			"labels": map[string]interface{}{
				"creator":     "Rainbond",
				"team_id":     "team-a",
				"app_id":      "app-a",
				"service_id":  "svc-a",
				"web-service": "service_alias",
			},
		},
		"spec": map[string]interface{}{
			"http": []interface{}{
				map[string]interface{}{"name": "apisix-http-route-id"},
				map[string]interface{}{
					"name": "route-with-backend",
					"backends": []interface{}{
						map[string]interface{}{"serviceName": "backend-svc-a"},
					},
				},
			},
		},
	}}

	mappings := RouteMappingsFromApisixRoute("tenant-ns", route)
	if len(mappings) != 8 {
		t.Fatalf("mappings length = %d; want 8", len(mappings))
	}
	if mappings[0].RouteID != "domain-p-p" {
		t.Fatalf("first route id = %q; want domain-p-p", mappings[0].RouteID)
	}
	if mappings[1].RouteID != "tenant-ns_domain-p-p" {
		t.Fatalf("second route id = %q; want tenant-ns_domain-p-p", mappings[1].RouteID)
	}
	if mappings[0].TeamID != "team-a" || mappings[0].AppID != "app-a" || mappings[0].ComponentID != "svc-a" {
		t.Fatalf("unexpected mapping: %#v", mappings[0])
	}
	if mappings[0].ServiceAlias != "web-service" {
		t.Fatalf("service alias = %q; want web-service", mappings[0].ServiceAlias)
	}
	if mappings[0].PrometheusRoute != "tenant-ns_domain-p-p" {
		t.Fatalf("prometheus route = %q; want tenant-ns_domain-p-p", mappings[0].PrometheusRoute)
	}
	wantIDs := []string{
		"apisix-http-route-id",
		"domain-p-p_apisix-http-route-id",
		"tenant-ns_domain-p-p_apisix-http-route-id",
	}
	for _, routeID := range wantIDs {
		if findMappingByRouteID(mappings, routeID) == nil {
			t.Fatalf("missing route id alias %q in %#v", routeID, mappings)
		}
	}
	child := findMappingByRouteID(mappings, "tenant-ns_domain-p-p_apisix-http-route-id")
	if child.PrometheusRoute != "tenant-ns_domain-p-p_apisix-http-route-id" {
		t.Fatalf("child prometheus route = %q; want tenant-ns_domain-p-p_apisix-http-route-id", child.PrometheusRoute)
	}
}

func TestRouteMappingsFromApisixRouteFallsBackToBackendServiceName(t *testing.T) {
	route := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name": "domain-p-p",
			"labels": map[string]interface{}{
				"creator": "Rainbond",
				"app_id":  "app-a",
			},
		},
		"spec": map[string]interface{}{
			"http": []interface{}{
				map[string]interface{}{
					"name": "route-with-backend",
					"backends": []interface{}{
						map[string]interface{}{"serviceName": "backend-svc-a"},
					},
				},
			},
		},
	}}

	mappings := RouteMappingsFromApisixRoute("tenant-ns", route)
	if len(mappings) != 5 {
		t.Fatalf("mappings length = %d; want 5", len(mappings))
	}
	child := findMappingByRouteID(mappings, "tenant-ns_domain-p-p_route-with-backend")
	if child == nil {
		t.Fatalf("missing backend apisix route id mapping in %#v", mappings)
	}
	if child.ComponentID != "backend-svc-a" {
		t.Fatalf("backend component id = %q; want backend-svc-a", child.ComponentID)
	}
}

func findMappingByRouteID(mappings []model.RouteMapping, routeID string) *model.RouteMapping {
	for i := range mappings {
		if mappings[i].RouteID == routeID {
			return &mappings[i]
		}
	}
	return nil
}
