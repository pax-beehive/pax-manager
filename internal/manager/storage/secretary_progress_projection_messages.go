package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var secretaryProgressChangedSQL = buildSecretaryProgressBatchSQL(true)

type secretaryProgressMessage struct {
	id, turn, hash string
	source         int64
	completed      bool
}

func refreshSecretaryProgressMessages(
	ctx context.Context,
	tx *sql.Tx,
	owner string,
	ids []string,
) error {
	rawIDs := secretaryProgressIDs(ids)
	sources, err := secretaryProgressOldSources(ctx, tx, owner, rawIDs)
	if err != nil {
		return err
	}
	messages, err := secretaryProgressCurrentMessages(ctx, tx, owner, rawIDs)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM secretary_progress_messages WHERE owner_user_id=$1 AND message_id IN (SELECT jsonb_array_elements_text($2::jsonb))`, owner, rawIDs); err != nil {
		return err
	}
	for _, message := range messages {
		sources[message.source] = true
		_, err = tx.ExecContext(
			ctx,
			`INSERT INTO secretary_progress_messages(owner_user_id,message_id,source_id,turn_id,current_hash,canonicalization_version,completed)
 VALUES($1,$2,$3,$4,$5,$6,$7)`,
			owner,
			message.id,
			message.source,
			message.turn,
			message.hash,
			domain.SearchCanonicalizationVersion,
			message.completed,
		)
		if err != nil {
			return err
		}
	}
	for source := range sources {
		if err = refreshSecretaryProgressSession(ctx, tx, owner, source); err != nil {
			return err
		}
	}
	return nil
}

func secretaryProgressOldSources(
	ctx context.Context,
	tx *sql.Tx,
	owner, ids string,
) (map[int64]bool, error) {
	rows, err := tx.QueryContext(
		ctx,
		`SELECT DISTINCT source_id FROM secretary_progress_messages WHERE owner_user_id=$1 AND message_id IN (SELECT jsonb_array_elements_text($2::jsonb))`,
		owner,
		ids,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	sources := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		sources[id] = true
	}
	return sources, rows.Err()
}

func secretaryProgressCurrentMessages(
	ctx context.Context,
	tx *sql.Tx,
	owner, ids string,
) ([]secretaryProgressMessage, error) {
	rows, err := tx.QueryContext(
		ctx,
		secretaryProgressChangedSQL,
		owner,
		secretaryProgressWhitespace,
		ids,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var messages []secretaryProgressMessage
	for rows.Next() {
		var item secretaryProgressMessage
		var source, agent, session, title string
		var completedAt sql.NullTime
		var raw []byte
		var processedHash, canonical, processedOwner string
		err = rows.Scan(
			&item.id,
			&source,
			&agent,
			&session,
			&title,
			&completedAt,
			&raw,
			&item.hash,
			&processedHash,
			&canonical,
			&processedOwner,
		)
		if err != nil {
			return nil, err
		}
		var message domain.Message
		if err = json.Unmarshal(raw, &message); err != nil {
			return nil, err
		}
		if !domain.SearchVisibleMessageMetadata(message) {
			continue
		}
		if item.source, err = strconv.ParseInt(source, 10, 64); err != nil {
			return nil, err
		}
		item.turn = message.TurnID
		item.completed = processedHash == item.hash &&
			canonical == domain.SearchCanonicalizationVersion &&
			processedOwner == owner
		messages = append(messages, item)
	}
	return messages, rows.Err()
}

func refreshSecretaryProgressSession(
	ctx context.Context,
	tx *sql.Tx,
	owner string,
	source int64,
) error {
	_, err := tx.ExecContext(
		ctx,
		`DELETE FROM secretary_progress_sessions WHERE owner_user_id=$1 AND source_id=$2`,
		owner,
		source,
	)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `WITH messages AS MATERIALIZED (
 SELECT turn_id,completed FROM secretary_progress_messages WHERE owner_user_id=$1 AND source_id=$2
), turns AS (SELECT bool_and(completed) completed FROM messages WHERE turn_id<>'' GROUP BY turn_id)
 INSERT INTO secretary_progress_sessions(owner_user_id,source_id,total_messages,completed_messages,pending_messages,total_turns,completed_turns,pending_turns,ungrouped_messages)
 SELECT $1,$2,count(*),count(*) FILTER(WHERE completed),count(*) FILTER(WHERE NOT completed),
 (SELECT count(*) FROM turns),(SELECT count(*) FROM turns WHERE completed),(SELECT count(*) FROM turns WHERE NOT completed),count(*) FILTER(WHERE turn_id='')
 FROM messages HAVING count(*)>0`, owner, source)
	return err
}
