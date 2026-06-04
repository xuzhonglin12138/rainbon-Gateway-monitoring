package gateway

import (
	"testing"

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
	if len(mappings) != 3 {
		t.Fatalf("mappings length = %d; want 3", len(mappings))
	}
	if mappings[0].RouteID != "domain-p-p" {
		t.Fatalf("first route id = %q; want domain-p-p", mappings[0].RouteID)
	}
	if mappings[1].RouteID != "apisix-http-route-id" {
		t.Fatalf("second route id = %q; want apisix-http-route-id", mappings[1].RouteID)
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
	if mappings[1].PrometheusRoute != "tenant-ns_domain-p-p_apisix-http-route-id" {
		t.Fatalf("second prometheus route = %q; want tenant-ns_domain-p-p_apisix-http-route-id", mappings[1].PrometheusRoute)
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
	if len(mappings) != 2 {
		t.Fatalf("mappings length = %d; want 2", len(mappings))
	}
	if mappings[1].ComponentID != "backend-svc-a" {
		t.Fatalf("backend component id = %q; want backend-svc-a", mappings[1].ComponentID)
	}
}
