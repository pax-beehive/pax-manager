package storage

import (
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type memoryKnowledgeNode struct {
	OwnerUserID     string
	Type            string
	Status          string
	CurrentRevision int64
	OriginSourceID  string
}

func summaryOriginSource(summary domain.SummaryVersion) string {
	const prefix = "secretary:session:"
	if summary.SubjectType != "fragment" || !strings.HasPrefix(summary.SubjectID, prefix) {
		return ""
	}
	return strings.TrimPrefix(summary.SubjectID, prefix)
}

func summaryNodeMetadata(summary domain.SummaryVersion) domain.SummaryVersion {
	summary.NodeType = "summary"
	summary.NodeStatus = "active"
	summary.OriginSourceID, summary.OriginAgentID, summary.OriginSessionID, summary.OriginSessionTitle = "", "", "", ""
	if len(summary.Coverage)+len(summary.SummarySources) == 0 &&
		summary.Body == domain.SecretaryEmptySummaryBody {
		summary.NodeStatus = "empty"
	}
	return summary
}
