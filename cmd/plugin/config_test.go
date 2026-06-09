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
