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
	URI             string
	Timeout         int
	SSLVerify       bool
	BatchMaxSize    int
	InactiveTimeout int
	BufferDuration  int
	LogFormat       map[string]string
}

func (c HTTPLoggerConfig) plugin() map[string]interface{} {
	config := map[string]interface{}{
		"uri":        c.URI,
		"timeout":    int64(c.Timeout),
		"ssl_verify": c.SSLVerify,
	}
	if c.BatchMaxSize > 0 {
		config["batch_max_size"] = int64(c.BatchMaxSize)
	}
	if c.InactiveTimeout > 0 {
		config["inactive_timeout"] = int64(c.InactiveTimeout)
	}
	if c.BufferDuration > 0 {
		config["buffer_duration"] = int64(c.BufferDuration)
	}
	if len(c.LogFormat) > 0 {
		config["log_format"] = copyStringMap(c.LogFormat)
	}
	return map[string]interface{}{
		"name":   HTTPLoggerPluginName,
		"enable": true,
		"config": config,
	}
}

func DefaultHTTPLoggerLogFormat() map[string]string {
	return map[string]string{
		"timestamp":              "$time_iso8601",
		"route_id":               "$route_name",
		"route_name":             "$route_name",
		"apisix_route_id":        "$route_id",
		"host":                   "$host",
		"method":                 "$request_method",
		"uri":                    "$uri",
		"request_uri":            "$request_uri",
		"status":                 "$status",
		"request_time":           "$request_time",
		"upstream_status":        "$upstream_status",
		"upstream_response_time": "$upstream_response_time",
		"body_bytes_sent":        "$body_bytes_sent",
		"bytes_sent":             "$bytes_sent",
		"client_ip":              "$remote_addr",
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

func RemoveManagedHTTPLoggerPlugin(route *unstructured.Unstructured) (bool, error) {
	if route == nil {
		return false, nil
	}
	annotations := route.GetAnnotations()
	if annotations[HTTPLoggerManagedAnnotation] != "true" {
		return false, nil
	}
	return removeHTTPLoggerPlugin(route, func(map[string]interface{}) bool { return true })
}

func RemoveMatchingHTTPLoggerPlugin(route *unstructured.Unstructured, cfg HTTPLoggerConfig) (bool, error) {
	if route == nil {
		return false, nil
	}
	annotations := route.GetAnnotations()
	managed := annotations[HTTPLoggerManagedAnnotation] == "true"
	return removeHTTPLoggerPlugin(route, func(plugin map[string]interface{}) bool {
		if managed {
			return true
		}
		return cfg.URI != "" && httpLoggerPluginURI(plugin) == cfg.URI
	})
}

func removeHTTPLoggerPlugin(route *unstructured.Unstructured, shouldRemove func(map[string]interface{}) bool) (bool, error) {
	annotations := route.GetAnnotations()
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
		filtered := make([]interface{}, 0, len(plugins))
		for _, pluginItem := range plugins {
			plugin, ok := pluginItem.(map[string]interface{})
			if ok && plugin["name"] == HTTPLoggerPluginName && shouldRemove(plugin) {
				changed = true
				continue
			}
			filtered = append(filtered, pluginItem)
		}
		if len(filtered) == 0 {
			delete(httpRoute, "plugins")
		} else {
			httpRoute["plugins"] = filtered
		}
		httpRoutes[i] = httpRoute
	}
	if !changed {
		return false, nil
	}
	if err := unstructured.SetNestedSlice(route.Object, httpRoutes, "spec", "http"); err != nil {
		return false, err
	}
	if annotations != nil {
		delete(annotations, HTTPLoggerManagedAnnotation)
		route.SetAnnotations(annotations)
	}
	return true, nil
}

func httpLoggerPluginURI(plugin map[string]interface{}) string {
	config, _ := plugin["config"].(map[string]interface{})
	if config == nil {
		return ""
	}
	uri, _ := config["uri"].(string)
	return uri
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
	for _, key := range []string{"batch_max_size", "inactive_timeout", "buffer_duration"} {
		if value, ok := desiredConfig[key]; ok {
			config[key] = value
		}
	}
	if logFormat, ok := desiredConfig["log_format"]; ok {
		config["log_format"] = logFormat
	}
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

func copyStringMap(input map[string]string) map[string]interface{} {
	output := make(map[string]interface{}, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
