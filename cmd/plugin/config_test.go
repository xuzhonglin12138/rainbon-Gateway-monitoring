package main

import (
	"os"
	"testing"
)

func TestDeploymentIdentityMatchesGatewayMonitoringPlugin(t *testing.T) {
	if PluginID != "rainbond-gateway-monitoring" {
		t.Fatalf("PluginID = %q, want rainbond-gateway-monitoring", PluginID)
	}
}

func TestDefaultCollectorURIUsesGatewayMonitoringService(t *testing.T) {
	want := "http://rainbond-gateway-monitoring.rbd-system.svc:8080" + CollectorPath
	if DefaultCollectorURI != want {
		t.Fatalf("DefaultCollectorURI = %q, want %q", DefaultCollectorURI, want)
	}
}

func TestDefaultHTTPLoggerBatchSettingsAreConservative(t *testing.T) {
	if DefaultHTTPLoggerBatchMaxSize != 100 {
		t.Fatalf("DefaultHTTPLoggerBatchMaxSize = %d; want 100", DefaultHTTPLoggerBatchMaxSize)
	}
	if DefaultHTTPLoggerInactiveTimeout != 2 {
		t.Fatalf("DefaultHTTPLoggerInactiveTimeout = %d; want 2", DefaultHTTPLoggerInactiveTimeout)
	}
	if DefaultHTTPLoggerBufferDuration != 10 {
		t.Fatalf("DefaultHTTPLoggerBufferDuration = %d; want 10", DefaultHTTPLoggerBufferDuration)
	}
}

func TestCollectorURIFromEnvUsesExplicitCustomValue(t *testing.T) {
	t.Setenv("NM_COLLECTOR_URI", "http://collector.example.com:30004"+CollectorPath)
	t.Setenv("_SERVICE_ALIAS", "gra6b16b")
	t.Setenv("_HOST_IP", "172.16.0.169")
	t.Setenv("GRA6B16B_30004_SERVICE_HOST", "10.43.168.162")
	t.Setenv("GRA6B16B_30004_SERVICE_PORT", "8080")

	if got := collectorURIFromEnv(); got != "http://collector.example.com:30004"+CollectorPath {
		t.Fatalf("collectorURIFromEnv() = %q", got)
	}
}

func TestCollectorURIFromEnvDerivesRainbondNodePortWhenDefaultIsConfigured(t *testing.T) {
	t.Setenv("NM_COLLECTOR_URI", DefaultCollectorURI)
	t.Setenv("_SERVICE_ALIAS", "gra6b16b")
	t.Setenv("_HOST_IP", "172.16.0.169")
	t.Setenv("GRA6B16B_30001_SERVICE_HOST", "10.43.168.161")
	t.Setenv("GRA6B16B_30001_SERVICE_PORT", "5000")
	t.Setenv("GRA6B16B_30004_SERVICE_HOST", "10.43.168.162")
	t.Setenv("GRA6B16B_30004_SERVICE_PORT", "8080")

	want := "http://172.16.0.169:30004" + CollectorPath
	if got := collectorURIFromEnv(); got != want {
		t.Fatalf("collectorURIFromEnv() = %q, want %q", got, want)
	}
}

func TestCollectorURIFromEnvOverridesKubernetesServiceURIWithRainbondNodePort(t *testing.T) {
	t.Setenv("NM_COLLECTOR_URI", "http://gra6b16b.rbd-plugins.svc.cluster.local:8080"+CollectorPath)
	t.Setenv("_SERVICE_ALIAS", "gra6b16b")
	t.Setenv("_HOST_IP", "172.16.0.169")
	t.Setenv("GRA6B16B_30004_SERVICE_HOST", "10.43.168.162")
	t.Setenv("GRA6B16B_30004_SERVICE_PORT", "8080")

	want := "http://172.16.0.169:30004" + CollectorPath
	if got := collectorURIFromEnv(); got != want {
		t.Fatalf("collectorURIFromEnv() = %q, want %q", got, want)
	}
}

func TestCollectorURIFromEnvFallsBackToDefaultWithoutRainbondRuntimeEnv(t *testing.T) {
	unsetEnv(t, "NM_COLLECTOR_URI", "_SERVICE_ALIAS", "_HOST_IP", "GRA6B16B_30004_SERVICE_HOST", "GRA6B16B_30004_SERVICE_PORT")

	if got := collectorURIFromEnv(); got != DefaultCollectorURI {
		t.Fatalf("collectorURIFromEnv() = %q, want %q", got, DefaultCollectorURI)
	}
}

