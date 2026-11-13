package storage

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func operationHelperFixture() (domain.SecretaryOperation, domain.SecretaryOperationSubmit) {
	operation := domain.SecretaryOperation{
		OperationID: "operation-1",
		SourceID:    "1",
		Kind:        "human_summary",
		Revision:    1,
		Targets: []domain.SecretaryOperationTarget{
			{
				MessageID:               "m1",
				ContentHash:             domain.SearchContentHash("first"),
				CanonicalizationVersion: domain.SearchCanonicalizationVersion,
				ChangeRevision:          3,
			},
			{
				MessageID:               "m2",
				ContentHash:             domain.SearchContentHash("second"),
				CanonicalizationVersion: domain.SearchCanonicalizationVersion,
				ChangeRevision:          4,
			},
		},
	}
	input := domain.SecretaryOperationSubmit{
		RequestID:        "request-1",
		ExpectedRevision: 1,
		Title:            "Decisions",
		Body:             "Useful conclusions.",
	}
	return operation, input
}

func TestSecretaryOperationPrepareGivenScopeThenOnlyExplicitCompatibleModesAreAccepted(
	t *testing.T,
) {
	for _, input := range []domain.SecretaryOperationPrepare{
		{SourceID: "1", Kind: "guided_summary", Scope: "pending"},
		{SourceID: "1", Kind: "human_summary", Scope: "all"},
		{SourceID: "1", Kind: "human_summary", Scope: "selected", MessageIDs: []string{"m1"}},
		{SourceID: "1", Kind: "human_edit", SummaryID: "summary-1"},
	} {
		require.NoError(t, validateSecretaryOperationPrepare(input))
	}
	for name, input := range map[string]domain.SecretaryOperationPrepare{
		"missing_source":         {Kind: "human_summary", Scope: "all"},
		"noncanonical_source":    {SourceID: "01", Kind: "human_summary", Scope: "all"},
		"unknown_kind":           {SourceID: "1", Kind: "unknown", Scope: "all"},
		"missing_edit_summary":   {SourceID: "1", Kind: "human_edit"},
		"edit_selected_messages": {SourceID: "1", Kind: "human_edit", SummaryID: "s", MessageIDs: []string{"m1"}},
		"summary_with_edit_id":   {SourceID: "1", Kind: "human_summary", Scope: "all", SummaryID: "s"},
		"unknown_scope":          {SourceID: "1", Kind: "human_summary", Scope: "forever"},
		"all_and_ids":            {SourceID: "1", Kind: "human_summary", Scope: "all", MessageIDs: []string{"m1"}},
		"selected_empty":         {SourceID: "1", Kind: "human_summary", Scope: "selected"},
		"too_many":               {SourceID: "1", Kind: "human_summary", Scope: "selected", MessageIDs: make([]string, domain.SecretaryOperationTargetLimit+1)},
	} {
		t.Run(
			name,
			func(t *testing.T) { assert.ErrorIs(t, validateSecretaryOperationPrepare(input), ErrConflict) },
		)
	}
}

