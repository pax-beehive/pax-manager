package storage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestMemorySecretAndArtifactLifecycle(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, node, agent := seedMemoryNodeAgent(t, ctx, store, now)
	principal := UserPrincipal{User: owner}

	secret, version, err := store.CreateSecret(
		ctx,
		principal,
		CreateSecretRequest{
			Name:        "OPENAI_API_KEY",
			Kind:        "token",
			Description: "Used by local agent tests.",
			Metadata:    json.RawMessage(`{"scope":"agent"}`),
		},
		SecretVersion{Ciphertext: []byte("cipher-v1"), Nonce: []byte("nonce-v1"), KeyID: "key_1"},
	)
	require.NoError(t, err)
	require.Equal(t, owner.UserID, secret.OwnerUserID)
	require.Equal(t, int64(1), version.VersionNumber)

	listed, err := store.ListSecrets(ctx, principal)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	gotSecret, err := store.GetSecret(ctx, principal, secret.SecretID)
	require.NoError(t, err)
	require.Equal(t, secret.SecretID, gotSecret.SecretID)

	resolvedSecret, resolvedVersion, err := store.GetSecretVersionForNode(
		ctx,
		node,
		agent.AgentID,
		secret.SecretID,
		"latest",
	)
	require.NoError(t, err)
	require.Equal(t, secret.SecretID, resolvedSecret.SecretID)
	require.Equal(t, version.VersionID, resolvedVersion.VersionID)

	_, _, err = store.GetSecretVersionForNode(ctx, node, "missing_agent", secret.SecretID, "latest")
	require.ErrorIs(t, err, ErrNotFound)
	_, _, err = store.CreateSecretVersion(ctx, node, agent.AgentID, WriteSecretVersionRequest{
		SecretID:                 secret.SecretID,
		MakeCurrent:              true,
		ExpectedCurrentVersionID: "wrong_version",
	}, SecretVersion{Ciphertext: []byte("cipher-v2")})
	require.ErrorIs(t, err, ErrConflict)

	now = now.Add(time.Minute)
	second, current, err := store.CreateSecretVersion(
		ctx,
		node,
		agent.AgentID,
		WriteSecretVersionRequest{
			SecretID:                 secret.SecretID,
			MakeCurrent:              true,
			ExpectedCurrentVersionID: version.VersionID,
			IdempotencyKey:           "idem_1",
		},
		SecretVersion{Ciphertext: []byte("cipher-v2"), Nonce: []byte("nonce-v2"), KeyID: "key_1"},
	)
	require.NoError(t, err)
	require.True(t, current)
	require.Equal(t, int64(2), second.VersionNumber)

	again, current, err := store.CreateSecretVersion(
		ctx,
		node,
		agent.AgentID,
		WriteSecretVersionRequest{SecretID: secret.SecretID, IdempotencyKey: "idem_1"},
		SecretVersion{Ciphertext: []byte("ignored")},
	)
	require.NoError(t, err)
	require.True(t, current)
	require.Equal(t, second.VersionID, again.VersionID)

	artifact, err := store.CreatePaxdArtifact(ctx, CreatePaxdArtifactRequest{
		Product:     "paxd",
		Platform:    "darwin-arm64",
		Tags:        []string{"stable", "latest"},
		Version:     "v1.2.3",
		Bucket:      "artifacts",
		Object:      "paxd/darwin-arm64",
		Generation:  7,
		SHA256:      "abc123",
		SizeBytes:   42,
		ContentType: "application/octet-stream",
	}, "tester")
	require.NoError(t, err)
	found, err := store.FindPaxdArtifact(ctx, FindPaxdArtifactRequest{
		Product:  "paxd",
		Platform: "darwin-arm64",
		Tags:     []string{"stable"},
	})
	require.NoError(t, err)
	require.Equal(t, artifact.ArtifactID, found.ArtifactID)
	_, err = store.FindPaxdArtifact(ctx, FindPaxdArtifactRequest{
		Product:  "paxd",
		Platform: "linux-amd64",
	})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryAgentConversationGivenSameOwnerAgentsWhenStartedThenCreatesBindingsSessionsAndHistory(
	t *testing.T,
) {
	ctx := context.Background()
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, node, sourceAgent := seedMemoryNodeAgent(t, ctx, store, now)
	targetAgent, _, err := store.CreateNodeAgent(
		ctx,
		UserPrincipal{User: owner},
		CreateAgentRequest{
			NodeID:    node.NodeID,
			Name:      "reviewer",
			AgentType: "codex",
		},
	)
	require.NoError(t, err)
	seedMemoryRepresentativeAgent(
		store,
		"rep_source",
		"profile_source",
		sourceAgent.AgentID,
		owner.UserID,
		now,
	)
	seedMemoryRepresentativeAgent(
		store,
		"rep_target",
		"profile_target",
		targetAgent.AgentID,
		owner.UserID,
		now,
	)

	start, err := store.StartAgentConversation(ctx, node, domain.StartAgentConversationRequest{
		FromRuntimeAgentID:      sourceAgent.AgentID,
		ToRepresentativeAgentID: "rep_target",
		Input:                   "Please review this plan.",
		MaxTurns:                3,
	})
	require.NoError(t, err)
	require.Equal(t, domain.ConversationTypeAgentThread, start.Conversation.ConversationType)
	require.Equal(t, "rep_source", start.SourceBinding.RepresentativeAgentID)
	require.Equal(t, "rep_target", start.TargetBinding.RepresentativeAgentID)
	require.Empty(t, start.Invocation.ParentInvocationID)
	require.Equal(t, "rep_source", start.Invocation.SourceRepresentativeAgentID)
	require.Equal(t, sourceAgent.AgentID, start.Invocation.SourceRuntimeAgentID)
	require.Equal(t, start.SourceSession.SessionID, start.Invocation.SourceSessionID)
	require.Equal(t, "rep_target", start.Invocation.TargetRepresentativeAgentID)
	require.Equal(t, targetAgent.AgentID, start.Invocation.TargetRuntimeAgentID)
	require.Equal(t, start.TargetSession.SessionID, start.Invocation.TargetSessionID)
	require.Equal(t, 3, start.Invocation.MaxTurns)
	require.Equal(t, 3, start.Invocation.RemainingTurns)
	require.Equal(t, start.Conversation.ConversationID, start.SourceSession.ConversationID)
	require.Equal(t, start.Conversation.ConversationID, start.TargetSession.ConversationID)
	require.Equal(t, "rep_source", start.SourceSession.RepresentativeAgentID)
	require.Equal(t, "rep_target", start.TargetSession.RepresentativeAgentID)
	require.Equal(t, domain.MessageDirectionUserToAgent, start.PromptMessage.Direction)
	require.Equal(t, domain.MessageTypePaxUser, start.PromptMessage.MessageType)

	messages, err := store.ListConversationMessages(
		ctx,
		UserPrincipal{User: owner},
		start.Conversation.ConversationID,
		10,
	)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	require.Equal(t, "Please review this plan.", messages[0].Parts[0].Text)
	require.Equal(t, domain.MessageTypePaxUser, messages[0].MessageType)
	require.Equal(t, domain.MessageTypePaxInvocation, messages[1].MessageType)
	require.Equal(t, messages[0].MessageID, messages[1].ParentMessageID)
	require.Contains(t, string(messages[1].RawJSON), messages[0].MessageID)
	normal := domain.NormalTranscriptMessages(messages)
	require.Len(t, normal, 1)
	require.Equal(t, domain.MessageTypePaxInvocation, normal[0].MessageType)

	require.NoError(
		t,
		store.CompleteAgentConversationInvocation(ctx, start.Invocation.InvocationID),
	)
	require.Equal(
		t,
		domain.ConversationAgentInvocationStatusCompleted,
		store.conversationAgentInvocations[start.Invocation.InvocationID].Status,
	)
}

func TestMemoryAgentConversationGivenAgentTargetWhenDeliveredThenEnsuresRepresentativesIdempotently(
	t *testing.T,
) {
	ctx := context.Background()
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, node, sourceAgent := seedMemoryNodeAgent(t, ctx, store, now)
	targetAgent, _, err := store.CreateNodeAgent(
		ctx,
		UserPrincipal{User: owner},
		CreateAgentRequest{NodeID: node.NodeID, Name: "reviewer", AgentType: "codex"},
	)
	require.NoError(t, err)

	req := domain.DeliverConversationRequest{
		Source: domain.ConversationDeliverySource{
			AgentID:   sourceAgent.AgentID,
			SessionID: "sess_src",
		},
		Target: domain.ConversationDeliveryTarget{
			Kind:    domain.ConversationDeliveryTargetAgent,
			AgentID: targetAgent.AgentID,
		},
		Instruction: "Please review this plan.",
	}
	delivery, err := store.DeliverAgentConversation(ctx, node, req)
	require.NoError(t, err)

	sourceProfile := deterministicAgentProfileID(sourceAgent.AgentID, owner.UserID)
	wantSourceRep := deterministicRepresentativeAgentID(sourceAgent.AgentID, sourceProfile, "user", owner.UserID)
	targetProfile := deterministicAgentProfileID(targetAgent.AgentID, owner.UserID)
	wantTargetRep := deterministicRepresentativeAgentID(targetAgent.AgentID, targetProfile, "user", owner.UserID)

	require.Equal(t, wantSourceRep, delivery.Invocation.SourceRepresentativeAgentID)
	require.Equal(t, wantTargetRep, delivery.Invocation.TargetRepresentativeAgentID)
	require.Equal(t, sourceAgent.AgentID, delivery.Invocation.SourceRuntimeAgentID)
	require.Equal(t, targetAgent.AgentID, delivery.Invocation.TargetRuntimeAgentID)
	require.NotEmpty(t, delivery.ReceiptToken)
	require.Contains(t, store.representativeAgents, wantSourceRep)
	require.Contains(t, store.representativeAgents, wantTargetRep)

	// The canonical representative uses no approval policy, so self/same-owner
	// delivery is friction-free.
	require.Empty(t, store.representativeAgents[wantTargetRep].ApprovalPolicyID)

	// A second delivery reuses the same representatives and profiles.
	repCount := len(store.representativeAgents)
	profileCount := len(store.agentProfiles)
	_, err = store.DeliverAgentConversation(ctx, node, req)
	require.NoError(t, err)
	require.Equal(t, repCount, len(store.representativeAgents))
	require.Equal(t, profileCount, len(store.agentProfiles))
}

func TestMemoryAgentConversationGivenSelfAgentTargetWhenDeliveredThenSucceeds(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	_, node, agent := seedMemoryNodeAgent(t, ctx, store, now)

	delivery, err := store.DeliverAgentConversation(ctx, node, domain.DeliverConversationRequest{
		Source: domain.ConversationDeliverySource{AgentID: agent.AgentID, SessionID: "sess_src"},
		Target: domain.ConversationDeliveryTarget{
			Kind:    domain.ConversationDeliveryTargetAgent,
			AgentID: agent.AgentID,
		},
		Instruction: "Handing off to a fresh session.",
	})
	require.NoError(t, err)
	require.Equal(t, agent.AgentID, delivery.Invocation.SourceRuntimeAgentID)
	require.Equal(t, agent.AgentID, delivery.Invocation.TargetRuntimeAgentID)
	require.NotEmpty(t, delivery.ReceiptToken)
}

func TestMemoryAgentConversationGivenAgentTargetOfAnotherOwnerThenReturnsUnauthorized(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	_, node, sourceAgent := seedMemoryNodeAgent(t, ctx, store, now)
	other, err := store.EnsureUser(ctx, "other@example.com", "Other", "user")
	require.NoError(t, err)
	otherNode, err := store.RegisterNode(
		ctx, other,
		domain.RegisterNodeRequest{Name: "other-node", Hostname: "other", OS: "linux"}, "hash_o")
	require.NoError(t, err)
	foreignAgent, _, err := store.CreateNodeAgent(
		ctx, UserPrincipal{User: other},
		CreateAgentRequest{NodeID: otherNode.NodeID, Name: "foreign", AgentType: "codex"})
	require.NoError(t, err)

	_, err = store.DeliverAgentConversation(ctx, node, domain.DeliverConversationRequest{
		Source: domain.ConversationDeliverySource{AgentID: sourceAgent.AgentID, SessionID: "sess_src"},
		Target: domain.ConversationDeliveryTarget{
			Kind:    domain.ConversationDeliveryTargetAgent,
			AgentID: foreignAgent.AgentID,
		},
		Instruction: "hi",
	})
	require.ErrorIs(t, err, ErrUnauthorized)
}

func TestMemoryAgentConversationDisplayGivenToolCallThenCreatesPendingInvocation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, node, agent := seedMemoryNodeAgent(t, ctx, store, now)
	conversationID := "conv_tool_group"
	sessionID := "sess_tool_group"
	toolCall := Message{
		MessageID:      "msg_tool_call",
		ConversationID: conversationID,
		OwnerUserID:    owner.UserID,
		NodeID:         node.NodeID,
		AgentID:        agent.AgentID,
		SessionID:      sessionID,
		Source:         domain.MessageSourceACPTunnel,
		Direction:      domain.MessageDirectionAgentToUser,
		Role:           "assistant",
		Status:         "received",
		MessageType:    "tool_call",
		RawJSON: json.RawMessage(
			`{"method":"session/update","params":{"sessionId":"sess_tool_group","update":{"sessionUpdate":"tool_call","toolCallId":"tool_1"}}}`,
		),
	}
	require.NoError(t, store.UpsertMessage(ctx, &toolCall))
	prompt, err := store.createConversationPromptMessageLocked(
		conversationID,
		agent,
		sessionID,
		"Ask another agent.",
		sourceInvocationPromptDisplay(
			domain.ConversationAgentInvocation{InvocationID: "inv_1"},
			"rep_source",
			agent.AgentID,
			sessionID,
			"rep_target",
			"agent_target",
			"sess_target",
			"inquiry",
			"source",
			"Asked target for input.",
			"Ask another agent.",
		),
		now,
	)
	require.NoError(t, err)

	messages, err := store.ListMessages(ctx, agent.AgentID, sessionID, 20)
	require.NoError(t, err)
	require.NotContains(t, messageTypes(messages), domain.MessageTypePaxInvocation)
	pending := requireMessageType(t, messages, domain.MessageTypePaxInvocationPending)
	require.Equal(t, "msg_tool_call", pending.ParentMessageID)
	require.Contains(t, string(pending.RawJSON), `"tool_call_id":"tool_1"`)
	require.Contains(t, string(pending.RawJSON), `"prompt_message_id":"`+prompt.MessageID+`"`)
	require.Contains(
		t,
		string(pending.RawJSON),
		`"replaces_message_ids":["msg_tool_call","`+prompt.MessageID+`"]`,
	)
	normal := domain.NormalTranscriptMessages([]domain.MessageWithParts{
		{Message: messages[0]},
		{Message: messages[1]},
		{Message: pending},
	})
	require.Len(t, normal, 1)
	require.Equal(t, domain.MessageTypePaxInvocationPending, normal[0].MessageType)
	require.Equal(t, pending.MessageID, normal[0].MessageID)
}

