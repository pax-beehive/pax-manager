package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBinaryQualityIsExclusiveAndPreservesArtifactKind(t *testing.T) {
	for _, tc := range []struct {
		tags       []string
		state      string
		normalized []string
	}{
		{nil, "testing", []string{"testing"}},
		{[]string{"testing"}, "testing", []string{"testing"}},
		{[]string{"testing", "stable", "installer"}, "stable", []string{"installer", "stable"}},
		{[]string{"testing", "stable", "disabled", "installer"}, "disabled", []string{"disabled", "installer"}},
	} {
		require.Equal(t, tc.state, BinaryQuality(tc.tags))
		require.Equal(t, tc.normalized, BinaryQualityTags(tc.tags))
	}
}
