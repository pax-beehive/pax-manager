package manager

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

type secretaryControlHTTPStore struct {
	owner   string
	writes  int
	err     error
	prepare domain.SecretaryOperationPrepare
	submit  domain.SecretaryOperationSubmit
}

func (f *secretaryControlHTTPStore) GetSecretaryGuidance(
	_ context.Context,
	owner, scope, source string,
) (domain.SecretaryGuidance, error) {
	if owner != f.owner {
		return domain.SecretaryGuidance{}, domain.ErrNotFound
	}
	return domain.SecretaryGuidance{
		OwnerUserID: owner,
		Scope:       scope,
		SourceID:    source,
		Instruction: "Private guidance",
		Revision:    3,
	}, f.err
}

func (f *secretaryControlHTTPStore) UpdateSecretaryGuidance(
	ctx context.Context,
	owner, scope, source string,
	_ domain.SecretaryGuidanceUpdate,
) (domain.SecretaryGuidance, error) {
	if owner == f.owner && f.err == nil {
		f.writes++
	}
	return f.GetSecretaryGuidance(ctx, owner, scope, source)
}

func (f *secretaryControlHTTPStore) PrepareSecretaryOperation(
	_ context.Context,
	owner string,
	request domain.SecretaryOperationPrepare,
) (domain.SecretaryOperation, error) {
	if owner != f.owner {
		return domain.SecretaryOperation{}, domain.ErrNotFound
	}
	f.prepare = request
	if f.err == nil {
		f.writes++
	}
	return domain.SecretaryOperation{
		OperationID: "operation",
		SourceID:    request.SourceID,
		Status:      "draft",
		Revision:    1,
	}, f.err
}

func (f *secretaryControlHTTPStore) GetSecretaryOperation(
	_ context.Context,
	owner, id string,
) (domain.SecretaryOperation, error) {
	if owner != f.owner || id != "operation" {
		return domain.SecretaryOperation{}, domain.ErrNotFound
	}
	return domain.SecretaryOperation{
		OperationID: id,
		SourceID:    "42",
		Status:      "draft",
		Revision:    1,
	}, f.err
}

func (f *secretaryControlHTTPStore) SubmitSecretaryOperation(
	ctx context.Context,
	owner, id string,
	request domain.SecretaryOperationSubmit,
) (domain.SecretaryOperation, error) {
	f.submit = request
	result, err := f.GetSecretaryOperation(ctx, owner, id)
	if err == nil {
		f.writes++
	}
	return result, err
}

func secretaryControlHTTPFixture(t *testing.T) (*Server, *secretaryControlHTTPStore) {
	t.Helper()
	s, _ := testServer(t, "control-owner@example.test")
	f := &secretaryControlHTTPStore{
		owner: testUserPrincipal(t, s, "control-owner@example.test").User.UserID,
	}
	s.secretaryControl = userapi.NewSecretaryControlService(s.auth, f, f)
	return s, f
}

func TestSecretaryControlHTTPGivenOwnerThenRoutesGuidancePreparationAndSubmission(t *testing.T) {
	s, f := secretaryControlHTTPFixture(t)
	base := "/api/v1/user/self/secretary"
	for _, input := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodGet, "/guidance?scope=user", "", 200},
		{http.MethodGet, "/guidance?scope=session&source_id=42", "", 200},
		{http.MethodPut, "/guidance?scope=user", `{"instruction":"Concise","expected_revision":3}`, 200},
		{http.MethodPost, "/operations/prepare", `{"source_id":"42","kind":"human_summary"}`, 201},
		{http.MethodGet, "/operations/operation", "", 200},
		{http.MethodPost, "/operations/operation/submit", `{"request_id":"request","expected_revision":1,"message_ids":["m1"],"coverage_message_ids":["m1"],"title":"Manual","body":"Repeated releases","confirm_replace":true}`, 200},
	} {
		r := taskHTTPRequest(
			t,
			s,
			input.method,
			base+input.path,
			"control-owner@example.test",
			input.body,
		)
		require.Equal(t, input.status, r.Code, r.Body.String())
	}
	assert.Equal(t, "pending", f.prepare.Scope)
	assert.Equal(t, []string{"m1"}, f.submit.MessageIDs)
	assert.Equal(t, []string{"m1"}, f.submit.CoverageMessageIDs)
	assert.True(t, f.submit.ConfirmReplace)
	assert.Equal(t, 3, f.writes)
}

func TestSecretaryControlHTTPGivenForeignUserThenHidesGuidanceAndOperations(t *testing.T) {
	s, f := secretaryControlHTTPFixture(t)
	for _, user := range []string{"self", f.owner} {
		for _, input := range []struct{ method, path, body string }{
			{http.MethodGet, "/guidance?scope=session&source_id=42", ""},
			{http.MethodPut, "/guidance?scope=user", `{"instruction":"changed","expected_revision":3}`},
			{http.MethodPost, "/operations/prepare", `{"source_id":"42","kind":"human_summary"}`},
			{http.MethodGet, "/operations/operation", ""},
			{http.MethodPost, "/operations/operation/submit", `{"request_id":"request","expected_revision":1}`},
		} {
			r := taskHTTPRequest(
				t,
				s,
				input.method,
				"/api/v1/user/"+user+"/secretary"+input.path,
				"foreign@example.test",
				input.body,
			)
			assert.Equal(t, http.StatusNotFound, r.Code, r.Body.String())
			assert.NotContains(t, r.Body.String(), "Private guidance")
		}
	}
	assert.Zero(t, f.writes)
}

