package domain

// HarnessUsageView is the sanitized plan usage paxd reads from a native harness
// CLI. It never contains credentials or raw native output.
type HarnessUsageView struct {
	Harness    string               `json:"harness"`
	State      string               `json:"state"`
	ObservedAt string               `json:"observed_at,omitempty"`
	Plan       string               `json:"plan,omitempty"`
	Windows    []HarnessUsageWindow `json:"windows,omitempty"`
	ExtraUsage *HarnessUsageExtra   `json:"extra_usage,omitempty"`
	ErrorCode  string               `json:"error_code,omitempty"`
}

// HarnessUsageWindow is one rate-limit window. UsedPercent ranges from 0 to 100.
type HarnessUsageWindow struct {
	ID            string  `json:"id"`
	WindowMinutes int     `json:"window_minutes"`
	Scope         string  `json:"scope,omitempty"`
	UsedPercent   float64 `json:"used_percent"`
	ResetsAt      string  `json:"resets_at,omitempty"`
}

// HarnessUsageExtra reports only whether paid usage beyond the plan is enabled.
type HarnessUsageExtra struct {
	Enabled bool `json:"enabled"`
}

type HarnessUsageData struct {
	HarnessUsage HarnessUsageView `json:"harness_usage"`
}
