package userapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestWorkspaceSearchGivenTrustedPrincipalCloneThenKeepsReceiptKeyAndOriginalIdentity(
	t *testing.T,
) {
	service, _, _ := workspaceFixture()
	clone := service.WithPrincipal(workspacePrincipal{owner: "owner"})
	value, err := clone.Read(
		t.Context(),
		auth.RequestMetadata{},
		WorkspaceReadRequest{Ref: SearchRef{Kind: "message", ID: "m1"}},
	)
	require.NoError(t, err)
	read := value.(WorkspaceMessageRead)
	ref := domain.MessageVersionRef{MessageID: read.MessageID, ContentHash: read.Ref.ContentHash,
		CanonicalizationVersion: read.Ref.CanonicalizationVersion}
	verified, err := service.VerifyReadReceipt(t.Context(), "owner", ref, read.ReadReceipt)
	require.NoError(t, err)
	assert.Equal(t, read.Text, verified.Text)
	_, err = service.VerifyReadReceipt(t.Context(), "foreign", ref, read.ReadReceipt)
	require.Error(t, err)
	_, err = service.WithPrincipal(workspacePrincipal{owner: "foreign"}).
		Read(t.Context(), auth.RequestMetadata{}, WorkspaceReadRequest{Ref: read.Ref})
	assert.ErrorIs(t, err, domain.ErrNotFound)
	owner, err := service.owner(t.Context(), auth.RequestMetadata{})
	require.NoError(t, err)
	assert.Equal(t, "owner", owner)
}
