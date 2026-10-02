package storage

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) ReadE2EEReplay(
	_ context.Context,
	owner, session string,
	q domain.E2EEReplayQuery,
) (domain.E2EEReplayPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := make([]AgentEvent, 0)
	for _, e := range s.agentEvents {
		if e.OwnerUserID == owner && e.SessionID == session {
			events = append(events, cloneAgentEvent(e))
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Cursor < events[j].Cursor })
	page := domain.E2EEReplayPage{Events: []AgentEvent{}}
	if len(events) == 0 {
		return page, nil
	}
	page.HeadCursor = events[len(events)-1].Cursor
	if q.ThroughCursor > 0 {
		page.HeadCursor = q.ThroughCursor
	}
	page.TurnRef = q.TurnRef
	if q.ThroughCursor == 0 {
		found := false
		for i := len(events) - 1; i >= 0; i-- {
			if q.BeforeTurn == 0 || events[i].Cursor < q.BeforeTurn {
				page.TurnRef = events[i].TurnRef
				found = true
				if page.TurnRef != "" {
					break
				}
			}
		}
		if !found {
			return page, nil
		}
	}
	for _, e := range events {
		if e.Cursor > page.HeadCursor || e.TurnRef != page.TurnRef {
			continue
		}
		if page.TurnStartCursor == 0 {
			page.TurnStartCursor = e.Cursor
		}
		if e.Cursor > q.AfterCursor {
			page.Events = append(page.Events, e)
		}
	}
	page.HasOlder = page.TurnStartCursor > events[0].Cursor
	finishReplayPage(&page, q.Limit, q.AfterCursor)
	return page, nil
}

func finishReplayPage(page *domain.E2EEReplayPage, limit int, after int64) {
	if limit <= 0 {
		limit = 100
	}
	page.HasMore = len(page.Events) > limit
	if page.HasMore {
		page.Events = page.Events[:limit]
	}
	page.NextAfterCursor = after
	if len(page.Events) > 0 {
		page.NextAfterCursor = page.Events[len(page.Events)-1].Cursor
	}
}

// Repeatable read keeps the chosen turn, replay boundary and frames in one snapshot.
func (s *PostgresStore) ReadE2EEReplay(
	ctx context.Context,
	owner, session string,
	q domain.E2EEReplayQuery,
) (domain.E2EEReplayPage, error) {
	page := domain.E2EEReplayPage{Events: []AgentEvent{}, TurnRef: q.TurnRef}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer func() { _ = tx.Rollback() }()
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(cursor),0) FROM agent_events WHERE owner_user_id=$1 AND session_id=$2`, owner, session).
		Scan(&page.HeadCursor)
	if err != nil {
		return page, err
	}
	if page.HeadCursor == 0 {
		return page, nil
	}
	if q.ThroughCursor > 0 {
		page.HeadCursor = q.ThroughCursor
	} else {
		err = tx.QueryRowContext(ctx, `SELECT turn_ref FROM agent_events WHERE owner_user_id=$1 AND session_id=$2 AND turn_ref<>'' AND ($3=0 OR cursor<$3) ORDER BY cursor DESC LIMIT 1`, owner, session, q.BeforeTurn).Scan(&page.TurnRef)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, `SELECT turn_ref FROM agent_events WHERE owner_user_id=$1 AND session_id=$2 AND ($3=0 OR cursor<$3) ORDER BY cursor DESC LIMIT 1`, owner, session, q.BeforeTurn).Scan(&page.TurnRef)
		}
		if errors.Is(err, sql.ErrNoRows) {
			return page, nil
		}
		if err != nil {
			return page, err
		}
	}
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(MIN(cursor),0) FROM agent_events WHERE owner_user_id=$1 AND session_id=$2 AND turn_ref=$3 AND cursor<=$4`, owner, session, page.TurnRef, page.HeadCursor).
		Scan(&page.TurnStartCursor)
	if err != nil {
		return page, err
	}
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_events WHERE owner_user_id=$1 AND session_id=$2 AND cursor<$3)`, owner, session, page.TurnStartCursor).
		Scan(&page.HasOlder)
	if err != nil {
		return page, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	rows, err := tx.QueryContext(
		ctx,
		agentEventSelect+` WHERE owner_user_id=$1 AND session_id=$2 AND turn_ref=$3 AND cursor>$4 AND cursor<=$5 ORDER BY cursor LIMIT $6`,
		owner,
		session,
		page.TurnRef,
		q.AfterCursor,
		page.HeadCursor,
		limit+1,
	)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		e, scanErr := scanAgentEvent(rows)
		if scanErr != nil {
			_ = rows.Close()
			return page, scanErr
		}
		page.Events = append(page.Events, e)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return page, err
	}
	finishReplayPage(&page, limit, q.AfterCursor)
	return page, tx.Commit()
}

var _ domain.E2EEReplayStore = (*MemoryStore)(nil)
var _ domain.E2EEReplayStore = (*PostgresStore)(nil)
