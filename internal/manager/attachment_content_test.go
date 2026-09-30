package manager

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserAttachmentGivenStoredUploadWhenPreviewedThenAccessIsChecked(t *testing.T) {
	for _, test := range []struct {
		name        string
		email       string
		contentType string
		complete    bool
		bucket      string
		status      int
		disposition string
	}{
		{"image owner", "owner@example.com", "image/png", true, "attachments", 302, "inline"},
		{"other owner", "other@example.com", "image/png", true, "attachments", 404, ""},
		{"pending upload", "owner@example.com", "image/png", false, "attachments", 409, ""},
		{"wrong bucket", "owner@example.com", "image/png", true, "different", 403, ""},
		{"missing config", "owner@example.com", "image/png", true, "", 500, ""},
		{"active content", "owner@example.com", "text/html", true, "attachments", 302, "attachment"},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv, _ := testServer(t, "owner@example.com")
			srv.cfg.ObjectStorageBucket = "attachments"
			backend := &fakePaxdArtifactBackend{attrs: paxdArtifactObjectAttrs{
				ContentType: test.contentType, Generation: 101,
			}}
			srv.paxdArtifacts = backend
			request := func(method, path, body, email string) *httptest.ResponseRecorder {
				t.Helper()
				req := httptest.NewRequest(method, path, strings.NewReader(body))
				req.Header.Set("X-User-Email", email)
				rec := httptest.NewRecorder()
				srv.routes().ServeHTTP(rec, req)
				return rec
			}
			created := request(http.MethodPost, "/api/v1/user/self/attachments",
				`{"filename":"photo.png","content_type":"`+test.contentType+`"}`,
				"owner@example.com")
			require.Equal(t, http.StatusOK, created.Code, created.Body.String())
			ticket := decodeData[UserAttachmentUploadTicket](t, created.Body.Bytes())
			path := "/api/v1/user/self/attachments/" + ticket.Attachment.AttachmentID
			if test.complete {
				completed := request(http.MethodPost, path+"/complete", "", "owner@example.com")
				require.Equal(t, http.StatusOK, completed.Code, completed.Body.String())
			}
			srv.cfg.ObjectStorageBucket = test.bucket
			got := request(http.MethodGet, path+"/content", "", test.email)
			require.Equal(t, test.status, got.Code, got.Body.String())
			ticketResponse := request(http.MethodGet, path+"/content?ticket=1", "", test.email)
			expectedTicketStatus := test.status
			if expectedTicketStatus == http.StatusFound {
				expectedTicketStatus = http.StatusOK
			}
			require.Equal(
				t,
				expectedTicketStatus,
				ticketResponse.Code,
				ticketResponse.Body.String(),
			)
			if expectedTicketStatus == http.StatusOK {
				data := decodeData[map[string]string](t, ticketResponse.Body.Bytes())
				assert.Equal(t, got.Header().Get("Location"), data["url"])
				assert.Equal(t, "private, no-store", ticketResponse.Header().Get("Cache-Control"))
			}

			if test.status != http.StatusFound {
				assert.Empty(t, got.Header().Get("Location"))
				assert.Empty(t, backend.signedArtifact.Object)
				return
			}
			assert.NotEmpty(t, backend.signedArtifact.Object)
			assert.Equal(
				t,
				"https://signed.example/"+backend.signedArtifact.Object,
				got.Header().Get("Location"),
			)
			assert.Equal(t, "private, no-store", got.Header().Get("Cache-Control"))
			assert.Equal(t, int64(101), backend.signedArtifact.Generation)
			assert.Equal(t, test.contentType, backend.signedQuery["response-content-type"])
			assert.Contains(
				t,
				backend.signedQuery["response-content-disposition"],
				test.disposition+";",
			)
			assert.True(t, backend.expiresAt.After(srv.clock()))
		})
	}
}
