package domain

import "context"

// MessageToolSummary contains only fields needed by a collapsed tool row.
type MessageToolSummary struct {
	ToolCallID        string `json:"tool_call_id"`
	Title             string `json:"title"`
	Status            string `json:"status"`
	Kind              string `json:"kind,omitempty"`
	ContextCompaction bool   `json:"context_compaction,omitempty"`
	// Subagent marks an Agent or Task call of the main agent, and
	// ParentToolCallID marks a call made inside such a subagent.
	Subagent         *MessageToolSubagent `json:"subagent,omitempty"`
	ParentToolCallID string               `json:"parent_tool_call_id,omitempty"`
}

type MessageToolSubagent struct {
	Background  bool   `json:"background,omitempty"`
	Description string `json:"description,omitempty"`
	AgentType   string `json:"agent_type,omitempty"`
}

// MessageDetailPage offsets count Unicode code points, not bytes. A revision
// pins subsequent pages so a running tool cannot splice two output versions.
type MessageDetailPage struct {
	MessageID  string `json:"message_id"`
	Section    string `json:"section"`
	Format     string `json:"format"`
	Text       string `json:"text"`
	Revision   string `json:"revision"`
	NextOffset int    `json:"next_offset"`
	HasMore    bool   `json:"has_more"`
}

const MessageDetailPageSize = 16 * 1024

type MessageSummaryStore interface {
	ListMessageSummaryPage(
		ctx context.Context,
		agentID, sessionID string,
		afterSeq, beforeSeq int64,
		limit int,
	) (MessageHistoryPage, error)
	ListMessageSummaryParts(
		ctx context.Context,
		messageIDs []string,
	) (map[string][]MessagePart, error)
	GetMessageDetailPage(
		ctx context.Context,
		agentID, sessionID, messageID, section string,
		offset, limit int,
	) (MessageDetailPage, error)
}
