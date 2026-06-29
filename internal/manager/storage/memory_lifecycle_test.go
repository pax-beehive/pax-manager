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
		}),
	)
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
