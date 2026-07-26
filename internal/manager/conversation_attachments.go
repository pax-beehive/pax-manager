package manager

import (
	"context"
	"net/http"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
)

type conversationContentBlock struct {
	Type         string `json:"type"`
	Text         string `json:"text,omitempty"`
	AttachmentID string `json:"attachment_id,omitempty"`
}

func (s *Service) resolveConversationPrompt(
	ctx context.Context,
	principal UserPrincipal,
	nodeID string,
	req conversationRequest,
) ([]map[string]any, error) {
	blocks := make([]conversationContentBlock, 0, len(req.Content)+1)
	if req.Input != "" {
		blocks = append(blocks, conversationContentBlock{Type: "text", Text: req.Input})
	}
	blocks = append(blocks, req.Content...)

	attachmentIDs := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "attachment" {
			attachmentIDs = append(attachmentIDs, block.AttachmentID)
		}
	}
	localized, err := s.ensureAttachmentsLocal(ctx, principal, nodeID, attachmentIDs)
	if err != nil {
		return nil, err
	}

	prompt := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			prompt = append(prompt, map[string]any{"type": "text", "text": block.Text})
		case "attachment":
			item, ok := localized[block.AttachmentID]
			if !ok {
				return nil, apperr.Error{
					Status:  http.StatusBadGateway,
					Message: "attachment was not localized",
				}
			}
			prompt = append(prompt, map[string]any{
				"type":     "resource_link",
				"uri":      item.State.LocalURI,
				"name":     item.Attachment.Filename,
				"mimeType": item.Attachment.ContentType,
			})
		}
	}
	return prompt, nil
}
