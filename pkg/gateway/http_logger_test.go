package gateway

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestEnsureHTTPLoggerPluginAppendsRouteLevelLoggerWithoutTouchingOtherPlugins(t *testing.T) {
	route := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]interface{}{
				"creator": "Rainbond",
				"app_id":  "app-a",
			},
		},
		"spec": map[string]interface{}{
			"http": []interface{}{
				map[string]interface{}{
					"name": "r1",
					"plugins": []interface{}{
						map[string]interface{}{
							"name":   "response-rewrite",
							"enable": true,
							"config": map[string]interface{}{"status_code": int64(404)},
						},
					},
				},
			},
		},
	}}

	changed, err := EnsureHTTPLoggerPlugin(route, HTTPLoggerConfig{
		URI:       "http://network-monitor-plugin/api/v1/collector/apisix/logs",
		Timeout:   3,
		SSLVerify: false,
	})
	if err != nil {
		t.Fatalf("EnsureHTTPLoggerPlugin() unexpected error: %v", err)
	}
	if !changed {
		t.Fatal("EnsureHTTPLoggerPlugin() changed = false; want true")
	}

	httpRoutes, ok, err := unstructured.NestedSlice(route.Object, "spec", "http")
	if err != nil || !ok {
		t.Fatalf("read spec.http: ok=%v err=%v", ok, err)
	}
	httpRoute := httpRoutes[0].(map[string]interface{})
	plugins := httpRoute["plugins"].([]interface{})
	if len(plugins) != 2 {
		t.Fatalf("plugins length = %d; want 2", len(plugins))
	}
	if plugins[0].(map[string]interface{})["name"] != "response-rewrite" {
		t.Fatalf("first plugin was overwritten: %#v", plugins[0])
	}
	logger := plugins[1].(map[string]interface{})
	if logger["name"] != "http-logger" {
		t.Fatalf("second plugin name = %v; want http-logger", logger["name"])
	}
	if logger["enable"] != true {
		t.Fatalf("http-logger enable = %v; want true", logger["enable"])
	}
	config := logger["config"].(map[string]interface{})
	if config["uri"] != "http://network-monitor-plugin/api/v1/collector/apisix/logs" {
		t.Fatalf("http-logger uri = %v", config["uri"])
	}
	if config["log_format"] != nil {
		t.Fatalf("http-logger log_format = %v; want nil when not configured", config["log_format"])
	}

	annotations, _, err := unstructured.NestedStringMap(route.Object, "metadata", "annotations")
	if err != nil {
		t.Fatalf("read annotations: %v", err)
	}
	if annotations[HTTPLoggerManagedAnnotation] != "true" {
		t.Fatalf("managed annotation = %q; want true", annotations[HTTPLoggerManagedAnnotation])
	}
}

func TestEnsureHTTPLoggerPluginRepairsManagedFieldsIdempotently(t *testing.T) {
	route := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]interface{}{"app_id": "app-a"},
		},
		"spec": map[string]interface{}{
			"http": []interface{}{
				map[string]interface{}{
					"name": "r1",
					"plugins": []interface{}{
						map[string]interface{}{
							"name":   "http-logger",
							"enable": false,
							"config": map[string]interface{}{"uri": "http://old", "timeout": int64(10)},
						},
					},
				},
			},
		},
	}}

	cfg := HTTPLoggerConfig{
		URI:       "http://new/api/v1/collector/apisix/logs",
		Timeout:   3,
		SSLVerify: false,
		LogFormat: DefaultHTTPLoggerLogFormat(),
	}
	changed, err := EnsureHTTPLoggerPlugin(route, cfg)
	if err != nil {
		t.Fatalf("EnsureHTTPLoggerPlugin() unexpected error: %v", err)
	}
	if !changed {
		t.Fatal("first repair changed = false; want true")
	}

	changed, err = EnsureHTTPLoggerPlugin(route, cfg)
	if err != nil {
		t.Fatalf("EnsureHTTPLoggerPlugin() second call unexpected error: %v", err)
	}
	if changed {
		t.Fatal("second repair changed = true; want false")
	}

	httpRoutes, ok, err := unstructured.NestedSlice(route.Object, "spec", "http")
	if err != nil || !ok {
		t.Fatalf("read spec.http: ok=%v err=%v", ok, err)
	}
	plugins := httpRoutes[0].(map[string]interface{})["plugins"].([]interface{})
	config := plugins[0].(map[string]interface{})["config"].(map[string]interface{})
	logFormat := config["log_format"].(map[string]interface{})
	if logFormat["route_name"] != "$route_name" {
		t.Fatalf("route_name log format = %q; want $route_name", logFormat["route_name"])
	}
	if logFormat["body_bytes_sent"] != "$body_bytes_sent" {
		t.Fatalf("body_bytes_sent log format = %q; want $body_bytes_sent", logFormat["body_bytes_sent"])
	}
	if logFormat["bytes_sent"] != "$bytes_sent" {
		t.Fatalf("bytes_sent log format = %q; want $bytes_sent", logFormat["bytes_sent"])
	}
}
