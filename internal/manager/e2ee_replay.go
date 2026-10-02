package manager

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// JSON replay shares authorization and the durable journal with the SSE endpoint.
func (s *Service) handleE2EEReplay(w http.ResponseWriter, r *http.Request) {
	control, err := s.resolveConversationTurnControlSession(r)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	q, err := parseE2EEReplayQuery(r)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}
	page, err := s.store.ReadE2EEReplay(
		r.Context(),
		control.principal.User.UserID,
		control.session.SessionID,
		q,
	)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	type replayEvent struct {
		Cursor    int64        `json:"cursor"`
		CreatedAt string       `json:"created_at"`
		Envelope  e2eeEnvelope `json:"envelope"`
	}
	events := make([]replayEvent, 0, len(page.Events))
	for _, e := range page.Events {
		events = append(
			events,
			replayEvent{
				Cursor:    e.Cursor,
				CreatedAt: e.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
				Envelope:  encodeE2EERecord(e.E2EERecord),
			},
		)
	}
	writeHTTPData(w, http.StatusOK, map[string]any{
		"events": events, "turn_ref": page.TurnRef, "turn_start_cursor": page.TurnStartCursor,
		"head_cursor": page.HeadCursor, "next_after_cursor": page.NextAfterCursor,
		"has_more": page.HasMore, "has_older": page.HasOlder,
	})
}

func parseE2EEReplayQuery(r *http.Request) (domain.E2EEReplayQuery, error) {
	q := domain.E2EEReplayQuery{Limit: 100, TurnRef: r.URL.Query().Get("turn_ref")}
	for name, target := range map[string]*int64{"before_turn": &q.BeforeTurn, "after_cursor": &q.AfterCursor, "through_cursor": &q.ThroughCursor} {
		if raw := r.URL.Query().Get(name); raw != "" {
			v, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || v < 0 || v > 9007199254740991 {
				return q, fmt.Errorf("invalid %s", name)
			}
			*target = v
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 500 {
			return q, fmt.Errorf("limit must be between 1 and 500")
		}
		q.Limit = v
	}
	if q.TurnRef != "" && !validE2EEIdentifier(q.TurnRef) {
		return q, fmt.Errorf("invalid turn_ref")
	}
	if q.BeforeTurn > 0 && (q.AfterCursor > 0 || q.ThroughCursor > 0 || q.TurnRef != "") {
		return q, fmt.Errorf("before_turn cannot be combined with a replay continuation")
	}
	if (q.AfterCursor > 0 || q.TurnRef != "") && q.ThroughCursor == 0 {
		return q, fmt.Errorf("continuation requires through_cursor")
	}
	if q.AfterCursor > q.ThroughCursor {
		return q, fmt.Errorf("after_cursor exceeds through_cursor")
	}
	return q, nil
}
