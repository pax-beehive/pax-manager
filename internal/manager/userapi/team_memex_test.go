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
		"tmrun":     {"tmrun_publish"},
		"tmattempt": {"tmattempt_publish_1"},
		"tmdoc":     {"tmdoc_product"},
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
	require.Len(t, run.Attempts, 1)
	require.Equal(t, domain.TeamMemexRunAttemptStatusSucceeded, run.Attempts[0].Status)
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

func TestTeamMemexRunRepairsInvalidManifest(t *testing.T) {
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

	executor := &scriptedTeamMemexExecutor{manifests: []domain.TeamMemexManifest{
		{Operations: []domain.TeamMemexManifestOperation{{
			Operation: domain.TeamMemexOperationUpdateDoc,
			Path:      "index.md",
			Title:     "Index",
			Summary:   "Should not be writable",
			BodyMD:    "# Index\n",
		}}},
		{Operations: []domain.TeamMemexManifestOperation{{
			Operation: domain.TeamMemexOperationUpdateDoc,
			Path:      "runtime/sessions.md",
			Title:     "Sessions",
			Summary:   "Repaired session storage notes",
			Tags:      json.RawMessage(`["runtime","sessions"]`),
			BodyMD:    "# Sessions\nThe repaired manifest updates the existing doc.\n",
		}}},
	}}
	svc := NewService(
		store,
		func() time.Time { return now },
		fixedTeamMemexPrincipal{principal: domain.UserPrincipal{User: owner}},
		&sequenceSecretIssuer{values: map[string][]string{
			"tmrun":     {"tmrun_repair"},
			"tmattempt": {"tmattempt_repair_1", "tmattempt_repair_2"},
		}},
	)
	svc.memexExecutor = executor

	status, data, err := svc.CreateTeamMemexRun(ctx, auth.RequestMetadata{}, teamID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	run := data.(map[string]any)["run"].(teamMemexRunResponse)
	require.Equal(t, domain.TeamMemexRunStatusSucceeded, run.Status)
	require.Nil(t, run.ValidationReport)
	require.Len(t, run.Attempts, 2)
	require.Equal(t, domain.TeamMemexRunAttemptStatusValidationFailed, run.Attempts[0].Status)
	require.Equal(t, "INDEX_IS_READ_ONLY", run.Attempts[0].ValidationReport.Errors[0].Code)
	require.Equal(t, domain.TeamMemexRunAttemptStatusSucceeded, run.Attempts[1].Status)
	require.Len(t, executor.inputs, 2)
	require.Equal(t, 1, executor.inputs[0].attemptNumber)
	require.Nil(t, executor.inputs[0].previousManifest)
	require.Nil(t, executor.inputs[0].validationReport)
	require.Equal(t, 2, executor.inputs[1].attemptNumber)
	require.NotNil(t, executor.inputs[1].previousManifest)
	require.NotNil(t, executor.inputs[1].validationReport)
	require.Equal(t, "INDEX_IS_READ_ONLY", executor.inputs[1].validationReport.Errors[0].Code)

	document, err := store.GetTeamMemexDocument(
		ctx,
		storage.UserPrincipal{User: owner},
		teamID,
		"runtime/sessions.md",
	)
	require.NoError(t, err)
	require.Equal(t, "Repaired session storage notes", document.Summary)
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
		&sequenceSecretIssuer{values: map[string][]string{
			"tmrun": {"tmrun_invalid"},
			"tmattempt": {
				"tmattempt_invalid_1",
				"tmattempt_invalid_2",
				"tmattempt_invalid_3",
			},
		}},
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
	require.Len(t, run.Attempts, 3)
	for _, attempt := range run.Attempts {
		require.Equal(t, domain.TeamMemexRunAttemptStatusValidationFailed, attempt.Status)
		require.Equal(t, "INDEX_IS_READ_ONLY", attempt.ValidationReport.Errors[0].Code)
	}

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

type observedTeamMemexExecutorInput struct {
	attemptNumber    int
	previousManifest *domain.TeamMemexManifest
	validationReport *domain.TeamMemexValidationReport
}

type scriptedTeamMemexExecutor struct {
	manifests []domain.TeamMemexManifest
	inputs    []observedTeamMemexExecutorInput
}

func (e *scriptedTeamMemexExecutor) MaintainTeamMemex(
	_ context.Context,
	input TeamMemexExecutorInput,
) (domain.TeamMemexManifest, error) {
	e.inputs = append(e.inputs, observedTeamMemexExecutorInput{
		attemptNumber:    input.AttemptNumber,
		previousManifest: input.PreviousManifest,
		validationReport: input.ValidationReport,
	})
	index := len(e.inputs) - 1
	if index >= len(e.manifests) {
		index = len(e.manifests) - 1
	}
	return e.manifests[index], nil
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
