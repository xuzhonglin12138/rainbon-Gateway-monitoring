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

func TestCollectorURIFromEnvUsesExplicitCustomValue(t *testing.T) {
	t.Setenv("NM_COLLECTOR_URI", "http://collector.custom.svc:8080"+CollectorPath)
	t.Setenv("_SERVICE_ALIAS", "gra6b16b")
	t.Setenv("_NAMESPACE", "rbd-plugins")
	t.Setenv("HTTP_8080_PORT", "8080")

	if got := collectorURIFromEnv(); got != "http://collector.custom.svc:8080"+CollectorPath {
		t.Fatalf("collectorURIFromEnv() = %q", got)
	}
}

func TestCollectorURIFromEnvDerivesRainbondServiceWhenDefaultIsConfigured(t *testing.T) {
	t.Setenv("NM_COLLECTOR_URI", DefaultCollectorURI)
	t.Setenv("_SERVICE_ALIAS", "gra6b16b")
	t.Setenv("_NAMESPACE", "rbd-plugins")
	t.Setenv("HTTP_8080_PORT", "8080")

	want := "http://gra6b16b.rbd-plugins.svc:8080" + CollectorPath
	if got := collectorURIFromEnv(); got != want {
		t.Fatalf("collectorURIFromEnv() = %q, want %q", got, want)
	}
}

func TestCollectorURIFromEnvFallsBackToDefaultWithoutRainbondRuntimeEnv(t *testing.T) {
	unsetEnv(t, "NM_COLLECTOR_URI", "_SERVICE_ALIAS", "_NAMESPACE", "HTTP_8080_PORT")

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
