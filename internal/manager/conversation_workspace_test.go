package manager

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsSupportedSessionWorkspace(t *testing.T) {
	tests := []struct {
		name      string
		workspace string
		expected  bool
	}{
		{name: "given empty optional workspace", workspace: "", expected: true},
		{name: "given absolute workspace", workspace: "/workspace/project", expected: true},
		{name: "given home workspace", workspace: "~", expected: true},
		{name: "given workspace below home", workspace: "~/project", expected: true},
		{name: "given ordinary relative workspace", workspace: "project", expected: false},
		{name: "given current directory workspace", workspace: "./project", expected: false},
		{name: "given parent directory workspace", workspace: "../project", expected: false},
		{name: "given another users home", workspace: "~alice/project", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isSupportedSessionWorkspace(tt.workspace))
		})
	}
}

func TestConversationRequestValidationBDD(t *testing.T) {
	t.Parallel()

	t.Run("given resume content combinations then only resume-only input is accepted", func(t *testing.T) {
		require.NoError(t, validateConversationResumeContent(
			conversationRequest{},
			conversationResumeRequest{Requested: true},
		))
		assert.EqualError(t, validateConversationResumeContent(
			conversationRequest{Input: "hello"},
			conversationResumeRequest{Requested: true},
		), "content and resume are mutually exclusive")
		assert.EqualError(t, validateConversationResumeContent(
			conversationRequest{Content: []conversationContentBlock{{Type: "text"}}},
			conversationResumeRequest{Requested: true},
		), "content and resume are mutually exclusive")
	})

	t.Run("given session options then creation-only and supported values are enforced", func(t *testing.T) {
		tests := []struct {
			name    string
			req     conversationRequest
			message string
		}{
			{
				name:    "unsupported cwd",
				req:     conversationRequest{CWD: "relative"},
				message: "cwd must be an absolute path, ~, or start with ~/",
			},
			{
				name:    "continued session cwd",
				req:     conversationRequest{SessionID: "sess_1", CWD: "/repo"},
				message: "cwd can only be set when creating a session",
			},
			{
				name: "continued session approval mode",
				req: conversationRequest{
					SessionID:    "sess_1",
					ApprovalMode: "manual",
				},
				message: "approval_mode can only be set when creating a session; use session PATCH to change it",
			},
			{
				name: "continued session project",
				req: conversationRequest{
					SessionID:        "sess_1",
					PrimaryProjectID: "proj_1",
				},
				message: "project context can only be set when creating a session",
			},
			{
				name:    "unsupported approval mode",
				req:     conversationRequest{ApprovalMode: "always"},
				message: "approval_mode must be manual or auto_approve_all",
			},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				assert.EqualError(t, validateConversationSessionOptions(test.req), test.message)
			})
		}
		require.NoError(t, validateConversationSessionOptions(conversationRequest{
			ApprovalMode:     "manual",
			CWD:              "~/repo",
			PrimaryProjectID: "proj_1",
		}))
	})

	t.Run("given prompt state then resume and new session requirements are enforced", func(t *testing.T) {
		assert.EqualError(t, validateConversationPromptState(
			conversationRequest{},
			conversationResumeRequest{Requested: true},
		), "session_id is required for resume")
		assert.EqualError(t, validateConversationPromptState(
			conversationRequest{},
			conversationResumeRequest{},
		), "input or content is required")
		require.NoError(t, validateConversationPromptState(
			conversationRequest{Input: "hello"},
			conversationResumeRequest{},
		))
		require.NoError(t, validateConversationPromptState(
			conversationRequest{SessionID: "sess_1"},
			conversationResumeRequest{Requested: true},
		))
	})
}
