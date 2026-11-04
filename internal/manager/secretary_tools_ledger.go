package manager

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

func (g *secretaryToolGrant) record(result any) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return domain.ErrSecretaryFenced
	}
	var reads []userapi.WorkspaceMessageRead
	switch value := result.(type) {
	case userapi.WorkspaceMessageRead:
		reads = append(reads, value)
		reads = append(reads, value.Context...)
	case secretaryHistoryPage:
		reads = value.Messages
	case domain.SummaryVersion:
		return g.recordSummariesLocked([]domain.SummaryVersion{value})
	case domain.SecretarySummaryPage:
		return g.recordSummariesLocked(value.Items)
	case secretarySummaryToolPage:
		return g.recordSummaryPageLocked(value)
	}
	newCount, newBytes := 0, 0
	for _, read := range reads {
		if _, ok := g.reads[read.ReadReceipt]; !ok {
			newCount++
			newBytes += len(read.Text)
		}
	}
	if len(g.reads)+newCount > 512 || g.readBytes+newBytes > 4*1024*1024 {
		return secretaryToolLedgerLimit()
	}
	for _, read := range reads {
		read.Context = nil
		g.reads[read.ReadReceipt] = read
	}
	g.readBytes += newBytes
	return nil
}

func (g *secretaryToolGrant) recordSummaryPageLocked(page secretarySummaryToolPage) error {
	var exposed []domain.SummaryVersion
	headerBytes := 0
	for _, item := range page.Items {
		if item.RequiresRead {
			encoded, _ := json.Marshal(item)
			headerBytes += len(encoded)
		} else {
			exposed = append(exposed, item.SummaryVersion)
		}
	}
	if g.readBytes+headerBytes > 4*1024*1024 {
		return secretaryToolLedgerLimit()
	}
	g.readBytes += headerBytes
	if err := g.recordSummariesLocked(exposed); err != nil {
		g.readBytes -= headerBytes
		return err
	}
	return nil
}

func (g *secretaryToolGrant) recordSummariesLocked(summaries []domain.SummaryVersion) error {
	newCount, newBytes := 0, 0
	for _, summary := range summaries {
		ref := domain.SummaryRef{SummaryID: summary.SummaryID, Revision: summary.Revision}
		if !g.summaries[ref] {
			newCount++
			encoded, _ := json.Marshal(summary)
			newBytes += len(encoded)
		}
	}
	if len(g.summaries)+newCount > 100 || g.readBytes+newBytes > 4*1024*1024 {
		return secretaryToolLedgerLimit()
	}
	if g.summaryVersions == nil {
		g.summaryVersions = map[domain.SummaryRef]domain.SummaryVersion{}
	}
	for _, summary := range summaries {
		ref := domain.SummaryRef{SummaryID: summary.SummaryID, Revision: summary.Revision}
		g.summaries[ref] = true
		g.summaryVersions[ref] = summary
	}
	g.readBytes += newBytes
	return nil
}

func secretaryToolLedgerLimit() error {
	return apperr.Error{
		Status:  http.StatusBadRequest,
		Message: "secretary source read budget exceeded",
	}
}

func (s *Service) secretaryToolsReads(capability string) domain.SecretaryToolReads {
	result := domain.SecretaryToolReads{}
	value, ok := s.secretaryTools.Load(capability)
	if !ok {
		return result
	}
	grant := value.(*secretaryToolGrant)
	grant.mu.Lock()
	defer grant.mu.Unlock()
	for _, read := range grant.reads {
		result.Messages = append(result.Messages, domain.SecretaryReadMessage{
			Ref: domain.MessageVersionRef{
				MessageID:               read.MessageID,
				ContentHash:             read.Ref.ContentHash,
				CanonicalizationVersion: read.Ref.CanonicalizationVersion,
				ReadReceipt:             read.ReadReceipt,
			},
			Text: read.Text, View: read.View, ReadReceipt: read.ReadReceipt,
		})
	}
	for ref := range grant.summaries {
		result.Summaries = append(result.Summaries, ref)
		if summary, ok := grant.summaryVersions[ref]; ok {
			result.SummaryVersions = append(result.SummaryVersions, summary)
		}
	}
	sort.Slice(
		result.Messages,
		func(i, j int) bool { return result.Messages[i].ReadReceipt < result.Messages[j].ReadReceipt },
	)
	sort.Slice(result.Summaries, func(i, j int) bool {
		if result.Summaries[i].SummaryID == result.Summaries[j].SummaryID {
			return result.Summaries[i].Revision < result.Summaries[j].Revision
		}
		return result.Summaries[i].SummaryID < result.Summaries[j].SummaryID
	})
	sort.Slice(result.SummaryVersions, func(i, j int) bool {
		if result.SummaryVersions[i].SummaryID == result.SummaryVersions[j].SummaryID {
			return result.SummaryVersions[i].Revision < result.SummaryVersions[j].Revision
		}
		return result.SummaryVersions[i].SummaryID < result.SummaryVersions[j].SummaryID
	})
	return result
}
