package userapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type secretaryControlFixture struct {
	owner, scope, source, id string
	calls                    int
	update                   domain.SecretaryGuidanceUpdate
	prepare                  domain.SecretaryOperationPrepare
	submit                   domain.SecretaryOperationSubmit
	err                      error
}

func (f *secretaryControlFixture) GetSecretaryGuidance(
	_ context.Context,
	owner, scope, source string,
) (domain.SecretaryGuidance, error) {
	f.owner, f.scope, f.source = owner, scope, source
	f.calls++
	return domain.SecretaryGuidance{OwnerUserID: owner, Scope: scope, SourceID: source}, f.err
}

func (f *secretaryControlFixture) UpdateSecretaryGuidance(
	ctx context.Context,
	owner, scope, source string,
	input domain.SecretaryGuidanceUpdate,
) (domain.SecretaryGuidance, error) {
	f.update = input
	return f.GetSecretaryGuidance(ctx, owner, scope, source)
}

func (f *secretaryControlFixture) PrepareSecretaryOperation(
	_ context.Context,
	owner string,
	input domain.SecretaryOperationPrepare,
) (domain.SecretaryOperation, error) {
	f.owner, f.prepare = owner, input
	f.calls++
	return domain.SecretaryOperation{OperationID: "operation", SourceID: input.SourceID}, f.err
}

func (f *secretaryControlFixture) GetSecretaryOperation(
	_ context.Context,
	owner, id string,
) (domain.SecretaryOperation, error) {
	f.owner, f.id = owner, id
	f.calls++
	return domain.SecretaryOperation{OperationID: id}, f.err
}

func (f *secretaryControlFixture) SubmitSecretaryOperation(
	ctx context.Context,
	owner, id string,
	input domain.SecretaryOperationSubmit,
) (domain.SecretaryOperation, error) {
	f.submit = input
	return f.GetSecretaryOperation(ctx, owner, id)
}

func TestSecretaryControlGivenOwnerThenUsesAuthenticatedIdentityAndNormalizesGuidance(
	t *testing.T,
) {
	f := &secretaryControlFixture{}
	s := NewSecretaryControlService(workspacePrincipal{owner: "owner"}, f, f)
	_, err := s.GetGuidance(t.Context(), auth.RequestMetadata{}, "user", "")
	require.NoError(t, err)
	assert.Equal(t, "owner", f.owner)
	_, err = s.UpdateGuidance(
		t.Context(),
		auth.RequestMetadata{},
		"session",
		"42",
		domain.SecretaryGuidanceUpdate{Instruction: "  concise  ", ExpectedRevision: 3},
	)
	require.NoError(t, err)
	assert.Equal(t, "session", f.scope)
	assert.Equal(t, "42", f.source)
	assert.Equal(t, "concise", f.update.Instruction)
	assert.EqualValues(t, 3, f.update.ExpectedRevision)
	_, err = s.Prepare(
		t.Context(),
		auth.RequestMetadata{},
		domain.SecretaryOperationPrepare{SourceID: "42", Kind: "guided_summary"},
	)
	require.NoError(t, err)
	assert.Equal(t, "pending", f.prepare.Scope)
	_, err = s.GetOperation(t.Context(), auth.RequestMetadata{}, "operation")
	require.NoError(t, err)
	_, err = s.Submit(
		t.Context(),
		auth.RequestMetadata{},
		"operation",
		domain.SecretaryOperationSubmit{
			RequestID:          "request",
			ExpectedRevision:   1,
			RunInstruction:     "  focus  ",
			MessageIDs:         []string{"m1"},
			CoverageMessageIDs: []string{"m1"},
		},
	)
	require.NoError(t, err)
	assert.Equal(t, "owner", f.owner)
	assert.Equal(t, "operation", f.id)
	assert.Equal(t, "focus", f.submit.RunInstruction)
	assert.Equal(t, []string{"m1"}, f.submit.MessageIDs)
}

