package storage

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// Current ownership and session visibility gate both global counts and pages.
// Message-level changes gate readiness through the transactional dirty ledger.
const secretaryProgressCountersSQL = `WITH visible AS (
 SELECT p.*,s.agent_id,s.session_id,COALESCE(s.custom_session_name,s.session_name,'') title,j.completed_at last_completed_at,
 CASE WHEN p.pending_messages=0 THEN 'completed' WHEN p.completed_messages=0 THEN 'unstarted' ELSE 'partial' END state
 FROM secretary_progress_sessions p JOIN agent_sessions s ON s.id=p.source_id
 JOIN agents a ON a.agent_id=s.agent_id LEFT JOIN nodes an ON an.node_id=a.node_id
 LEFT JOIN nodes sn ON sn.node_id=s.node_id
 LEFT JOIN secretary_jobs j ON j.source_id=s.id AND j.owner_user_id=$1
 WHERE p.owner_user_id=$1 AND a.owner_user_id=$1 AND a.deleted_at IS NULL AND s.archived_at IS NULL
 AND an.deleted_at IS NULL AND sn.deleted_at IS NULL AND COALESCE(s.source,'')<>'secretary_internal'
 AND NOT starts_with(s.session_id,'sess_secretary_') AND NOT EXISTS(
 SELECT 1 FROM secretary_internal_sessions i WHERE i.agent_id=s.agent_id AND i.session_id=s.session_id)
) `

func readSecretaryProgressCounters(
	ctx context.Context,
	tx *sql.Tx,
	owner, after string,
	limit int,
) (domain.SecretaryProgressPage, error) {
	page := domain.SecretaryProgressPage{Sessions: []domain.SecretarySessionProgress{}}
	counts := &page.Counts
	err := tx.QueryRowContext(ctx, secretaryProgressCountersSQL+`SELECT CURRENT_TIMESTAMP,count(*),
 count(*) FILTER(WHERE state='completed'),count(*) FILTER(WHERE state='partial'),count(*) FILTER(WHERE state='unstarted'),
 COALESCE(sum(total_messages),0),COALESCE(sum(completed_messages),0),COALESCE(sum(pending_messages),0),
 COALESCE(sum(total_turns),0),COALESCE(sum(completed_turns),0),COALESCE(sum(pending_turns),0),COALESCE(sum(ungrouped_messages),0) FROM visible`, owner).Scan(
		&page.AsOf, &counts.TotalSessions, &counts.CompletedSessions, &counts.PartialSessions, &counts.UnstartedSessions,
		&counts.TotalMessages, &counts.CompletedMessages, &counts.PendingMessages, &counts.TotalTurns, &counts.CompletedTurns, &counts.PendingTurns, &counts.UngroupedMessages)
	if err != nil {
		return page, err
	}
	rows, err := tx.QueryContext(
		ctx,
		secretaryProgressCountersSQL+`SELECT source_id::text,agent_id,session_id,title,state,last_completed_at,
 total_messages,completed_messages,pending_messages,total_turns,completed_turns,pending_turns,ungrouped_messages
 FROM visible WHERE source_id::text COLLATE "C">$2 ORDER BY source_id::text COLLATE "C" LIMIT $3`,
		owner,
		after,
		limit+1,
	)
	if err != nil {
		return page, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var row domain.SecretarySessionProgress
		if err = rows.Scan(&row.SourceID, &row.AgentID, &row.SessionID, &row.Title, &row.State, &row.LastCompletedAt,
			&row.TotalMessages, &row.CompletedMessages, &row.PendingMessages, &row.TotalTurns, &row.CompletedTurns, &row.PendingTurns, &row.UngroupedMessages); err != nil {
			return page, err
		}
		page.Sessions = append(page.Sessions, row)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Sessions) > limit {
		page.Sessions = page.Sessions[:limit]
		raw, _ := json.Marshal(
			secretaryProgressCursor{
				Version: 1,
				Owner:   domain.SearchContentHash(owner),
				After:   page.Sessions[limit-1].SourceID,
			},
		)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}
