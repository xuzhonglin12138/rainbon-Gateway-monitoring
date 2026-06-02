package gateway

import (
	"reflect"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	HTTPLoggerPluginName        = "http-logger"
	HTTPLoggerManagedAnnotation = "network-monitor.rainbond.io/http-logger"
)

type HTTPLoggerConfig struct {
	URI       string
	Timeout   int
	SSLVerify bool
}

func (c HTTPLoggerConfig) plugin() map[string]interface{} {
	return map[string]interface{}{
		"name":   HTTPLoggerPluginName,
		"enable": true,
		"config": map[string]interface{}{
			"uri":        c.URI,
			"timeout":    int64(c.Timeout),
			"ssl_verify": c.SSLVerify,
		},
	}
}

func EnsureHTTPLoggerPlugin(route *unstructured.Unstructured, cfg HTTPLoggerConfig) (bool, error) {
	if route == nil || !IsRainbondManagedRoute(route) {
		return false, nil
	}

	httpRoutes, ok, err := unstructured.NestedSlice(route.Object, "spec", "http")
	if err != nil || !ok {
		return false, err
	}

	changed := false
	for i, item := range httpRoutes {
		httpRoute, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		plugins, _ := httpRoute["plugins"].([]interface{})
		newPlugins, pluginChanged := ensurePlugin(plugins, cfg.plugin())
		if pluginChanged {
			httpRoute["plugins"] = newPlugins
			httpRoutes[i] = httpRoute
			changed = true
		}
	}

	if changed {
		if err := unstructured.SetNestedSlice(route.Object, httpRoutes, "spec", "http"); err != nil {
			return false, err
		}
		annotations := route.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[HTTPLoggerManagedAnnotation] = "true"
		route.SetAnnotations(annotations)
	}
	return changed, nil
}

func IsRainbondManagedRoute(route *unstructured.Unstructured) bool {
	labels := route.GetLabels()
	if labels["creator"] == "Rainbond" || labels["creator"] == "rainbond" {
		return true
	}
	if labels["app_id"] != "" || labels["service_alias"] != "" {
		return true
	}
	for _, value := range labels {
		if value == "service_alias" {
			return true
		}
	}
	return false
}

func ensurePlugin(plugins []interface{}, desired map[string]interface{}) ([]interface{}, bool) {
	changed := false
	found := false
	result := make([]interface{}, 0, len(plugins)+1)
	for _, item := range plugins {
		plugin, ok := item.(map[string]interface{})
		if !ok {
			result = append(result, item)
			continue
		}
		if plugin["name"] != HTTPLoggerPluginName {
			result = append(result, item)
			continue
		}
		found = true
		merged := mergeHTTPLoggerPlugin(plugin, desired)
		if !reflect.DeepEqual(plugin, merged) {
			changed = true
		}
		result = append(result, merged)
	}
	if !found {
		result = append(result, desired)
		changed = true
	}
	return result, changed
}

func mergeHTTPLoggerPlugin(existing, desired map[string]interface{}) map[string]interface{} {
	merged := copyMap(existing)
	merged["name"] = HTTPLoggerPluginName
	merged["enable"] = true

	config, _ := merged["config"].(map[string]interface{})
	if config == nil {
		config = make(map[string]interface{})
	} else {
		config = copyMap(config)
	}
	desiredConfig := desired["config"].(map[string]interface{})
	config["uri"] = desiredConfig["uri"]
	config["timeout"] = desiredConfig["timeout"]
	config["ssl_verify"] = desiredConfig["ssl_verify"]
	merged["config"] = config
	return merged
}

func copyMap(input map[string]interface{}) map[string]interface{} {
	output := make(map[string]interface{}, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
