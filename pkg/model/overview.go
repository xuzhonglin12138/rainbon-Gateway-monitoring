package model

type Overview struct {
	Scope              AggregateScope `json:"scope"`
	Window             Window         `json:"window"`
	RequestCount        float64        `json:"request_count"`
	ErrorCount          float64        `json:"error_count"`
	ErrorRate           float64        `json:"error_rate"`
	AvgLatencyMs        float64        `json:"avg_latency_ms"`
	EgressBytesPerSec   float64        `json:"egress_bytes_per_sec"`
	ThroughputPerSecond float64        `json:"throughput_per_second,omitempty"`
	NetworkReceiveBps   float64        `json:"network_receive_bps,omitempty"`
	NetworkTransmitBps  float64        `json:"network_transmit_bps,omitempty"`
	EvidenceLevel       string         `json:"evidence_level"`
}