func TestSecretaryOperationTargetsGivenSelectionThenProcessingScopeDoesNotFollowRepresentativeSources(
	t *testing.T,
) {
	operation, input := operationHelperFixture()
	input.CoverageMessageIDs = []string{"m1"}
	all, err := secretaryOperationTargets(operation, input)
	require.NoError(t, err)
	assert.Equal(t, operation.Targets, all)
	input.MessageIDs = []string{"m2"}
	selected, err := secretaryOperationTargets(operation, input)
	require.NoError(t, err)
	require.Len(t, selected, 1)
	assert.Equal(t, "m2", selected[0].MessageID)
	assert.Equal(t, []string{"m1"}, input.CoverageMessageIDs)
	operation.Kind = "human_edit"
	input.MessageIDs = nil
	input.CoverageMessageIDs = nil
	empty, err := secretaryOperationTargets(operation, input)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestSecretaryOperationTargetsGivenUnsafeSubmissionThenItCannotReachPublication(t *testing.T) {
	for name, mutate := range map[string]func(*domain.SecretaryOperation, *domain.SecretaryOperationSubmit){
		"missing_request": func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) { i.RequestID = "" },
		"long_request": func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) {
			i.RequestID = strings.Repeat("x", 257)
		},
		"no_revision": func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) { i.ExpectedRevision = 0 },
		"long_run": func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) {
			i.RunInstruction = strings.Repeat("x", domain.SecretaryInstructionMaxBytes+1)
		},
		"many_run_runes": func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) {
			i.RunInstruction = strings.Repeat("x", domain.SecretaryInstructionMaxRunes+1)
		},
		"nul_run": func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) { i.RunInstruction = "a\x00b" },
		"invalid_run": func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) {
			i.RunInstruction = string([]byte{0xff})
		},
		"long_body": func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) {
			i.Body = strings.Repeat("x", 64*1024+1)
		},
		"long_title": func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) {
			i.Title = strings.Repeat("x", 4097)
		},
		"invalid_body":  func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) { i.Body = string([]byte{0xff}) },
		"invalid_title": func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) { i.Title = string([]byte{0xff}) },
		"nul_body":      func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) { i.Body = "a\x00b" },
		"nul_title":     func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) { i.Title = "a\x00b" },
		"too_many_sources": func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) {
			i.CoverageMessageIDs = make([]string, 513)
		},
		"empty_selection": func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) { i.MessageIDs = []string{} },
		"empty_id":        func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) { i.MessageIDs = []string{""} },
		"duplicate_id": func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) {
			i.MessageIDs = []string{"m1", "m1"}
		},
		"foreign_id": func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) {
			i.MessageIDs = []string{"other"}
		},
		"edit_message_scope": func(o *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) {
			o.Kind = "human_edit"
			i.MessageIDs = []string{"m1"}
		},
		"edit_coverage_scope": func(o *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) {
			o.Kind = "human_edit"
			i.CoverageMessageIDs = []string{"m1"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			o, i := operationHelperFixture()
			mutate(&o, &i)
			_, err := secretaryOperationTargets(o, i)
			assert.ErrorIs(t, err, ErrConflict)
		})
	}
}

func TestSecretaryOperationSummaryGivenManualCreationThenOnlyRepresentativeCoverageIsStored(
	t *testing.T,
) {
	operation, input := operationHelperFixture()
	request, err := secretaryOperationSummaryRequest(operation, input)
	RequireOperationSummary(t, request, err, "m1")
	assert.Equal(t, "summary_manual_operation-1", request.SummaryID)
	assert.Equal(t, secretarySubject("1"), request.SubjectID)
	input.CoverageMessageIDs = []string{"m2"}
	request, err = secretaryOperationSummaryRequest(operation, input)
	RequireOperationSummary(t, request, err, "m2")
	assert.Len(t, operation.Targets, 2)
}

func RequireOperationSummary(
	t *testing.T,
	request domain.PublishSummaryRequest,
	err error,
	message string,
) {
	t.Helper()
	require.NoError(t, err)
	require.Len(t, request.Coverage, 1)
	assert.Equal(t, message, request.Coverage[0].MessageID)
}

func TestSecretaryOperationSummaryGivenHumanEditThenExactBaselineSourcesAreInheritedWithoutNewProgress(
	t *testing.T,
) {
	operation, input := operationHelperFixture()
	operation.Kind = "human_edit"
	baseline := domain.SummaryVersion{
		SummaryID:      "existing",
		Revision:       3,
		SubjectType:    "fragment",
		SubjectID:      secretarySubject("1"),
		Coverage:       []domain.MessageVersionRef{operation.Targets[1].MessageRef()},
		SummarySources: []domain.SummaryRef{{SummaryID: "child", Revision: 2}},
	}
	operation.Summary = &baseline
	input.SummaryID = "existing"
	input.ExpectedSummaryRevision = 3
	request, err := secretaryOperationSummaryRequest(operation, input)
	RequireOperationSummary(t, request, err, "m2")
	assert.Equal(t, baseline.SummaryID, request.SummaryID)
	assert.Equal(t, baseline.Revision, request.ExpectedRevision)
	assert.Equal(t, baseline.SummarySources, request.SummarySources)
	request.Coverage[0].MessageID = "changed"
	assert.Equal(t, "m2", baseline.Coverage[0].MessageID)
	request.SummarySources[0].Revision = 9
	assert.EqualValues(t, 2, baseline.SummarySources[0].Revision)
}

