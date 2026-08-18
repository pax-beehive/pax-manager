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
	srv.cfg.ObjectStorageBucket = "session-artifacts-test"
	srv.cfg.SessionArtifactUploadTTL = time.Minute
	srv.paxdArtifacts = &fakePaxdArtifactBackend{
		attrs: paxdArtifactObjectAttrs{
			Generation:  101,
			SizeBytes:   12,
			ContentType: "text/plain",
			SHA256:      "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
	}

	createReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/attachments",
		strings.NewReader(`{
			"filename":"notes.txt",
			"content_type":"text/plain",
			"size_bytes":12,
			"sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		}`),
	)
	createReq.Header.Set("X-User-Email", "attachment@example.com")
	createRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())

	ticket := decodeData[UserAttachmentUploadTicket](t, createRec.Body.Bytes())
	require.NotEmpty(t, ticket.Attachment.AttachmentID)
	assert.Equal(t, domain.UserAttachmentUploadPending, ticket.Attachment.UploadStatus)
	assert.Equal(t, "s3_presigned_put", ticket.Upload.Protocol)
	assert.Equal(t, http.MethodPut, ticket.Upload.Method)
	assert.Equal(t, "text/plain", ticket.Upload.Headers["Content-Type"])
	assert.Equal(t, "*", ticket.Upload.Headers["If-None-Match"])
	assert.Equal(
		t,
		"ASNFZ4mrze8BI0VniavN7wEjRWeJq83vASNFZ4mrze8=",
		ticket.Upload.Headers["x-amz-checksum-sha256"],
	)
	assert.Equal(t, ticket.Attachment.SHA256, ticket.Upload.Headers["x-amz-meta-sha256"])
	assert.Zero(t, ticket.Upload.ChunkAlignment)
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
	srv.cfg.ObjectStorageBucket = "session-artifacts-test"
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

func TestUserAttachmentGivenDeclaredZeroBytesWhenCompletingThenSizeMustMatch(t *testing.T) {
	tests := []struct {
		name       string
		storedSize int64
		wantStatus int
	}{
		{name: "empty object", storedSize: 0, wantStatus: http.StatusOK},
		{name: "non-empty object", storedSize: 1, wantStatus: http.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv, _ := testServer(t, "attachment-zero@example.com")
			srv.cfg.ObjectStorageBucket = "session-artifacts-test"
			srv.paxdArtifacts = &fakePaxdArtifactBackend{attrs: paxdArtifactObjectAttrs{
				Generation:  101,
				SizeBytes:   test.storedSize,
				ContentType: "text/plain",
			}}

			createReq := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/user/self/attachments",
				strings.NewReader(
					`{"filename":"empty.txt","content_type":"text/plain","size_bytes":0}`,
				),
			)
			createReq.Header.Set("X-User-Email", "attachment-zero@example.com")
			createRec := httptest.NewRecorder()
			srv.routes().ServeHTTP(createRec, createReq)
			require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())
			ticket := decodeData[UserAttachmentUploadTicket](t, createRec.Body.Bytes())

			completeReq := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/user/self/attachments/"+ticket.Attachment.AttachmentID+"/complete",
				nil,
			)
			completeReq.Header.Set("X-User-Email", "attachment-zero@example.com")
			completeRec := httptest.NewRecorder()
			srv.routes().ServeHTTP(completeRec, completeReq)

			require.Equal(t, test.wantStatus, completeRec.Code, completeRec.Body.String())
			if test.wantStatus == http.StatusConflict {
				assert.Contains(t, completeRec.Body.String(), "size does not match")
			}
		})
	}
}