func TestGrafanaBaseURLFromEnvUsesExplicitValue(t *testing.T) {
	unsetEnv(t, "GRAFANA_URL", "GRAFANA_BASE_URL", "GF_SERVER_ROOT_URL", "GRAFANA_HOST", "GRAFANA_PORT")
	t.Setenv("NM_GRAFANA_BASE_URL", "http://grafana.example.local:3000")
	t.Setenv("GRAFANA_HOST", "10.43.1.10")
	t.Setenv("GRAFANA_PORT", "3000")

	if got := grafanaBaseURLFromEnv(); got != "http://grafana.example.local:3000" {
		t.Fatalf("grafanaBaseURLFromEnv() = %q", got)
	}
}

func TestGrafanaBaseURLFromEnvUsesGrafanaURL(t *testing.T) {
	unsetEnv(t, "NM_GRAFANA_BASE_URL", "GRAFANA_BASE_URL", "GF_SERVER_ROOT_URL", "GRAFANA_HOST", "GRAFANA_PORT")
	t.Setenv("GRAFANA_URL", "http://grafana.rbd-system.svc:3000/")

	if got := grafanaBaseURLFromEnv(); got != "http://grafana.rbd-system.svc:3000" {
		t.Fatalf("grafanaBaseURLFromEnv() = %q", got)
	}
}

func TestGrafanaBaseURLFromEnvUsesRainbondConnectionInfo(t *testing.T) {
	unsetEnv(t, "NM_GRAFANA_BASE_URL", "GRAFANA_URL", "GRAFANA_BASE_URL", "GF_SERVER_ROOT_URL")
	t.Setenv("GATEWAY_MONITORING_GRAFANA_HOST", "10.43.2.15")
	t.Setenv("GATEWAY_MONITORING_GRAFANA_PORT", "3000")
	t.Setenv("GATEWAY_MONITORING_GRAFANA_8080_HOST", "10.43.2.16")
	t.Setenv("GATEWAY_MONITORING_GRAFANA_8080_PORT", "8080")

	if got := grafanaBaseURLFromEnv(); got != "http://10.43.2.15:3000" {
		t.Fatalf("grafanaBaseURLFromEnv() = %q", got)
	}
}

func TestGrafanaBaseURLFromEnvPrefersExactGrafanaConnectionInfo(t *testing.T) {
	unsetEnv(t, "NM_GRAFANA_BASE_URL", "GRAFANA_URL", "GRAFANA_BASE_URL", "GF_SERVER_ROOT_URL")
	t.Setenv("GATEWAY_MONITORING_GRAFANA_HOST", "10.43.2.15")
	t.Setenv("GATEWAY_MONITORING_GRAFANA_PORT", "3000")
	t.Setenv("GRAFANA_HOST", "10.43.2.20")
	t.Setenv("GRAFANA_PORT", "3000")

	if got := grafanaBaseURLFromEnv(); got != "http://10.43.2.20:3000" {
		t.Fatalf("grafanaBaseURLFromEnv() = %q", got)
	}
}

func TestRedisAddrFromEnvUsesExplicitValue(t *testing.T) {
	unsetEnv(t, "REDIS_ADDR", "REDIS_ADDRESS", "REDIS_URL", "REDIS_HOST", "REDIS_PORT")
	t.Setenv("NM_REDIS_ADDR", "redis.example.local:6379")
	t.Setenv("REDIS_HOST", "10.43.3.10")
	t.Setenv("REDIS_PORT", "6379")

	if got := redisAddrFromEnv(); got != "redis.example.local:6379" {
		t.Fatalf("redisAddrFromEnv() = %q", got)
	}
}

func TestRedisAddrFromEnvUsesRedisURL(t *testing.T) {
	unsetEnv(t, "NM_REDIS_ADDR", "REDIS_ADDR", "REDIS_ADDRESS", "REDIS_HOST", "REDIS_PORT")
	t.Setenv("REDIS_URL", "redis://:secret@redis.rbd-system.svc:6379/0")

	if got := redisAddrFromEnv(); got != "redis.rbd-system.svc:6379" {
		t.Fatalf("redisAddrFromEnv() = %q", got)
	}
}

