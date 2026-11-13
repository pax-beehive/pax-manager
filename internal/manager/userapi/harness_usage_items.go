package userapi

import (
	"encoding/json"
	"math"
	"strconv"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const maxHarnessUsageItems = 20

// scopeSeparator joins a window label and its model scope ("Weekly - Fable"
// rendered with a middle dot), kept as an escape so source stays ASCII.
const scopeSeparator = " \u00b7 "

// Optional fields stay raw so one malformed value drops only that value.
type harnessUsageItemPayload struct {
	ID            json.RawMessage `json:"id"`
	Label         json.RawMessage `json:"label"`
	Kind          json.RawMessage `json:"kind"`
	Severity      json.RawMessage `json:"severity"`
	UsedPercent   json.RawMessage `json:"used_percent"`
	WindowMinutes json.RawMessage `json:"window_minutes"`
	ResetsAt      json.RawMessage `json:"resets_at"`
	Used          json.RawMessage `json:"used"`
	Limit         json.RawMessage `json:"limit"`
	Unit          json.RawMessage `json:"unit"`
	Value         json.RawMessage `json:"value"`
}

func harnessUsageItems(raw json.RawMessage) []domain.HarnessUsageItem {
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil {
		return nil
	}
	var items []domain.HarnessUsageItem
	seen := map[string]bool{}
	for _, entry := range entries {
		if len(items) == maxHarnessUsageItems {
			break
		}
		item, ok := harnessUsageItem(entry)
		if !ok || seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		items = append(items, item)
	}
	return items
}

func harnessUsageItem(raw json.RawMessage) (domain.HarnessUsageItem, bool) {
	var payload harnessUsageItemPayload
	if json.Unmarshal(raw, &payload) != nil {
		return domain.HarnessUsageItem{}, false
	}
	item := domain.HarnessUsageItem{
		ID:       harnessUsageString(payload.ID),
		Label:    harnessAuthAccountField(harnessUsageString(payload.Label), 64),
		Kind:     harnessUsageString(payload.Kind),
		Severity: harnessUsageSeverity(harnessUsageString(payload.Severity)),
		ResetsAt: harnessUsageTime(payload.ResetsAt),
	}
	if !validHarnessUsageWindowID(item.ID) || item.Label == "" {
		return domain.HarnessUsageItem{}, false
	}
	switch item.Kind {
	case "percent":
		used, ok := harnessUsageNumber(payload.UsedPercent)
		if !ok || used < 0 || used > 100 {
			return domain.HarnessUsageItem{}, false
		}
		item.UsedPercent = &used
		if minutes, ok := harnessUsageNumber(payload.WindowMinutes); ok &&
			minutes == math.Trunc(minutes) && minutes >= 1 && minutes <= maxHarnessUsageMinutes {
			item.WindowMinutes = int(minutes)
		}
	case "amount":
		used, ok := harnessUsageNumber(payload.Used)
		if !ok || used < 0 {
			return domain.HarnessUsageItem{}, false
		}
		item.Used = &used
		if limit, ok := harnessUsageNumber(payload.Limit); ok && limit > 0 {
			item.Limit = &limit
		}
		item.Unit = harnessUsageUnit(harnessUsageString(payload.Unit))
	case "status":
		item.Value = harnessAuthAccountField(harnessUsageString(payload.Value), 64)
		if item.Value == "" {
			return domain.HarnessUsageItem{}, false
		}
		item.ResetsAt = ""
	default:
		return domain.HarnessUsageItem{}, false
	}
	return item, true
}

func harnessUsageSeverity(value string) string {
	switch value {
	case "normal", "warning", "critical":
		return value
	}
	return ""
}

func harnessUsageUnit(value string) string {
	switch value {
	case "usd", "eur", "cny", "credits", "tokens", "requests":
		return value
	}
	return ""
}

// legacyHarnessUsageItems builds items from the deprecated windows and
// extra_usage fields of an older paxd, so clients only need items.
func legacyHarnessUsageItems(
	windows []domain.HarnessUsageWindow, extra *domain.HarnessUsageExtra,
) []domain.HarnessUsageItem {
	var items []domain.HarnessUsageItem
	for _, window := range windows {
		used := window.UsedPercent
		label := harnessUsageWindowLabel(window.WindowMinutes)
		if window.Scope != "" {
			label += scopeSeparator + window.Scope
		}
		items = append(items, domain.HarnessUsageItem{
			ID:            window.ID,
			Label:         harnessAuthAccountField(label, 64),
			Kind:          "percent",
			UsedPercent:   &used,
			WindowMinutes: window.WindowMinutes,
			ResetsAt:      window.ResetsAt,
		})
	}
	if extra != nil {
		value := "off"
		if extra.Enabled {
			value = "on"
		}
		items = append(items, domain.HarnessUsageItem{
			ID: "extra_usage", Label: "Extra usage", Kind: "status", Value: value,
		})
	}
	return items
}

func harnessUsageWindowLabel(minutes int) string {
	switch {
	case minutes <= 0:
		return "Usage"
	case minutes == 7*24*60:
		return "Weekly"
	case minutes%(24*60) == 0:
		return strconv.Itoa(minutes/(24*60)) + "-day"
	case minutes%60 == 0 && minutes < 24*60:
		return strconv.Itoa(minutes/60) + "-hour"
	default:
		return strconv.Itoa(minutes) + "-minute"
	}
}
