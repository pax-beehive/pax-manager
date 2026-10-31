package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// A repeatable read keeps the message, its parts and its version in one snapshot.
func (s *PostgresStore) ReadSearchDocument(
	ctx context.Context,
	ownerID, kind, sourceID string,
) (domain.SearchDocument, error) {
	if ownerID == "" || sourceID == "" {
		return domain.SearchDocument{}, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return domain.SearchDocument{}, err
	}
	defer func() { _ = tx.Rollback() }()
	doc, err := loadSearchDocument(ctx, tx, kind, sourceID)
	if err != nil {
		return domain.SearchDocument{}, err
	}
	if doc.OwnerUserID != ownerID || doc.Deleted {
		return domain.SearchDocument{}, ErrNotFound
	}
	return doc, tx.Commit()
}

func loadSearchDocument(
	ctx context.Context,
	tx *sql.Tx,
	kind, sourceID string,
) (domain.SearchDocument, error) {
	switch kind {
	case "message":
		return loadSearchMessage(ctx, tx, sourceID)
	case "session":
		return loadSearchSession(ctx, tx, sourceID)
	case "summary":
		return loadSummarySearchDocument(ctx, tx, sourceID)
	default:
		return domain.SearchDocument{}, ErrNotFound
	}
}

const searchMessageVisibilitySQL = `
 FROM messages m
 JOIN agents a ON a.agent_id = m.agent_id
 LEFT JOIN nodes an ON an.node_id = a.node_id
 LEFT JOIN nodes mn ON mn.node_id = m.node_id
 LEFT JOIN agent_sessions s ON s.agent_id = m.agent_id AND s.session_id = m.session_id
 LEFT JOIN nodes sn ON sn.node_id = s.node_id
 WHERE m.message_id = $1 AND a.deleted_at IS NULL
 AND an.deleted_at IS NULL AND mn.deleted_at IS NULL AND sn.deleted_at IS NULL
 AND s.archived_at IS NULL
 AND COALESCE(s.source,'') <> 'secretary_internal'
 AND NOT starts_with(COALESCE(s.session_id,''),'sess_secretary_')
 AND (NULLIF(m.session_id,'') IS NULL OR s.id IS NOT NULL)
 AND NOT EXISTS (
   SELECT 1 FROM messages display
   WHERE display.agent_id = m.agent_id AND display.session_id IS NOT DISTINCT FROM m.session_id
   AND display.message_type IN ('pax:invocation', 'pax:invocation_pending', 'pax:artifact')
   AND CASE WHEN jsonb_typeof(display.raw_json->'replaces_message_ids') = 'array' THEN
     CASE WHEN display.raw_json->'replaces_message_ids' <> '[]'::jsonb
       AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(display.raw_json->'replaces_message_ids') item
         WHERE jsonb_typeof(item) NOT IN ('string','null'))
       THEN display.raw_json->'replaces_message_ids' ? m.message_id
       ELSE display.parent_message_id = m.message_id END
     ELSE display.parent_message_id = m.message_id END
 )`