func TestSecretaryControlGivenInvalidGuidanceThenDoesNotReadOrWriteStore(t *testing.T) {
	f := &secretaryControlFixture{}
	s := NewSecretaryControlService(workspacePrincipal{owner: "owner"}, f, f)
	for _, pair := range [][2]string{{"", ""}, {"user", "42"}, {"session", ""}, {"session", "01"}, {"session", "-1"}, {"session", "native-session"}} {
		_, err := s.GetGuidance(t.Context(), auth.RequestMetadata{}, pair[0], pair[1])
		assertTaskAPIStatus(t, err, http.StatusBadRequest)
		_, err = s.UpdateGuidance(
			t.Context(),
			auth.RequestMetadata{},
			pair[0],
			pair[1],
			domain.SecretaryGuidanceUpdate{},
		)
		assertTaskAPIStatus(t, err, http.StatusBadRequest)
	}
	for _, input := range []domain.SecretaryGuidanceUpdate{
		{ExpectedRevision: -1}, {Instruction: strings.Repeat("a", 8193)},
		{Instruction: strings.Repeat("a", 4097)}, {Instruction: string([]byte{255})}, {Instruction: "a\x00b"},
	} {
		_, err := s.UpdateGuidance(t.Context(), auth.RequestMetadata{}, "user", "", input)
		assertTaskAPIStatus(t, err, http.StatusBadRequest)
	}
	assert.Zero(t, f.calls)
}

func TestSecretaryControlGivenInvalidSelectionThenRejectsBeforePreparing(t *testing.T) {
	f := &secretaryControlFixture{}
	s := NewSecretaryControlService(workspacePrincipal{owner: "owner"}, f, f)
	for _, input := range []domain.SecretaryOperationPrepare{
		{SourceID: "invalid", Kind: "guided_summary"}, {SourceID: "1", Kind: "delete"},
		{SourceID: "1", Kind: "guided_summary", Scope: "anything"},
		{SourceID: "1", Kind: "human_summary", Scope: "selected"},
		{SourceID: "1", Kind: "human_summary", MessageIDs: []string{"m1", "m1"}},
		{SourceID: "1", Kind: "human_summary", MessageIDs: []string{"a/b"}},
		{SourceID: "1", Kind: "human_summary", MessageIDs: make([]string, 4097)},
		{SourceID: "1", Kind: "human_edit"},
	} {
		_, err := s.Prepare(t.Context(), auth.RequestMetadata{}, input)
		assertTaskAPIStatus(t, err, http.StatusBadRequest)
	}
	for _, id := range []string{"", " bad", "bad\n", strings.Repeat("a", 513)} {
		_, err := s.GetOperation(t.Context(), auth.RequestMetadata{}, id)
		assertTaskAPIStatus(t, err, http.StatusBadRequest)
	}
	assert.Zero(t, f.calls)
	_, err := s.Prepare(
		t.Context(),
		auth.RequestMetadata{},
		domain.SecretaryOperationPrepare{SourceID: "1", Kind: "human_edit", SummaryID: "summary"},
	)
	require.NoError(t, err)
}

func TestSecretaryControlGivenInvalidSubmissionThenDoesNotMutate(t *testing.T) {
	f := &secretaryControlFixture{}
	s := NewSecretaryControlService(workspacePrincipal{owner: "owner"}, f, f)
	for _, mutate := range []func(*domain.SecretaryOperationSubmit){
		func(r *domain.SecretaryOperationSubmit) { r.RequestID = "" },
		func(r *domain.SecretaryOperationSubmit) { r.RequestID = strings.Repeat("a", 257) },
		func(r *domain.SecretaryOperationSubmit) { r.ExpectedRevision = 0 },
		func(r *domain.SecretaryOperationSubmit) { r.ExpectedSummaryRevision = -1 },
		func(r *domain.SecretaryOperationSubmit) { r.MessageIDs = []string{"m", "m"} },
		func(r *domain.SecretaryOperationSubmit) { r.CoverageMessageIDs = make([]string, 513) },
		func(r *domain.SecretaryOperationSubmit) { r.RunInstruction = strings.Repeat("a", 4097) },
		func(r *domain.SecretaryOperationSubmit) { r.RunInstruction = "a\x00b" },
		func(r *domain.SecretaryOperationSubmit) { r.Title = strings.Repeat("a", 4097) },
		func(r *domain.SecretaryOperationSubmit) { r.Title = string([]byte{255}) },
		func(r *domain.SecretaryOperationSubmit) { r.Body = strings.Repeat("a", 65537) },
		func(r *domain.SecretaryOperationSubmit) { r.Body = string([]byte{255}) },
		func(r *domain.SecretaryOperationSubmit) { r.Body = "bad\x00body" },
		func(r *domain.SecretaryOperationSubmit) { r.Title = "bad\x00title" },
	} {
		input := domain.SecretaryOperationSubmit{RequestID: "request", ExpectedRevision: 1}
		mutate(&input)
		_, err := s.Submit(t.Context(), auth.RequestMetadata{}, "operation", input)
		assertTaskAPIStatus(t, err, http.StatusBadRequest)
	}
	assert.Zero(t, f.calls)
}