func TestMemoryAgentConversationReplyGivenMultipleSessionsThenUsesSourceSessionInvocation(
	t *testing.T,
) {
	ctx := context.Background()
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, node, sourceAgent := seedMemoryNodeAgent(t, ctx, store, now)
	targetAgent, _, err := store.CreateNodeAgent(
		ctx,
		UserPrincipal{User: owner},
		CreateAgentRequest{
			NodeID:    node.NodeID,
			Name:      "target",
			AgentType: "codex",
		},
	)
	require.NoError(t, err)
	seedMemoryRepresentativeAgent(
		store,
		"rep_source",
		"profile_source",
		sourceAgent.AgentID,
		owner.UserID,
		now,
	)
	seedMemoryRepresentativeAgent(
		store,
		"rep_target",
		"profile_target",
		targetAgent.AgentID,
		owner.UserID,
		now,
	)
	stale, err := store.createConversationInvocationLocked(
		"conv_stale",
		"",
		"rep_source",
		sourceAgent.AgentID,
		"sess_A",
		"rep_target",
		targetAgent.AgentID,
		"sess_C_stale",
		owner.UserID,
		1,
		"",
		now,
	)
	require.NoError(t, err)
	current, err := store.createConversationInvocationLocked(
		"conv_current",
		"",
		"rep_source",
		sourceAgent.AgentID,
		"sess_B",
		"rep_target",
		targetAgent.AgentID,
		"sess_C",
		owner.UserID,
		1,
		"",
		now,
	)
	require.NoError(t, err)

	delivery, err := store.DeliverAgentConversation(ctx, node, domain.DeliverConversationRequest{
		Source: domain.ConversationDeliverySource{
			AgentID:               targetAgent.AgentID,
			RepresentativeAgentID: "rep_target",
			SessionID:             "sess_C",
		},
		Target: domain.ConversationDeliveryTarget{
			Kind: domain.ConversationDeliveryTargetActiveInvocation,
		},
		Instruction: "reply from C",
	})

	require.NoError(t, err)
	require.Equal(t, current.InvocationID, delivery.Invocation.InvocationID)
	require.NotEqual(t, stale.InvocationID, delivery.Invocation.InvocationID)
	require.Equal(t, "sess_C", delivery.SourceSession.SessionID)
	require.Equal(t, "sess_B", delivery.TargetSession.SessionID)
}