func loadSearchMessage(
	ctx context.Context,
	tx *sql.Tx,
	messageID string,
) (domain.SearchDocument, error) {
	var messageJSON, partsJSON []byte
	var ownerID string
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT to_jsonb(m), a.owner_user_id,
 COALESCE((SELECT jsonb_agg(to_jsonb(p) ORDER BY p.part_index) FROM message_parts p WHERE p.message_id=m.message_id),'[]'::jsonb),
 COALESCE((SELECT desired_revision FROM search_sync_state WHERE kind='message' AND source_id=m.message_id),0)`+
		searchMessageVisibilitySQL, messageID).Scan(&messageJSON, &ownerID, &partsJSON, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SearchDocument{}, ErrNotFound
	}
	if err != nil {
		return domain.SearchDocument{}, err
	}
	var message domain.MessageWithParts
	if err := json.Unmarshal(messageJSON, &message.Message); err != nil {
		return domain.SearchDocument{}, fmt.Errorf("decode search message: %w", err)
	}
	if err := json.Unmarshal(partsJSON, &message.Parts); err != nil {
		return domain.SearchDocument{}, fmt.Errorf("decode search parts: %w", err)
	}
	doc, err := domain.CanonicalSearchMessage(message)
	if err != nil {
		return domain.SearchDocument{}, err
	}
	doc.OwnerUserID = ownerID
	doc.Revision = revision
	return doc, nil
}

func loadSearchSession(
	ctx context.Context,
	tx *sql.Tx,
	sourceID string,
) (domain.SearchDocument, error) {
	doc := domain.SearchDocument{
		Kind:                    "session",
		SourceID:                sourceID,
		CanonicalizationVersion: domain.SearchCanonicalizationVersion,
	}
	err := tx.QueryRowContext(ctx, `SELECT a.owner_user_id, s.agent_id, s.session_id,
 COALESCE(s.conversation_id,''), COALESCE(s.custom_session_name,s.session_name,''), s.created_at,
 COALESCE(q.desired_revision,0)
 FROM agent_sessions s JOIN agents a ON a.agent_id=s.agent_id
 LEFT JOIN nodes an ON an.node_id=a.node_id
 LEFT JOIN nodes sn ON sn.node_id=s.node_id
 LEFT JOIN search_sync_state q ON q.kind='session' AND q.source_id=s.id::text
 WHERE s.id::text=$1 AND s.archived_at IS NULL AND a.deleted_at IS NULL
 AND COALESCE(s.source,'') <> 'secretary_internal'
 AND NOT starts_with(s.session_id,'sess_secretary_')
 AND an.deleted_at IS NULL AND sn.deleted_at IS NULL`, sourceID).
		Scan(&doc.OwnerUserID, &doc.AgentID, &doc.SessionID, &doc.ConversationID, &doc.Title, &doc.CreatedAt, &doc.Revision)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && strings.TrimSpace(doc.Title) == "") {
		return domain.SearchDocument{}, ErrNotFound
	}
	if err != nil {
		return domain.SearchDocument{}, err
	}
	doc.ContentHash = domain.SearchContentHash(doc.Title)
	return doc, nil
}

func (s *PostgresStore) ReadSearchContext(
	ctx context.Context,
	ownerID, messageID string,
	before, after int,
) ([]domain.SearchDocument, error) {
	if ownerID == "" || messageID == "" || before < 0 || after < 0 || before > 100 || after > 100 {
		return nil, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	anchor, err := loadSearchMessage(ctx, tx, messageID)
	if err != nil {
		return nil, err
	}
	if anchor.OwnerUserID != ownerID {
		return nil, ErrNotFound
	}
	rows, err := tx.QueryContext(ctx, `WITH anchor AS (
 SELECT id, COALESCE(session_seq,0) AS seq FROM messages WHERE message_id=$1
 ), neighbors AS (
 (SELECT m.message_id, COALESCE(m.session_seq,0) AS seq, m.id FROM messages m, anchor
 WHERE m.agent_id=$2 AND m.session_id IS NOT DISTINCT FROM NULLIF($3,'')
 AND (COALESCE(m.session_seq,0),m.id)<(anchor.seq,anchor.id)
 ORDER BY COALESCE(m.session_seq,0) DESC,m.id DESC LIMIT $4)
 UNION ALL
 (SELECT m.message_id, COALESCE(m.session_seq,0) AS seq, m.id FROM messages m, anchor
 WHERE m.agent_id=$2 AND m.session_id IS NOT DISTINCT FROM NULLIF($3,'')
 AND (COALESCE(m.session_seq,0),m.id)>(anchor.seq,anchor.id)
 ORDER BY COALESCE(m.session_seq,0),m.id LIMIT $5)
 ) SELECT message_id FROM neighbors ORDER BY seq,id`, messageID, anchor.AgentID, anchor.SessionID, before, after)
	if err != nil {
		return nil, err
	}
	ids, err := scanSearchIDs(rows)
	if err != nil {
		return nil, err
	}
	docs := []domain.SearchDocument{anchor}
	for _, id := range ids {
		doc, err := loadSearchMessage(ctx, tx, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if doc.OwnerUserID == ownerID {
			docs = append(docs, doc)
		}
	}
	sortSearchContext(docs)
	return docs, tx.Commit()
}

func scanSearchIDs(rows *sql.Rows) ([]string, error) {
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Backfill only inserts missing queue rows. Triggers capture concurrent changes.
func (s *PostgresStore) BackfillSearch(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO search_sync_state(kind,source_id)
 SELECT 'message',message_id FROM messages
 UNION ALL SELECT 'session',id::text FROM agent_sessions
 UNION ALL SELECT DISTINCT 'summary',summary_id FROM summary_versions
 ON CONFLICT (kind,source_id) DO NOTHING`)
	return err
}

