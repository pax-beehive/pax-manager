package storage

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryPrimarySource(summary domain.SummaryVersion) string {
	const prefix = "secretary:session:"
	if summary.SubjectType != "fragment" || !strings.HasPrefix(summary.SubjectID, prefix) {
		return ""
	}
	id := strings.TrimPrefix(summary.SubjectID, prefix)
	if id == "" {
		return ""
	}
	numeric, err := strconv.ParseInt(id, 10, 64)
	if err != nil || numeric <= 0 || strconv.FormatInt(numeric, 10) != id {
		return ""
	}
	return id
}

func (s *PostgresStore) ReadSearchGroup(
	ctx context.Context,
	owner string,
	doc domain.SearchDocument,
) (domain.SearchGroupMetadata, error) {
	if owner == "" || doc.OwnerUserID != owner {
		return domain.SearchGroupMetadata{}, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return domain.SearchGroupMetadata{}, err
	}
	defer func() { _ = tx.Rollback() }()
	group, err := readSearchGroupTx(ctx, tx, owner, doc)
	if err != nil {
		return domain.SearchGroupMetadata{}, err
	}
	return group, tx.Commit()
}

func readSearchGroupTx(
	ctx context.Context,
	tx *sql.Tx,
	owner string,
	doc domain.SearchDocument,
) (domain.SearchGroupMetadata, error) {
	var source string
	if doc.Kind == "summary" {
		summary := domain.SummaryVersion{SummaryID: doc.SourceID, OwnerUserID: owner}
		err := tx.QueryRowContext(ctx, `SELECT subject_type,subject_id,title FROM summary_versions
WHERE summary_id=$1 AND owner_user_id=$2 AND revision=$3`, doc.SourceID, owner, doc.SummaryRevision).
			Scan(&summary.SubjectType, &summary.SubjectID, &summary.Title)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.SearchGroupMetadata{}, ErrNotFound
		}
		if err != nil {
			return domain.SearchGroupMetadata{}, err
		}
		source = secretaryPrimarySource(summary)
		if source != "" {
			group, groupErr := readSessionSearchGroupTx(ctx, tx, owner, source, "", "")
			if !errors.Is(groupErr, ErrNotFound) {
				return group, groupErr
			}
			source = ""
		}
		if source == "" {
			doc.AgentID, doc.SessionID = "", ""
			return domain.SearchGroupMetadata{
				Key:   domain.SearchDocumentGroupKey(doc),
				Title: searchGroupTitle(summary.Title, summary.SummaryID),
			}, nil
		}
	}
	if source == "" && (doc.AgentID == "" || doc.SessionID == "") {
		return domain.SearchGroupMetadata{
			Key:   domain.SearchDocumentGroupKey(doc),
			Title: searchGroupTitle(doc.Title, doc.SourceID),
		}, nil
	}
	return readSessionSearchGroupTx(ctx, tx, owner, source, doc.AgentID, doc.SessionID)
}

func readSessionSearchGroupTx(
	ctx context.Context,
	tx *sql.Tx,
	owner, source, agent, session string,
) (domain.SearchGroupMetadata, error) {
	var group domain.SearchGroupMetadata
	predicate := "s.agent_id=$2 AND s.session_id=$3"
	args := []any{owner, agent, session}
	if source != "" {
		predicate = "s.id=$2::bigint"
		args = []any{owner, source}
	}
	err := tx.QueryRowContext(ctx, `SELECT s.agent_id,s.session_id,
COALESCE(s.custom_session_name,s.session_name,'') FROM agent_sessions s
JOIN agents a ON a.agent_id=s.agent_id
LEFT JOIN nodes an ON an.node_id=a.node_id LEFT JOIN nodes sn ON sn.node_id=s.node_id
WHERE a.owner_user_id=$1 AND `+predicate+`
AND a.deleted_at IS NULL AND an.deleted_at IS NULL AND sn.deleted_at IS NULL AND s.archived_at IS NULL
AND COALESCE(s.source,'')<>'secretary_internal' AND NOT starts_with(s.session_id,'sess_secretary_')`, args...).
		Scan(&group.AgentID, &group.SessionID, &group.Title)
	if errors.Is(err, sql.ErrNoRows) {
		return group, ErrNotFound
	}
	if err != nil {
		return group, err
	}
	group.Title = searchGroupTitle(group.Title, group.SessionID)
	group.Key = domain.SearchDocumentGroupKey(
		domain.SearchDocument{
			OwnerUserID: owner,
			AgentID:     group.AgentID,
			SessionID:   group.SessionID,
		},
	)
	return group, nil
}

func searchGroupTitle(title, fallback string) string {
	if strings.TrimSpace(title) == "" {
		return fallback
	}
	return title
}