func TestMemoryAgentConversationReplyGivenMismatchedSourceSessionThenDoesNotFallback(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, node, sourceAgent := seedMemoryNodeAgent(t, ctx, store, now)
	targetAgent, _, err := store.CreateNodeAgent(
		ctx,
		UserPrincipal{User: owner},
		CreateAgentRequest{
			NodeID:    node.NodeID,
			Name:      "target",
			AgentType: "codex",
		},
	)
	require.NoError(t, err)
	seedMemoryRepresentativeAgent(
		store,
		"rep_source",
		"profile_source",
		sourceAgent.AgentID,
		owner.UserID,
		now,
	)
	seedMemoryRepresentativeAgent(
		store,
		"rep_target",
		"profile_target",
		targetAgent.AgentID,
		owner.UserID,
		now,
	)
	_, err = store.createConversationInvocationLocked(
		"conv_stale",
		"",
		"rep_source",
		sourceAgent.AgentID,
		"sess_A",
		"rep_target",
		targetAgent.AgentID,
		"sess_C_stale",
		owner.UserID,
		1,
		"",
		now,
	)
	require.NoError(t, err)

	_, err = store.DeliverAgentConversation(ctx, node, domain.DeliverConversationRequest{
		Source: domain.ConversationDeliverySource{
			AgentID:               targetAgent.AgentID,
			RepresentativeAgentID: "rep_target",
			SessionID:             "sess_unknown",
		},
		Target: domain.ConversationDeliveryTarget{
			Kind: domain.ConversationDeliveryTargetActiveInvocation,
		},
		Instruction: "must not go to A",
	})

	require.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryRepresentativeAgentGivenOwnedRuntimeAgentWhenUpsertedThenItCanBeListed(
	t *testing.T,
) {
	ctx := context.Background()
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, _, agent := seedMemoryNodeAgent(t, ctx, store, now)
	principal := UserPrincipal{User: owner}

	rep, profile, err := store.UpsertRepresentativeAgent(
		ctx,
		principal,
		domain.UpsertRepresentativeAgentRequest{
			RuntimeAgentID: agent.AgentID,
			DisplayName:    "Primary reviewer",
			Description:    "Reviews inquiries from other agents.",
		},
	)

	require.NoError(t, err)
	require.Equal(t, agent.AgentID, rep.RuntimeAgentID)
	require.Equal(t, owner.UserID, rep.RepresentsID)
	require.Equal(t, "Primary reviewer", profile.DisplayName)
	require.Equal(t, profile.ProfileID, rep.ProfileID)

	listed, err := store.ListRepresentativeAgents(ctx, principal, agent.AgentID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, rep.RepresentativeAgentID, listed[0].RepresentativeAgentID)
}

func TestMemoryAgentConversationGivenUserOwnedSourceWhenStartedForUserThenCreatesHistory(
	t *testing.T,
) {
	ctx := context.Background()
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, node, sourceAgent := seedMemoryNodeAgent(t, ctx, store, now)
	targetAgent, _, err := store.CreateNodeAgent(
		ctx,
		UserPrincipal{User: owner},
		CreateAgentRequest{
			NodeID:    node.NodeID,
			Name:      "target",
			AgentType: "codex",
		},
	)
	require.NoError(t, err)
	principal := UserPrincipal{User: owner}
	sourceRep, _, err := store.UpsertRepresentativeAgent(
		ctx,
		principal,
		domain.UpsertRepresentativeAgentRequest{
			RuntimeAgentID: sourceAgent.AgentID,
			DisplayName:    "Source",
		},
	)
	require.NoError(t, err)
	targetRep, _, err := store.UpsertRepresentativeAgent(
		ctx,
		principal,
		domain.UpsertRepresentativeAgentRequest{
			RuntimeAgentID: targetAgent.AgentID,
			DisplayName:    "Target",
		},
	)
	require.NoError(t, err)

	start, err := store.StartAgentConversationForUser(
		ctx,
		principal,
		domain.StartAgentConversationRequest{
			FromRuntimeAgentID:        sourceAgent.AgentID,
			FromRepresentativeAgentID: sourceRep.RepresentativeAgentID,
			ToRepresentativeAgentID:   targetRep.RepresentativeAgentID,
			Input:                     "Can you answer this inquiry?",
			MaxTurns:                  2,
		},
	)

	require.NoError(t, err)
	require.Equal(
		t,
		sourceRep.RepresentativeAgentID,
		start.SourceRepresentative.RepresentativeAgentID,
	)
	require.Equal(
		t,
		targetRep.RepresentativeAgentID,
		start.TargetRepresentative.RepresentativeAgentID,
	)
	require.Equal(t, 2, start.Invocation.MaxTurns)
	messages, err := store.ListConversationMessages(
		ctx,
		principal,
		start.Conversation.ConversationID,
		10,
	)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	require.Equal(t, "Can you answer this inquiry?", messages[0].Parts[0].Text)
	normal := domain.NormalTranscriptMessages(messages)
	require.Len(t, normal, 1)
	require.Equal(t, domain.MessageTypePaxInvocation, normal[0].MessageType)
}

func TestMemoryAgentConversationGivenSharedTeamUsersWhenStartedThenBothUsersCanReadHistory(
	t *testing.T,
) {
	ctx := context.Background()
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, node, sourceAgent := seedMemoryNodeAgent(t, ctx, store, now)
	targetOwner, err := store.EnsureUser(ctx, "target@example.com", "Target", "user")
	require.NoError(t, err)
	targetNode, err := store.RegisterNode(ctx, targetOwner, RegisterNodeRequest{
		Name:     "Target Mac",
		Hostname: "target",
		OS:       "darwin",
	}, "target_node_hash")
	require.NoError(t, err)
	targetAgent, _, err := store.CreateNodeAgent(
		ctx,
		UserPrincipal{User: targetOwner},
		CreateAgentRequest{NodeID: targetNode.NodeID, Name: "target", AgentType: "codex"},
	)
	require.NoError(t, err)
	seedTeam(t, ctx, store, "team_shared", owner, map[string]User{
		domain.TeamRoleMember: targetOwner,
	}, now)
	seedMemoryRepresentativeAgent(
		store,
		"rep_source",
		"profile_source",
		sourceAgent.AgentID,
		owner.UserID,
		now,
	)
	seedMemoryRepresentativeAgent(
		store,
		"rep_target",
		"profile_target",
		targetAgent.AgentID,
		targetOwner.UserID,
		now,
	)

	start, err := store.StartAgentConversation(ctx, node, domain.StartAgentConversationRequest{
		FromRuntimeAgentID:      sourceAgent.AgentID,
		ToRepresentativeAgentID: "rep_target",
		Input:                   "Can you inspect this?",
	})

	require.NoError(t, err)
	require.Equal(t, 1, start.Invocation.MaxTurns)
	require.Equal(
		t,
		domain.ConversationMemberRoleMember,
		store.conversationMembers[start.Conversation.ConversationID+":"+targetOwner.UserID].Role,
	)
	targetMessages, err := store.ListConversationMessages(
		ctx,
		UserPrincipal{User: targetOwner},
		start.Conversation.ConversationID,
		10,
	)
	require.NoError(t, err)
	require.Len(t, targetMessages, 2)
	require.Len(t, domain.NormalTranscriptMessages(targetMessages), 1)
}

func TestMemoryAgentConversationGivenUnrelatedUsersWhenStartedThenReturnsUnauthorized(
	t *testing.T,
) {
	ctx := context.Background()
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, node, sourceAgent := seedMemoryNodeAgent(t, ctx, store, now)
	targetOwner, err := store.EnsureUser(ctx, "target@example.com", "Target", "user")
	require.NoError(t, err)
	targetNode, err := store.RegisterNode(ctx, targetOwner, RegisterNodeRequest{
		Name:     "Target Mac",
		Hostname: "target",
		OS:       "darwin",
	}, "target_node_hash")
	require.NoError(t, err)
	targetAgent, _, err := store.CreateNodeAgent(
		ctx,
		UserPrincipal{User: targetOwner},
		CreateAgentRequest{NodeID: targetNode.NodeID, Name: "target", AgentType: "codex"},
	)
	require.NoError(t, err)
	seedMemoryRepresentativeAgent(
		store,
		"rep_source",
		"profile_source",
		sourceAgent.AgentID,
		owner.UserID,
		now,
	)
	seedMemoryRepresentativeAgent(
		store,
		"rep_target",
		"profile_target",
		targetAgent.AgentID,
		targetOwner.UserID,
		now,
	)

	_, err = store.StartAgentConversation(ctx, node, domain.StartAgentConversationRequest{
		FromRuntimeAgentID:      sourceAgent.AgentID,
		ToRepresentativeAgentID: "rep_target",
		Input:                   "Can you inspect this?",
	})

	require.ErrorIs(t, err, ErrUnauthorized)
}

func TestMemoryAgentConversationGivenNonMemberWhenListingHistoryThenReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, node, sourceAgent := seedMemoryNodeAgent(t, ctx, store, now)
	outsider, err := store.EnsureUser(ctx, "outsider@example.com", "Outsider", "user")
	require.NoError(t, err)
	targetAgent, _, err := store.CreateNodeAgent(
		ctx,
		UserPrincipal{User: owner},
		CreateAgentRequest{NodeID: node.NodeID, Name: "target", AgentType: "codex"},
	)
	require.NoError(t, err)
	seedMemoryRepresentativeAgent(
		store,
		"rep_source",
		"profile_source",
		sourceAgent.AgentID,
		owner.UserID,
		now,
	)
	seedMemoryRepresentativeAgent(
		store,
		"rep_target",
		"profile_target",
		targetAgent.AgentID,
		owner.UserID,
		now,
	)
	start, err := store.StartAgentConversation(ctx, node, domain.StartAgentConversationRequest{
		FromRuntimeAgentID:      sourceAgent.AgentID,
		ToRepresentativeAgentID: "rep_target",
		Input:                   "Keep this private.",
	})
	require.NoError(t, err)

	_, err = store.ListConversationMessages(
		ctx,
		UserPrincipal{User: outsider},
		start.Conversation.ConversationID,
		10,
	)

	require.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryAgentConversationGivenMissingInvocationWhenCompletingThenReturnsNotFound(
	t *testing.T,
) {
	store := NewMemoryStore(
		func() time.Time { return time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC) },
	)

	err := store.CompleteAgentConversationInvocation(context.Background(), "missing_invocation")

	require.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryNodeRegistrationLifecycle(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, err := store.EnsureUser(ctx, "owner@example.com", "Owner", "user")
	require.NoError(t, err)
	session := NodeRegistrationSession{
		RegistrationID: "reg_1",
		PairCode:       "PAIR-1",
		PollTokenHash:  "poll_hash",
		Status:         domain.NodeRegistrationStatusPending,
		Request: RegisterNodeRequest{
			Name:     "Studio Mac",
			Hostname: "studio",
			OS:       "",
			Metadata: json.RawMessage(`{"region":"local"}`),
		},
		ExpiresAt: now.Add(time.Hour),
		CreatedAt: now,
	}
	require.NoError(t, store.CreateNodeRegistrationSession(ctx, session))
	require.ErrorIs(t, store.CreateNodeRegistrationSession(ctx, session), ErrConflict)

	got, err := store.GetNodeRegistrationSession(ctx, "PAIR-1")
	require.NoError(t, err)
	require.Equal(t, domain.NodeRegistrationStatusPending, got.Status)
	polled, err := store.PollNodeRegistrationSession(ctx, "reg_1", "poll_hash")
	require.NoError(t, err)
	require.Equal(t, "PAIR-1", polled.PairCode)
	approved, err := store.ApproveNodeRegistrationSession(
		ctx,
		UserPrincipal{User: owner},
		"PAIR-1",
	)
	require.NoError(t, err)
	require.Equal(t, domain.NodeRegistrationStatusApproved, approved.Status)

	node, err := store.ConsumeNodeRegistrationSession(ctx, "reg_1", "poll_hash", "node_hash")
	require.NoError(t, err)
	require.Equal(t, owner.UserID, node.OwnerUserID)
	require.Equal(t, "unknown", node.OS)
	authenticated, err := store.AuthenticateNode(ctx, "node_hash")
	require.NoError(t, err)
	require.Equal(t, node.NodeID, authenticated.NodeID)

	require.NoError(t, store.DeleteStaleNodeRegistrationSessions(ctx, now.Add(2*time.Hour)))
	_, err = store.GetNodeRegistrationSession(ctx, "PAIR-1")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryPaxlDeviceLoginLifecycle(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, err := store.EnsureUser(ctx, "owner@example.com", "Owner", "user")
	require.NoError(t, err)
	session := PaxlDeviceLoginSession{
		LoginID:       "login_1",
		UserCode:      "ABCD-EFGH",
		PollTokenHash: "poll_hash",
		Status:        domain.PaxlDeviceLoginStatusPending,
		ClientName:    "Studio Mac",
		ExpiresAt:     now.Add(time.Hour),
		CreatedAt:     now,
	}
	require.NoError(t, store.CreatePaxlDeviceLoginSession(ctx, session))
	require.ErrorIs(t, store.CreatePaxlDeviceLoginSession(ctx, session), ErrConflict)

	apiKey := UserAPIKey{KeyID: "key_1", OwnerUserID: owner.UserID, Prefix: "paxu_123"}
	approved, err := store.ApprovePaxlDeviceLoginSession(
		ctx,
		UserPrincipal{User: owner},
		"ABCD-EFGH",
		apiKey,
		"paxu_raw",
	)
	require.NoError(t, err)
	require.Equal(t, domain.PaxlDeviceLoginStatusApproved, approved.Status)
	require.NotEmpty(t, approved.NodeID)
	polled, err := store.PollPaxlDeviceLoginSession(ctx, "login_1", "poll_hash")
	require.NoError(t, err)
	require.Equal(t, approved.NodeID, polled.NodeID)
	consumed, err := store.ConsumePaxlDeviceLoginSession(ctx, "login_1", "poll_hash")
	require.NoError(t, err)
	require.Equal(t, "paxu_raw", consumed.APIKey)
	_, err = store.ConsumePaxlDeviceLoginSession(ctx, "login_1", "poll_hash")
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, store.DeleteStalePaxlDeviceLoginSessions(ctx, now.Add(2*time.Hour)))
}

func TestMemoryNodeAgentMailboxAndApprovalLifecycle(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, node, agent := seedMemoryNodeAgent(t, ctx, store, now)
	principal := UserPrincipal{User: owner}
	session, err := store.CreateNodeAgentSession(ctx, principal, CreateSessionRequest{
		NodeID:    node.NodeID,
		AgentID:   agent.AgentID,
		SessionID: "sess_manager",
		NativeID:  "native_session",
		Source:    "test",
	})
	require.NoError(t, err)

	now = now.Add(time.Minute)
	require.NoError(t, store.UpsertNodeStatus(ctx, node, NodeStatusReport{
		Hostname: "studio-updated",
		Agents: []AgentStatusInput{{
			AgentID: agent.AgentID,
			Name:    "codex-updated",
			Status:  "online",
			Sessions: []SessionStatusInput{{
				SessionID:    "native_session",
				NativeID:     "native_session",
				SessionName:  "Build feature",
				Status:       "running",
				MessageCount: 3,
			}},
		}},
		Metadata: json.RawMessage(`{"load":1}`),
	}))
	updatedNode, err := store.GetNode(ctx, principal, node.NodeID)
	require.NoError(t, err)
	require.Equal(t, "studio-updated", updatedNode.Hostname)
	nodes, err := store.ListNodes(ctx, principal)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	renamedNode, err := store.UpdateNode(ctx, principal, UpdateNodeRequest{
		NodeID:       node.NodeID,
		Name:         "Renamed Studio",
		Description:  "Updated by unit test",
		UserMetadata: json.RawMessage(`{"purpose":"coverage"}`),
	})
	require.NoError(t, err)
	require.Equal(t, "Renamed Studio", renamedNode.Name)
	require.Equal(t, `{"purpose":"coverage"}`, string(renamedNode.UserMetadata))
	gotAgent, err := store.GetNodeAgent(ctx, node.NodeID, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, agent.AgentID, gotAgent.AgentID)
	nodeAgents, err := store.ListNodeAgents(ctx, principal, node.NodeID)
	require.NoError(t, err)
	require.Len(t, nodeAgents, 1)
	updatedAgent, err := store.UpdateNodeAgent(ctx, principal, UpdateAgentProfileRequest{
		NodeID:       node.NodeID,
		AgentID:      agent.AgentID,
		Name:         "codex profile",
		Description:  "profile update",
		Card:         json.RawMessage(`{"skills":["go"]}`),
		UserMetadata: json.RawMessage(`{"favorite":true}`),
	})
	require.NoError(t, err)
	require.Equal(t, "codex profile", updatedAgent.Name)
	authAgent, err := store.AuthenticateAgent(ctx, "")
	require.ErrorIs(t, err, ErrUnauthorized)
	require.Empty(t, authAgent.AgentID)
	require.NoError(t, store.UpdateOffset(ctx, agent.AgentID, 10))
	require.NoError(t, store.UpdateOffset(ctx, agent.AgentID, 5))
	require.Equal(t, int64(10), store.offsets[agent.AgentID])
	require.NoError(t, store.UpdateNodeOffset(ctx, node.NodeID, 20))
	require.NoError(t, store.UpdateNodeOffset(ctx, node.NodeID, 15))
	require.Equal(t, int64(20), store.offsets[node.NodeID])

	require.NoError(t, store.UpdateSessionRuntimeState(ctx, SessionRuntimeState{
		AgentID:   agent.AgentID,
		SessionID: session.SessionID,
		Lifecycle: domain.RuntimeLifecycleWaitingApproval,
		ActiveToolCalls: []domain.RuntimeToolCall{{
			ToolCallID: "tool_1",
			Title:      "Run tests",
		}},
	}))
	gotSession, err := store.GetSession(ctx, principal, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, domain.RuntimeLifecycleWaitingApproval, gotSession.Status)
	require.Equal(t, "Run tests", gotSession.CurrentTask)

	userMessage, err := store.CreateMailboxMessage(ctx, principal, CreateMailboxRequest{
		NodeID:    node.NodeID,
		AgentID:   agent.AgentID,
		SessionID: session.SessionID,
		Message:   "run unit tests",
	})
	require.NoError(t, err)
	nodePull, err := store.PullNodeMailbox(ctx, node.NodeID, agent.AgentID, "native_session", 0, 10)
	require.NoError(t, err)
	require.Len(t, nodePull.Messages, 1)
	require.Equal(t, "native_session", nodePull.Messages[0].SessionID)
	require.NoError(
		t,
		store.MarkNodeMessageResult(ctx, node.NodeID, userMessage.MessageID, MessageResultRequest{
			Status: "completed",
			Result: "node ok",
			Events: json.RawMessage(
				`[{"entity_type":"tool","event_type":"call","turnId":"turn_1","callId":"call_1","name":"shell","arguments":"{\"command\":\"go test ./...\"}"},{"entity_type":"tool","event_type":"result","turnId":"turn_1","callId":"call_1","name":"shell","output":"ok"}]`,
			),
			FileChanges: []FileChange{{Path: "internal/manager/audit.go", Tool: "apply_patch"}},
		}),
	)
	audit, err := store.ListAuditEvents(ctx, AuditEventFilter{
		Principal: principal,
		Query:     "go test",
		EventType: domain.AuditEventToolCallRequested,
	})
	require.NoError(t, err)
	require.Len(t, audit, 1)
	require.Equal(t, domain.AuditEventToolCallRequested, audit[0].EventType)
	require.Equal(t, "shell", audit[0].ToolName)
	fileAudit, err := store.ListAuditEvents(ctx, AuditEventFilter{
		Principal: principal,
		EventType: domain.AuditEventFileChanged,
	})
	require.NoError(t, err)
	require.Len(t, fileAudit, 1)
	require.Equal(t, "File changed: internal/manager/audit.go", fileAudit[0].Title)
	userMessage, err = store.CreateMailboxMessage(ctx, principal, CreateMailboxRequest{
		NodeID:    node.NodeID,
		AgentID:   agent.AgentID,
		SessionID: session.SessionID,
		Message:   "run unit tests again",
	})
	require.NoError(t, err)
	pull, err := store.PullMailbox(ctx, agent.AgentID, "native_session", 0, 10)
	require.NoError(t, err)
	require.Len(t, pull.Messages, 1)
	require.Equal(t, "native_session", pull.Messages[0].SessionID)
	require.NoError(
		t,
		store.MarkMessageResult(ctx, agent.AgentID, userMessage.MessageID, MessageResultRequest{
			Status: "completed",
			Result: "ok",
		}),
	)

	nodeMessage, err := store.CreateNodeOutboundMessage(ctx, node, CreateOutboundMessageRequest{
		AgentID:   agent.AgentID,
		SessionID: "native_session",
		Content:   "tests passed",
		Payload:   json.RawMessage(`{"session_id":"sess_manager","content":"tests passed"}`),
	})
	require.NoError(t, err)
	require.Equal(t, "native_session", nodeMessage.SessionID)
	require.NoError(t, store.MarkNodeMessageDelivered(ctx, node.NodeID, MarkDeliveredRequest{
		MessageID: nodeMessage.MessageID,
	}))
	require.NoError(
		t,
		store.MarkNodeMessageResult(ctx, node.NodeID, nodeMessage.MessageID, MessageResultRequest{
			Status: "completed",
			Result: "ack",
		}),
	)
	listedMailbox, err := store.ListMailbox(
		ctx,
		MailboxFilter{Principal: principal, NodeID: node.NodeID},
	)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(listedMailbox), 2)

	approval, err := store.CreateApproval(ctx, node, CreateApprovalRequest{
		AgentID:           agent.AgentID,
		SessionID:         "native_session",
		Operation:         "read_value",
		ResourceType:      "secret",
		ResourceRef:       "secret_1",
		ActionFingerprint: "fingerprint_1",
		RequestBody:       json.RawMessage(`{"secret_id":"secret_1"}`),
		RequestedEffects:  json.RawMessage(`[{"kind":"read"}]`),
	})
	require.NoError(t, err)
	gotApproval, err := store.GetApproval(ctx, principal, approval.ApprovalID)
	require.NoError(t, err)
	require.Equal(t, "native_session", gotApproval.RequestSessionID)
	nodeApproval, err := store.GetNodeApproval(ctx, node, agent.AgentID, approval.ApprovalID)
	require.NoError(t, err)
	require.Equal(t, "native_session", nodeApproval.RequestSessionID)
	pending, err := store.ListApprovals(ctx, ApprovalFilter{
		Principal: principal,
		Status:    "pending",
		Domain:    "agent_action",
	})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	decided, err := store.DecideApproval(
		ctx,
		principal,
		approval.ApprovalID,
		ApprovalDecisionRequest{
			DecisionOption: "allow_for_this_agent",
		},
	)
	require.NoError(t, err)
	require.Equal(t, "allow", decided.Decision)
	reusable, err := store.FindReusableApprovalGrant(ctx, ApprovalGrantLookup{
		OwnerUserID:       owner.UserID,
		RequestNodeID:     node.NodeID,
		RequestAgentID:    agent.AgentID,
		RequestSessionID:  session.SessionID,
		Domain:            "agent_action",
		Operation:         "read_value",
		ActionFingerprint: "fingerprint_1",
	})
	require.NoError(t, err)
	require.Equal(t, approval.ApprovalID, reusable.ApprovalID)
	grantList, err := store.ListApprovalGrants(ctx, ApprovalGrantFilter{
		Principal:     principal,
		DecisionScope: "agent",
		ActiveOnly:    true,
	})
	require.NoError(t, err)
	require.Len(t, grantList, 1)
	revoked, err := store.RevokeApprovalGrant(
		ctx,
		principal,
		approval.ApprovalID,
		RevokeApprovalGrantRequest{
			Reason: "test complete",
		},
	)
	require.NoError(t, err)
	require.NotNil(t, revoked.GrantRevokedAt)
	approvalAudit, err := store.ListAuditEvents(ctx, AuditEventFilter{
		Principal:  principal,
		ApprovalID: approval.ApprovalID,
	})
	require.NoError(t, err)
	require.Len(t, approvalAudit, 3)
	approvalAuditTypes := map[string]bool{}
	for _, event := range approvalAudit {
		approvalAuditTypes[event.EventType] = true
	}
	require.True(t, approvalAuditTypes[domain.AuditEventApprovalRequested])
	require.True(t, approvalAuditTypes[domain.AuditEventApprovalDecided])
	require.True(t, approvalAuditTypes[domain.AuditEventApprovalRevoked])
	_, err = store.RevokeApprovalGrant(
		ctx,
		principal,
		approval.ApprovalID,
		RevokeApprovalGrantRequest{},
	)
	require.NoError(t, err)
	_, err = store.FindReusableApprovalGrant(ctx, ApprovalGrantLookup{
		OwnerUserID:       owner.UserID,
		RequestNodeID:     node.NodeID,
		RequestAgentID:    agent.AgentID,
		RequestSessionID:  session.SessionID,
		Domain:            "agent_action",
		Operation:         "read_value",
		ActionFingerprint: "fingerprint_1",
	})
	require.True(t, errors.Is(err, ErrNotFound))
}

