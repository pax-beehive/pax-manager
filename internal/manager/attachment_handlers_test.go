package manager

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestUserAttachmentGivenNoAgentWhenUploadCompletesThenItIsReusable(t *testing.T) {
	srv, _ := testServer(t, "attachment@example.com")
	srv.cfg.SessionArtifactGCSBucket = "session-artifacts-test"
	srv.cfg.SessionArtifactUploadTTL = time.Minute
	srv.paxdArtifacts = &fakePaxdArtifactBackend{
		attrs: paxdArtifactObjectAttrs{
			Generation:  101,
			SizeBytes:   12,
			ContentType: "text/plain",
		},
	}

	createReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/attachments",
		strings.NewReader(`{
			"filename":"notes.txt",
			"content_type":"text/plain",
			"size_bytes":12
		}`),
	)
	createReq.Header.Set("X-User-Email", "attachment@example.com")
	createRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())

	ticket := decodeData[UserAttachmentUploadTicket](t, createRec.Body.Bytes())
	require.NotEmpty(t, ticket.Attachment.AttachmentID)
	assert.Equal(t, domain.UserAttachmentUploadPending, ticket.Attachment.UploadStatus)
	assert.Equal(t, "gcs_resumable", ticket.Upload.Protocol)
	assert.Equal(t, http.MethodPost, ticket.Upload.Method)
	assert.Equal(t, "start", ticket.Upload.Headers["x-goog-resumable"])
	assert.NotContains(t, createRec.Body.String(), "agent_id")
	assert.NotContains(t, createRec.Body.String(), "node_id")

	complete := func() CompleteUserAttachmentData {
		t.Helper()
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/user/self/attachments/"+ticket.Attachment.AttachmentID+"/complete",
			nil,
		)
		req.Header.Set("X-User-Email", "attachment@example.com")
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return decodeData[CompleteUserAttachmentData](t, rec.Body.Bytes())
	}

	first := complete()
	second := complete()
	assert.Equal(t, domain.UserAttachmentUploadCompleted, first.Attachment.UploadStatus)
	assert.Equal(t, first.Attachment, second.Attachment)
	assert.Equal(t, int64(101), first.Attachment.Generation)
}

func TestUserAttachmentGivenDifferentOwnerWhenCompletingThenItIsNotFound(t *testing.T) {
	srv, _ := testServer(t, "owner@example.com")
	srv.cfg.SessionArtifactGCSBucket = "session-artifacts-test"
	srv.paxdArtifacts = &fakePaxdArtifactBackend{}

	createReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/attachments",
		strings.NewReader(`{"filename":"private.txt","content_type":"text/plain"}`),
	)
	createReq.Header.Set("X-User-Email", "owner@example.com")
	createRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())
	ticket := decodeData[UserAttachmentUploadTicket](t, createRec.Body.Bytes())

	completeReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/attachments/"+ticket.Attachment.AttachmentID+"/complete",
		nil,
	)
	completeReq.Header.Set("X-User-Email", "other@example.com")
	completeRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(completeRec, completeReq)
	assert.Equal(t, http.StatusNotFound, completeRec.Code, completeRec.Body.String())
}
