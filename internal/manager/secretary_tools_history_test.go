package manager

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

type secretaryExpandedHistoryStore struct {
	domain.Store
	page domain.MessageHistoryPage
}

func (s *secretaryExpandedHistoryStore) ListMessageSummaryPage(
	context.Context,
	string,
	string,
	int64,
	int64,
	int,
) (domain.MessageHistoryPage, error) {
	return s.page, nil
}

func (s *secretaryExpandedHistoryStore) ListMessageSummaryParts(
	context.Context,
	[]string,
) (map[string][]domain.MessagePart, error) {
	return nil, nil
}

type secretaryCountingSources struct {
	agentID string
	reads   []string
}

func (s *secretaryCountingSources) ReadSearchDocument(
	_ context.Context,
	owner, kind, id string,
) (domain.SearchDocument, error) {
	s.reads = append(s.reads, id)
	return domain.SearchDocument{
		Kind:                    kind,
		SourceID:                id,
		OwnerUserID:             owner,
		AgentID:                 s.agentID,
		SessionID:               "source-session",
		Role:                    "user",
		Body:                    id,
		ContentHash:             domain.SearchContentHash(id),
		CanonicalizationVersion: domain.SearchCanonicalizationVersion,
	}, nil
}

func (*secretaryCountingSources) ReadSearchContext(
	context.Context,
	string,
	string,
	int,
	int,
) ([]domain.SearchDocument, error) {
	return nil, nil
}

func TestSecretaryToolsGivenThousandTurnContextRowsThenOnlyReadsServerWindow(t *testing.T) {
	srv, fixture, _, _, capability := secretaryToolsFixture(t)
	page := domain.MessageHistoryPage{
		HeadSeq:       1000,
		NextBeforeSeq: 999,
		NextAfterSeq:  1000,
		HasOlder:      true,
		HasMore:       true,
	}
	for seq := int64(1); seq <= 1000; seq++ {
		page.Messages = append(
			page.Messages,
			domain.Message{
				MessageID:   fmt.Sprintf("m%d", seq),
				SessionSeq:  seq,
				TurnID:      "same-turn",
				Role:        "user",
				MessageType: "user_message",
				Source:      domain.MessageSourceACPTunnel,
				Direction:   domain.MessageDirectionUserToAgent,
			},
		)
	}
	// Duplicate context does not consume another canonical read or alter cursors.
	page.Messages = append(page.Messages, page.Messages[998])
	srv.store = &secretaryExpandedHistoryStore{Store: srv.store, page: page}
	sources := &secretaryCountingSources{agentID: fixture.agentID}
	srv.workspaceSearch = userapi.NewWorkspaceSearchService(
		nil,
		sources,
		nil,
		nil,
		[]byte(strings.Repeat("k", 32)),
	)
	data, err := secretaryToolCall(
		t,
		srv,
		fixture,
		capability,
		"history",
		`{"session_id":"source-session","limit":2}`,
	)
	require.NoError(t, err)
	history := data.(secretaryHistoryPage)
	require.Len(t, history.Messages, 2)
	assert.Equal(t, []string{"m999", "m1000"}, sources.reads)
	assert.Equal(
		t,
		domain.MessageHistoryPagination{
			HeadSeq:       1000,
			NextBeforeSeq: 999,
			NextAfterSeq:  1000,
			HasOlder:      true,
			HasMore:       true,
		},
		history.Pagination,
	)
	assert.True(
		t,
		secretaryHistoryInWindow(
			domain.MessageWithParts{Message: domain.Message{SessionSeq: 999}},
			secretaryHistoryRequest{AfterSeq: 998},
			history.Pagination,
		),
	)
	assert.False(
		t,
		secretaryHistoryInWindow(
			domain.MessageWithParts{Message: domain.Message{SessionSeq: 999}},
			secretaryHistoryRequest{AfterSeq: 999},
			history.Pagination,
		),
	)
	assert.False(
		t,
		secretaryHistoryInWindow(
			domain.MessageWithParts{Message: domain.Message{SessionSeq: 1000}},
			secretaryHistoryRequest{BeforeSeq: 1000},
			history.Pagination,
		),
	)
}
