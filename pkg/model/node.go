package model

type PlatformNode struct {
	Name                   string
	Cluster                string
	Status                 string
	CPURequestedCores      float64
	CPUAllocatableCores    float64
	CPUAllocatedPercent    float64
	MemoryRequestedBytes   float64
	MemoryAllocatableBytes float64
	MemoryAllocatedPercent float64
}

type PlatformNodeSummary struct {
	Name                   string  `json:"name"`
	Cluster                string  `json:"cluster,omitempty"`
	Status                 string  `json:"status,omitempty"`
	RequestCount           float64 `json:"request_count"`
	P50LatencyMs           float64 `json:"p50_latency_ms"`
	AvgLatencyMs           float64 `json:"avg_latency_ms"`
	ErrorCount             float64 `json:"error_count"`
	EgressBytesPerSec      float64 `json:"egress_bytes_per_sec"`
	CPURequestedCores      float64 `json:"cpu_requested_cores"`
	CPUAllocatableCores    float64 `json:"cpu_allocatable_cores"`
	CPUAllocatedPercent    float64 `json:"cpu_allocated_percent"`
	MemoryRequestedBytes   float64 `json:"memory_requested_bytes"`
	MemoryAllocatableBytes float64 `json:"memory_allocatable_bytes"`
	MemoryAllocatedPercent float64 `json:"memory_allocated_percent"`
}

type PlatformNodeDetail struct {
	Name                   string  `json:"name"`
	Cluster                string  `json:"cluster,omitempty"`
	Status                 string  `json:"status"`
	CPUUsagePercent        float64 `json:"cpu_usage_percent"`
	MemoryUsagePercent     float64 `json:"memory_usage_percent"`
	CPURequestedCores      float64 `json:"cpu_requested_cores"`
	CPUAllocatableCores    float64 `json:"cpu_allocatable_cores"`
	CPUAllocatedPercent    float64 `json:"cpu_allocated_percent"`
	MemoryRequestedBytes   float64 `json:"memory_requested_bytes"`
	MemoryAllocatableBytes float64 `json:"memory_allocatable_bytes"`
	MemoryAllocatedPercent float64 `json:"memory_allocated_percent"`
}
