package domain

// HarnessUsageView is the sanitized plan usage paxd reads from a native harness
// CLI. It never contains credentials or raw native output.
// Items is the generic list clients render; Windows and ExtraUsage are
// deprecated and kept for older clients.
type HarnessUsageView struct {
	Harness      string               `json:"harness"`
	HarnessLabel string               `json:"harness_label,omitempty"`
	State        string               `json:"state"`
	ObservedAt   string               `json:"observed_at,omitempty"`
	Plan         string               `json:"plan,omitempty"`
	Items        []HarnessUsageItem   `json:"items,omitempty"`
	Windows      []HarnessUsageWindow `json:"windows,omitempty"`
	ExtraUsage   *HarnessUsageExtra   `json:"extra_usage,omitempty"`
	ErrorCode    string               `json:"error_code,omitempty"`
}

// HarnessUsageItem is one displayable usage entry. Kind selects the fields:
// percent (UsedPercent, optional WindowMinutes and ResetsAt), amount (Used,
// optional Limit, Unit, and ResetsAt), or status (Value).
type HarnessUsageItem struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Kind          string   `json:"kind"`
	Severity      string   `json:"severity,omitempty"`
	UsedPercent   *float64 `json:"used_percent,omitempty"`
	WindowMinutes int      `json:"window_minutes,omitempty"`
	ResetsAt      string   `json:"resets_at,omitempty"`
	Used          *float64 `json:"used,omitempty"`
	Limit         *float64 `json:"limit,omitempty"`
	Unit          string   `json:"unit,omitempty"`
	Value         string   `json:"value,omitempty"`
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
