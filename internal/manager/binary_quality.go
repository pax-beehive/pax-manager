package manager

import (
	"context"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func binaryQuality(tags []string) string                { return domain.BinaryQuality(tags) }
func normalizeBinaryQualityTags(tags []string) []string { return domain.BinaryQualityTags(tags) }

func binaryQualityMetadata(artifact PaxdArtifact) map[string]any {
	state := binaryQuality(artifact.Tags)
	artifact.Tags = normalizeBinaryQualityTags(artifact.Tags)
	result := map[string]any{"artifact": artifact, "status": state}
	if state == "disabled" {
		result["warning"] = "BINARY_DISABLED_UPGRADE_RECOMMENDED"
	}
	return result
}

func binaryQualityWarning(state string) string {
	if state == "disabled" {
		return "BINARY_DISABLED_UPGRADE_RECOMMENDED"
	}
	return ""
}

func (s *Service) currentBinaryQuality(
	c context.Context,
	ctx *app.RequestContext,
	req FindPaxdArtifactRequest,
) string {
	version := strings.TrimSpace(string(ctx.QueryArgs().Peek("current_version")))
	if version == "" {
		return ""
	}
	artifact, err := s.store.FindPaxdArtifact(
		c,
		FindPaxdArtifactRequest{
			Product:         req.Product,
			Platform:        req.Platform,
			Version:         version,
			IncludeDisabled: true,
		},
	)
	if err != nil {
		return ""
	}
	return binaryQuality(artifact.Tags)
}
