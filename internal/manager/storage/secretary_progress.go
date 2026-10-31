package storage

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type secretaryProgressCursor struct {
	Version int    `json:"v"`
	Owner   string `json:"o"`
	After   string `json:"a"`
}

func secretaryProgressQuery(
	owner string,
	query domain.SecretaryProgressQuery,
) (string, int, error) {
	if owner == "" {
		return "", 0, ErrUnauthorized
	}
	if query.Limit < 0 || query.Limit > 100 || len(query.Cursor) > 2048 {
		return "", 0, domain.ErrInvalidCursor
	}
	limit := query.Limit
	if limit == 0 {
		limit = 20
	}
	if query.Cursor == "" {
		return "", limit, nil
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(query.Cursor)
	if err != nil {
		return "", 0, domain.ErrInvalidCursor
	}
	var cursor secretaryProgressCursor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&cursor); err != nil {
		return "", 0, domain.ErrInvalidCursor
	}
	if decoder.Decode(new(any)) != io.EOF || cursor.Version != 1 ||
		cursor.Owner != domain.SearchContentHash(
			owner,
		) || !secretaryProgressSourceID(cursor.After) {
		return "", 0, domain.ErrInvalidCursor
	}
	return cursor.After, limit, nil
}

func secretaryProgressSourceID(id string) bool {
	numeric, err := strconv.ParseInt(id, 10, 64)
	if err == nil && numeric > 0 && strconv.FormatInt(numeric, 10) == id {
		return true
	}
	if !strings.HasPrefix(id, "memory:sha256:") || len(id) != 78 {
		return false
	}
	_, err = hex.DecodeString(id[14:])
	return err == nil
}

type secretaryProgressSession struct {
	row   domain.SecretarySessionProgress
	turns map[string]bool
}

type secretaryProgressAccumulator map[string]*secretaryProgressSession

func (a secretaryProgressAccumulator) add(
	row domain.SecretarySessionProgress,
	turnID string,
	completed bool,
) {
	session := a[row.SourceID]
	if session == nil {
		session = &secretaryProgressSession{row: row, turns: map[string]bool{}}
		a[row.SourceID] = session
	}
	session.row.TotalMessages++
	if completed {
		session.row.CompletedMessages++
	} else {
		session.row.PendingMessages++
	}
	if turnID == "" {
		session.row.UngroupedMessages++
		return
	}
	wasPending := session.turns[turnID]
	session.turns[turnID] = wasPending || !completed
}

func (a secretaryProgressAccumulator) page(
	owner, after string,
	limit int,
	asOf time.Time,
) domain.SecretaryProgressPage {
	page := domain.SecretaryProgressPage{AsOf: asOf, Sessions: []domain.SecretarySessionProgress{}}
	ids := make([]string, 0, len(a))
	for id := range a {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		session := a[id]
		row := session.row
		for _, pending := range session.turns {
			row.TotalTurns++
			if pending {
				row.PendingTurns++
			} else {
				row.CompletedTurns++
			}
		}
		page.Counts.TotalSessions++
		switch {
		case row.PendingMessages == 0:
			row.State = "completed"
			page.Counts.CompletedSessions++
		case row.CompletedMessages == 0:
			row.State = "unstarted"
			page.Counts.UnstartedSessions++
		default:
			row.State = "partial"
			page.Counts.PartialSessions++
		}
		addSecretaryCoverageCounts(
			&page.Counts.SecretaryCoverageCounts,
			row.SecretaryCoverageCounts,
		)
		if id > after && len(page.Sessions) <= limit {
			page.Sessions = append(page.Sessions, row)
		}
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
	return page
}

func addSecretaryCoverageCounts(
	target *domain.SecretaryCoverageCounts,
	source domain.SecretaryCoverageCounts,
) {
	target.TotalMessages += source.TotalMessages
	target.CompletedMessages += source.CompletedMessages
	target.PendingMessages += source.PendingMessages
	target.TotalTurns += source.TotalTurns
	target.CompletedTurns += source.CompletedTurns
	target.PendingTurns += source.PendingTurns
	target.UngroupedMessages += source.UngroupedMessages
}