func TestMemoryCreateNodeAgentSessionGivenExistingNativeWhenUpsertOmitsNativeThenPreservesNative(
	t *testing.T,
) {
	ctx := context.Background()
	now := time.Date(2026, 7, 4, 9, 30, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, node, agent := seedMemoryNodeAgent(t, ctx, store, now)
	principal := UserPrincipal{User: owner}

	session, err := store.CreateNodeAgentSession(ctx, principal, CreateSessionRequest{
		NodeID:    node.NodeID,
		AgentID:   agent.AgentID,
		SessionID: "sess_manager",
		NativeID:  "native_session",
		Source:    domain.MessageSourceACPTunnel,
	})
	require.NoError(t, err)
	require.Equal(t, "native_session", session.NativeID)

	now = now.Add(time.Minute)
	session, err = store.CreateNodeAgentSession(ctx, principal, CreateSessionRequest{
		NodeID:    node.NodeID,
		AgentID:   agent.AgentID,
		SessionID: "sess_manager",
		Source:    domain.MessageSourceACPTunnel,
	})
	require.NoError(t, err)
	require.Equal(t, "native_session", session.NativeID)
}

func TestMemoryAgentStatusAndMailboxEdgeCases(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, node, agent := seedMemoryNodeAgent(t, ctx, store, now)
	principal := UserPrincipal{User: owner}

	require.NoError(t, store.UpsertAgentStatus(ctx, AgentStatusReport{
		AgentID:  agent.AgentID,
		Hostname: "agent-host",
		Sessions: []SessionStatusInput{{
			SessionID:    "native_from_agent",
			NativeID:     "native_from_agent",
			SessionName:  "Agent reported session",
			Status:       "running",
			CurrentTask:  "report status",
			MessageCount: 4,
		}},
	}))
	sessions, err := store.ListAgentSessions(ctx, principal, agent.AgentID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.NotEqual(t, "native_from_agent", sessions[0].SessionID)
	require.Equal(t, "native_from_agent", sessions[0].NativeID)
	require.Equal(t, node.NodeID, sessions[0].NodeID)

	message, err := store.CreateMailboxMessage(ctx, principal, CreateMailboxRequest{
		NodeID:    node.NodeID,
		AgentID:   agent.AgentID,
		SessionID: sessions[0].SessionID,
		Message:   "deliver to node",
	})
	require.NoError(t, err)
	pulled, err := store.PullNodeMailbox(
		ctx,
		node.NodeID,
		agent.AgentID,
		"native_from_agent",
		0,
		1,
	)
	require.NoError(t, err)
	require.Len(t, pulled.Messages, 1)
	require.Equal(t, "native_from_agent", pulled.Messages[0].SessionID)
	require.True(t, pulled.MaxOffset >= message.ID)

	err = store.MarkMessageResult(ctx, agent.AgentID, message.MessageID, MessageResultRequest{
		Status: "invalid",
	})
	require.ErrorIs(t, err, ErrConflict)
	err = store.MarkNodeMessageResult(ctx, node.NodeID, "missing", MessageResultRequest{
		Status: "completed",
	})
	require.ErrorIs(t, err, ErrNotFound)
	err = store.UpdateOffset(ctx, "missing_agent", 1)
	require.ErrorIs(t, err, ErrNotFound)
	err = store.UpdateNodeOffset(ctx, "missing_node", 1)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryFleetDeletionLifecycle(t *testing.T) {
	t.Run(
		"Given a registered agent when deleting it then normal fleet reads hide it",
		func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)
			store := NewMemoryStore(func() time.Time { return now })
			owner, err := store.EnsureUser(ctx, "owner@example.com", "Owner", "user")
			require.NoError(t, err)
			agent, err := store.RegisterAgent(ctx, owner, RegisterAgentRequest{
				Name:     "legacy codex",
				Hostname: "studio",
				OS:       "darwin",
			}, "agent_hash")
			require.NoError(t, err)
			principal := UserPrincipal{User: owner}

			authenticated, err := store.AuthenticateAgent(ctx, "agent_hash")
			require.NoError(t, err)
			require.Equal(t, agent.AgentID, authenticated.AgentID)
			deleted, err := store.DeleteAgent(
				ctx,
				principal,
				DeleteAgentRequest{AgentID: agent.AgentID},
			)

			require.NoError(t, err)
			require.Equal(t, agent.AgentID, deleted.AgentID)
			agents, err := store.ListAgents(ctx, principal)
			require.NoError(t, err)
			require.Empty(t, agents)
			_, err = store.GetAgent(ctx, principal, agent.AgentID)
			require.ErrorIs(t, err, ErrNotFound)
			_, err = store.AuthenticateAgent(ctx, "agent_hash")
			require.ErrorIs(t, err, ErrUnauthorized)
		},
	)

	t.Run(
		"Given a node agent when deleting it then normal fleet reads hide it",
		func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)
			store := NewMemoryStore(func() time.Time { return now })
			owner, node, agent := seedMemoryNodeAgent(t, ctx, store, now)
			principal := UserPrincipal{User: owner}

			deleted, err := store.DeleteNodeAgent(ctx, principal, DeleteAgentRequest{
				NodeID:  node.NodeID,
				AgentID: agent.AgentID,
			})

			require.NoError(t, err)
			require.Equal(t, agent.AgentID, deleted.AgentID)
			_, err = store.GetNodeAgent(ctx, node.NodeID, agent.AgentID)
			require.ErrorIs(t, err, ErrNotFound)
			_, err = store.GetAgent(ctx, principal, agent.AgentID)
			require.ErrorIs(t, err, ErrNotFound)
			agents, err := store.ListNodeAgents(ctx, principal, node.NodeID)
			require.NoError(t, err)
			require.Empty(t, agents)
		},
	)

	t.Run(
		"Given a node when deleting it then node key is revoked and agents are hidden",
		func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)
			store := NewMemoryStore(func() time.Time { return now })
			owner, node, agent := seedMemoryNodeAgent(t, ctx, store, now)
			principal := UserPrincipal{User: owner}

			authenticated, err := store.AuthenticateNode(ctx, "node_hash")
			require.NoError(t, err)
			require.Equal(t, node.NodeID, authenticated.NodeID)
			deleted, err := store.DeleteNode(ctx, principal, DeleteNodeRequest{NodeID: node.NodeID})

			require.NoError(t, err)
			require.Equal(t, node.NodeID, deleted.NodeID)
			nodes, err := store.ListNodes(ctx, principal)
			require.NoError(t, err)
			require.Empty(t, nodes)
			_, err = store.GetNode(ctx, principal, node.NodeID)
			require.ErrorIs(t, err, ErrNotFound)
			_, err = store.GetAgent(ctx, principal, agent.AgentID)
			require.ErrorIs(t, err, ErrNotFound)
			_, err = store.AuthenticateNode(ctx, "node_hash")
			require.ErrorIs(t, err, ErrUnauthorized)
		},
	)
}

