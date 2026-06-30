package manager

import (
	"encoding/json"
	"fmt"
)

type acpPendingSessionNews struct {
	requests map[string]string
}

func (p *acpPendingSessionNews) track(requestID string, managerSessionID string) {
	if requestID == "" || managerSessionID == "" {
		return
	}
	if p.requests == nil {
		p.requests = make(map[string]string)
	}
	p.requests[requestID] = managerSessionID
}

func (p *acpPendingSessionNews) take(requestID string) (string, bool) {
	if requestID == "" || p.requests == nil {
		return "", false
	}
	managerSessionID, ok := p.requests[requestID]
	delete(p.requests, requestID)
	return managerSessionID, ok
}

type acpHistoryGroups struct {
	groups map[string]string
}

func (h *acpHistoryGroups) groupID(
	seq int64,
	sessionID string,
	payload json.RawMessage,
) string {
	var rpc acpHistoryRPC
	_ = json.Unmarshal(payload, &rpc)
	fields := extractACPHistoryFields(payload, rpc)
	fields, projection := classifyACPHistoryProjection(rpc, fields)
	if projection != acpHistoryProjectionText {
		h.groups = nil
		return ""
	}
	key := firstNonEmpty(fields.SessionID, sessionID) + "\x00" +
		firstNonEmpty(fields.SessionUpdate, "_") + "\x00" +
		firstNonEmpty(fields.Role, "_")
	if h.groups == nil {
		h.groups = make(map[string]string)
	}
	if groupID, ok := h.groups[key]; ok {
		return groupID
	}
	groupID := fmt.Sprintf("seq:%d", seq)
	h.groups[key] = groupID
	return groupID
}

func (h *acpHistoryGroups) observeBoundary(payload json.RawMessage) {
	var rpc acpJSONRPCMessage
	if err := json.Unmarshal(payload, &rpc); err != nil {
		return
	}
	if len(rpc.Result) == 0 && len(rpc.Error) == 0 {
		return
	}
	h.groups = nil
}
