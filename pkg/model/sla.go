package model

type SLAConfig struct {
	AppID  string  `json:"app_id"`
	Target float64 `json:"target"`
}

type SLAStatus struct {
	AppID                 string  `json:"app_id"`
	Window                Window  `json:"window"`
	Current               float64 `json:"current"`
	Target                float64 `json:"target"`
	MeetingTarget         bool    `json:"meeting_target"`
	TotalRequests         float64 `json:"total_requests"`
	ErrorRequests         float64 `json:"error_requests"`
	ErrorBudget           float64 `json:"error_budget"`
	ErrorBudgetRemaining  float64 `json:"error_budget_remaining"`
	LastViolationUnix     int64   `json:"last_violation_unix,omitempty"`
	EvidenceLevel         string  `json:"evidence_level"`
	PrometheusQuerySource string  `json:"prometheus_query_source"`
}
