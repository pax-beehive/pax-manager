package userapi

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	// Node control requests are serialized per tunnel, so a slow native usage
	// read must not hold the tunnel for long.
	harnessUsageQueryTimeout = 15 * time.Second
	maxHarnessUsageWindows   = 12
	maxHarnessUsageMinutes   = 31 * 24 * 60
)

type harnessUsageResult struct {
	HarnessUsage *harnessUsagePayload `json:"harness_usage"`
	Error        *struct {
		Code string `json:"code"`
	} `json:"error"`
}

// Optional fields stay raw so one malformed value drops only that value.
type harnessUsagePayload struct {
	Harness      string          `json:"harness"`
	HarnessLabel json.RawMessage `json:"harness_label"`
	State        string          `json:"state"`
	ObservedAt   json.RawMessage `json:"observed_at"`
	Plan         json.RawMessage `json:"plan"`
	Items        json.RawMessage `json:"items"`
	Windows      json.RawMessage `json:"windows"`
	ExtraUsage   json.RawMessage `json:"extra_usage"`
	ErrorCode    json.RawMessage `json:"error_code"`
}

type harnessUsageWindowPayload struct {
	ID            json.RawMessage `json:"id"`
	WindowMinutes json.RawMessage `json:"window_minutes"`
	Scope         json.RawMessage `json:"scope"`
	UsedPercent   json.RawMessage `json:"used_percent"`
	ResetsAt      json.RawMessage `json:"resets_at"`
}

// GetNodeHarnessUsage forwards an on-demand plan usage read to the node owner's
// paxd. Nothing is persisted or cached here.
func (s *Service) GetNodeHarnessUsage(
	ctx context.Context, meta auth.RequestMetadata, nodeID, harness string,
) (int, any, error) {
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return 0, nil, err
	}
	if !validHarnessAuthTarget(nodeID, harness, "") {
		// Any PAX agent type id; agent types without an adapter report unsupported.
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "invalid harness usage request",
		}
	}
	if _, err = s.harnessAuthRemote(ctx, principal, nodeID); err != nil {
		return 0, nil, err
	}
	native := nativeHarness(harness)
	queryCtx, cancel := context.WithTimeout(ctx, harnessUsageQueryTimeout)
	defer cancel()
	raw, err := s.queryNodeControl(queryCtx, nodeID, map[string]any{
		"type":                 "harness_usage.status",
		"harness_usage_status": map[string]any{"harness": native},
	})
	if err != nil {
		return 0, nil, err
	}
	view, err := decodeHarnessUsage(raw, native)
	if err != nil {
		return 0, nil, err
	}
	view.Harness = harness
	return http.StatusOK, domain.HarnessUsageData{HarnessUsage: view}, nil
}

// decodeHarnessUsage validates a paxd view for the native harness name.
func decodeHarnessUsage(raw json.RawMessage, native string) (domain.HarnessUsageView, error) {
	var result harnessUsageResult
	if json.Unmarshal(raw, &result) != nil {
		return domain.HarnessUsageView{}, invalidHarnessUsageResponse()
	}
	if result.Error != nil {
		if result.Error.Code == "invalid_argument" {
			// The harness id was validated above, so the node's paxd predates the
			// query (Claude Code and Codex) or has no adapter for this agent type.
			view := domain.HarnessUsageView{
				Harness:      native,
				HarnessLabel: harnessFallbackLabel(native),
				State:        "unsupported",
			}
			if nativeLoginHarness(native) {
				view.State, view.ErrorCode = "unavailable", "daemon_unsupported"
			}
			return view, nil
		}
		return domain.HarnessUsageView{}, harnessUsageDaemonError(result.Error.Code)
	}
	payload := result.HarnessUsage
	if payload == nil || payload.Harness != native {
		return domain.HarnessUsageView{}, invalidHarnessUsageResponse()
	}
	view := domain.HarnessUsageView{
		Harness:      native,
		HarnessLabel: harnessAuthAccountField(harnessUsageString(payload.HarnessLabel), 64),
		State:        payload.State,
		ObservedAt:   harnessUsageTime(payload.ObservedAt),
		Plan:         harnessAuthAccountField(harnessUsageString(payload.Plan), 128),
	}
	if view.HarnessLabel == "" {
		view.HarnessLabel = harnessFallbackLabel(native)
	}
	switch payload.State {
	case "available":
		view.Windows = harnessUsageWindows(payload.Windows)
		view.ExtraUsage = harnessUsageExtra(payload.ExtraUsage)
		if len(payload.Items) > 0 && string(payload.Items) != "null" {
			view.Items = harnessUsageItems(payload.Items)
		} else {
			// Older paxd sends only windows and extra_usage.
			view.Items = legacyHarnessUsageItems(view.Windows, view.ExtraUsage)
		}
	case "unavailable":
		view.ErrorCode = harnessUsageErrorCode(harnessUsageString(payload.ErrorCode))
	case "unsupported":
	default:
		return domain.HarnessUsageView{}, invalidHarnessUsageResponse()
	}
	return view, nil
}

