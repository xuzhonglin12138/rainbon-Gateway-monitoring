package model

type SLAConfig struct {
	AppID            string  `json:"app_id"`
	Enabled          bool    `json:"enabled"`
	URL              string  `json:"url,omitempty"`
	Target           float64 `json:"target"`
	IntervalSeconds  int     `json:"interval_seconds"`
	TimeoutSeconds   int     `json:"timeout_seconds"`
	SuccessStatusMin int     `json:"success_status_min"`
	SuccessStatusMax int     `json:"success_status_max"`
	UpdatedAt        int64   `json:"updated_at,omitempty"`
}

type SLAHealthSample struct {
	AppID      string  `json:"app_id"`
	CheckedAt  int64   `json:"checked_at"`
	Success    bool    `json:"success"`
	StatusCode int     `json:"status_code"`
	LatencyMs  float64 `json:"latency_ms"`
	ErrorType  string  `json:"error_type,omitempty"`
}

type SLAHealthAggregate struct {
	TotalChecks    int64   `json:"total_checks"`
	SuccessChecks  int64   `json:"success_checks"`
	FailureChecks  int64   `json:"failure_checks"`
	LatencySumMs   float64 `json:"latency_sum_ms"`
	LastCheckedAt  int64   `json:"last_checked_at,omitempty"`
	LastStatusCode int     `json:"last_status_code,omitempty"`
	LastErrorType  string  `json:"last_error_type,omitempty"`
}

type SLAStatus struct {
	AppID                 string  `json:"app_id"`
	Window                Window  `json:"window"`
	HealthWindow          string  `json:"health_window,omitempty"`
	Configured            bool    `json:"configured"`
	Current               float64 `json:"current"`
	Target                float64 `json:"target"`
	MeetingTarget         bool    `json:"meeting_target"`
	TotalRequests         float64 `json:"total_requests"`
	ErrorRequests         float64 `json:"error_requests"`
	TotalChecks           int64   `json:"total_checks,omitempty"`
	SuccessChecks         int64   `json:"success_checks,omitempty"`
	FailureChecks         int64   `json:"failure_checks,omitempty"`
	AvgLatencyMs          float64 `json:"avg_latency_ms,omitempty"`
	LastCheckedAt         int64   `json:"last_checked_at,omitempty"`
	LastStatusCode        int     `json:"last_status_code,omitempty"`
	LastErrorType         string  `json:"last_error_type,omitempty"`
	IntervalSeconds       int     `json:"interval_seconds,omitempty"`
	TimeoutSeconds        int     `json:"timeout_seconds,omitempty"`
	SuccessStatusRange    string  `json:"success_status_range,omitempty"`
	ErrorBudget           float64 `json:"error_budget"`
	ErrorBudgetRemaining  float64 `json:"error_budget_remaining"`
	LastViolationUnix     int64   `json:"last_violation_unix,omitempty"`
	EvidenceLevel         string  `json:"evidence_level"`
	PrometheusQuerySource string  `json:"prometheus_query_source"`
}
