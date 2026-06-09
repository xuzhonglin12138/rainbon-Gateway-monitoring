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
		URI:             "http://new/api/v1/collector/apisix/logs",
		Timeout:         3,
		SSLVerify:       false,
		BatchMaxSize:    100,
		InactiveTimeout: 2,
		BufferDuration:  10,
		LogFormat:       DefaultHTTPLoggerLogFormat(),
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
	if config["batch_max_size"] != int64(100) {
		t.Fatalf("batch_max_size = %v; want 100", config["batch_max_size"])
	}
	if config["inactive_timeout"] != int64(2) {
		t.Fatalf("inactive_timeout = %v; want 2", config["inactive_timeout"])
	}
	if config["buffer_duration"] != int64(10) {
		t.Fatalf("buffer_duration = %v; want 10", config["buffer_duration"])
	}
	if logFormat["route_id"] != "$route_name" {
		t.Fatalf("route_id log format = %q; want $route_name", logFormat["route_id"])
	}
	if logFormat["route_name"] != "$route_name" {
		t.Fatalf("route_name log format = %q; want $route_name", logFormat["route_name"])
	}
	if logFormat["apisix_route_id"] != "$route_id" {
		t.Fatalf("apisix_route_id log format = %q; want $route_id", logFormat["apisix_route_id"])
	}
	if logFormat["body_bytes_sent"] != "$body_bytes_sent" {
		t.Fatalf("body_bytes_sent log format = %q; want $body_bytes_sent", logFormat["body_bytes_sent"])
	}
	if logFormat["bytes_sent"] != "$bytes_sent" {
		t.Fatalf("bytes_sent log format = %q; want $bytes_sent", logFormat["bytes_sent"])
	}
}

func TestRemoveManagedHTTPLoggerPluginRemovesOnlyPluginManagedByThisPlugin(t *testing.T) {
	route := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{HTTPLoggerManagedAnnotation: "true"},
			"labels":      map[string]interface{}{"creator": "Rainbond", "app_id": "app-a"},
		},
		"spec": map[string]interface{}{
			"http": []interface{}{
				map[string]interface{}{
					"name": "r1",
					"plugins": []interface{}{
						map[string]interface{}{
							"name":   "response-rewrite",
							"enable": true,
						},
						map[string]interface{}{
							"name":   "http-logger",
							"enable": true,
							"config": map[string]interface{}{"uri": "http://collector"},
						},
					},
				},
			},
		},
	}}

	changed, err := RemoveManagedHTTPLoggerPlugin(route)
	if err != nil {
		t.Fatalf("RemoveManagedHTTPLoggerPlugin() unexpected error: %v", err)
	}
	if !changed {
		t.Fatal("RemoveManagedHTTPLoggerPlugin() changed = false; want true")
	}

	httpRoutes, ok, err := unstructured.NestedSlice(route.Object, "spec", "http")
	if err != nil || !ok {
		t.Fatalf("read spec.http: ok=%v err=%v", ok, err)
	}
	plugins := httpRoutes[0].(map[string]interface{})["plugins"].([]interface{})
	if len(plugins) != 1 {
		t.Fatalf("plugins length = %d; want only non-monitor plugin left", len(plugins))
	}
	if plugins[0].(map[string]interface{})["name"] != "response-rewrite" {
		t.Fatalf("remaining plugin = %#v; want response-rewrite", plugins[0])
	}
	annotations := route.GetAnnotations()
	if _, ok := annotations[HTTPLoggerManagedAnnotation]; ok {
		t.Fatalf("managed annotation still present: %#v", annotations)
	}
}

func TestRemoveManagedHTTPLoggerPluginKeepsUnmanagedHTTPLogger(t *testing.T) {
	route := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]interface{}{"creator": "Rainbond", "app_id": "app-a"},
		},
		"spec": map[string]interface{}{
			"http": []interface{}{
				map[string]interface{}{
					"name": "r1",
					"plugins": []interface{}{
						map[string]interface{}{
							"name":   "http-logger",
							"enable": true,
							"config": map[string]interface{}{"uri": "http://user-collector"},
						},
					},
				},
			},
		},
	}}

	changed, err := RemoveManagedHTTPLoggerPlugin(route)
	if err != nil {
		t.Fatalf("RemoveManagedHTTPLoggerPlugin() unexpected error: %v", err)
	}
	if changed {
		t.Fatal("RemoveManagedHTTPLoggerPlugin() changed = true; want false for unmanaged logger")
	}
}

func TestRemoveMatchingHTTPLoggerPluginRemovesSameCollectorURIWithoutAnnotation(t *testing.T) {
	route := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]interface{}{"creator": "Rainbond", "app_id": "app-a"},
		},
		"spec": map[string]interface{}{
			"http": []interface{}{
				map[string]interface{}{
					"name": "r1",
					"plugins": []interface{}{
						map[string]interface{}{
							"name":   "http-logger",
							"enable": true,
							"config": map[string]interface{}{"uri": "http://collector"},
						},
					},
				},
			},
		},
	}}

	changed, err := RemoveMatchingHTTPLoggerPlugin(route, HTTPLoggerConfig{URI: "http://collector"})
	if err != nil {
		t.Fatalf("RemoveMatchingHTTPLoggerPlugin() unexpected error: %v", err)
	}
	if !changed {
		t.Fatal("RemoveMatchingHTTPLoggerPlugin() changed = false; want same collector logger removed")
	}
	httpRoutes, ok, err := unstructured.NestedSlice(route.Object, "spec", "http")
	if err != nil || !ok {
		t.Fatalf("read spec.http: ok=%v err=%v", ok, err)
	}
	if _, ok := httpRoutes[0].(map[string]interface{})["plugins"]; ok {
		t.Fatalf("route-level plugins still present: %#v", httpRoutes[0])
	}
}

func TestRemoveMatchingHTTPLoggerPluginKeepsDifferentCollectorURI(t *testing.T) {
	route := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]interface{}{"creator": "Rainbond", "app_id": "app-a"},
		},
		"spec": map[string]interface{}{
			"http": []interface{}{
				map[string]interface{}{
					"name": "r1",
					"plugins": []interface{}{
						map[string]interface{}{
							"name":   "http-logger",
							"enable": true,
							"config": map[string]interface{}{"uri": "http://user-collector"},
						},
					},
				},
			},
		},
	}}

	changed, err := RemoveMatchingHTTPLoggerPlugin(route, HTTPLoggerConfig{URI: "http://collector"})
	if err != nil {
		t.Fatalf("RemoveMatchingHTTPLoggerPlugin() unexpected error: %v", err)
	}
	if changed {
		t.Fatal("RemoveMatchingHTTPLoggerPlugin() changed = true; want user collector logger kept")
	}
}