func TestSecretaryOperationSummaryGivenInvalidCoverageOrBaselineThenNoSummaryIsComposed(
	t *testing.T,
) {
	for name, mutate := range map[string]func(*domain.SecretaryOperation, *domain.SecretaryOperationSubmit){
		"foreign_source": func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) {
			i.CoverageMessageIDs = []string{"other"}
		},
		"duplicate_source": func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) {
			i.CoverageMessageIDs = []string{"m1", "m1"}
		},
		"no_source": func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) {
			i.CoverageMessageIDs = []string{}
		},
		"wrong_summary_id":       func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) { i.SummaryID = "other" },
		"wrong_summary_revision": func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) { i.ExpectedSummaryRevision = 3 },
		"blank_title":            func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) { i.Title = " " },
		"invalid_body":           func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) { i.Body = string([]byte{0xff}) },
		"invalid_title":          func(_ *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) { i.Title = string([]byte{0xff}) },
		"missing_baseline":       func(o *domain.SecretaryOperation, _ *domain.SecretaryOperationSubmit) { o.Kind = "human_edit" },
		"wrong_baseline": func(o *domain.SecretaryOperation, i *domain.SecretaryOperationSubmit) {
			o.Kind = "human_edit"
			o.Summary = &domain.SummaryVersion{SummaryID: "existing", Revision: 1}
			i.SummaryID = "existing"
			i.ExpectedSummaryRevision = 2
		},
	} {
		t.Run(name, func(t *testing.T) {
			o, i := operationHelperFixture()
			mutate(&o, &i)
			_, err := secretaryOperationSummaryRequest(o, i)
			require.Error(t, err)
		})
	}
}

func TestSecretaryOperationGivenCopiesAndIdempotencyThenSnapshotsDoNotAliasAndInputChangesHaveDifferentHashes(
	t *testing.T,
) {
	operation, input := operationHelperFixture()
	operation.Instructions = &domain.SecretaryInstructionSnapshot{Run: "Keep key outcomes."}
	copy := cloneSecretaryOperation(operation)
	copy.Targets[0].MessageID = "changed"
	copy.Instructions.Run = "changed"
	assert.Equal(t, "m1", operation.Targets[0].MessageID)
	assert.Equal(t, "Keep key outcomes.", operation.Instructions.Run)
	first := secretaryOperationHash(input)
	assert.Equal(t, first, secretaryOperationHash(input))
	input.RunInstruction = "Different selection."
	assert.NotEqual(t, first, secretaryOperationHash(input))
	assert.ErrorIs(t, secretaryOperationVersionError("m1"), domain.ErrSourceVersionUnavailable)
	assert.Contains(t, secretaryOperationVersionError("m1").Error(), "refresh the selected range")
	assert.ErrorIs(t, fmtSecretaryReplacementRequired(), ErrConflict)
	assert.Equal(t, "legacy_unknown", summaryAuthorKind(""))
	assert.Equal(t, "user", summaryAuthorKind("user"))
}

func TestSecretaryOperationPreviewGivenLongUTF8ThenItIsBoundedWithoutSplittingRunes(t *testing.T) {
	for _, tc := range []struct{ role, body string }{
		{"user", "Short user request."}, {"user", strings.Repeat("\u4e2d", 1024)}, {"assistant", "Earlier details.\n\nLast paragraph."},
	} {
		text := secretaryOperationPreview(domain.SearchDocument{Role: tc.role, Body: tc.body})
		assert.True(t, utf8.ValidString(text))
		assert.LessOrEqual(t, len(text), 1027)
		if tc.role == "assistant" {
			assert.Equal(t, "Last paragraph.", text)
		}
	}
}