func TestSecretaryControlHTTPGivenMalformedInputThenRejectsBeforeWrite(t *testing.T) {
	s, f := secretaryControlHTTPFixture(t)
	base := "/api/v1/user/self/secretary"
	for _, query := range []string{"?scope=user&scope=session", "?scope=user&owner_user_id=other", "?scope=session&source_id=42&source_id=43", "?scope=session&source_id=01"} {
		r := taskHTTPRequest(
			t,
			s,
			http.MethodGet,
			base+"/guidance"+query,
			"control-owner@example.test",
			"",
		)
		assert.Equal(t, 400, r.Code, r.Body.String())
		r = taskHTTPRequest(
			t,
			s,
			http.MethodPut,
			base+"/guidance"+query,
			"control-owner@example.test",
			`{"instruction":"a","expected_revision":0}`,
		)
		assert.Equal(t, 400, r.Code, r.Body.String())
	}
	for _, input := range []struct{ path, body string }{
		{"/guidance?scope=user", `{}`},
		{"/guidance?scope=user", `{"instruction":"a","expected_revision":0,"instruction":"b"}`},
		{"/guidance?scope=user", `{"instruction":"a","expected_revision":0,"Instruction":"b"}`},
		{"/guidance?scope=user", `{"instruction":null,"expected_revision":0}`},
		{"/guidance?scope=user", `{"instruction":"a","expected_revision":0,"owner_user_id":"other"}`},
		{"/guidance?scope=user", `{"instruction":"a","expected_revision":0} {}`},
		{"/guidance?scope=user", strings.Repeat(" ", 16*1024+1)},
		{"/operations/prepare", `{"source_id":"42"}`},
		{"/operations/prepare", `{"source_id":"42","kind":"human_summary","owner_user_id":"other"}`},
		{"/operations/prepare?unexpected=1", `{"source_id":"42","kind":"human_summary"}`},
		{"/operations/operation/submit", `{"request_id":"request"}`},
		{"/operations/operation/submit", `{"request_id":"request","expected_revision":1,"message_ids":null}`},
		{"/operations/operation/submit", `{"request_id":"request","expected_revision":1,"targets":["m1"]}`},
		{"/operations/operation/submit?unexpected=1", `{"request_id":"request","expected_revision":1}`},
	} {
		method := http.MethodPost
		if strings.HasPrefix(input.path, "/guidance") {
			method = http.MethodPut
		}
		r := taskHTTPRequest(
			t,
			s,
			method,
			base+input.path,
			"control-owner@example.test",
			input.body,
		)
		assert.Equal(t, 400, r.Code, r.Body.String())
	}
	r := taskHTTPRequest(
		t,
		s,
		http.MethodGet,
		base+"/operations/operation?owner_user_id=other",
		"control-owner@example.test",
		"",
	)
	assert.Equal(t, 400, r.Code, r.Body.String())
	assert.Zero(t, f.writes)
}

func TestSecretaryControlHTTPGivenWriteFailureThenNeverClaimsSuccess(t *testing.T) {
	s, f := secretaryControlHTTPFixture(t)
	for _, input := range []struct {
		err    error
		status int
	}{
		{domain.ErrConflict, 409}, {domain.ErrSecretaryFenced, 409}, {domain.ErrSourceVersionUnavailable, 409}, {errors.New("private SQL error"), 500},
	} {
		f.err = input.err
		r := taskHTTPRequest(
			t,
			s,
			http.MethodPost,
			"/api/v1/user/self/secretary/operations/operation/submit",
			"control-owner@example.test",
			`{"request_id":"request","expected_revision":1}`,
		)
		assert.Equal(t, input.status, r.Code, r.Body.String())
		assert.NotContains(t, r.Body.String(), "private SQL error")
	}
	assert.Zero(t, f.writes)
}

func TestSecretaryControlDecodeGivenInvalidJSONThenRejectsAmbiguousValues(t *testing.T) {
	for _, raw := range []string{"", "[]", "null", "{", `{"instruction"`, `{"instruction":`, `{"instruction":"x"`, `{"instruction":"x"} rubbish`, `{"instruction":"x","unknown":1}`, `{"instruction":2}`} {
		var value domain.SecretaryGuidanceUpdate
		assert.False(t, decodeSecretaryControl([]byte(raw), &value, 1024, "instruction"), raw)
	}
	var value domain.SecretaryGuidanceUpdate
	assert.True(
		t,
		decodeSecretaryControl(
			[]byte(`{"instruction":"","expected_revision":0}`),
			&value,
			1024,
			"instruction",
			"expected_revision",
		),
	)
	assert.False(t, decodeSecretaryControl([]byte{'{', '"', 255, '"', ':', '1', '}'}, &value, 1024))
	assert.Empty(t, secretaryControlJSONFields(nil))
	assert.Empty(t, secretaryControlJSONFields("invalid target"))
}
