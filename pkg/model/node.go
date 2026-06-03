package model

type PlatformNodeSummary struct {
	Name              string  `json:"name"`
	Cluster           string  `json:"cluster,omitempty"`
	RequestCount      float64 `json:"request_count"`
	P50LatencyMs      float64 `json:"p50_latency_ms"`
	ErrorCount        float64 `json:"error_count"`
	EgressBytesPerSec float64 `json:"egress_bytes_per_sec"`
}

type PlatformNodeDetail struct {
	Name               string  `json:"name"`
	Cluster            string  `json:"cluster,omitempty"`
	Status             string  `json:"status"`
	CPUUsagePercent    float64 `json:"cpu_usage_percent"`
	MemoryUsagePercent float64 `json:"memory_usage_percent"`
}
