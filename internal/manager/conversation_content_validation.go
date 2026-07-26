package manager

import (
	"errors"
	"strings"
)

func normalizeConversationContentBlocks(blocks []conversationContentBlock) error {
	for i := range blocks {
		blocks[i].Type = strings.ToLower(strings.TrimSpace(blocks[i].Type))
		blocks[i].Text = strings.TrimSpace(blocks[i].Text)
		blocks[i].AttachmentID = strings.TrimSpace(blocks[i].AttachmentID)
		switch blocks[i].Type {
		case "text":
			if blocks[i].Text == "" {
				return errors.New("text content block requires text")
			}
		case "attachment":
			if blocks[i].AttachmentID == "" {
				return errors.New("attachment content block requires attachment_id")
			}
		default:
			return errors.New("content block type must be text or attachment")
		}
	}
	return nil
}