func TestSecretaryControlGivenMissingIdentityOrStoreThenFailsClosed(t *testing.T) {
	f := &secretaryControlFixture{}
	for _, s := range []*SecretaryControlService{nil, NewSecretaryControlService(nil, f, f), NewSecretaryControlService(workspacePrincipal{}, f, f), NewSecretaryControlService(workspacePrincipal{err: domain.ErrUnauthorized}, f, f)} {
		_, err := s.GetGuidance(t.Context(), auth.RequestMetadata{}, "user", "")
		assert.ErrorIs(t, err, domain.ErrUnauthorized)
		_, err = s.UpdateGuidance(
			t.Context(),
			auth.RequestMetadata{},
			"user",
			"",
			domain.SecretaryGuidanceUpdate{},
		)
		assert.ErrorIs(t, err, domain.ErrUnauthorized)
		_, err = s.Prepare(t.Context(), auth.RequestMetadata{}, domain.SecretaryOperationPrepare{})
		assert.ErrorIs(t, err, domain.ErrUnauthorized)
		_, err = s.GetOperation(t.Context(), auth.RequestMetadata{}, "operation")
		assert.ErrorIs(t, err, domain.ErrUnauthorized)
		_, err = s.Submit(
			t.Context(),
			auth.RequestMetadata{},
			"operation",
			domain.SecretaryOperationSubmit{},
		)
		assert.ErrorIs(t, err, domain.ErrUnauthorized)
	}
	s := NewSecretaryControlService(workspacePrincipal{owner: "owner"}, nil, nil)
	_, err := s.GetGuidance(t.Context(), auth.RequestMetadata{}, "user", "")
	assertTaskAPIStatus(t, err, http.StatusServiceUnavailable)
	_, err = s.UpdateGuidance(
		t.Context(),
		auth.RequestMetadata{},
		"user",
		"",
		domain.SecretaryGuidanceUpdate{},
	)
	assertTaskAPIStatus(t, err, http.StatusServiceUnavailable)
	_, err = s.Prepare(
		t.Context(),
		auth.RequestMetadata{},
		domain.SecretaryOperationPrepare{SourceID: "1", Kind: "human_summary"},
	)
	assertTaskAPIStatus(t, err, http.StatusServiceUnavailable)
	_, err = s.GetOperation(t.Context(), auth.RequestMetadata{}, "operation")
	assertTaskAPIStatus(t, err, http.StatusServiceUnavailable)
	_, err = s.Submit(
		t.Context(),
		auth.RequestMetadata{},
		"operation",
		domain.SecretaryOperationSubmit{RequestID: "request", ExpectedRevision: 1},
	)
	assertTaskAPIStatus(t, err, http.StatusServiceUnavailable)
	assert.Zero(t, f.calls)
}

func TestSecretaryControlGivenStoreFailureThenReturnsSafeConflictOrFailure(t *testing.T) {
	for _, input := range []struct {
		err    error
		status int
	}{
		{domain.ErrConflict, 409}, {domain.ErrSecretaryFenced, 409},
		{domain.ErrSummaryVersionMismatch, 409}, {domain.ErrSourceVersionUnavailable, 409},
		{domain.ErrSecretarySourceIncomplete, 409}, {errors.New("private storage detail"), 500},
	} {
		f := &secretaryControlFixture{err: input.err}
		s := NewSecretaryControlService(workspacePrincipal{owner: "owner"}, f, f)
		_, err := s.GetOperation(t.Context(), auth.RequestMetadata{}, "operation")
		assertTaskAPIStatus(t, err, input.status)
		assert.NotContains(t, err.Error(), "private storage detail")
	}
	for _, input := range []error{domain.ErrNotFound, domain.ErrUnauthorized} {
		assert.ErrorIs(t, secretaryControlError(input), input)
	}
}