func (s *PostgresStore) ClaimSearchBatch(
	ctx context.Context,
	limit int,
	lease time.Duration,
) ([]domain.SearchWork, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if lease <= 0 || lease > 15*time.Minute {
		lease = time.Minute
	}
	token, err := newSecret("search")
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `WITH candidates AS (
 SELECT kind,source_id FROM search_sync_state
 WHERE desired_revision>indexed_revision AND available_at<=NOW()
 AND (lease_until IS NULL OR lease_until<=NOW())
 ORDER BY available_at,kind,source_id LIMIT $1 FOR UPDATE SKIP LOCKED
 ) UPDATE search_sync_state q SET lease_token=$2,
 lease_until=NOW()+$3*INTERVAL '1 millisecond'
 FROM candidates c WHERE q.kind=c.kind AND q.source_id=c.source_id
 RETURNING q.kind,q.source_id,q.desired_revision,q.lease_token`, limit, token, lease.Milliseconds())
	if err != nil {
		return nil, err
	}
	work, err := scanSearchWork(rows)
	if err != nil {
		return nil, err
	}
	for i := range work {
		claimed := work[i].Document
		doc, err := loadSearchDocument(ctx, tx, claimed.Kind, claimed.SourceID)
		if errors.Is(err, ErrNotFound) {
			doc = domain.SearchDocument{
				Kind:     claimed.Kind,
				SourceID: claimed.SourceID,
				Deleted:  true,
			}
		} else if err != nil {
			return nil, err
		}
		doc.Revision = claimed.Revision
		work[i].Document = doc
	}
	return work, tx.Commit()
}

func scanSearchWork(rows *sql.Rows) ([]domain.SearchWork, error) {
	defer func() { _ = rows.Close() }()
	var work []domain.SearchWork
	for rows.Next() {
		var item domain.SearchWork
		if err := rows.Scan(&item.Document.Kind, &item.Document.SourceID, &item.Document.Revision, &item.LeaseToken); err != nil {
			return nil, err
		}
		work = append(work, item)
	}
	return work, rows.Err()
}

func (s *PostgresStore) CompleteSearchWork(
	ctx context.Context,
	work domain.SearchWork,
	syncErr error,
) error {
	if work.LeaseToken == "" || work.Document.Revision <= 0 {
		return fmt.Errorf("invalid search lease")
	}
	if syncErr != nil {
		message := syncErr.Error()
		if len(message) > 2000 {
			message = message[:2000]
		}
		_, err := s.db.ExecContext(ctx, `UPDATE search_sync_state
 SET lease_token='',lease_until=NULL,attempts=attempts + 1,last_error=$4,
 available_at=NOW()+LEAST(300,POWER(2,LEAST(attempts,8))) * INTERVAL '1 second'
 WHERE kind=$1 AND source_id=$2 AND lease_token = $3`, work.Document.Kind, work.Document.SourceID, work.LeaseToken, message)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE search_sync_state
 SET indexed_revision=GREATEST(indexed_revision, $4),lease_token='',lease_until=NULL,
 attempts=0,last_error='',available_at=NOW()
 WHERE kind=$1 AND source_id=$2 AND lease_token = $3`, work.Document.Kind, work.Document.SourceID, work.LeaseToken, work.Document.Revision)
	return err
}