func seedMemoryNodeAgent(
	t *testing.T,
	ctx context.Context,
	store *MemoryStore,
	now time.Time,
) (User, Node, Agent) {
	t.Helper()
	owner, err := store.EnsureUser(ctx, "owner@example.com", "Owner", "user")
	require.NoError(t, err)
	node, err := store.RegisterNode(ctx, owner, RegisterNodeRequest{
		Name:        "Studio Mac",
		Description: "Local development node",
		Hostname:    "studio",
		OS:          "darwin",
		Metadata:    json.RawMessage(`{"created_for":"test"}`),
	}, "node_hash")
	require.NoError(t, err)
	agent, bootstrap, err := store.CreateNodeAgent(
		ctx,
		UserPrincipal{User: owner},
		CreateAgentRequest{
			NodeID:      node.NodeID,
			Name:        "codex",
			Description: "Coding agent",
			AgentType:   "codex",
			Metadata:    json.RawMessage(`{"created_at":"` + now.Format(time.RFC3339) + `"}`),
		},
	)
	require.NoError(t, err)
	require.NotEmpty(t, bootstrap.MessageID)
	return owner, node, agent
}

func seedMemoryRepresentativeAgent(
	store *MemoryStore,
	representativeAgentID string,
	profileID string,
	runtimeAgentID string,
	representsID string,
	now time.Time,
) {
	store.representativeAgents[representativeAgentID] = domain.RepresentativeAgent{
		RepresentativeAgentID: representativeAgentID,
		ProfileID:             profileID,
		RuntimeAgentID:        runtimeAgentID,
		RepresentsType:        "user",
		RepresentsID:          representsID,
		Status:                domain.ConversationStatusActive,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
}

func requireMessageType(t *testing.T, messages []Message, messageType string) Message {
	t.Helper()
	for _, message := range messages {
		if message.MessageType == messageType {
			return message
		}
	}
	require.Failf(
		t,
		"missing message type",
		"message type %q not found in %+v",
		messageType,
		messages,
	)
	return Message{}
}

func messageTypes(messages []Message) []string {
	types := make([]string, 0, len(messages))
	for _, message := range messages {
		types = append(types, message.MessageType)
	}
	return types
}

func TestMemoryAgentConversationGivenForeignConversationIDWhenStartedThenReturnsNotFound(
	t *testing.T,
) {
	ctx := context.Background()
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, node, sourceAgent := seedMemoryNodeAgent(t, ctx, store, now)
	targetAgent, _, err := store.CreateNodeAgent(
		ctx,
		UserPrincipal{User: owner},
		CreateAgentRequest{NodeID: node.NodeID, Name: "target", AgentType: "codex"},
	)
	require.NoError(t, err)
	seedMemoryRepresentativeAgent(
		store,
		"rep_source",
		"profile_source",
		sourceAgent.AgentID,
		owner.UserID,
		now,
	)
	seedMemoryRepresentativeAgent(
		store,
		"rep_target",
		"profile_target",
		targetAgent.AgentID,
		owner.UserID,
		now,
	)
	start, err := store.StartAgentConversation(ctx, node, domain.StartAgentConversationRequest{
		FromRuntimeAgentID:      sourceAgent.AgentID,
		ToRepresentativeAgentID: "rep_target",
		Input:                   "Keep this private.",
	})
	require.NoError(t, err)

	outsider, err := store.EnsureUser(ctx, "outsider@example.com", "Outsider", "user")
	require.NoError(t, err)
	outsiderNode, err := store.RegisterNode(ctx, outsider, RegisterNodeRequest{
		Name:     "Outsider Mac",
		Hostname: "outsider",
		OS:       "darwin",
	}, "outsider_node_hash")
	require.NoError(t, err)
	outsiderAgent, _, err := store.CreateNodeAgent(
		ctx,
		UserPrincipal{User: outsider},
		CreateAgentRequest{NodeID: outsiderNode.NodeID, Name: "outsider", AgentType: "codex"},
	)
	require.NoError(t, err)
	seedMemoryRepresentativeAgent(
		store,
		"rep_outsider",
		"profile_outsider",
		outsiderAgent.AgentID,
		outsider.UserID,
		now,
	)

	_, err = store.StartAgentConversation(ctx, outsiderNode, domain.StartAgentConversationRequest{
		FromRuntimeAgentID:      outsiderAgent.AgentID,
		ToRepresentativeAgentID: "rep_outsider",
		ConversationID:          start.Conversation.ConversationID,
		Input:                   "Let me in.",
	})

	require.ErrorIs(t, err, ErrNotFound)
	_, err = store.ListConversationMessages(
		ctx,
		UserPrincipal{User: outsider},
		start.Conversation.ConversationID,
		10,
	)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryAgentConversationGivenMemberConversationIDWhenStartedThenContinues(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, node, sourceAgent := seedMemoryNodeAgent(t, ctx, store, now)
	targetAgent, _, err := store.CreateNodeAgent(
		ctx,
		UserPrincipal{User: owner},
		CreateAgentRequest{NodeID: node.NodeID, Name: "target", AgentType: "codex"},
	)
	require.NoError(t, err)
	seedMemoryRepresentativeAgent(
		store,
		"rep_source",
		"profile_source",
		sourceAgent.AgentID,
		owner.UserID,
		now,
	)
	seedMemoryRepresentativeAgent(
		store,
		"rep_target",
		"profile_target",
		targetAgent.AgentID,
		owner.UserID,
		now,
	)
	start, err := store.StartAgentConversation(ctx, node, domain.StartAgentConversationRequest{
		FromRuntimeAgentID:      sourceAgent.AgentID,
		ToRepresentativeAgentID: "rep_target",
		Input:                   "Start a thread.",
	})
	require.NoError(t, err)

	again, err := store.StartAgentConversation(ctx, node, domain.StartAgentConversationRequest{
		FromRuntimeAgentID:      sourceAgent.AgentID,
		ToRepresentativeAgentID: "rep_target",
		ConversationID:          start.Conversation.ConversationID,
		Input:                   "Follow up.",
	})

	require.NoError(t, err)
	require.Equal(t, start.Conversation.ConversationID, again.Conversation.ConversationID)
}

func TestMemoryUpsertRepresentativeAgentGivenForeignProfileIDThenReturnsConflict(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	victim, _, victimAgent := seedMemoryNodeAgent(t, ctx, store, now)
	_, victimProfile, err := store.UpsertRepresentativeAgent(
		ctx,
		UserPrincipal{User: victim},
		domain.UpsertRepresentativeAgentRequest{RuntimeAgentID: victimAgent.AgentID},
	)
	require.NoError(t, err)

	attacker, err := store.EnsureUser(ctx, "attacker@example.com", "Attacker", "user")
	require.NoError(t, err)
	attackerNode, err := store.RegisterNode(ctx, attacker, RegisterNodeRequest{
		Name:     "Attacker Mac",
		Hostname: "attacker",
		OS:       "darwin",
	}, "attacker_node_hash")
	require.NoError(t, err)
	attackerAgent, _, err := store.CreateNodeAgent(
		ctx,
		UserPrincipal{User: attacker},
		CreateAgentRequest{NodeID: attackerNode.NodeID, Name: "attacker", AgentType: "codex"},
	)
	require.NoError(t, err)

	_, _, err = store.UpsertRepresentativeAgent(
		ctx,
		UserPrincipal{User: attacker},
		domain.UpsertRepresentativeAgentRequest{
			RuntimeAgentID: attackerAgent.AgentID,
			ProfileID:      victimProfile.ProfileID,
			DisplayName:    "defaced",
		},
	)
	require.ErrorIs(t, err, ErrConflict)
	require.NotEqual(t, "defaced", store.agentProfiles[victimProfile.ProfileID].DisplayName)
}

func TestMemoryUpsertRepresentativeAgentGivenForeignRepresentsThenReturnsUnauthorized(
	t *testing.T,
) {
	ctx := context.Background()
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	victim, _, _ := seedMemoryNodeAgent(t, ctx, store, now)

	attacker, err := store.EnsureUser(ctx, "attacker@example.com", "Attacker", "user")
	require.NoError(t, err)
	attackerNode, err := store.RegisterNode(ctx, attacker, RegisterNodeRequest{
		Name:     "Attacker Mac",
		Hostname: "attacker",
		OS:       "darwin",
	}, "attacker_node_hash")
	require.NoError(t, err)
	attackerAgent, _, err := store.CreateNodeAgent(
		ctx,
		UserPrincipal{User: attacker},
		CreateAgentRequest{NodeID: attackerNode.NodeID, Name: "attacker", AgentType: "codex"},
	)
	require.NoError(t, err)

	_, _, err = store.UpsertRepresentativeAgent(
		ctx,
		UserPrincipal{User: attacker},
		domain.UpsertRepresentativeAgentRequest{
			RuntimeAgentID: attackerAgent.AgentID,
			RepresentsType: "user",
			RepresentsID:   victim.UserID,
		},
	)
	require.ErrorIs(t, err, ErrUnauthorized)

	_, _, err = store.UpsertRepresentativeAgent(
		ctx,
		UserPrincipal{User: attacker},
		domain.UpsertRepresentativeAgentRequest{
			RuntimeAgentID: attackerAgent.AgentID,
			RepresentsType: "team",
			RepresentsID:   "team_missing",
		},
	)
	require.ErrorIs(t, err, ErrUnauthorized)
}

func TestMemoryCreateNodeAgentSessionGivenForeignReferencesThenReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, node, sourceAgent := seedMemoryNodeAgent(t, ctx, store, now)
	targetAgent, _, err := store.CreateNodeAgent(
		ctx,
		UserPrincipal{User: owner},
		CreateAgentRequest{NodeID: node.NodeID, Name: "target", AgentType: "codex"},
	)
	require.NoError(t, err)
	seedMemoryRepresentativeAgent(
		store,
		"rep_source",
		"profile_source",
		sourceAgent.AgentID,
		owner.UserID,
		now,
	)
	seedMemoryRepresentativeAgent(
		store,
		"rep_target",
		"profile_target",
		targetAgent.AgentID,
		owner.UserID,
		now,
	)
	start, err := store.StartAgentConversation(ctx, node, domain.StartAgentConversationRequest{
		FromRuntimeAgentID:      sourceAgent.AgentID,
		ToRepresentativeAgentID: "rep_target",
		Input:                   "Private thread.",
	})
	require.NoError(t, err)

	attacker, err := store.EnsureUser(ctx, "attacker@example.com", "Attacker", "user")
	require.NoError(t, err)
	attackerNode, err := store.RegisterNode(ctx, attacker, RegisterNodeRequest{
		Name:     "Attacker Mac",
		Hostname: "attacker",
		OS:       "darwin",
	}, "attacker_node_hash")
	require.NoError(t, err)
	attackerAgent, _, err := store.CreateNodeAgent(
		ctx,
		UserPrincipal{User: attacker},
		CreateAgentRequest{NodeID: attackerNode.NodeID, Name: "attacker", AgentType: "codex"},
	)
	require.NoError(t, err)

	_, err = store.CreateNodeAgentSession(ctx, UserPrincipal{User: attacker}, CreateSessionRequest{
		NodeID:         attackerNode.NodeID,
		AgentID:        attackerAgent.AgentID,
		ConversationID: start.Conversation.ConversationID,
	})
	require.ErrorIs(t, err, ErrNotFound)

	_, err = store.CreateNodeAgentSession(ctx, UserPrincipal{User: attacker}, CreateSessionRequest{
		NodeID:    attackerNode.NodeID,
		AgentID:   attackerAgent.AgentID,
		ProfileID: "profile_source",
	})
	require.ErrorIs(t, err, ErrNotFound)

	_, err = store.CreateNodeAgentSession(ctx, UserPrincipal{User: attacker}, CreateSessionRequest{
		NodeID:                attackerNode.NodeID,
		AgentID:               attackerAgent.AgentID,
		RepresentativeAgentID: "rep_source",
	})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryCreateNodeAgentSessionGivenSpoofedCreatedByThenCoercesToPrincipal(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, node, agent := seedMemoryNodeAgent(t, ctx, store, now)

	session, err := store.CreateNodeAgentSession(
		ctx,
		UserPrincipal{User: owner},
		CreateSessionRequest{
			NodeID:          node.NodeID,
			AgentID:         agent.AgentID,
			CreatedByUserID: "usr_victim",
		},
	)

	require.NoError(t, err)
	require.Equal(t, owner.UserID, session.CreatedByUserID)
}
