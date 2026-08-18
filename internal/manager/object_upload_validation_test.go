package manager

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSinglePutObjectSizeValidation(t *testing.T) {
	tests := []struct {
		name      string
		normalize func(int64) error
	}{
		{
			name: "generic artifact",
			normalize: func(size int64) error {
				_, err := normalizeCreateArtifactUploadRequest(CreateArtifactUploadRequest{
					Filename:  "artifact.bin",
					SizeBytes: size,
				})
				return err
			},
		},
		{
			name: "user attachment",
			normalize: func(size int64) error {
				_, err := normalizeCreateUserAttachmentRequest(CreateUserAttachmentRequest{
					Filename:  "attachment.bin",
					SizeBytes: size,
				})
				return err
			},
		},
		{
			name: "node publication",
			normalize: func(size int64) error {
				_, err := normalizePrepareArtifactPublicationRequest(
					PrepareArtifactPublicationRequest{
						Filename:  "publication.bin",
						SizeBytes: size,
						SHA256: "0123456789abcdef0123456789abcdef" +
							"0123456789abcdef0123456789abcdef",
					},
				)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(
			"Given "+test.name+" at the single PUT limit when validating then it is accepted",
			func(t *testing.T) {
				require.NoError(t, test.normalize(maxSinglePutObjectSizeBytes))
			},
		)
		t.Run(
			"Given "+test.name+" above the single PUT limit when validating then multipart is recommended",
			func(t *testing.T) {
				err := test.normalize(maxSinglePutObjectSizeBytes + 1)

				require.Error(t, err)
				assert.Contains(t, err.Error(), "5 GiB")
				assert.Contains(t, err.Error(), "multipart")
			},
		)
	}
}
