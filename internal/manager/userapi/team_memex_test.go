package userapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func TestTeamMemexRunAppliesExecutorManifest(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	store := storage.NewMemoryStore(func() time.Time { return now })
	owner, err := store.EnsureUser(ctx, "owner@example.com", "", "user")
	require.NoError(t, err)
	operator, err := store.EnsureUser(ctx, "operator@example.com", "", "user")
	require.NoError(t, err)
	teamID := "team_1"
	_, err = store.CreateTeam(ctx, domain.Team{
		TeamID:      teamID,
		OwnerUserID: owner.UserID,
		Name:        "Core",
		Status:      domain.TeamStatusActive,
		CreatedAt:   now,
	}, domain.TeamMember{
		TeamID:        teamID,
		UserID:        owner.UserID,
		Email:         owner.Email,
		Role:          domain.TeamRoleOwner,
		Status:        domain.TeamMemberStatusActive,
		InvitedByUser: owner.UserID,
		JoinedAt:      now,
	})
	require.NoError(t, err)
	_, err = store.CreateTeamInvite(ctx, storage.UserPrincipal{User: owner}, domain.TeamInvite{
		InviteID:        "tinv_operator",
		TeamID:          teamID,
		Email:           operator.Email,
		RecipientUserID: operator.UserID,
		Role:            domain.TeamRoleOperator,
		Status:          domain.TeamInviteStatusPending,
		InvitedByUserID: owner.UserID,
		CreatedAt:       now,
	})
	require.NoError(t, err)
	_, err = store.AcceptTeamInvite(
		ctx,
		storage.UserPrincipal{User: operator},
		"tinv_operator",
		now,
	)
	require.NoError(t, err)
	seedTeamMemexDocuments(t, ctx, store, owner, teamID, now)

	executor := &capturingTeamMemexExecutor{
		manifest: domain.TeamMemexManifest{Operations: []domain.TeamMemexManifestOperation{
			{
				Operation: domain.TeamMemexOperationCreateDoc,
				Path:      "product/llm-wiki.md",
				Title:     "LLM Wiki",
				Summary:   "Team-maintained wiki",
				Tags:      json.RawMessage(`["product"]`),
				BodyMD:    "# LLM Wiki\nThe wiki is maintained from team agent work.\n",
			},
			{
				Operation: domain.TeamMemexOperationUpdateDoc,
				Path:      "runtime/sessions.md",
				Title:     "Sessions",
				Summary:   "Updated session storage notes",
				Tags:      json.RawMessage(`["runtime","sessions"]`),
				BodyMD:    "# Sessions\nMessages are processed through the memex run cursor.\n",
			},
			{
				Operation: domain.TeamMemexOperationArchiveDoc,
				Path:      "old/cleanup.md",
			},
		}},
	}
	secrets := &sequenceSecretIssuer{values: map[string][]string{
		"tmrun": {"tmrun_publish"},
		"tmdoc": {"tmdoc_product"},
	}}
	svc := NewService(
		store,
		func() time.Time { return now },
		fixedTeamMemexPrincipal{principal: domain.UserPrincipal{User: operator}},
		secrets,
	)
	svc.memexExecutor = executor

	status, data, err := svc.CreateTeamMemexRun(ctx, auth.RequestMetadata{}, teamID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	run := data.(map[string]any)["run"].(teamMemexRunResponse)
	require.Equal(t, "tmrun_publish", run.RunID)
	require.Equal(t, domain.TeamMemexRunStatusSucceeded, run.Status)
	require.Nil(t, run.ValidationReport)
	require.Contains(t, executor.indexMD, "runtime/sessions.md")
	require.Empty(t, executor.searchBody)
	require.Contains(t, executor.readBody, "Old session notes")

	documents, err := store.ListTeamMemexDocuments(
		ctx,
		storage.UserPrincipal{User: owner},
		teamID,
	)
	require.NoError(t, err)
	require.Len(t, documents, 2)
	require.Equal(t, "product/llm-wiki.md", documents[0].Path)
	require.Equal(t, "runtime/sessions.md", documents[1].Path)
	require.Equal(t, "Updated session storage notes", documents[1].Summary)
	require.Equal(
		t,
		"# Sessions\nMessages are processed through the memex run cursor.\n",
		documents[1].BodyMD,
	)

	_, err = store.GetTeamMemexDocument(
		ctx,
		storage.UserPrincipal{User: owner},
		teamID,
		"old/cleanup.md",
	)
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestTeamMemexRunStoresValidationReportWithoutPublishing(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	store := storage.NewMemoryStore(func() time.Time { return now })
	owner, err := store.EnsureUser(ctx, "owner@example.com", "", "user")
	require.NoError(t, err)
	teamID := "team_1"
	_, err = store.CreateTeam(ctx, domain.Team{
		TeamID:      teamID,
		OwnerUserID: owner.UserID,
		Name:        "Core",
		Status:      domain.TeamStatusActive,
		CreatedAt:   now,
	}, domain.TeamMember{
		TeamID:        teamID,
		UserID:        owner.UserID,
		Email:         owner.Email,
		Role:          domain.TeamRoleOwner,
		Status:        domain.TeamMemberStatusActive,
		InvitedByUser: owner.UserID,
		JoinedAt:      now,
	})
	require.NoError(t, err)
	seedTeamMemexDocuments(t, ctx, store, owner, teamID, now)

	svc := NewService(
		store,
		func() time.Time { return now },
		fixedTeamMemexPrincipal{principal: domain.UserPrincipal{User: owner}},
		&sequenceSecretIssuer{values: map[string][]string{"tmrun": {"tmrun_invalid"}}},
	)
	svc.memexExecutor = &capturingTeamMemexExecutor{
		manifest: domain.TeamMemexManifest{Operations: []domain.TeamMemexManifestOperation{{
			Operation: domain.TeamMemexOperationUpdateDoc,
			Path:      "index.md",
			Title:     "Index",
			Summary:   "Should not be writable",
			BodyMD:    "# Index\n",
		}}},
	}

	status, data, err := svc.CreateTeamMemexRun(ctx, auth.RequestMetadata{}, teamID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	run := data.(map[string]any)["run"].(teamMemexRunResponse)
	require.Equal(t, domain.TeamMemexRunStatusValidationFailed, run.Status)
	require.NotNil(t, run.ValidationReport)
	require.Equal(t, "INDEX_IS_READ_ONLY", run.ValidationReport.Errors[0].Code)
	require.Contains(t, run.Error, "validation failed")

	document, err := store.GetTeamMemexDocument(
		ctx,
		storage.UserPrincipal{User: owner},
		teamID,
		"runtime/sessions.md",
	)
	require.NoError(t, err)
	require.Equal(t, "Old session notes", document.Summary)
}

func TestTeamMemexRunAuthorizesBeforeCallingExecutor(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	store := storage.NewMemoryStore(func() time.Time { return now })
	owner, err := store.EnsureUser(ctx, "owner@example.com", "", "user")
	require.NoError(t, err)
	member, err := store.EnsureUser(ctx, "member@example.com", "", "user")
	require.NoError(t, err)
	teamID := "team_1"
	_, err = store.CreateTeam(ctx, domain.Team{
		TeamID:      teamID,
		OwnerUserID: owner.UserID,
		Name:        "Core",
		Status:      domain.TeamStatusActive,
		CreatedAt:   now,
	}, domain.TeamMember{
		TeamID:        teamID,
		UserID:        owner.UserID,
		Email:         owner.Email,
		Role:          domain.TeamRoleOwner,
		Status:        domain.TeamMemberStatusActive,
		InvitedByUser: owner.UserID,
		JoinedAt:      now,
	})
	require.NoError(t, err)
	_, err = store.CreateTeamInvite(ctx, storage.UserPrincipal{User: owner}, domain.TeamInvite{
		InviteID:        "tinv_member",
		TeamID:          teamID,
		Email:           member.Email,
		RecipientUserID: member.UserID,
		Role:            domain.TeamRoleMember,
		Status:          domain.TeamInviteStatusPending,
		InvitedByUserID: owner.UserID,
		CreatedAt:       now,
	})
	require.NoError(t, err)
	_, err = store.AcceptTeamInvite(
		ctx,
		storage.UserPrincipal{User: member},
		"tinv_member",
		now,
	)
	require.NoError(t, err)

	executor := &panicTeamMemexExecutor{}
	svc := NewService(
		store,
		func() time.Time { return now },
		fixedTeamMemexPrincipal{principal: domain.UserPrincipal{User: member}},
		&sequenceSecretIssuer{},
	)
	svc.memexExecutor = executor

	_, _, err = svc.CreateTeamMemexRun(ctx, auth.RequestMetadata{}, teamID)
	require.ErrorIs(t, err, storage.ErrUnauthorized)
	require.False(t, executor.called)
}

func seedTeamMemexDocuments(
	t *testing.T,
	ctx context.Context,
	store *storage.MemoryStore,
	owner domain.User,
	teamID string,
	now time.Time,
) {
	t.Helper()
	_, err := store.PublishTeamMemexRun(
		ctx,
		storage.UserPrincipal{User: owner},
		domain.TeamMemexRun{
			RunID:             "tmrun_seed",
			TeamID:            teamID,
			RequestedByUserID: owner.UserID,
			ExecutorType:      domain.TeamMemexRunExecutorDryRun,
			Status:            domain.TeamMemexRunStatusSucceeded,
			Constraints:       domain.DefaultTeamMemexRunConstraints(),
			StartedAt:         now,
			CompletedAt:       &now,
		},
		[]domain.TeamMemexDocumentOperation{
			{
				Operation:  domain.TeamMemexOperationCreateDoc,
				DocumentID: "tmdoc_runtime",
				Path:       "runtime/sessions.md",
				Title:      "Sessions",
				Summary:    "Old session notes",
				Tags:       json.RawMessage(`["runtime"]`),
				BodyMD:     "# Sessions\nOld session notes.\n",
			},
			{
				Operation:  domain.TeamMemexOperationCreateDoc,
				DocumentID: "tmdoc_old",
				Path:       "old/cleanup.md",
				Title:      "Cleanup",
				Summary:    "Old cleanup notes",
				BodyMD:     "# Cleanup\n",
			},
		},
		now,
	)
	require.NoError(t, err)
}

type capturingTeamMemexExecutor struct {
	manifest   domain.TeamMemexManifest
	indexMD    string
	searchBody string
	readBody   string
}

func (e *capturingTeamMemexExecutor) MaintainTeamMemex(
	ctx context.Context,
	input TeamMemexExecutorInput,
) (domain.TeamMemexManifest, error) {
	e.indexMD = input.Workspace.ListIndex()
	matches, err := input.Workspace.SearchDocs("session")
	if err != nil {
		return domain.TeamMemexManifest{}, err
	}
	if len(matches) > 0 {
		e.searchBody = matches[0].BodyMD
	}
	doc, err := input.Workspace.ReadDoc("runtime/sessions.md")
	if err != nil {
		return domain.TeamMemexManifest{}, err
	}
	e.readBody = doc.BodyMD
	return e.manifest, nil
}

type panicTeamMemexExecutor struct {
	called bool
}

func (e *panicTeamMemexExecutor) MaintainTeamMemex(
	context.Context,
	TeamMemexExecutorInput,
) (domain.TeamMemexManifest, error) {
	e.called = true
	return domain.TeamMemexManifest{}, nil
}

type fixedTeamMemexPrincipal struct {
	principal domain.UserPrincipal
}

func (r fixedTeamMemexPrincipal) Principal(
	context.Context,
	auth.RequestMetadata,
) (domain.UserPrincipal, error) {
	return r.principal, nil
}

type sequenceSecretIssuer struct {
	values map[string][]string
}

func (s *sequenceSecretIssuer) New(prefix string) (string, error) {
	values := s.values[prefix]
	if len(values) == 0 {
		return prefix + "_auto", nil
	}
	next := values[0]
	s.values[prefix] = values[1:]
	return next, nil
}

func (s *sequenceSecretIssuer) Hash(secret string) string {
	return "hashed_" + secret
}

func (s *sequenceSecretIssuer) Prefix(secret string) string {
	return secret
}
