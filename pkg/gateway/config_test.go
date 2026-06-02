package gateway

import "testing"

func TestAPISIXConfigGVRs(t *testing.T) {
	if apisixUpstreamGVR.Resource != "apisixupstreams" {
		t.Fatalf("upstream resource = %q", apisixUpstreamGVR.Resource)
	}
	if apisixTLSGVR.Resource != "apisixtlses" {
		t.Fatalf("tls resource = %q", apisixTLSGVR.Resource)
	}
}