func harnessUsageWindows(raw json.RawMessage) []domain.HarnessUsageWindow {
	var items []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var windows []domain.HarnessUsageWindow
	seen := map[string]bool{}
	for _, item := range items {
		if len(windows) == maxHarnessUsageWindows {
			break
		}
		window, ok := harnessUsageWindow(item)
		if !ok || seen[window.ID] {
			continue
		}
		seen[window.ID] = true
		windows = append(windows, window)
	}
	return windows
}

func harnessUsageWindow(raw json.RawMessage) (domain.HarnessUsageWindow, bool) {
	var payload harnessUsageWindowPayload
	if json.Unmarshal(raw, &payload) != nil {
		return domain.HarnessUsageWindow{}, false
	}
	id := harnessUsageString(payload.ID)
	minutes, minutesOK := harnessUsageNumber(payload.WindowMinutes)
	used, usedOK := harnessUsageNumber(payload.UsedPercent)
	if !validHarnessUsageWindowID(id) || !minutesOK || minutes != math.Trunc(minutes) ||
		minutes < 1 || minutes > maxHarnessUsageMinutes || !usedOK || used < 0 || used > 100 {
		return domain.HarnessUsageWindow{}, false
	}
	return domain.HarnessUsageWindow{
		ID:            id,
		WindowMinutes: int(minutes),
		Scope:         harnessAuthAccountField(harnessUsageString(payload.Scope), 64),
		UsedPercent:   used,
		ResetsAt:      harnessUsageTime(payload.ResetsAt),
	}, true
}

func harnessUsageExtra(raw json.RawMessage) *domain.HarnessUsageExtra {
	var payload struct {
		Enabled *bool `json:"enabled"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &payload) != nil || payload.Enabled == nil {
		return nil
	}
	return &domain.HarnessUsageExtra{Enabled: *payload.Enabled}
}

func harnessUsageErrorCode(code string) string {
	switch code {
	case "cli_unsupported", "read_failed", "timeout", "daemon_unsupported":
		return code
	default:
		return "read_failed"
	}
}

func harnessUsageString(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

// JSON null decodes into a zero float64 without an error, so reject it here.
func harnessUsageNumber(raw json.RawMessage) (float64, bool) {
	var value float64
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) ||
		json.Unmarshal(raw, &value) != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

func harnessUsageTime(raw json.RawMessage) string {
	value := harnessUsageString(raw)
	if value == "" || len(value) > 64 {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339)
}

func validHarnessUsageWindowID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

func invalidHarnessUsageResponse() error {
	return apperr.Error{
		Status:  http.StatusBadGateway,
		Message: "invalid harness usage response",
	}
}

func harnessUsageDaemonError(code string) error {
	status := http.StatusBadGateway
	switch code {
	case "not_found":
		status = http.StatusNotFound
	case "conflict":
		status = http.StatusConflict
	}
	return apperr.Error{
		Status:  status,
		Message: "harness usage failed: " + safeHarnessAuthErrorCode(code),
	}
}
