package userapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestWorkspaceReadGivenSecretaryPreviewLabelThenRequiresAnOriginalMessageView(t *testing.T) {
	service, store, _ := workspaceFixture()
	document := store.docs["message:m1"]
	document.Role = "user"
	document.Body = "Opening. " + strings.Repeat("x", 64*1024) + " Closing."
	document.ContentHash = domain.SearchContentHash(document.Body)
	store.docs[document.DocumentID()] = document
	value, err := service.Read(t.Context(), auth.RequestMetadata{}, WorkspaceReadRequest{
		Ref: SearchRef{Kind: "message", ID: document.SourceID}, View: "head_tail",
	})
	requireWorkspaceStatus(t, err, http.StatusBadRequest)
	assert.Nil(t, value)

	value, err = service.Read(t.Context(), auth.RequestMetadata{}, WorkspaceReadRequest{
		Ref: SearchRef{Kind: "message", ID: document.SourceID}, View: "full",
	})
	require.NoError(t, err)
	read := value.(WorkspaceMessageRead)
	assert.Equal(t, "full", read.View)
	assert.Equal(t, []string{"first", "last", "full"}, read.AvailableViews)
	assert.Equal(t, document.ContentHash, read.Ref.ContentHash)
	assert.True(t, read.HasMore)
	assert.NotEmpty(t, read.ReadReceipt)
}