func TestRedisAddrFromEnvUsesRainbondConnectionInfo(t *testing.T) {
	unsetEnv(t, "NM_REDIS_ADDR", "REDIS_ADDR", "REDIS_ADDRESS", "REDIS_URL")
	t.Setenv("GATEWAY_MONITORING_REDIS_HOST", "10.43.4.15")
	t.Setenv("GATEWAY_MONITORING_REDIS_PORT", "6379")

	if got := redisAddrFromEnv(); got != "10.43.4.15:6379" {
		t.Fatalf("redisAddrFromEnv() = %q", got)
	}
}

func TestRedisAddrFromEnvPrefersExactRedisConnectionInfo(t *testing.T) {
	unsetEnv(t, "NM_REDIS_ADDR", "REDIS_ADDR", "REDIS_ADDRESS", "REDIS_URL")
	t.Setenv("GATEWAY_MONITORING_REDIS_HOST", "10.43.4.15")
	t.Setenv("GATEWAY_MONITORING_REDIS_PORT", "6379")
	t.Setenv("REDIS_HOST", "10.43.4.20")
	t.Setenv("REDIS_PORT", "6379")

	if got := redisAddrFromEnv(); got != "10.43.4.20:6379" {
		t.Fatalf("redisAddrFromEnv() = %q", got)
	}
}

func TestRedisAddrFromEnvFallsBackToLocalDefault(t *testing.T) {
	unsetEnv(t, "NM_REDIS_ADDR", "REDIS_ADDR", "REDIS_ADDRESS", "REDIS_URL", "REDIS_HOST", "REDIS_PORT", "GATEWAY_MONITORING_REDIS_HOST", "GATEWAY_MONITORING_REDIS_PORT")

	if got := redisAddrFromEnv(); got != "127.0.0.1:6379" {
		t.Fatalf("redisAddrFromEnv() = %q", got)
	}
}

func TestPrometheusBaseURLFromEnvUsesExplicitValue(t *testing.T) {
	unsetEnv(t, "PROMETHEUS_URL", "PROMETHEUS_BASE_URL", "PROMETHEUS_HOST", "PROMETHEUS_PORT")
	t.Setenv("NM_PROMETHEUS_URL", "http://prometheus.example.local:9090/")
	t.Setenv("PROMETHEUS_HOST", "10.43.5.10")
	t.Setenv("PROMETHEUS_PORT", "9090")

	if got := prometheusBaseURLFromEnv(); got != "http://prometheus.example.local:9090" {
		t.Fatalf("prometheusBaseURLFromEnv() = %q", got)
	}
}

func TestPrometheusBaseURLFromEnvUsesCommonURL(t *testing.T) {
	unsetEnv(t, "NM_PROMETHEUS_URL", "PROMETHEUS_BASE_URL", "PROMETHEUS_HOST", "PROMETHEUS_PORT")
	t.Setenv("PROMETHEUS_URL", "http://prometheus.rbd-system.svc:9090/")

	if got := prometheusBaseURLFromEnv(); got != "http://prometheus.rbd-system.svc:9090" {
		t.Fatalf("prometheusBaseURLFromEnv() = %q", got)
	}
}

func TestPrometheusBaseURLFromEnvUsesRainbondConnectionInfo(t *testing.T) {
	unsetEnv(t, "NM_PROMETHEUS_URL", "PROMETHEUS_URL", "PROMETHEUS_BASE_URL")
	t.Setenv("RBD_MONITOR_PROMETHEUS_HOST", "10.43.5.15")
	t.Setenv("RBD_MONITOR_PROMETHEUS_PORT", "9090")

	if got := prometheusBaseURLFromEnv(); got != "http://10.43.5.15:9090" {
		t.Fatalf("prometheusBaseURLFromEnv() = %q", got)
	}
}

func TestPrometheusBaseURLFromEnvFallsBackToRainbondMonitor(t *testing.T) {
	unsetEnv(t, "NM_PROMETHEUS_URL", "PROMETHEUS_URL", "PROMETHEUS_BASE_URL", "PROMETHEUS_HOST", "PROMETHEUS_PORT", "RBD_MONITOR_PROMETHEUS_HOST", "RBD_MONITOR_PROMETHEUS_PORT")

	if got := prometheusBaseURLFromEnv(); got != "http://rbd-monitor.rbd-system.svc:9999" {
		t.Fatalf("prometheusBaseURLFromEnv() = %q", got)
	}
}

func unsetEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		previous, existed := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(key, previous)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}
