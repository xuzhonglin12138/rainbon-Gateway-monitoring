package main

import "testing"

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
